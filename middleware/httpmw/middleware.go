// Package httpmw provides framework-neutral net/http middleware for
// resolving, validating and scoping requests to a tenant. Every value the
// middleware produces travels in the request context; read it with the
// helpers in package tenant.
package httpmw

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/alexalmadav/go-multitenant/limits"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Config configures the middleware.
type Config struct {
	// SkipPaths are path prefixes that bypass ResolveTenant, e.g. "/health".
	// Middlewares downstream of ResolveTenant pass such requests through.
	SkipPaths []string
	// SkipHosts are hosts that bypass ResolveTenant entirely, matched against
	// r.Host without its port and ignoring case. Use it for an origin that
	// serves no tenant, such as a single sign-on host like "auth.app.com",
	// where resolution would necessarily fail.
	SkipHosts []string
	// ErrorHandler writes the response for a tenant error. Defaults to
	// DefaultErrorHandler.
	ErrorHandler func(w http.ResponseWriter, r *http.Request, err error)
	// ClientIP extracts the client address for the access log. Default: the
	// host part of r.RemoteAddr, which cannot be spoofed by the client. Behind
	// a trusted reverse proxy that sets X-Forwarded-For, use ForwardedClientIP.
	ClientIP func(*http.Request) string
}

// Middleware builds tenant middlewares for net/http.
type Middleware struct {
	manager    tenant.Manager
	resolver   tenant.Resolver
	logger     *zap.Logger
	config     Config
	limits     limits.Enforcer
	membership tenant.Membership
}

// Option configures a Middleware.
type Option func(*Middleware)

// WithLimits enables plan-limit enforcement in EnforceLimits and Standard.
func WithLimits(e limits.Enforcer) Option {
	return func(m *Middleware) { m.limits = e }
}

// WithMembership supplies the authorization check RequireMembership applies.
// Without it, RequireMembership denies every request; see its documentation
// for why that differs from WithLimits.
func WithMembership(m tenant.Membership) Option {
	return func(mw *Middleware) { mw.membership = m }
}

// New creates a Middleware.
func New(manager tenant.Manager, resolver tenant.Resolver, logger *zap.Logger, cfg Config, opts ...Option) *Middleware {
	if cfg.ErrorHandler == nil {
		cfg.ErrorHandler = DefaultErrorHandler
	}
	if cfg.ClientIP == nil {
		cfg.ClientIP = RemoteAddrIP
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	m := &Middleware{manager: manager, resolver: resolver, logger: logger.Named("http_middleware"), config: cfg}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Chain applies middlewares in order: Chain(h, a, b) handles a request as a -> b -> h.
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Standard is ResolveTenant, ValidateTenant, RequireMembership, EnforceLimits
// and SetTenantDB in that order. RequireMembership is included only when New
// was given WithMembership, so the bundle never denies every request because
// an option was forgotten; apply RequireMembership by hand for the
// fail-closed behaviour. EnforceLimits is a pass-through unless New was given
// WithLimits.
func (m *Middleware) Standard() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		mws := []func(http.Handler) http.Handler{m.ResolveTenant(), m.ValidateTenant()}
		if m.membership != nil {
			mws = append(mws, m.RequireMembership())
		}
		mws = append(mws, m.EnforceLimits(), m.SetTenantDB())
		return Chain(next, mws...)
	}
}

// ResolveTenant resolves the tenant from the request and stores the tenant
// context, tenant id and full tenant record in the request context.
func (m *Middleware) ResolveTenant() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m.shouldSkip(r) {
				next.ServeHTTP(w, r)
				return
			}

			tenantID, err := m.resolver.ResolveTenant(r.Context(), r)
			if err != nil {
				m.logger.Debug("Failed to resolve tenant",
					zap.String("path", r.URL.Path), zap.String("host", r.Host), zap.Error(err))
				m.config.ErrorHandler(w, r, &tenant.TenantError{
					Code: "TENANT_NOT_FOUND", Message: "Unable to resolve tenant from request"})
				return
			}

			t, err := m.manager.GetTenant(r.Context(), tenantID)
			if err != nil {
				m.logger.Error("Failed to get tenant details",
					zap.String("tenant_id", tenantID.String()), zap.Error(err))
				m.config.ErrorHandler(w, r, &tenant.TenantError{
					TenantID: tenantID, Code: "TENANT_NOT_FOUND", Message: "Tenant not found"})
				return
			}

			ctx := context.WithValue(r.Context(), tenant.ContextKeyTenant, &tenant.Context{
				TenantID:   t.ID,
				Subdomain:  t.Subdomain,
				SchemaName: t.SchemaName,
				Status:     t.Status,
			})
			ctx = context.WithValue(ctx, tenant.ContextKeyTenantID, t.ID)
			ctx = tenant.WithTenantObject(ctx, t)

			m.logger.Debug("Resolved tenant",
				zap.String("tenant_id", t.ID.String()), zap.String("subdomain", t.Subdomain), zap.String("path", r.URL.Path))

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ValidateTenant rejects requests whose tenant is not active.
func (m *Middleware) ValidateTenant() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m.shouldSkip(r) {
				next.ServeHTTP(w, r)
				return
			}

			tc, ok := tenant.GetTenantFromContext(r.Context())
			if !ok {
				m.config.ErrorHandler(w, r, &tenant.TenantError{
					Code: "TENANT_CONTEXT_MISSING", Message: "Tenant context not found - ensure ResolveTenant middleware is applied first"})
				return
			}
			switch tc.Status {
			case tenant.StatusActive:
				next.ServeHTTP(w, r)
			case tenant.StatusSuspended:
				m.config.ErrorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "TENANT_SUSPENDED", Message: "Account suspended. Please contact support."})
			case tenant.StatusPending:
				m.config.ErrorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "TENANT_PENDING", Message: "Account pending verification. Please check your email."})
			case tenant.StatusCancelled:
				m.config.ErrorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "TENANT_CANCELLED", Message: "Account cancelled."})
			default:
				m.config.ErrorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "TENANT_INVALID_STATUS", Message: "Account status invalid."})
			}
		})
	}
}

// EnforceLimits checks plan limits for the resolved tenant with the enforcer
// given to New via WithLimits. Without that option it is a pass-through.
// Requests on a SkipPaths prefix bypass the check. Unlike the package
// function, a failed check is logged with its underlying cause before the
// response is sanitised.
func (m *Middleware) EnforceLimits() func(http.Handler) http.Handler {
	inner := enforceLimits(m.limits, m.config.ErrorHandler, func(tenantID uuid.UUID, err error) {
		m.logger.Error("Plan limits check failed",
			zap.String("tenant_id", tenantID.String()), zap.Error(err))
	})
	return func(next http.Handler) http.Handler {
		guarded := inner(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m.shouldSkip(r) {
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// EnforceLimits checks the resolved tenant's plan limits with e and stores
// the checked limits in the context (read them with limits.FromContext).
// A nil e is a pass-through. A nil errorHandler uses DefaultErrorHandler.
//
// The error the client sees is sanitised; nothing is logged. Use the
// Middleware method, or wrap e, if the cause must reach the operator.
func EnforceLimits(e limits.Enforcer, errorHandler func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return enforceLimits(e, errorHandler, nil)
}

// enforceLimits is EnforceLimits plus an optional callback that receives the
// original, unsanitised error from CheckTenant.
func enforceLimits(e limits.Enforcer, errorHandler func(http.ResponseWriter, *http.Request, error), onError func(tenantID uuid.UUID, err error)) func(http.Handler) http.Handler {
	if errorHandler == nil {
		errorHandler = DefaultErrorHandler
	}
	return func(next http.Handler) http.Handler {
		if e == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tc, ok := tenant.GetTenantFromContext(r.Context())
			if !ok {
				errorHandler(w, r, &tenant.TenantError{Code: "TENANT_CONTEXT_MISSING", Message: "Tenant context not found"})
				return
			}
			checked, err := e.CheckTenant(r.Context(), tc.TenantID)
			if err != nil {
				if onError != nil {
					onError(tc.TenantID, err)
				}
				var tenantErr *tenant.TenantError
				switch {
				case errors.As(err, &tenantErr) && (tenantErr.Code == "LIMIT_EXCEEDED" || tenantErr.Code == "FEATURE_NOT_ALLOWED"):
					errorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "PLAN_LIMIT_EXCEEDED", Message: tenantErr.Message})
				case tenantErr != nil && tenantErr.Code == "PLAN_NOT_CONFIGURED":
					errorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "PLAN_NOT_CONFIGURED", Message: tenantErr.Message})
				default:
					errorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "LIMIT_CHECK_FAILED", Message: "Unable to verify plan limits"})
				}
				return
			}
			next.ServeHTTP(w, r.WithContext(limits.WithLimits(r.Context(), checked)))
		})
	}
}

// SetTenantDB acquires a tenant-scoped connection for the request and
// releases it when the handler returns. Requests without a resolved tenant
// (skipped paths) pass through without a connection.
func (m *Middleware) SetTenantDB() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tc, ok := tenant.GetTenantFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			conn, err := m.manager.GetTenantConn(r.Context(), tc.TenantID)
			if err != nil {
				m.logger.Error("Failed to get tenant database connection", zap.String("tenant_id", tc.TenantID.String()), zap.Error(err))
				m.config.ErrorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "DATABASE_ERROR", Message: "Failed to access tenant database"})
				return
			}
			defer func() {
				if err := conn.Close(); err != nil {
					m.logger.Error("Failed to close tenant database connection", zap.String("tenant_id", tc.TenantID.String()), zap.Error(err))
				}
			}()
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tenant.ContextKeyTenantConn, conn)))
		})
	}
}

// LogAccess logs one line per request that has a resolved tenant.
func (m *Middleware) LogAccess() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc, ok := tenant.GetTenantFromContext(r.Context()); ok {
				userID, _ := tenant.UserIDFromContext(r.Context())
				m.logger.Info("Tenant access",
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.String("user_id", userID),
					zap.String("tenant_id", tc.TenantID.String()),
					zap.String("subdomain", tc.Subdomain),
					zap.String("client_ip", m.config.ClientIP(r)),
					zap.String("user_agent", r.UserAgent()))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// shouldSkip reports whether the request bypasses tenant handling, either
// because its path is under a SkipPaths prefix or its host is in SkipHosts.
func (m *Middleware) shouldSkip(r *http.Request) bool {
	return m.shouldSkipPath(r.URL.Path) || m.shouldSkipHost(r.Host)
}

func (m *Middleware) shouldSkipPath(path string) bool {
	for _, p := range m.config.SkipPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (m *Middleware) shouldSkipHost(host string) bool {
	if len(m.config.SkipHosts) == 0 {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	for _, sh := range m.config.SkipHosts {
		if strings.EqualFold(host, sh) {
			return true
		}
	}
	return false
}

// ForwardedClientIP returns the first X-Forwarded-For entry, else X-Real-IP,
// else the RemoteAddr host. Header values that do not parse as an IP are
// ignored. Only use it behind a proxy you control that overwrites these
// headers; otherwise clients can choose the logged address.
func ForwardedClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, _ := strings.Cut(xff, ","); net.ParseIP(strings.TrimSpace(first)) != nil {
			return strings.TrimSpace(first)
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(xr) != nil {
		return xr
	}
	return RemoteAddrIP(r)
}

// RemoteAddrIP returns the host part of r.RemoteAddr, or r.RemoteAddr
// unchanged if it has no port.
func RemoteAddrIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

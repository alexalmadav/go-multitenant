// Package gin adapts the framework-neutral tenant middleware in
// github.com/alexalmadav/go-multitenant/middleware/httpmw to Gin.
//
// Every value is available both in the request context (read with package
// tenant's helpers) and, for backward compatibility, under the Gin context
// keys "tenant", "tenant_id", "tenant_object", "tenant_conn" and
// "plan_limits".
package gin

import (
	"context"
	"net/http"

	"github.com/alexalmadav/go-multitenant/middleware/httpmw"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Config configures the Gin middleware.
type Config struct {
	// SkipPaths are path prefixes that bypass tenant resolution.
	SkipPaths []string
	// ErrorHandler is called with the Gin context when a tenant error occurs.
	// It must write a response; the chain is aborted afterwards. Defaults to
	// the JSON handler from package httpmw.
	ErrorHandler func(*gin.Context, error)
	// ClientIP extracts the client address for the access log. Default: the
	// host part of r.RemoteAddr, which cannot be spoofed by the client. Behind
	// a trusted reverse proxy that sets X-Forwarded-For, use
	// httpmw.ForwardedClientIP.
	ClientIP func(*http.Request) string
}

// Middleware provides Gin handlers backed by httpmw.
type Middleware struct {
	core *httpmw.Middleware
}

// ginContextKey is internal-only; the stored *gin.Context is read
// synchronously by the error-handler bridge during the request and must
// never be retained beyond it (Gin pools contexts).
type ginContextKey struct{}

// NewMiddleware creates the Gin middleware.
func NewMiddleware(manager tenant.Manager, resolver tenant.Resolver, logger *zap.Logger, cfg Config) *Middleware {
	coreCfg := httpmw.Config{SkipPaths: cfg.SkipPaths, ClientIP: cfg.ClientIP}
	if cfg.ErrorHandler != nil {
		coreCfg.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			if c, ok := r.Context().Value(ginContextKey{}).(*gin.Context); ok {
				cfg.ErrorHandler(c, err)
				return
			}
			httpmw.DefaultErrorHandler(w, r, err)
		}
	}
	return &Middleware{core: httpmw.New(manager, resolver, logger, coreCfg)}
}

// adapt turns a net/http middleware into a Gin handler. The rest of the Gin
// chain runs inside the core middleware's next handler, so anything the core
// defers (releasing a tenant connection) happens after the handlers.
func (m *Middleware) adapt(mw func(http.Handler) http.Handler) gin.HandlerFunc {
	return func(c *gin.Context) {
		reached := false
		ctx := context.WithValue(c.Request.Context(), ginContextKey{}, c)
		if _, ok := tenant.UserIDFromContext(ctx); !ok {
			if uid := c.GetString("user_id"); uid != "" {
				ctx = tenant.WithUserID(ctx, uid)
			}
		}
		req := c.Request.WithContext(ctx)
		h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			c.Request = r
			syncContext(c)
			c.Next()
		}))
		h.ServeHTTP(c.Writer, req)
		if !reached {
			c.Abort()
		}
	}
}

// syncContext mirrors request-context values into the Gin context keys.
func syncContext(c *gin.Context) {
	ctx := c.Request.Context()
	if tc, ok := tenant.GetTenantFromContext(ctx); ok {
		c.Set("tenant", tc)
		c.Set("tenant_id", tc.TenantID.String())
	}
	if t, ok := tenant.TenantObjectFromContext(ctx); ok {
		c.Set("tenant_object", t)
	}
	if conn, ok := tenant.GetTenantConnFromContext(ctx); ok {
		c.Set("tenant_conn", conn)
	}
	if l, ok := tenant.PlanLimitsFromContext(ctx); ok {
		c.Set("plan_limits", l)
	}
}

// ResolveTenant resolves the tenant from the request.
func (m *Middleware) ResolveTenant() gin.HandlerFunc { return m.adapt(m.core.ResolveTenant()) }

// ValidateTenant rejects requests whose tenant is not active.
func (m *Middleware) ValidateTenant() gin.HandlerFunc { return m.adapt(m.core.ValidateTenant()) }

// EnforceLimits checks plan limits and stores them under "plan_limits".
func (m *Middleware) EnforceLimits() gin.HandlerFunc { return m.adapt(m.core.EnforceLimits()) }

// SetTenantDB acquires a tenant-scoped connection for the request.
func (m *Middleware) SetTenantDB() gin.HandlerFunc { return m.adapt(m.core.SetTenantDB()) }

// LogAccess logs one line per tenant request. Set the user id with
// tenant.WithUserID on the request context to include it.
func (m *Middleware) LogAccess() gin.HandlerFunc { return m.adapt(m.core.LogAccess()) }

// GetTenantFromContext returns the tenant context set by ResolveTenant.
func GetTenantFromContext(c *gin.Context) (*tenant.Context, bool) {
	v, ok := c.Get("tenant")
	if !ok {
		return nil, false
	}
	t, ok := v.(*tenant.Context)
	return t, ok
}

// GetTenantFromGinContext returns the full tenant record set by ResolveTenant.
func GetTenantFromGinContext(c *gin.Context) (*tenant.Tenant, bool) {
	v, ok := c.Get("tenant_object")
	if !ok {
		return nil, false
	}
	t, ok := v.(*tenant.Tenant)
	return t, ok
}

// GetTenantLimitsFromContext returns the limits set by EnforceLimits.
func GetTenantLimitsFromContext(c *gin.Context) (*tenant.Limits, bool) {
	v, ok := c.Get("plan_limits")
	if !ok {
		return nil, false
	}
	l, ok := v.(*tenant.Limits)
	return l, ok
}

// GetTenantConnFromContext returns the tenant connection set by SetTenantDB.
// Do not close it; it is released when the request completes.
func GetTenantConnFromContext(c *gin.Context) (*tenant.Conn, bool) {
	v, ok := c.Get("tenant_conn")
	if !ok {
		return nil, false
	}
	conn, ok := v.(*tenant.Conn)
	return conn, ok
}

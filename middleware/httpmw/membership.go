package httpmw

import (
	"net/http"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// RequireMembership rejects requests whose authenticated subject does not
// belong to the resolved tenant. Place it after ResolveTenant and after the
// application's own auth middleware, which must put the caller in the request
// context with tenant.WithPrincipal or tenant.WithUserID.
//
// A request that carries no resolved tenant and matches a skipped path or
// host passes through untouched. Once a tenant has been resolved the check
// always runs, whatever the skip lists say: a skip list must not be able to
// disable an authorization control for a request that a downstream
// middleware will still scope to that tenant's schema.
//
// A request with no principal, or one whose principal has an empty subject,
// is treated as unauthenticated and rejected with USER_NOT_AUTHENTICATED
// (401); a principal the Membership refuses is rejected with ACCESS_DENIED
// (403), and the refusal's cause is logged rather than returned to the
// client.
//
// Unlike WithLimits, a Membership that was never configured does not disable
// the check: it denies every request and logs the misconfiguration. A missed
// limit check costs money; a missed membership check serves one tenant's data
// to another.
func (m *Middleware) RequireMembership() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tc, ok := tenant.GetTenantFromContext(r.Context())
			if !ok {
				if m.shouldSkip(r) {
					next.ServeHTTP(w, r)
					return
				}
				m.config.ErrorHandler(w, r, &tenant.TenantError{
					Code: "TENANT_CONTEXT_MISSING", Message: "Tenant context not found - ensure ResolveTenant middleware is applied first"})
				return
			}
			// A resolved tenant is always enforced, whatever the skip lists
			// say. shouldSkip re-reads r.URL.Path and r.Host, which a rewrite
			// such as http.StripPrefix can change between ResolveTenant and
			// here.

			// Report the misconfiguration before the principal check, so that
			// a service receiving only unauthenticated traffic still says why
			// it refuses every request instead of answering 401 in silence.
			if m.membership == nil {
				m.logger.Error("RequireMembership is applied without WithMembership; denying the request",
					zap.String("tenant_id", tc.TenantID.String()))
			}

			p, ok := tenant.PrincipalFromContext(r.Context())
			if !ok || p.Subject == "" {
				m.config.ErrorHandler(w, r, &tenant.TenantError{
					TenantID: tc.TenantID, Code: "USER_NOT_AUTHENTICATED", Message: "Authentication required"})
				return
			}

			if m.membership == nil {
				m.deny(w, r, tc.TenantID)
				return
			}

			if err := m.membership.Allow(r.Context(), p.Subject, tc.TenantID); err != nil {
				m.logger.Warn("Membership check denied the request",
					zap.String("tenant_id", tc.TenantID.String()),
					zap.String("subject", p.Subject),
					zap.Error(err))
				m.deny(w, r, tc.TenantID)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// deny writes the sanitised refusal. The cause has already been logged.
func (m *Middleware) deny(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID) {
	m.config.ErrorHandler(w, r, &tenant.TenantError{
		TenantID: tenantID, Code: "ACCESS_DENIED", Message: "Access denied to this tenant"})
}

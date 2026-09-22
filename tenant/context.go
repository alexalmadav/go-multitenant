package tenant

import "context"

// Context keys for values the middleware passes downstream. The tenant,
// tenant id and tenant connection keys live in interfaces.go.
const (
	// ContextKeyTenantObject carries the full *Tenant loaded by ResolveTenant.
	ContextKeyTenantObject ContextKey = "tenant_object"
	// ContextKeyUserID carries the authenticated user's id, set by the
	// application's auth middleware and read by LogAccess.
	ContextKeyUserID ContextKey = "user_id"
	// ContextKeyPrincipal carries the authenticated caller as a Principal,
	// set by the application's auth middleware with WithPrincipal.
	ContextKeyPrincipal ContextKey = "principal"
	// ContextKeyPlanLimits carries the limits checked by EnforceLimits.
	ContextKeyPlanLimits ContextKey = "plan_limits"
)

// WithTenantObject returns a context carrying the full tenant record.
func WithTenantObject(ctx context.Context, t *Tenant) context.Context {
	return context.WithValue(ctx, ContextKeyTenantObject, t)
}

// TenantObjectFromContext returns the full tenant record set by ResolveTenant.
func TenantObjectFromContext(ctx context.Context) (*Tenant, bool) {
	t, ok := ctx.Value(ContextKeyTenantObject).(*Tenant)
	return t, ok
}

// WithUserID returns a context carrying the authenticated user's id. It is
// shorthand for WithPrincipal with only a subject; use WithPrincipal when the
// caller also has claims.
func WithUserID(ctx context.Context, userID string) context.Context {
	return WithPrincipal(ctx, Principal{Subject: userID})
}

// UserIDFromContext returns the user id set with WithUserID.
func UserIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ContextKeyUserID).(string)
	return id, ok
}

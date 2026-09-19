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

// WithUserID returns a context carrying the authenticated user's id.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ContextKeyUserID, userID)
}

// UserIDFromContext returns the user id set with WithUserID.
func UserIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ContextKeyUserID).(string)
	return id, ok
}

// WithPlanLimits returns a context carrying the tenant's checked limits.
func WithPlanLimits(ctx context.Context, l *Limits) context.Context {
	return context.WithValue(ctx, ContextKeyPlanLimits, l)
}

// PlanLimitsFromContext returns the limits set by EnforceLimits.
func PlanLimitsFromContext(ctx context.Context) (*Limits, bool) {
	l, ok := ctx.Value(ContextKeyPlanLimits).(*Limits)
	return l, ok
}

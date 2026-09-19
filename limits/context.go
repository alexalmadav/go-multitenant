package limits

import (
	"context"

	"github.com/alexalmadav/go-multitenant/tenant"
)

// WithLimits stores a tenant's checked limits in the context.
func WithLimits(ctx context.Context, l FlexibleLimits) context.Context {
	return context.WithValue(ctx, tenant.ContextKeyPlanLimits, l)
}

// FromContext returns the limits stored by the EnforceLimits middleware.
func FromContext(ctx context.Context) (FlexibleLimits, bool) {
	l, ok := ctx.Value(tenant.ContextKeyPlanLimits).(FlexibleLimits)
	return l, ok
}

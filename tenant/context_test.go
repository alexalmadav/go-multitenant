package tenant

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestContext_TenantObjectRoundTrip(t *testing.T) {
	tn := &Tenant{ID: uuid.New(), Subdomain: "acme"}
	ctx := WithTenantObject(context.Background(), tn)
	got, ok := TenantObjectFromContext(ctx)
	if !ok || got != tn {
		t.Fatalf("TenantObjectFromContext = %v, %v; want the same pointer", got, ok)
	}
	if _, ok := TenantObjectFromContext(context.Background()); ok {
		t.Error("empty context should report no tenant object")
	}
}

func TestContext_UserIDRoundTrip(t *testing.T) {
	ctx := WithUserID(context.Background(), "user-1")
	got, ok := UserIDFromContext(ctx)
	if !ok || got != "user-1" {
		t.Fatalf("UserIDFromContext = %q, %v", got, ok)
	}
	if _, ok := UserIDFromContext(context.Background()); ok {
		t.Error("empty context should report no user id")
	}
}

func TestContext_PlanLimitsRoundTrip(t *testing.T) {
	l := &Limits{MaxProjects: 3}
	ctx := WithPlanLimits(context.Background(), l)
	got, ok := PlanLimitsFromContext(ctx)
	if !ok || got != l {
		t.Fatalf("PlanLimitsFromContext = %v, %v", got, ok)
	}
}

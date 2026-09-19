package limits

import (
	"context"
	"errors"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type memRepo struct{ tenants map[uuid.UUID]*tenant.Tenant }

func (r *memRepo) Create(ctx context.Context, t *tenant.Tenant) error {
	r.tenants[t.ID] = t
	return nil
}
func (r *memRepo) GetByID(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	if t, ok := r.tenants[id]; ok {
		return t, nil
	}
	return nil, errors.New("not found")
}
func (r *memRepo) GetBySubdomain(ctx context.Context, s string) (*tenant.Tenant, error) {
	return nil, errors.New("not found")
}
func (r *memRepo) Update(ctx context.Context, t *tenant.Tenant) error { return nil }
func (r *memRepo) Delete(ctx context.Context, id uuid.UUID) error     { return nil }
func (r *memRepo) List(ctx context.Context, page, perPage int) ([]*tenant.Tenant, int, error) {
	return nil, 0, nil
}
func (r *memRepo) FindByMetadata(ctx context.Context, k, v string) ([]*tenant.Tenant, error) {
	return nil, nil
}

type fixedUsage map[string]int

func (u fixedUsage) GetCurrentUsage(ctx context.Context, id uuid.UUID, name string) (interface{}, error) {
	if v, ok := u[name]; ok {
		return v, nil
	}
	return nil, nil
}
func (fixedUsage) IncrementUsage(context.Context, uuid.UUID, string, interface{}) error { return nil }
func (fixedUsage) DecrementUsage(context.Context, uuid.UUID, string, interface{}) error { return nil }
func (fixedUsage) ResetUsage(context.Context, uuid.UUID, string) error                  { return nil }

func TestExampleConfig_HasThreePlansAndUsageTables(t *testing.T) {
	cfg := ExampleConfig()
	for _, p := range []string{PlanBasic, PlanPro, PlanEnterprise} {
		if _, ok := cfg.PlanLimits[p]; !ok {
			t.Errorf("missing plan %s", p)
		}
	}
	if cfg.UsageTables["max_projects"] != "projects" || cfg.UsageTables["max_users"] != "tenant_users" {
		t.Errorf("UsageTables = %v", cfg.UsageTables)
	}
	if !cfg.EnforceLimits {
		t.Error("ExampleConfig should enforce")
	}
}

func TestChecker_PlanOfDefaultsToMetadataPlan(t *testing.T) {
	id := uuid.New()
	tn := &tenant.Tenant{ID: id}
	tn.SetPlan(PlanBasic)
	repo := &memRepo{tenants: map[uuid.UUID]*tenant.Tenant{id: tn}}
	c := NewChecker(ExampleConfig(), repo, zap.NewNop())
	c.SetUsageTracker(fixedUsage{"max_projects": 11}) // basic allows 10

	_, err := c.CheckTenant(context.Background(), id)
	var terr *tenant.TenantError
	if !errors.As(err, &terr) || terr.Code != "LIMIT_EXCEEDED" {
		t.Fatalf("want LIMIT_EXCEEDED, got %v", err)
	}
}

func TestChecker_PlanOfOverride(t *testing.T) {
	id := uuid.New()
	repo := &memRepo{tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id}}}
	cfg := ExampleConfig()
	cfg.PlanOf = func(*tenant.Tenant) string { return PlanEnterprise } // unlimited projects
	c := NewChecker(cfg, repo, zap.NewNop())
	c.SetUsageTracker(fixedUsage{"max_projects": 1000})

	snapshot, err := c.CheckTenant(context.Background(), id)
	if err != nil {
		t.Fatalf("enterprise should pass: %v", err)
	}
	if !snapshot.IsUnlimited("max_projects") {
		t.Errorf("snapshot should be the enterprise limits")
	}
}

func TestChecker_UsageReportsConfiguredLimitsOnly(t *testing.T) {
	id := uuid.New()
	tn := &tenant.Tenant{ID: id}
	tn.SetPlan(PlanBasic)
	repo := &memRepo{tenants: map[uuid.UUID]*tenant.Tenant{id: tn}}
	c := NewChecker(ExampleConfig(), repo, zap.NewNop())
	c.SetUsageTracker(fixedUsage{"max_projects": 3, "max_users": 2, "api_calls_per_month": 999})

	usage, err := c.Usage(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if usage["max_projects"] != 3 || usage["max_users"] != 2 {
		t.Errorf("usage = %v", usage)
	}
	if _, ok := usage["api_calls_per_month"]; ok {
		t.Errorf("only UsageTables keys should be reported, got %v", usage)
	}
}

// panicRepo fails the test if the checker consults the repository.
type panicRepo struct{ t *testing.T }

func (r panicRepo) Create(context.Context, *tenant.Tenant) error { return nil }
func (r panicRepo) GetByID(context.Context, uuid.UUID) (*tenant.Tenant, error) {
	r.t.Error("repository must not be consulted with enforcement off")
	return nil, errors.New("must not be called")
}
func (r panicRepo) GetBySubdomain(context.Context, string) (*tenant.Tenant, error) {
	return nil, errors.New("not found")
}
func (r panicRepo) Update(context.Context, *tenant.Tenant) error { return nil }
func (r panicRepo) Delete(context.Context, uuid.UUID) error      { return nil }
func (r panicRepo) List(context.Context, int, int) ([]*tenant.Tenant, int, error) {
	return nil, 0, nil
}
func (r panicRepo) FindByMetadata(context.Context, string, string) ([]*tenant.Tenant, error) {
	return nil, nil
}

func TestChecker_CheckTenantWithEnforcementOffReturnsEmptyWithoutRepo(t *testing.T) {
	cfg := ExampleConfig()
	cfg.EnforceLimits = false
	c := NewChecker(cfg, panicRepo{t}, zap.NewNop())

	got, err := c.CheckTenant(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("CheckTenant with enforcement off: %v", err)
	}
	if got == nil || got.Len() != 0 {
		t.Errorf("want an empty snapshot, got %v", got)
	}
	if err := c.CheckAllLimits(context.Background(), uuid.New()); err != nil {
		t.Errorf("CheckAllLimits with enforcement off: %v", err)
	}
}

func TestChecker_CheckTenantUnknownPlanIsErrorWhenEnforcing(t *testing.T) {
	id := uuid.New()
	tn := &tenant.Tenant{ID: id}
	tn.SetPlan("does-not-exist")
	repo := &memRepo{tenants: map[uuid.UUID]*tenant.Tenant{id: tn}}
	c := NewChecker(ExampleConfig(), repo, zap.NewNop())

	if _, err := c.CheckTenant(context.Background(), id); err == nil {
		t.Fatal("an unconfigured plan should be an error while enforcing")
	}
}

type failingUsage struct{ err error }

func (u failingUsage) GetCurrentUsage(context.Context, uuid.UUID, string) (interface{}, error) {
	return nil, u.err
}
func (failingUsage) IncrementUsage(context.Context, uuid.UUID, string, interface{}) error {
	return nil
}
func (failingUsage) DecrementUsage(context.Context, uuid.UUID, string, interface{}) error {
	return nil
}
func (failingUsage) ResetUsage(context.Context, uuid.UUID, string) error { return nil }

func TestChecker_UsageReturnsErrorWhenTrackerFails(t *testing.T) {
	boom := errors.New("count failed")
	c := NewChecker(ExampleConfig(), &memRepo{tenants: map[uuid.UUID]*tenant.Tenant{}}, zap.NewNop())
	c.SetUsageTracker(failingUsage{boom})

	usage, err := c.Usage(context.Background(), uuid.New())
	if !errors.Is(err, boom) {
		t.Fatalf("Usage error = %v, want it to wrap %v", err, boom)
	}
	if usage != nil {
		t.Errorf("Usage should return no map on error, got %v", usage)
	}
}

func TestContext_LimitsRoundTrip(t *testing.T) {
	l := FlexibleLimits{}
	l.Set("max_projects", LimitTypeInt, 3)
	ctx := WithLimits(context.Background(), l)
	got, ok := FromContext(ctx)
	if !ok || got.Len() != 1 {
		t.Fatalf("FromContext = %v, %v", got, ok)
	}
}

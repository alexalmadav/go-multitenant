package main

import (
	"context"
	"errors"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeStripe struct {
	created []string
	deleted []string
	fail    bool
}

func (f *fakeStripe) CreateCustomer(ctx context.Context, name, subdomain string) (string, error) {
	if f.fail {
		return "", errors.New("stripe unavailable")
	}
	id := "cus_" + subdomain
	f.created = append(f.created, id)
	return id, nil
}

func (f *fakeStripe) DeleteCustomer(ctx context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// recordingManager stores whatever UpdateTenant is given.
type recordingManager struct {
	tenant.Manager
	updated *tenant.Tenant
}

func (m *recordingManager) UpdateTenant(ctx context.Context, t *tenant.Tenant) error {
	m.updated = t
	return nil
}

func TestStripeHook_CreatesCustomerAndStoresID(t *testing.T) {
	stripe := &fakeStripe{}
	mgr := &recordingManager{}
	h := NewStripeHook(stripe, mgr)
	tn := &tenant.Tenant{ID: uuid.New(), Name: "Acme", Subdomain: "acme", Metadata: tenant.TenantMetadata{}}

	if err := h.OnTenantCreated(context.Background(), tn); err != nil {
		t.Fatal(err)
	}
	if len(stripe.created) != 1 || stripe.created[0] != "cus_acme" {
		t.Errorf("created = %v", stripe.created)
	}
	if mgr.updated == nil {
		t.Fatal("hook should persist the customer id through UpdateTenant")
	}
	if id, _ := tenant.NewStripeExtension(mgr.updated.Metadata).GetCustomerID(); id != "cus_acme" {
		t.Errorf("stored customer id = %q", id)
	}
}

func TestStripeHook_DeletesCustomerOnTenantDeleted(t *testing.T) {
	stripe := &fakeStripe{}
	h := NewStripeHook(stripe, &recordingManager{})
	tn := &tenant.Tenant{ID: uuid.New(), Metadata: tenant.TenantMetadata{}}
	tenant.NewStripeExtension(tn.Metadata).SetCustomerID("cus_x")

	if err := h.OnTenantDeleted(context.Background(), tn); err != nil {
		t.Fatal(err)
	}
	if len(stripe.deleted) != 1 || stripe.deleted[0] != "cus_x" {
		t.Errorf("deleted = %v", stripe.deleted)
	}
}

func TestStripeHook_FailureSurfacesAsHookErrorAndTenantPersists(t *testing.T) {
	// Drive the hook through a real manager with mocks so HookError wrapping is exercised.
	repo := newInMemoryRepo()
	mgr := newInMemoryManager(repo)
	stripe := &fakeStripe{fail: true}
	mgr.RegisterHook(NewStripeHook(stripe, mgr))

	err := mgr.CreateTenant(context.Background(), &tenant.Tenant{Name: "Acme", Subdomain: "acme"})
	var herr *tenant.HookError
	if !errors.As(err, &herr) || herr.Event != "created" {
		t.Fatalf("want HookError on created, got %v", err)
	}
	if repo.count() != 1 {
		t.Error("tenant should persist when the Stripe call fails")
	}
}

// memRepo is an in-memory tenant.Repository.
type memRepo struct{ tenants map[uuid.UUID]*tenant.Tenant }

func newInMemoryRepo() *memRepo { return &memRepo{tenants: map[uuid.UUID]*tenant.Tenant{}} }
func (r *memRepo) count() int   { return len(r.tenants) }

func (r *memRepo) Create(ctx context.Context, t *tenant.Tenant) error {
	c := *t
	r.tenants[t.ID] = &c
	return nil
}
func (r *memRepo) GetByID(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	t, ok := r.tenants[id]
	if !ok {
		return nil, errors.New("not found")
	}
	c := *t
	return &c, nil
}
func (r *memRepo) GetBySubdomain(ctx context.Context, sub string) (*tenant.Tenant, error) {
	for _, t := range r.tenants {
		if t.Subdomain == sub {
			c := *t
			return &c, nil
		}
	}
	return nil, errors.New("not found")
}
func (r *memRepo) Update(ctx context.Context, t *tenant.Tenant) error {
	c := *t
	r.tenants[t.ID] = &c
	return nil
}
func (r *memRepo) Delete(ctx context.Context, id uuid.UUID) error {
	if t, ok := r.tenants[id]; ok {
		t.Status = tenant.StatusCancelled
	}
	return nil
}
func (r *memRepo) List(ctx context.Context, page, perPage int) ([]*tenant.Tenant, int, error) {
	var out []*tenant.Tenant
	for _, t := range r.tenants {
		out = append(out, t)
	}
	return out, len(out), nil
}
func (r *memRepo) FindByMetadata(ctx context.Context, key, value string) ([]*tenant.Tenant, error) {
	return nil, nil
}

// memSchema is an in-memory tenant.SchemaManager.
type memSchema struct{ schemas map[uuid.UUID]bool }

func (s *memSchema) CreateTenantSchema(ctx context.Context, id uuid.UUID) error {
	s.schemas[id] = true
	return nil
}
func (s *memSchema) DropTenantSchema(ctx context.Context, id uuid.UUID) error {
	delete(s.schemas, id)
	return nil
}
func (s *memSchema) SchemaExists(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.schemas[id], nil
}
func (s *memSchema) GetSchemaName(id uuid.UUID) string                       { return "tenant_" + id.String() }
func (s *memSchema) ListTenantSchemas(ctx context.Context) ([]string, error) { return nil, nil }

// memMig applies nothing.
type memMig struct{ tenant.MigrationManager }

func (memMig) ApplyPending(ctx context.Context, id uuid.UUID) error { return nil }
func (memMig) GetAppliedMigrations(ctx context.Context, id uuid.UUID) ([]*tenant.Migration, error) {
	return nil, nil
}

func newInMemoryManager(repo *memRepo) tenant.Manager {
	return tenant.NewManager(tenant.DefaultConfig(), nil, repo, &memSchema{schemas: map[uuid.UUID]bool{}}, memMig{}, zap.NewNop())
}

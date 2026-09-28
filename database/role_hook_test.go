package database

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// fakeRoles records calls, and refuses to act on a role Ensure has not
// created - the failure the original design would have hit on every first
// provisioning.
type fakeRoles struct {
	calls   []string
	created map[uuid.UUID]bool
	err     error
}

func (f *fakeRoles) Ensure(_ context.Context, t *tenant.Tenant) error {
	f.calls = append(f.calls, "ensure:"+t.Status)
	if f.created == nil {
		f.created = map[uuid.UUID]bool{}
	}
	f.created[t.ID] = true
	return f.err
}

func (f *fakeRoles) Grant(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "grant")
	return f.err
}

func (f *fakeRoles) LockOut(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "lockout")
	if !f.created[id] {
		return errors.New("role does not exist")
	}
	return f.err
}

func (f *fakeRoles) Drop(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "drop")
	return f.err
}

// fakeSchemas embeds the interface so unused methods panic.
type fakeSchemas struct {
	tenant.SchemaManager
	exists bool
}

func (f fakeSchemas) SchemaExists(context.Context, uuid.UUID) (bool, error) { return f.exists, nil }

func newTestHook(exists bool) (*RoleHook, *fakeRoles, *[]uuid.UUID) {
	roles := &fakeRoles{}
	var evicted []uuid.UUID
	h := NewRoleHook(roles, fakeSchemas{exists: exists}, func(id uuid.UUID) { evicted = append(evicted, id) }, zap.NewNop())
	return h, roles, &evicted
}

// ProvisionTenant fires OnTenantStatusChanged before OnTenantProvisioned.
func TestRoleHookProvisioningInEitherEventOrder(t *testing.T) {
	h, roles, _ := newTestHook(true)
	tn := &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusActive}
	ctx := context.Background()

	if err := h.OnTenantStatusChanged(ctx, tn, tenant.StatusPending); err != nil {
		t.Fatalf("status change during provisioning: %v", err)
	}
	if err := h.OnTenantProvisioned(ctx, tn); err != nil {
		t.Fatalf("provisioned: %v", err)
	}
	if want := []string{"ensure:active", "ensure:active"}; !slices.Equal(roles.calls, want) {
		t.Errorf("calls = %q, want %q", roles.calls, want)
	}
}

func TestRoleHookSuspensionLocksOutAndEvicts(t *testing.T) {
	h, roles, evicted := newTestHook(true)
	tn := &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusSuspended}

	if err := h.OnTenantStatusChanged(context.Background(), tn, tenant.StatusActive); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ensure:suspended", "lockout"}; !slices.Equal(roles.calls, want) {
		t.Errorf("calls = %q, want %q", roles.calls, want)
	}
	if !slices.Equal(*evicted, []uuid.UUID{tn.ID}) {
		t.Errorf("evicted = %v, want the suspended tenant", *evicted)
	}
}

// A status change for a tenant that was never provisioned has no schema to
// grant on and no role to manage.
func TestRoleHookIgnoresUnprovisionedTenants(t *testing.T) {
	h, roles, _ := newTestHook(false)
	if err := h.OnTenantStatusChanged(context.Background(), &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusSuspended}, tenant.StatusPending); err != nil {
		t.Fatal(err)
	}
	if len(roles.calls) != 0 {
		t.Errorf("calls = %q, want none", roles.calls)
	}
}

func TestRoleHookDeletionEvictsThenDrops(t *testing.T) {
	h, roles, evicted := newTestHook(true)
	tn := &tenant.Tenant{ID: uuid.New()}
	if err := h.OnTenantDeleted(context.Background(), tn); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(roles.calls, []string{"drop"}) || !slices.Equal(*evicted, []uuid.UUID{tn.ID}) {
		t.Errorf("calls = %q, evicted = %v; want drop, and the tenant evicted", roles.calls, *evicted)
	}
}

func TestRoleHookName(t *testing.T) {
	h, _, _ := newTestHook(true)
	if h.Name() != "role_isolation" {
		t.Errorf("Name() = %q", h.Name())
	}
}

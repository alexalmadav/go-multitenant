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

// fakeRoles records calls, in order, in one log shared with the hook's evict
// callback. Like the real RoleManager, LockOut and Drop succeed for a role that
// does not exist. Each method fails with its own error field, or with err.
type fakeRoles struct {
	calls     []string
	err       error
	ensureErr error
	lockErr   error
	dropErr   error
}

func (f *fakeRoles) Ensure(_ context.Context, t *tenant.Tenant) error {
	f.calls = append(f.calls, "ensure:"+t.Status)
	return errors.Join(f.err, f.ensureErr)
}

func (f *fakeRoles) Grant(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "grant")
	return f.err
}

func (f *fakeRoles) LockOut(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "lockout")
	return errors.Join(f.err, f.lockErr)
}

func (f *fakeRoles) Drop(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "drop")
	return errors.Join(f.err, f.dropErr)
}

// fakeSchemas embeds the interface so unused methods panic.
type fakeSchemas struct {
	tenant.SchemaManager
	exists bool
	err    error
}

func (f fakeSchemas) SchemaExists(context.Context, uuid.UUID) (bool, error) { return f.exists, f.err }

func newTestHook(schemas fakeSchemas, roles *fakeRoles) *RoleHook {
	return NewRoleHook(roles, schemas, func(uuid.UUID) { roles.calls = append(roles.calls, "evict") }, zap.NewNop())
}

// ProvisionTenant fires OnTenantStatusChanged before OnTenantProvisioned.
func TestRoleHookProvisioningInEitherEventOrder(t *testing.T) {
	roles := &fakeRoles{}
	h := newTestHook(fakeSchemas{exists: true}, roles)
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

// Suspension and cancellation lock out and evict before anything that can fail.
func TestRoleHookLocksOutAndEvictsSuspendedAndCancelled(t *testing.T) {
	for _, status := range []string{tenant.StatusSuspended, tenant.StatusCancelled} {
		roles := &fakeRoles{}
		h := newTestHook(fakeSchemas{exists: true}, roles)
		tn := &tenant.Tenant{ID: uuid.New(), Status: status}

		if err := h.OnTenantStatusChanged(context.Background(), tn, tenant.StatusActive); err != nil {
			t.Fatal(err)
		}
		if want := []string{"lockout", "evict", "ensure:" + status}; !slices.Equal(roles.calls, want) {
			t.Errorf("%s: calls = %q, want %q", status, roles.calls, want)
		}
	}
}

func TestRoleHookLocksOutWhenSchemaExistsFails(t *testing.T) {
	boom := errors.New("schema lookup failed")
	roles := &fakeRoles{}
	h := newTestHook(fakeSchemas{err: boom}, roles)

	err := h.OnTenantStatusChanged(context.Background(), &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusSuspended}, tenant.StatusActive)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the schema error", err)
	}
	if want := []string{"lockout", "evict"}; !slices.Equal(roles.calls, want) {
		t.Errorf("calls = %q, want %q", roles.calls, want)
	}
}

func TestRoleHookLocksOutWhenEnsureFails(t *testing.T) {
	boom := errors.New("ensure failed")
	roles := &fakeRoles{ensureErr: boom}
	h := newTestHook(fakeSchemas{exists: true}, roles)

	err := h.OnTenantStatusChanged(context.Background(), &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusSuspended}, tenant.StatusActive)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the ensure error", err)
	}
	if want := []string{"lockout", "evict", "ensure:suspended"}; !slices.Equal(roles.calls, want) {
		t.Errorf("calls = %q, want %q", roles.calls, want)
	}
}

func TestRoleHookEvictsWhenLockOutFails(t *testing.T) {
	boom := errors.New("lockout failed")
	roles := &fakeRoles{lockErr: boom}
	h := newTestHook(fakeSchemas{exists: true}, roles)

	err := h.OnTenantStatusChanged(context.Background(), &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusSuspended}, tenant.StatusActive)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the lockout error", err)
	}
	if want := []string{"lockout", "evict", "ensure:suspended"}; !slices.Equal(roles.calls, want) {
		t.Errorf("calls = %q, want %q", roles.calls, want)
	}
}

// A status change for a tenant that was never provisioned has no schema to
// grant on and no role to manage.
func TestRoleHookIgnoresUnprovisionedTenants(t *testing.T) {
	roles := &fakeRoles{}
	h := newTestHook(fakeSchemas{exists: false}, roles)
	if err := h.OnTenantStatusChanged(context.Background(), &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusActive}, tenant.StatusPending); err != nil {
		t.Fatal(err)
	}
	if len(roles.calls) != 0 {
		t.Errorf("calls = %q, want none", roles.calls)
	}
}

func TestRoleHookDeletionEvictsThenDrops(t *testing.T) {
	roles := &fakeRoles{}
	h := newTestHook(fakeSchemas{exists: true}, roles)
	if err := h.OnTenantDeleted(context.Background(), &tenant.Tenant{ID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"evict", "drop"}; !slices.Equal(roles.calls, want) {
		t.Errorf("calls = %q, want %q", roles.calls, want)
	}
}

func TestRoleHookName(t *testing.T) {
	h := newTestHook(fakeSchemas{exists: true}, &fakeRoles{})
	if h.Name() != "role_isolation" {
		t.Errorf("Name() = %q", h.Name())
	}
}

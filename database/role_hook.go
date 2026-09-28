package database

import (
	"context"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// RoleHook keeps each tenant's role in step with its registry record.
//
// Every lifecycle event calls Ensure rather than a narrower step, because
// Ensure is idempotent and order-independent: ProvisionTenant fires
// OnTenantStatusChanged before OnTenantProvisioned, so a hook that created the
// role in one event and toggled LOGIN in the other would fail on every first
// provisioning.
type RoleHook struct {
	tenant.BaseHook
	roles   TenantRoles
	schemas tenant.SchemaManager
	evict   func(uuid.UUID)
	logger  *zap.Logger
}

// NewRoleHook returns the isolation hook. evict closes a tenant's warm
// connection pool in this process.
func NewRoleHook(roles TenantRoles, schemas tenant.SchemaManager, evict func(uuid.UUID), logger *zap.Logger) *RoleHook {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RoleHook{roles: roles, schemas: schemas, evict: evict, logger: logger.Named("role_hook")}
}

// Name implements tenant.Hook.
func (h *RoleHook) Name() string { return "role_isolation" }

// OnTenantProvisioned ensures the new tenant's role.
func (h *RoleHook) OnTenantProvisioned(ctx context.Context, t *tenant.Tenant) error {
	return h.roles.Ensure(ctx, t)
}

// OnTenantStatusChanged ensures the role for the new status, and for a
// suspended or cancelled tenant also ends its sessions and closes its pool.
// A tenant with no schema has not been provisioned, so there is nothing to do.
func (h *RoleHook) OnTenantStatusChanged(ctx context.Context, t *tenant.Tenant, _ string) error {
	provisioned, err := h.schemas.SchemaExists(ctx, t.ID)
	if err != nil {
		return err
	}
	if !provisioned {
		return nil
	}
	if err := h.roles.Ensure(ctx, t); err != nil {
		return err
	}
	if t.Status == tenant.StatusSuspended || t.Status == tenant.StatusCancelled {
		if err := h.roles.LockOut(ctx, t.ID); err != nil {
			return err
		}
		h.evict(t.ID)
	}
	return nil
}

// OnTenantDeleted closes the tenant's pool and drops its role.
func (h *RoleHook) OnTenantDeleted(ctx context.Context, t *tenant.Tenant) error {
	h.evict(t.ID)
	return h.roles.Drop(ctx, t.ID)
}

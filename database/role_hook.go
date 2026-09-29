package database

import (
	"context"
	"errors"

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

// OnTenantStatusChanged ensures the role for the new status. For a suspended
// or cancelled tenant it first ends the tenant's sessions and closes its pool,
// and only then looks at the schema and the role: a lockout must not depend on
// anything that can fail, and the pool is closed whether or not LockOut
// succeeded. A tenant with no schema has not been provisioned, so there is no
// role to ensure. Every error is returned.
func (h *RoleHook) OnTenantStatusChanged(ctx context.Context, t *tenant.Tenant, _ string) error {
	var errs []error
	if t.Status == tenant.StatusSuspended || t.Status == tenant.StatusCancelled {
		if err := h.roles.LockOut(ctx, t.ID); err != nil {
			h.logger.Warn("Lock out of tenant role failed", zap.String("tenant_id", t.ID.String()), zap.Error(err))
			errs = append(errs, err)
		}
		h.evict(t.ID)
	}
	provisioned, err := h.schemas.SchemaExists(ctx, t.ID)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if provisioned {
		if err := h.roles.Ensure(ctx, t); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// OnTenantDeleted closes the tenant's pool and drops its role.
func (h *RoleHook) OnTenantDeleted(ctx context.Context, t *tenant.Tenant) error {
	h.evict(t.ID)
	err := h.roles.Drop(ctx, t.ID)
	if err != nil {
		h.logger.Warn("Drop of tenant role failed", zap.String("tenant_id", t.ID.String()), zap.Error(err))
	}
	return err
}

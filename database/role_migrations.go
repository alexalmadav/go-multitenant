package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

// grantingMigrations is a MigrationManager that reapplies tenant role grants
// after every migration run. ALTER DEFAULT PRIVILEGES covers tables the admin
// role creates; this covers tables created any other way, provided the admin
// role owns them or is a member of the role that does.
type grantingMigrations struct {
	tenant.MigrationManager
	roles   TenantRoles
	repo    tenant.Repository
	schemas tenant.SchemaManager
}

// NewGrantingMigrationManager wraps inner so that each migration run is
// followed by Grant for the tenants it touched, whether or not the run failed: a
// failed migration may have created tables first.
func NewGrantingMigrationManager(inner tenant.MigrationManager, roles TenantRoles, repo tenant.Repository, schemas tenant.SchemaManager) tenant.MigrationManager {
	return &grantingMigrations{MigrationManager: inner, roles: roles, repo: repo, schemas: schemas}
}

func (g *grantingMigrations) ApplyMigration(ctx context.Context, tenantID uuid.UUID, m *tenant.Migration) error {
	err := g.MigrationManager.ApplyMigration(ctx, tenantID, m)
	return errors.Join(err, g.roles.Grant(ctx, tenantID))
}

func (g *grantingMigrations) ApplyPending(ctx context.Context, tenantID uuid.UUID) error {
	err := g.MigrationManager.ApplyPending(ctx, tenantID)
	return errors.Join(err, g.roles.Grant(ctx, tenantID))
}

func (g *grantingMigrations) ApplyToAllTenants(ctx context.Context, m *tenant.Migration) error {
	return errors.Join(g.MigrationManager.ApplyToAllTenants(ctx, m), g.grantAll(ctx))
}

func (g *grantingMigrations) ApplyPendingToAllTenants(ctx context.Context) error {
	return errors.Join(g.MigrationManager.ApplyPendingToAllTenants(ctx), g.grantAll(ctx))
}

// grantAll grants for every provisioned tenant, even after a partly failed run, since
// the tenants it succeeded for have new tables.
func (g *grantingMigrations) grantAll(ctx context.Context) error {
	return ForEachProvisionedTenant(ctx, g.repo, g.schemas, func(t *tenant.Tenant) error {
		if err := g.roles.Grant(ctx, t.ID); err != nil {
			return fmt.Errorf("tenant %s: %w", t.ID, err)
		}
		return nil
	})
}

const listPageSize = 100

// ForEachProvisionedTenant calls fn for every tenant whose schema exists,
// which is every tenant that has been provisioned. Cancelled tenants are not
// listed by the repository and so are not visited. Errors are collected
// rather than stopping at the first.
func ForEachProvisionedTenant(ctx context.Context, repo tenant.Repository, schemas tenant.SchemaManager, fn func(*tenant.Tenant) error) error {
	return forEachTenant(ctx, repo, func(t *tenant.Tenant) error {
		provisioned, err := schemas.SchemaExists(ctx, t.ID)
		if err != nil || !provisioned {
			return err
		}
		return fn(t)
	})
}

// forEachTenant calls fn for every tenant the repository lists, page by page,
// collecting errors rather than stopping at the first.
func forEachTenant(ctx context.Context, repo tenant.Repository, fn func(*tenant.Tenant) error) error {
	var errs []error
	for page, seen := 1, 0; ; page++ {
		ts, total, err := repo.List(ctx, page, listPageSize)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, t := range ts {
			if err := fn(t); err != nil {
				errs = append(errs, err)
			}
		}
		seen += len(ts)
		if len(ts) == 0 || seen >= total {
			return errors.Join(errs...)
		}
	}
}

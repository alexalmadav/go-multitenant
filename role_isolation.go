package multitenant

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/alexalmadav/go-multitenant/database"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

// ErrRoleIsolationDisabled is returned by the role isolation methods when the
// Config did not select tenant.IsolationRole.
var ErrRoleIsolationDisabled = errors.New("multitenant: role isolation is not enabled")

// roleIsolation holds what role mode adds to a MultiTenant.
type roleIsolation struct {
	roles   *database.RoleManager
	creds   *tenant.CredentialSource
	pools   *tenant.Pools
	repo    tenant.Repository
	schemas tenant.SchemaManager
}

func setupRoleIsolation(ctx context.Context, cfg tenant.RoleIsolationConfig, db *sql.DB, schemas tenant.SchemaManager, repo tenant.Repository, logger *zap.Logger) (*roleIsolation, error) {
	creds := tenant.NewCredentialSource(cfg, schemas.GetSchemaName)
	roles := database.NewRoleManager(db, schemas, creds, logger)
	if err := roles.CheckPrerequisites(ctx); err != nil {
		return nil, err
	}
	open, err := tenantPoolOpener(cfg, creds, logger)
	if err != nil {
		return nil, err
	}
	pools, err := tenant.NewPools(tenant.PoolsConfig{
		MaxConns:          cfg.MaxConns,
		PerTenantMaxConns: cfg.PerTenantMaxConns,
		MaxWarmTenants:    cfg.MaxWarmTenants,
		IdleTimeout:       cfg.IdleTimeout,
	}, open, logger)
	if err != nil {
		return nil, err
	}
	return &roleIsolation{roles: roles, creds: creds, pools: pools, repo: repo, schemas: schemas}, nil
}

// tenantPoolOpener opens a tenant's pool at cfg.TenantDSN logged in as the
// tenant's role. It tries each candidate credential in turn, moving on only
// when the failure is an authentication failure, which is what makes
// rotation with PreviousSecrets work, behind PgBouncer as well as directly.
func tenantPoolOpener(cfg tenant.RoleIsolationConfig, creds *tenant.CredentialSource, logger *zap.Logger) (tenant.PoolOpener, error) {
	base, err := pgx.ParseConfig(cfg.TenantDSN)
	if err != nil {
		return nil, fmt.Errorf("multitenant: parse TenantDSN: %w", err)
	}
	return func(ctx context.Context, tenantID uuid.UUID) (*sql.DB, error) {
		candidates, err := creds.Candidates(tenantID)
		if err != nil {
			return nil, err
		}
		rc := &rotatingConnector{role: candidates[0].User, logger: logger}
		for _, c := range candidates {
			connCfg := base.Copy()
			connCfg.User, connCfg.Password = c.User, c.Password
			connCfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
			rc.connectors = append(rc.connectors, stdlib.GetConnector(*connCfg))
		}
		db := sql.OpenDB(rc)
		db.SetMaxOpenConns(cfg.PerTenantMaxConns)
		db.SetMaxIdleConns(1)
		db.SetConnMaxIdleTime(cfg.IdleTimeout)
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			return nil, fmt.Errorf("multitenant: tenant role %s could not log in at TenantDSN; check that EnsureTenantRoles has run, "+
				"that Secret matches, and, behind PgBouncer with an auth_file, that the file is current: %w", candidates[0].User, err)
		}
		return db, nil
	}, nil
}

// rotatingConnector is a driver.Connector over one connector per candidate
// credential. good is the index of the candidate that last logged in.
type rotatingConnector struct {
	role       string
	logger     *zap.Logger
	connectors []driver.Connector
	good       atomic.Int64
}

func (r *rotatingConnector) Driver() driver.Driver { return r.connectors[0].Driver() }

// Connect tries the candidate that last worked, then the others in order,
// moving on only after an authentication failure.
func (r *rotatingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	first := int(r.good.Load())
	order := make([]int, 0, len(r.connectors))
	order = append(order, first)
	for i := range r.connectors {
		if i != first {
			order = append(order, i)
		}
	}
	var lastErr error
	for _, i := range order {
		conn, err := r.connectors[i].Connect(ctx)
		if err == nil {
			if old := int(r.good.Swap(int64(i))); old != i && i > 0 {
				r.logger.Warn("A tenant role authenticated with a previous secret; run RotateTenantCredentials",
					zap.String("role", r.role), zap.Int("previous_secret", i))
			}
			return conn, nil
		}
		lastErr = err
		if !tenant.IsAuthFailure(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// EnsureTenantRoles brings every provisioned tenant's role to the state its
// registry record calls for, creating missing roles. Run it once before
// switching a deployment to role isolation; it is also the repair tool.
// Tenants created but not yet provisioned, and cancelled tenants, are skipped.
func (mt *MultiTenant) EnsureTenantRoles(ctx context.Context) error {
	ri := mt.roleIsolation
	if ri == nil {
		return ErrRoleIsolationDisabled
	}
	return database.ForEachProvisionedTenant(ctx, ri.repo, ri.schemas, func(t *tenant.Tenant) error {
		if err := ri.roles.Ensure(ctx, t); err != nil {
			return fmt.Errorf("tenant %s: %w", t.ID, err)
		}
		return nil
	})
}

// RotateTenantCredentials re-keys every provisioned tenant's role to the
// current Secret. Deploy the new Secret with the old one in PreviousSecrets
// first, so connections keep authenticating throughout; with a PgBouncer
// auth_file, regenerate it and reload PgBouncer immediately afterwards.
func (mt *MultiTenant) RotateTenantCredentials(ctx context.Context) error {
	return mt.EnsureTenantRoles(ctx)
}

// PgBouncerAuthFile writes PgBouncer auth_file entries for every active,
// provisioned tenant. The output holds working credentials; treat it as a
// secret. See database.WriteAuthFile.
func (mt *MultiTenant) PgBouncerAuthFile(ctx context.Context, w io.Writer) error {
	ri := mt.roleIsolation
	if ri == nil {
		return ErrRoleIsolationDisabled
	}
	return database.WriteAuthFile(ctx, w, ri.repo, ri.schemas, ri.creds)
}

// TenantPoolStats reports the tenant connection pools. The second result is
// false when role isolation is not enabled.
func (mt *MultiTenant) TenantPoolStats() (tenant.PoolStats, bool) {
	if mt.roleIsolation == nil {
		return tenant.PoolStats{}, false
	}
	return mt.roleIsolation.pools.Stats(), true
}

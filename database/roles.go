package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ErrRolePrerequisites reports a database that cannot run role isolation.
var ErrRolePrerequisites = errors.New("database: role isolation prerequisites not met")

// TenantRoles is what the isolation hook and the granting migration manager
// need from RoleManager.
type TenantRoles interface {
	Ensure(ctx context.Context, t *tenant.Tenant) error
	Grant(ctx context.Context, tenantID uuid.UUID) error
	LockOut(ctx context.Context, tenantID uuid.UUID) error
	Drop(ctx context.Context, tenantID uuid.UUID) error
}

// RoleManager creates and maintains one PostgreSQL login role per tenant, on
// the admin connection. Every method is idempotent. All of them must run as
// the same admin role: from PostgreSQL 16 a CREATEROLE role can alter and
// drop only the roles it created.
type RoleManager struct {
	db      *sql.DB
	schemas tenant.SchemaManager
	creds   *tenant.CredentialSource
	logger  *zap.Logger
}

// NewRoleManager returns a RoleManager that issues role DDL on db.
func NewRoleManager(db *sql.DB, schemas tenant.SchemaManager, creds *tenant.CredentialSource, logger *zap.Logger) *RoleManager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RoleManager{db: db, schemas: schemas, creds: creds, logger: logger.Named("role_manager")}
}

var _ TenantRoles = (*RoleManager)(nil)

// CheckPrerequisites reports whether the server and the admin role can run
// role isolation: PostgreSQL 15 or later, and CREATEROLE and pg_signal_backend
// for the admin role, or superuser.
func (rm *RoleManager) CheckPrerequisites(ctx context.Context) error {
	var version int
	if err := rm.db.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		return fmt.Errorf("database: read server version: %w", err)
	}
	if version < 150000 {
		return fmt.Errorf("%w: PostgreSQL %d, need 15 or later: before 15 every role can create tables in public", ErrRolePrerequisites, version/10000)
	}
	var super, createRole, signal bool
	if err := rm.db.QueryRowContext(ctx,
		`SELECT rolsuper, rolcreaterole, pg_has_role(current_user, 'pg_signal_backend', 'USAGE')
		   FROM pg_roles WHERE rolname = current_user`).Scan(&super, &createRole, &signal); err != nil {
		return fmt.Errorf("database: read admin role attributes: %w", err)
	}
	if !super && !createRole {
		return fmt.Errorf("%w: the admin role needs CREATEROLE to manage tenant roles", ErrRolePrerequisites)
	}
	if !super && !signal {
		return fmt.Errorf("%w: the admin role needs membership in pg_signal_backend to end a suspended tenant's sessions", ErrRolePrerequisites)
	}
	return nil
}

// Ensure brings t's role to the state its record calls for: created if
// missing, with the current password, LOGIN if the tenant is active in the
// registry (re-read under the role lock, not taken from t) and NOLOGIN
// otherwise, in which case its sessions are ended too, and DML on every table and sequence in its schema, now
// and in future. The tenant's schema must exist.
func (rm *RoleManager) Ensure(ctx context.Context, t *tenant.Tenant) error {
	role := quoteIdent(rm.creds.RoleName(t.ID))
	schema := quoteIdent(rm.schemas.GetSchemaName(t.ID))
	cred, err := rm.creds.Current(t.ID)
	if err != nil {
		return err
	}
	password := "NULL"
	if cred.Password != "" {
		verifier, err := tenant.NewSCRAMVerifier(cred.Password)
		if err != nil {
			return err
		}
		password = quoteLiteral(verifier)
	}

	tx, err := rm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("database: ensure role %s: %w", cred.User, err)
	}
	defer tx.Rollback()

	// Serialize concurrent Ensure calls for the same role: without this, two
	// callers can both see the role as missing and one CREATE ROLE fails.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, cred.User); err != nil {
		return fmt.Errorf("database: ensure role %s: %w", cred.User, err)
	}

	// Decide LOGIN from the registry, not from t: t is a snapshot that a
	// suspension on another instance may have overtaken. A LockOut that
	// committed before this lock is visible here; one that comes later waits
	// for this transaction and then applies NOLOGIN.
	var status string
	switch err := tx.QueryRowContext(ctx, `SELECT status FROM public.tenants WHERE id = $1`, t.ID).Scan(&status); {
	case errors.Is(err, sql.ErrNoRows):
		// A tenant with no record is not active.
	case err != nil:
		return fmt.Errorf("database: ensure role %s: read tenant status: %w", cred.User, err)
	}
	active := status == string(tenant.StatusActive)
	login := "NOLOGIN"
	if active {
		login = "LOGIN"
	}

	exists, err := roleExists(ctx, tx, cred.User)
	if err != nil {
		return err
	}
	attrs, err := lockedDownAttributes(ctx, tx)
	if err != nil {
		return err
	}
	stmts := []string{}
	if !exists {
		stmts = append(stmts, "CREATE ROLE "+role+" NOINHERIT NOCREATEDB NOCREATEROLE")
	}
	stmts = append(stmts,
		// Converge attributes of a role that already exists, too.
		"ALTER ROLE "+role+" "+attrs,
		"ALTER ROLE "+role+" PASSWORD "+password,
		"ALTER ROLE "+role+" "+login,
		"GRANT USAGE ON SCHEMA "+schema+" TO "+role,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA "+schema+" GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "+role,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA "+schema+" GRANT USAGE, SELECT ON SEQUENCES TO "+role,
	)
	stmts = append(stmts, backfillGrants(schema, role)...)
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("database: ensure role %s: %w", cred.User, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("database: ensure role %s: %w", cred.User, err)
	}
	if !active {
		if _, err := rm.terminateSessions(ctx, cred.User); err != nil {
			return err
		}
	}
	return nil
}

// Grant reapplies DML grants on every table and sequence in the tenant's
// schema. It does nothing if the tenant has no role yet.
func (rm *RoleManager) Grant(ctx context.Context, tenantID uuid.UUID) error {
	name := rm.creds.RoleName(tenantID)
	exists, err := roleExists(ctx, rm.db, name)
	if err != nil || !exists {
		return err
	}
	schema, role := quoteIdent(rm.schemas.GetSchemaName(tenantID)), quoteIdent(name)
	for _, s := range backfillGrants(schema, role) {
		if _, err := rm.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("database: grant to %s: %w", name, err)
		}
	}
	return nil
}

// LockOut stops the tenant's role from logging in and ends its sessions,
// including connections a pooler holds on its behalf.
func (rm *RoleManager) LockOut(ctx context.Context, tenantID uuid.UUID) error {
	name := rm.creds.RoleName(tenantID)
	exists, err := roleExists(ctx, rm.db, name)
	if err != nil || !exists {
		return err
	}
	// Under the same lock as Ensure, so a stale Ensure cannot re-enable the
	// login after this commits.
	tx, err := rm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("database: lock out %s: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, name); err != nil {
		return fmt.Errorf("database: lock out %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, "ALTER ROLE "+quoteIdent(name)+" NOLOGIN"); err != nil {
		return fmt.Errorf("database: lock out %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("database: lock out %s: %w", name, err)
	}
	ended, err := rm.terminateSessions(ctx, name)
	if err != nil {
		return err
	}
	rm.logger.Info("Locked out tenant role", zap.String("role", name), zap.Int("sessions_ended", ended))
	return nil
}

// terminateSessions ends every session of role, including connections a
// pooler holds on its behalf, and returns how many it ended.
func (rm *RoleManager) terminateSessions(ctx context.Context, role string) (int, error) {
	var ended int
	if err := rm.db.QueryRowContext(ctx,
		`SELECT count(*) FILTER (WHERE pg_terminate_backend(pid)) FROM pg_stat_activity WHERE usename = $1`, role).Scan(&ended); err != nil {
		return 0, fmt.Errorf("database: end sessions of %s: %w", role, err)
	}
	return ended, nil
}

// Drop locks the tenant's role out, revokes everything granted to it, and
// drops it. It does nothing if the role does not exist.
func (rm *RoleManager) Drop(ctx context.Context, tenantID uuid.UUID) error {
	name := rm.creds.RoleName(tenantID)
	exists, err := roleExists(ctx, rm.db, name)
	if err != nil || !exists {
		return err
	}
	if err := rm.LockOut(ctx, tenantID); err != nil {
		return err
	}
	role := quoteIdent(name)
	for _, s := range []string{"DROP OWNED BY " + role, "DROP ROLE " + role} {
		if _, err := rm.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("database: drop role %s: %w", name, err)
		}
	}
	return nil
}

// lockedDownAttributes returns the ALTER ROLE attribute clauses that strip a
// tenant role of privileges. PostgreSQL 16 lets a non-superuser CREATEROLE
// admin change SUPERUSER, CREATEDB, REPLICATION and BYPASSRLS only if it holds
// that attribute itself (even for a no-op), so each clause is included only
// when the admin may issue it. NOINHERIT and NOCREATEROLE are always allowed.
func lockedDownAttributes(ctx context.Context, q queryRower) (string, error) {
	var super, createDB, replication, bypassRLS bool
	if err := q.QueryRowContext(ctx,
		`SELECT rolsuper, rolcreatedb, rolreplication, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&super, &createDB, &replication, &bypassRLS); err != nil {
		return "", fmt.Errorf("database: read admin role attributes: %w", err)
	}
	attrs := []string{"NOINHERIT", "NOCREATEROLE"}
	if super {
		attrs = append(attrs, "NOSUPERUSER")
	}
	if super || createDB {
		attrs = append(attrs, "NOCREATEDB")
	}
	if super || replication {
		attrs = append(attrs, "NOREPLICATION")
	}
	if super || bypassRLS {
		attrs = append(attrs, "NOBYPASSRLS")
	}
	return strings.Join(attrs, " "), nil
}

func backfillGrants(schema, role string) []string {
	return []string{
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA " + schema + " TO " + role,
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA " + schema + " TO " + role,
	}
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func roleExists(ctx context.Context, q queryRower, name string) (bool, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, name).Scan(&exists); err != nil {
		return false, fmt.Errorf("database: look up role %s: %w", name, err)
	}
	return exists, nil
}

func quoteIdent(s string) string   { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func quoteLiteral(s string) string { return `'` + strings.ReplaceAll(s, `'`, `''`) + `'` }

package multitenant

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant/database"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

var testRoleSecret = bytes.Repeat([]byte("r"), tenant.MinRoleSecretLen)

// roleTestTenantDSN is where tenant roles log in: PgBouncer in the CI jobs
// that set ROLE_TEST_TENANT_DSN, otherwise the test database directly.
func roleTestTenantDSN() string {
	if dsn := os.Getenv("ROLE_TEST_TENANT_DSN"); dsn != "" {
		return dsn
	}
	return getTestDatabaseURL()
}

func testRoleName(id uuid.UUID) string {
	return "tenant_" + strings.ReplaceAll(id.String(), "-", "_")
}

// connectAsRole opens one connection as a tenant role, with the library's
// driver settings.
func connectAsRole(t *testing.T, dsn string, c tenant.RoleCredentials) (*pgx.Conn, error) {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = c.User, c.Password
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	return pgx.ConnectConfig(context.Background(), cfg)
}

// dropTestRoles removes tenant roles, which are cluster-wide and so outlive
// the tenants' schemas.
func dropTestRoles(db *sql.DB, ids []uuid.UUID) {
	for _, id := range ids {
		role := `"` + testRoleName(id) + `"`
		_, _ = db.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1", testRoleName(id))
		_, _ = db.Exec("DROP OWNED BY " + role)
		_, _ = db.Exec("DROP ROLE IF EXISTS " + role)
	}
}

func wantSQLState(t *testing.T, what string, err error, code string) {
	t.Helper()
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != code {
		t.Errorf("%s: got %v, want SQLSTATE %s", what, err, code)
	}
}

// TestIntegration_RoleIsolation_Guarantees checks the spec's five success
// criteria on a tenant role's own connection.
func TestIntegration_RoleIsolation_Guarantees(t *testing.T) {
	db := setupTestDatabase(t)
	ctx := context.Background()
	mt, err := New(testConfig(getTestDatabaseURL()))
	if err != nil {
		t.Fatal(err)
	}
	defer mt.Close()

	a, b := uuid.New(), uuid.New()
	defer cleanupTestData(db, []uuid.UUID{a, b})
	defer dropTestRoles(db, []uuid.UUID{a, b})
	for i, id := range []uuid.UUID{a, b} {
		if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "guarantee", Subdomain: []string{"guarantee-a", "guarantee-b"}[i]}); err != nil {
			t.Fatal(err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	schemas := database.NewSchemaManager(db, zap.NewNop(), "tenant_")
	creds := tenant.NewCredentialSource(tenant.RoleIsolationConfig{Secret: testRoleSecret}, schemas.GetSchemaName)
	roles := database.NewRoleManager(db, schemas, creds, zap.NewNop())
	if err := roles.CheckPrerequisites(ctx); err != nil {
		t.Fatalf("CheckPrerequisites: %v", err)
	}
	for _, id := range []uuid.UUID{a, b} {
		t.Logf("ensuring %s", testRoleName(id))
		tn, err := mt.Manager.GetTenant(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := roles.Ensure(ctx, tn); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if err := roles.Ensure(ctx, tn); err != nil {
			t.Fatalf("Ensure is not idempotent: %v", err)
		}
	}

	// A table a later migration adds, with a sequence, created by the admin
	// role after the tenant role exists: default privileges must cover it.
	if err := mt.Migrations.ApplyMigration(ctx, a, &tenant.Migration{
		Version: "900", Name: "widgets",
		SQL: "CREATE TABLE widgets (id bigserial PRIMARY KEY, name text NOT NULL)",
	}); err != nil {
		t.Fatal(err)
	}

	credA, err := creds.Current(a)
	if err != nil {
		t.Fatal(err)
	}
	// Straight to PostgreSQL, even in the PgBouncer jobs: these guarantees are
	// enforced by the database, whatever sits in front of it, and this test
	// renders no PgBouncer auth file.
	conn, err := connectAsRole(t, getTestDatabaseURL(), credA)
	if err != nil {
		t.Fatalf("tenant a's role could not log in: %v", err)
	}
	defer conn.Close(ctx)
	sa, sb := `"`+testRoleName(a)+`"`, `"`+testRoleName(b)+`"`
	exec := func(sql string) error { _, err := conn.Exec(ctx, sql); return err }

	// 1. Another tenant's schema, read and written by qualified name.
	wantSQLState(t, "qualified read of b", exec("SELECT count(*) FROM "+sb+".projects"), "42501")
	wantSQLState(t, "qualified write of b", exec("INSERT INTO "+sb+".projects (name) VALUES ('x')"), "42501")
	// 2. Becoming another tenant.
	wantSQLState(t, "SET ROLE b", exec("SET ROLE "+sb), "42501")
	// 3. The registry.
	wantSQLState(t, "read public.tenants", exec("SELECT count(*) FROM public.tenants"), "42501")
	wantSQLState(t, "read public.tenant_migrations", exec("SELECT count(*) FROM public.tenant_migrations"), "42501")
	// 4. DDL, in its own schema and in public.
	wantSQLState(t, "CREATE TABLE in own schema", exec("CREATE TABLE "+sa+".evil (id int)"), "42501")
	wantSQLState(t, "CREATE TABLE in public", exec("CREATE TABLE public.evil (id int)"), "42501")
	wantSQLState(t, "DROP own table", exec("DROP TABLE "+sa+".projects"), "42501")
	wantSQLState(t, "TRUNCATE own table", exec("TRUNCATE "+sa+".projects"), "42501")
	// 5. Its own data, including a sequence and a trigger the admin owns.
	for _, stmt := range []string{
		"INSERT INTO " + sa + ".projects (name) VALUES ('mine')",
		"SELECT count(*) FROM " + sa + ".projects",
		"UPDATE " + sa + ".projects SET name = 'still mine'",
		"DELETE FROM " + sa + ".projects",
		"INSERT INTO " + sa + ".widgets (name) VALUES ('uses the sequence')",
	} {
		if err := exec(stmt); err != nil {
			t.Errorf("own DML %q: %v", stmt, err)
		}
	}
}

package multitenant

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

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
	requirePasswordAuth(t)
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
	if err := exec("RESET ROLE"); err != nil {
		t.Errorf("RESET ROLE: %v", err)
	}
	wantSQLState(t, "qualified read of b after RESET ROLE", exec("SELECT count(*) FROM "+sb+".projects"), "42501")
	// 3. The registry.
	wantSQLState(t, "read public.tenants", exec("SELECT count(*) FROM public.tenants"), "42501")
	wantSQLState(t, "read public.tenant_migrations", exec("SELECT count(*) FROM public.tenant_migrations"), "42501")
	// 4. DDL, in its own schema and in public.
	wantSQLState(t, "CREATE TABLE in own schema", exec("CREATE TABLE "+sa+".evil (id int)"), "42501")
	wantSQLState(t, "CREATE TABLE in public", exec("CREATE TABLE public.evil (id int)"), "42501")
	wantSQLState(t, "ALTER own table", exec("ALTER TABLE "+sa+".projects ADD COLUMN x int"), "42501")
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

// provisionRoleTestTenant creates and provisions one tenant, and returns a
// RoleManager for it.
func provisionRoleTestTenant(t *testing.T, db *sql.DB, mt *MultiTenant, subdomain string) (uuid.UUID, *database.RoleManager) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	t.Cleanup(func() { cleanupTestData(db, []uuid.UUID{id}) })
	t.Cleanup(func() { dropTestRoles(db, []uuid.UUID{id}) })
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "roles", Subdomain: subdomain}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatal(err)
	}
	schemas := database.NewSchemaManager(db, zap.NewNop(), "tenant_")
	creds := tenant.NewCredentialSource(tenant.RoleIsolationConfig{Secret: testRoleSecret}, schemas.GetSchemaName)
	return id, database.NewRoleManager(db, schemas, creds, zap.NewNop())
}

// TestIntegration_RoleManager_EnsureConcurrent runs Ensure for one tenant from
// several goroutines on a fresh role: every call must succeed.
func TestIntegration_RoleManager_EnsureConcurrent(t *testing.T) {
	db := setupTestDatabase(t)
	ctx := context.Background()
	mt, err := New(testConfig(getTestDatabaseURL()))
	if err != nil {
		t.Fatal(err)
	}
	defer mt.Close()
	id, roles := provisionRoleTestTenant(t, db, mt, "ensure-concurrent")
	tn, err := mt.Manager.GetTenant(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			<-start
			errs <- roles.Ensure(ctx, tn)
		}()
	}
	close(start)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Ensure: %v", err)
		}
	}
}

// TestIntegration_RoleManager_EnsureConvergesAttributes checks that Ensure
// strips privileges an existing role has gained.
func TestIntegration_RoleManager_EnsureConvergesAttributes(t *testing.T) {
	db := setupTestDatabase(t)
	ctx := context.Background()
	mt, err := New(testConfig(getTestDatabaseURL()))
	if err != nil {
		t.Fatal(err)
	}
	defer mt.Close()
	id, roles := provisionRoleTestTenant(t, db, mt, "ensure-converge")
	tn, err := mt.Manager.GetTenant(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := roles.Ensure(ctx, tn); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER ROLE "` + testRoleName(id) + `" CREATEDB INHERIT`); err != nil {
		t.Fatal(err)
	}
	if err := roles.Ensure(ctx, tn); err != nil {
		t.Fatal(err)
	}
	var createDB, inherit bool
	if err := db.QueryRow(`SELECT rolcreatedb, rolinherit FROM pg_roles WHERE rolname = $1`, testRoleName(id)).Scan(&createDB, &inherit); err != nil {
		t.Fatal(err)
	}
	if createDB || inherit {
		t.Errorf("after Ensure: rolcreatedb=%v rolinherit=%v, want both false", createDB, inherit)
	}
}

// newRoleModeMT returns a MultiTenant in role mode against the test database,
// with tenant connections going to roleTestTenantDSN.
func newRoleModeMT(t *testing.T, secret []byte, previous ...[]byte) *MultiTenant {
	t.Helper()
	cfg := testConfig(getTestDatabaseURL())
	cfg.Database.Isolation = tenant.IsolationRole
	cfg.Database.RoleIsolation = tenant.RoleIsolationConfig{
		TenantDSN:         roleTestTenantDSN(),
		Secret:            secret,
		PreviousSecrets:   previous,
		MaxConns:          10,
		PerTenantMaxConns: 3,
		MaxWarmTenants:    20,
		IdleTimeout:       time.Minute,
	}
	mt, err := New(cfg)
	if err != nil {
		t.Fatalf("New in role mode: %v", err)
	}
	t.Cleanup(func() { mt.Close() })
	return mt
}

// refreshPgBouncerAuth regenerates PgBouncer's auth_file and reloads it, in
// the CI job that runs PgBouncer with one. Everywhere else it does nothing.
func refreshPgBouncerAuth(t *testing.T, mt *MultiTenant) {
	t.Helper()
	path, admin := os.Getenv("PGBOUNCER_AUTH_FILE"), os.Getenv("PGBOUNCER_ADMIN_DSN")
	if path == "" {
		return
	}
	var buf bytes.Buffer
	if base := os.Getenv("PGBOUNCER_AUTH_FILE_BASE"); base != "" {
		fixed, err := os.ReadFile(base)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(fixed)
	}
	if err := mt.PgBouncerAuthFile(context.Background(), &buf); err != nil {
		t.Fatalf("PgBouncerAuthFile: %v", err)
	}
	// 0644 rather than 0600 only because PgBouncer runs as another user in the
	// CI container. A production auth_file holds live credentials: 0600.
	if err := os.WriteFile(path+".tmp", buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(admin)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect to PgBouncer's admin console: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), "RELOAD"); err != nil {
		t.Fatalf("RELOAD: %v", err)
	}
}

func provisionForRoleTest(t *testing.T, mt *MultiTenant, subdomain string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: subdomain, Subdomain: subdomain}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("ProvisionTenant in role mode: %v", err)
	}
	refreshPgBouncerAuth(t, mt)
	return id
}

func currentUser(t *testing.T, mt *MultiTenant, id uuid.UUID) (string, error) {
	t.Helper()
	conn, err := mt.Manager.GetTenantConn(context.Background(), id)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	var user string
	err = conn.QueryRowContext(context.Background(), "SELECT current_user").Scan(&user)
	return user, err
}

// eventually retries fn for up to five seconds and returns its last error.
// It covers PgBouncer's server_login_retry backoff after a failed server
// login; against PostgreSQL directly the first attempt succeeds.
func eventually(fn func() error) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := fn()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// First provisioning must work in role mode, even though ProvisionTenant
// fires the status change before the provisioned event.
func TestIntegration_RoleIsolation_ProvisionAndConnect(t *testing.T) {
	db := setupTestDatabase(t)
	requirePasswordAuth(t)
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-provision")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	user, err := currentUser(t, mt, id)
	if err != nil {
		t.Fatalf("tenant connection: %v", err)
	}
	if user != testRoleName(id) {
		t.Errorf("current_user = %q, want the tenant's role %q", user, testRoleName(id))
	}
	if s, ok := mt.TenantPoolStats(); !ok || s.ColdOpens != 1 {
		t.Errorf("TenantPoolStats = %+v, %v; want one cold open", s, ok)
	}
}

// A table created by another role, outside the admin's default privileges,
// is granted by the next migration run.
func TestIntegration_RoleIsolation_MigrationRunGrantsOtherOwnersTables(t *testing.T) {
	db := setupTestDatabase(t)
	requirePasswordAuth(t)
	ctx := context.Background()
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-migrations")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	schema := `"` + testRoleName(id) + `"`
	migrator := "role_test_migrator_" + uuid.NewString()[:8]
	admin, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"CREATE ROLE " + migrator + " NOLOGIN",
		"GRANT USAGE, CREATE ON SCHEMA " + schema + " TO " + migrator,
		"SET ROLE " + migrator,
		"CREATE TABLE " + schema + ".gadgets (id int)",
		"RESET ROLE",
	} {
		if _, err := admin.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	admin.Close()
	defer db.Exec("DROP TABLE IF EXISTS " + schema + ".gadgets; DROP OWNED BY " + migrator + "; DROP ROLE IF EXISTS " + migrator)

	query := func() error {
		conn, err := mt.Manager.GetTenantConn(ctx, id)
		if err != nil {
			return err
		}
		defer conn.Close()
		var n int
		return conn.QueryRowContext(ctx, "SELECT count(*) FROM gadgets").Scan(&n)
	}
	wantSQLState(t, "before a migration run", query(), "42501")

	if err := mt.Migrations.ApplyMigration(ctx, id, &tenant.Migration{Version: "901", Name: "noop", SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	if err := query(); err != nil {
		t.Errorf("after a migration run: %v", err)
	}
}

func TestIntegration_RoleIsolation_SuspendLocksOut(t *testing.T) {
	db := setupTestDatabase(t)
	requirePasswordAuth(t)
	ctx := context.Background()
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-suspend")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})
	cred := tenant.RoleCredentials{User: testRoleName(id), Password: tenant.DerivePassword(testRoleSecret, id)}

	session, err := connectAsRole(t, roleTestTenantDSN(), cred)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(ctx)

	if err := mt.Manager.SuspendTenant(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Exec(ctx, "SELECT 1"); err == nil {
		t.Error("an open session kept working after its tenant was suspended")
	}
	// PgBouncer authenticates a client without a server connection, so behind
	// it a refused role only shows on the first statement.
	if c, err := connectAsRole(t, roleTestTenantDSN(), cred); err == nil {
		_, qerr := c.Exec(ctx, "SELECT 1")
		c.Close(ctx)
		if qerr == nil {
			t.Error("a suspended tenant's role could still log in")
		} else {
			t.Logf("suspended role's first statement was refused with: %v", qerr)
		}
	}

	if err := mt.Manager.ActivateTenant(ctx, id); err != nil {
		t.Fatal(err)
	}
	refreshPgBouncerAuth(t, mt)
	if err := eventually(func() error {
		c, err := connectAsRole(t, roleTestTenantDSN(), cred)
		if err == nil {
			c.Close(ctx)
		}
		return err
	}); err != nil {
		t.Fatalf("login after reactivation: %v", err)
	}
}

func TestIntegration_RoleIsolation_DeleteDropsRole(t *testing.T) {
	db := setupTestDatabase(t)
	requirePasswordAuth(t)
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-delete")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	if err := mt.Manager.DeleteTenant(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", testRoleName(id)).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("the tenant's role survived DeleteTenant")
	}
}

func TestIntegration_RoleIsolation_Rotation(t *testing.T) {
	db := setupTestDatabase(t)
	requirePasswordAuth(t)
	oldSecret := bytes.Repeat([]byte("o"), tenant.MinRoleSecretLen)
	newSecret := bytes.Repeat([]byte("n"), tenant.MinRoleSecretLen)
	mtOld := newRoleModeMT(t, oldSecret)
	id := provisionForRoleTest(t, mtOld, "role-rotate")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	// Step 1-2: the new key first, the old as a fallback. Behind PgBouncer the
	// first attempt fails with 08P01, which must trigger the fallback.
	mtBoth := newRoleModeMT(t, newSecret, oldSecret)
	if _, err := currentUser(t, mtBoth, id); err != nil {
		t.Fatalf("before rotation, with the old secret as fallback: %v", err)
	}

	// Step 3-4: re-key, then refresh PgBouncer's auth file.
	if err := mtBoth.RotateTenantCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshPgBouncerAuth(t, mtBoth)

	// The pool mtBoth opened before rotation authenticated with the old
	// secret. Holding several connections at once forces new physical
	// connections, which must now log in with the new one.
	ctx := context.Background()
	var held []*tenant.Conn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for i := 0; i < 3; i++ {
		c, err := mtBoth.Manager.GetTenantConn(ctx, id)
		if err != nil {
			t.Fatalf("connection %d on the warm pool after rotation: %v", i, err)
		}
		held = append(held, c)
		var user string
		if err := eventually(func() error { return c.QueryRowContext(ctx, "SELECT current_user").Scan(&user) }); err != nil {
			t.Fatalf("query %d on the warm pool after rotation: %v", i, err)
		}
	}

	// Step 5: only the new key.
	if _, err := currentUser(t, newRoleModeMT(t, newSecret), id); err != nil {
		t.Errorf("after rotation, with only the new secret: %v", err)
	}
	if _, err := currentUser(t, newRoleModeMT(t, oldSecret), id); err == nil {
		t.Error("after rotation, the old secret alone still logged in")
	}
}

func TestIntegration_RoleIsolation_EnsureRepairsAndSkips(t *testing.T) {
	db := setupTestDatabase(t)
	requirePasswordAuth(t)
	ctx := context.Background()
	mt := newRoleModeMT(t, testRoleSecret)
	repaired := provisionForRoleTest(t, mt, "role-repair")
	unprovisioned := uuid.New()
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: unprovisioned, Name: "role-skip", Subdomain: "role-skip"}); err != nil {
		t.Fatal(err)
	}
	defer cleanupTestData(db, []uuid.UUID{repaired, unprovisioned})
	defer dropTestRoles(db, []uuid.UUID{repaired, unprovisioned})

	dropTestRoles(db, []uuid.UUID{repaired})
	if _, err := currentUser(t, newRoleModeMT(t, testRoleSecret), repaired); err == nil {
		t.Fatal("a tenant whose role was dropped could still connect")
	}

	if err := mt.EnsureTenantRoles(ctx); err != nil {
		t.Fatalf("EnsureTenantRoles: %v", err)
	}
	refreshPgBouncerAuth(t, mt)
	fresh := newRoleModeMT(t, testRoleSecret)
	if err := eventually(func() error { _, err := currentUser(t, fresh, repaired); return err }); err != nil {
		t.Errorf("after repair: %v", err)
	}
	var exists bool
	if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", testRoleName(unprovisioned)).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("EnsureTenantRoles created a role for an unprovisioned tenant")
	}
}

// requirePasswordAuth skips (fails, under CI) when the server at
// roleTestTenantDSN does not check passwords. Role isolation's login-dependent
// guarantees mean nothing on a trust-auth server, where any password logs in.
// It probes with a throwaway LOGIN role and a deliberately wrong password.
// Behind a PgBouncer TenantDSN the probe role is absent from the auth_file, and
// PgBouncer checks passwords itself, so the wrong password is refused there
// too, as 08P01 "authentication failed".
// Call it after setupTestDatabase.
func requirePasswordAuth(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	probe := "role_test_authprobe_" + uuid.NewString()[:8]
	admin, err := pgx.Connect(ctx, getTestDatabaseURL())
	if err != nil {
		t.Fatalf("requirePasswordAuth: connect as admin: %v", err)
	}
	defer admin.Close(ctx)
	_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+probe)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+probe+" LOGIN PASSWORD 'the-right-password'"); err != nil {
		t.Fatalf("requirePasswordAuth: create probe role: %v", err)
	}
	defer admin.Exec(ctx, "DROP ROLE IF EXISTS "+probe)

	c, err := connectAsRole(t, roleTestTenantDSN(), tenant.RoleCredentials{User: probe, Password: "a-wrong-password"})
	if err == nil {
		c.Close(ctx)
		msg := "the server accepts a wrong password (trust authentication), so role isolation's login guarantees cannot be tested; use a password-authenticated server"
		if os.Getenv("CI") != "" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	// Only a rejected password proves the server checks passwords. Any other
	// failure, such as 28000 "role does not exist", proves nothing.
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || !(pe.Code == "28P01" || (pe.Code == "08P01" && strings.Contains(strings.ToLower(pe.Message), "authentication failed"))) {
		t.Fatalf("requirePasswordAuth: wrong password failed with something other than a rejected password: %v", err)
	}
}

// A connection request after Close must fail promptly. The tenant is real and
// its pool live before Close, so the failure is Close's doing and not a login
// failure for a tenant that has no role.
func TestIntegration_RoleIsolation_OperationsAfterClose(t *testing.T) {
	db := setupTestDatabase(t)
	requirePasswordAuth(t)
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-after-close")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	if _, err := currentUser(t, mt, id); err != nil {
		t.Fatalf("before Close: %v", err)
	}
	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := mt.Manager.GetTenantConn(context.Background(), id)
		if err == nil {
			conn.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("GetTenantConn succeeded after Close")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GetTenantConn hung after Close")
	}
}

// TestIntegration_RoleIsolation_PgBouncerLockoutGuard runs only in the CI
// jobs with PgBouncer. A role whose first login since PgBouncer started is a
// failure must still log in with the right password afterwards. PgBouncer
// 1.25.2 breaks this when its auth_file holds SCRAM verifiers, so this test
// fails if the auth_file is ever switched to verifiers.
func TestIntegration_RoleIsolation_PgBouncerLockoutGuard(t *testing.T) {
	if os.Getenv("PGBOUNCER_MODE") == "" {
		t.Skip("runs only through PgBouncer; set PGBOUNCER_MODE")
	}
	db := setupTestDatabase(t)
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-lockout-guard") // a role PgBouncer has never seen
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	wrong := tenant.RoleCredentials{User: testRoleName(id), Password: "not-the-password"}
	if c, err := connectAsRole(t, roleTestTenantDSN(), wrong); err == nil {
		c.Close(context.Background())
		t.Fatal("PgBouncer accepted a wrong password")
	}

	right := tenant.RoleCredentials{User: testRoleName(id), Password: tenant.DerivePassword(testRoleSecret, id)}
	c, err := connectAsRole(t, roleTestTenantDSN(), right)
	if err != nil {
		t.Fatalf("after one failed first login, the correct password was refused: %v", err)
	}
	c.Close(context.Background())
}

// TestIntegration_RoleManager_EnsureUsesRegistryStatus checks that Ensure
// decides LOGIN from the registry, not from the tenant value it is given: a
// stale active snapshot of a suspended tenant must leave the role NOLOGIN.
func TestIntegration_RoleManager_EnsureUsesRegistryStatus(t *testing.T) {
	db := setupTestDatabase(t)
	ctx := context.Background()
	mt, err := New(testConfig(getTestDatabaseURL()))
	if err != nil {
		t.Fatal(err)
	}
	defer mt.Close()
	id, roles := provisionRoleTestTenant(t, db, mt, "ensure-registry")
	stale, err := mt.Manager.GetTenant(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != tenant.StatusActive {
		t.Fatalf("setup: status %q, want active", stale.Status)
	}
	canLogin := func() bool {
		var b bool
		if err := db.QueryRow(`SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, testRoleName(id)).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if err := roles.Ensure(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if !canLogin() {
		t.Fatal("an active tenant's role is NOLOGIN after Ensure")
	}
	// Suspended elsewhere; stale still says active.
	if _, err := db.Exec(`UPDATE public.tenants SET status = 'suspended' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := roles.Ensure(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if canLogin() {
		t.Error("Ensure with a stale active tenant re-enabled the login of a suspended tenant")
	}
}

// TestIntegration_RoleManager_EnsureEndsSessionsWhenNotActive checks that
// Ensure on a tenant that is not active ends the role's live sessions.
func TestIntegration_RoleManager_EnsureEndsSessionsWhenNotActive(t *testing.T) {
	db := setupTestDatabase(t)
	requirePasswordAuth(t)
	ctx := context.Background()
	mt, err := New(testConfig(getTestDatabaseURL()))
	if err != nil {
		t.Fatal(err)
	}
	defer mt.Close()
	id, roles := provisionRoleTestTenant(t, db, mt, "ensure-ends")
	tn, err := mt.Manager.GetTenant(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := roles.Ensure(ctx, tn); err != nil {
		t.Fatal(err)
	}
	creds := tenant.NewCredentialSource(tenant.RoleIsolationConfig{Secret: testRoleSecret}, database.NewSchemaManager(db, zap.NewNop(), "tenant_").GetSchemaName)
	cred, err := creds.Current(id)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := connectAsRole(t, getTestDatabaseURL(), cred)
	if err != nil {
		t.Fatalf("the active tenant's role could not log in: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`UPDATE public.tenants SET status = 'suspended' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := roles.Ensure(ctx, tn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SELECT 1"); err == nil {
		t.Error("the role's session survived Ensure on a suspended tenant")
	}
}

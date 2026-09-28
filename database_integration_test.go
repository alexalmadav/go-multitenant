package multitenant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexalmadav/go-multitenant/database"
	dbpostgres "github.com/alexalmadav/go-multitenant/database/postgres"
	"github.com/alexalmadav/go-multitenant/limits"
	"github.com/alexalmadav/go-multitenant/middleware/httpmw"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// Database integration tests that verify actual PostgreSQL behavior.
// These tests use testcontainers to automatically spin up a PostgreSQL instance.
// No manual database setup required - just run: go test -v -run TestDatabase
//
// Requirements: Docker must be running

// postgresContainer holds a testcontainer PostgreSQL instance
type postgresContainer struct {
	*postgres.PostgresContainer
	ConnectionString string
}

// setupPostgresContainer creates a new PostgreSQL container for testing
func setupPostgresContainer(ctx context.Context) (container *postgresContainer, err error) {
	// Recover from panics that testcontainers may throw when Docker is not available
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("docker not available: %v", r)
			container = nil
		}
	}()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("test_multitenant"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start postgres container: %w", err)
	}

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("failed to get connection string: %w", err)
	}

	return &postgresContainer{
		PostgresContainer: pgContainer,
		ConnectionString:  connStr,
	}, nil
}

// testDB holds the database connection for integration tests
type testDB struct {
	db        *sql.DB
	logger    *zap.Logger
	t         *testing.T
	container *postgresContainer
	dsn       string
}

// newTestDB creates a new test database connection using testcontainers
func newTestDB(t *testing.T) *testDB {
	if testing.Short() {
		t.Skip("Skipping database integration test in short mode")
	}

	// openPgxDB opens a *sql.DB using pgx with simple protocol mode
	openPgxDB := func(dsn string) (*sql.DB, error) {
		connConfig, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, err
		}
		connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		db := stdlib.OpenDB(*connConfig)
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, err
		}
		return db, nil
	}

	// Check if we should use an external database
	if dbURL := os.Getenv("TEST_DATABASE_URL"); dbURL != "" {
		db, err := openPgxDB(dbURL)
		if err != nil {
			t.Fatalf("Failed to connect to external database: %v", err)
		}
		return &testDB{
			db:     db,
			logger: zaptest.NewLogger(t),
			t:      t,
			dsn:    dbURL,
		}
	}

	// Try default local PostgreSQL first
	defaultURL := "postgres://postgres:postgres@localhost:5432/test_multitenant?sslmode=disable"
	db, err := openPgxDB(defaultURL)
	if err == nil {
		t.Log("Using local PostgreSQL database")
		return &testDB{
			db:     db,
			logger: zaptest.NewLogger(t),
			t:      t,
			dsn:    defaultURL,
		}
	}

	// Use testcontainers as fallback
	ctx := context.Background()
	container, err := setupPostgresContainer(ctx)
	if err != nil {
		t.Skipf("Skipping integration test - no database available. Set TEST_DATABASE_URL or ensure Docker is running: %v", err)
	}

	db, err = openPgxDB(container.ConnectionString)
	if err != nil {
		container.Terminate(ctx)
		t.Fatalf("Failed to connect to container database: %v", err)
	}

	return &testDB{
		db:        db,
		logger:    zaptest.NewLogger(t),
		t:         t,
		container: container,
		dsn:       container.ConnectionString,
	}
}

// fixtureMigrationsDir is the schema every integration tenant gets.
var fixtureMigrationsDir = filepath.Join("testdata", "migrations")

// testConfig returns the config integration tests use: the given DSN, the
// fixture migrations directory, and usage tracking mapped to the two fixture
// tables (projects, tenant_users).
func testConfig(dsn string) Config {
	config := DefaultConfig()
	config.Database.DSN = dsn
	config.Database.MigrationsDir = fixtureMigrationsDir
	// These tests exercise the manager, schemas, migrations and limits, not
	// mt.HTTPMiddleware's membership check. The membership tests build their
	// own chains with a real Membership; see cross_tenant_integration_test.go.
	config.InsecureSkipMembership = true
	l := limits.ExampleConfig()
	l.UsageTables = map[string]string{
		"max_projects": "projects",
		"max_users":    "tenant_users",
	}
	config.Limits = &l
	return config
}

func (tdb *testDB) close() {
	if tdb.db != nil {
		tdb.db.Close()
	}
	if tdb.container != nil {
		tdb.container.Terminate(context.Background())
	}
}

func (tdb *testDB) getConnectionString() string {
	return tdb.dsn
}

// cleanupSchema drops a tenant schema and any tables that leaked to public
func (tdb *testDB) cleanupSchema(tenantID uuid.UUID, schemaPrefix string) {
	schemaName := fmt.Sprintf("%s%s", schemaPrefix, strings.ReplaceAll(tenantID.String(), "-", "_"))

	// Drop the tenant schema
	_, _ = tdb.db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schemaName))

	// Clean up any tables that leaked to public schema
	tenantTables := []string{"projects", "tasks", "documents", "tenant_users"}
	for _, table := range tenantTables {
		// Only drop if it exists and is not a master table
		var exists bool
		err := tdb.db.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)
		`, table).Scan(&exists)
		if err == nil && exists {
			// Check if it's a tenant table (should not exist in public)
			tdb.t.Logf("Warning: table %s exists in public schema, cleaning up", table)
			_, _ = tdb.db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS public.%s CASCADE`, table))
		}
	}
}

// listTablesInSchema returns all tables in a given schema
func (tdb *testDB) listTablesInSchema(schema string) ([]string, error) {
	rows, err := tdb.db.Query(`
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = $1
		AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// schemaExists checks if a schema exists
func (tdb *testDB) schemaExists(schema string) (bool, error) {
	var exists bool
	err := tdb.db.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)
	`, schema).Scan(&exists)
	return exists, err
}

// tableExistsInSchema checks if a specific table exists in a specific schema
func (tdb *testDB) tableExistsInSchema(schema, table string) (bool, error) {
	var exists bool
	err := tdb.db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = $1 AND table_name = $2
		)
	`, schema, table).Scan(&exists)
	return exists, err
}

// getCurrentSearchPath returns the current search_path setting
func (tdb *testDB) getCurrentSearchPath() (string, error) {
	var searchPath string
	err := tdb.db.QueryRow("SHOW search_path").Scan(&searchPath)
	return searchPath, err
}

// ============================================================================
// SCHEMA ISOLATION TESTS
// These tests verify that tenant tables are created ONLY in tenant schemas
// and NOT in the public schema
// ============================================================================

func TestDatabase_SchemaCreation_TablesOnlyInTenantSchema(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)
	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	defer cleanupTestData(tdb.db, []uuid.UUID{tenantID})

	// Record which tables existed in public before we start
	publicTablesBefore, err := tdb.listTablesInSchema("public")
	if err != nil {
		t.Fatalf("Failed to list public tables before test: %v", err)
	}
	publicTablesBeforeMap := make(map[string]bool)
	for _, table := range publicTablesBefore {
		publicTablesBeforeMap[table] = true
	}

	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: tenantID, Name: "Test Tenant", Subdomain: "tables-only-" + tenantID.String()[:8]}); err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Create the schema and apply the fixture migrations, the same two steps
	// ProvisionTenant performs.
	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)
	if err := sm.CreateTenantSchema(ctx, tenantID); err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
	}
	if err := mt.Migrations.ApplyPending(ctx, tenantID); err != nil {
		t.Fatalf("Failed to apply pending migrations: %v", err)
	}

	// Get the expected schema name
	schemaName := sm.GetSchemaName(tenantID)

	// Verify schema was created
	exists, err := tdb.schemaExists(schemaName)
	if err != nil {
		t.Fatalf("Failed to check schema existence: %v", err)
	}
	if !exists {
		t.Errorf("Tenant schema %s should exist", schemaName)
	}

	// Get tables in tenant schema
	tenantTables, err := tdb.listTablesInSchema(schemaName)
	if err != nil {
		t.Fatalf("Failed to list tenant tables: %v", err)
	}

	// Expected tenant tables, created by the fixture migrations
	expectedTables := []string{"projects", "tenant_users"}

	// Verify all expected tables exist in tenant schema
	for _, expected := range expectedTables {
		found := false
		for _, actual := range tenantTables {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected table %s to exist in tenant schema %s", expected, schemaName)
		}
	}

	// CRITICAL TEST: Verify tenant tables were NOT created in public schema
	publicTablesAfter, err := tdb.listTablesInSchema("public")
	if err != nil {
		t.Fatalf("Failed to list public tables after test: %v", err)
	}

	for _, table := range publicTablesAfter {
		// Skip tables that existed before
		if publicTablesBeforeMap[table] {
			continue
		}

		// Check if this is a tenant-specific table that leaked
		for _, tenantTable := range expectedTables {
			if table == tenantTable {
				t.Errorf("SCHEMA LEAKAGE: Tenant table %s was created in public schema instead of only in %s", table, schemaName)
			}
		}
	}

	t.Logf("Tenant schema %s has tables: %v", schemaName, tenantTables)
	t.Logf("Public schema tables after: %v", publicTablesAfter)
}

func TestDatabase_MultiTenant_DataIsolation(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)
	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenant1ID := uuid.New()
	tenant2ID := uuid.New()
	schemaPrefix := "tenant_"

	defer cleanupTestData(tdb.db, []uuid.UUID{tenant1ID, tenant2ID})

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)

	// Create both tenant schemas and apply the fixture migrations
	for i, id := range []uuid.UUID{tenant1ID, tenant2ID} {
		name := fmt.Sprintf("Tenant %d", i+1)
		if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: name, Subdomain: fmt.Sprintf("isolation-%d-%s", i, id.String()[:8])}); err != nil {
			t.Fatalf("Failed to create tenant %s: %v", id, err)
		}
		if err := sm.CreateTenantSchema(ctx, id); err != nil {
			t.Fatalf("Failed to create schema for tenant %s: %v", id, err)
		}
		if err := mt.Migrations.ApplyPending(ctx, id); err != nil {
			t.Fatalf("Failed to apply pending migrations for tenant %s: %v", id, err)
		}
	}

	schema1 := sm.GetSchemaName(tenant1ID)
	schema2 := sm.GetSchemaName(tenant2ID)

	// Insert data into tenant1's schema using fully qualified names
	_, err = tdb.db.Exec(fmt.Sprintf(`INSERT INTO "%s".projects (name) VALUES ($1)`, schema1),
		"Tenant1 Secret Project")
	if err != nil {
		t.Fatalf("Failed to insert into tenant1 projects: %v", err)
	}

	// Insert data into tenant2's schema
	_, err = tdb.db.Exec(fmt.Sprintf(`INSERT INTO "%s".projects (name) VALUES ($1)`, schema2),
		"Tenant2 Secret Project")
	if err != nil {
		t.Fatalf("Failed to insert into tenant2 projects: %v", err)
	}

	// Verify tenant1 can only see its own data
	var tenant1Count int
	err = tdb.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s".projects`, schema1)).Scan(&tenant1Count)
	if err != nil {
		t.Fatalf("Failed to count tenant1 projects: %v", err)
	}
	if tenant1Count != 1 {
		t.Errorf("Tenant1 should have exactly 1 project, got %d", tenant1Count)
	}

	// Verify tenant2 can only see its own data
	var tenant2Count int
	err = tdb.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s".projects`, schema2)).Scan(&tenant2Count)
	if err != nil {
		t.Fatalf("Failed to count tenant2 projects: %v", err)
	}
	if tenant2Count != 1 {
		t.Errorf("Tenant2 should have exactly 1 project, got %d", tenant2Count)
	}

	// CRITICAL: Verify tenant1's schema doesn't contain tenant2's data
	var tenant1SeesTenant2 int
	err = tdb.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s".projects WHERE name LIKE 'Tenant2%%'`, schema1)).Scan(&tenant1SeesTenant2)
	if err != nil {
		t.Fatalf("Failed to check cross-tenant visibility: %v", err)
	}
	if tenant1SeesTenant2 > 0 {
		t.Errorf("DATA LEAKAGE: Tenant1's schema can see %d of Tenant2's projects", tenant1SeesTenant2)
	}

	// Verify tenant2's schema doesn't contain tenant1's data
	var tenant2SeesTenant1 int
	err = tdb.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s".projects WHERE name LIKE 'Tenant1%%'`, schema2)).Scan(&tenant2SeesTenant1)
	if err != nil {
		t.Fatalf("Failed to check cross-tenant visibility: %v", err)
	}
	if tenant2SeesTenant1 > 0 {
		t.Errorf("DATA LEAKAGE: Tenant2's schema can see %d of Tenant1's projects", tenant2SeesTenant1)
	}
}

func TestDatabase_SchemaCreation_IndexesInCorrectSchema(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)
	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	defer cleanupTestData(tdb.db, []uuid.UUID{tenantID})

	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: tenantID, Name: "Test Tenant", Subdomain: "indexes-" + tenantID.String()[:8]}); err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)
	if err := sm.CreateTenantSchema(ctx, tenantID); err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
	}
	if err := mt.Migrations.ApplyPending(ctx, tenantID); err != nil {
		t.Fatalf("Failed to apply pending migrations: %v", err)
	}

	schemaName := sm.GetSchemaName(tenantID)

	// Query for indexes in the tenant schema
	rows, err := tdb.db.Query(`
		SELECT indexname, tablename
		FROM pg_indexes
		WHERE schemaname = $1
		ORDER BY tablename, indexname
	`, schemaName)
	if err != nil {
		t.Fatalf("Failed to query indexes: %v", err)
	}
	defer rows.Close()

	var indexes []string
	for rows.Next() {
		var indexName, tableName string
		if err := rows.Scan(&indexName, &tableName); err != nil {
			t.Fatalf("Failed to scan index: %v", err)
		}
		indexes = append(indexes, fmt.Sprintf("%s on %s", indexName, tableName))
	}

	t.Logf("Indexes in tenant schema %s: %v", schemaName, indexes)

	// Verify we have some indexes
	if len(indexes) == 0 {
		t.Error("Expected at least some indexes in tenant schema")
	}

	// Check that indexes are NOT created in public schema
	expectedIndexes := []string{"idx_projects_status"}
	for _, idx := range expectedIndexes {
		var existsInPublic bool
		err := tdb.db.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM pg_indexes
				WHERE schemaname = 'public' AND indexname = $1
			)
		`, idx).Scan(&existsInPublic)
		if err != nil {
			t.Logf("Warning: couldn't check index %s in public: %v", idx, err)
			continue
		}
		if existsInPublic {
			t.Errorf("INDEX LEAKAGE: Index %s was created in public schema", idx)
		}
	}
}

func TestDatabase_SchemaCreation_FunctionsInCorrectSchema(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)
	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	defer cleanupTestData(tdb.db, []uuid.UUID{tenantID})

	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: tenantID, Name: "Test Tenant", Subdomain: "functions-" + tenantID.String()[:8]}); err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)
	if err := sm.CreateTenantSchema(ctx, tenantID); err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
	}
	if err := mt.Migrations.ApplyPending(ctx, tenantID); err != nil {
		t.Fatalf("Failed to apply pending migrations: %v", err)
	}

	schemaName := sm.GetSchemaName(tenantID)

	// Check for the update_updated_at_column function in tenant schema
	var existsInTenant bool
	err = tdb.db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM pg_proc p
			JOIN pg_namespace n ON p.pronamespace = n.oid
			WHERE n.nspname = $1 AND p.proname = 'update_updated_at_column'
		)
	`, schemaName).Scan(&existsInTenant)
	if err != nil {
		t.Fatalf("Failed to check function existence in tenant schema: %v", err)
	}

	// Function MUST exist in tenant schema
	if !existsInTenant {
		t.Errorf("FUNCTION LEAKAGE: update_updated_at_column should exist in tenant schema %s", schemaName)
	}

	// Check that function does NOT exist in public schema (unless it was there before)
	var existsInPublic bool
	err = tdb.db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM pg_proc p
			JOIN pg_namespace n ON p.pronamespace = n.oid
			WHERE n.nspname = 'public' AND p.proname = 'update_updated_at_column'
		)
	`).Scan(&existsInPublic)
	if err != nil {
		t.Fatalf("Failed to check function existence in public schema: %v", err)
	}

	if existsInPublic {
		t.Errorf("FUNCTION LEAKAGE: update_updated_at_column was created in public schema instead of only in %s", schemaName)
	}

	t.Logf("update_updated_at_column correctly exists only in tenant schema %s", schemaName)
}

func TestDatabase_SchemaCreation_TriggersWork(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)
	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	defer cleanupTestData(tdb.db, []uuid.UUID{tenantID})

	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: tenantID, Name: "Test Tenant", Subdomain: "triggers-" + tenantID.String()[:8]}); err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)
	if err := sm.CreateTenantSchema(ctx, tenantID); err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
	}
	if err := mt.Migrations.ApplyPending(ctx, tenantID); err != nil {
		t.Fatalf("Failed to apply pending migrations: %v", err)
	}

	schemaName := sm.GetSchemaName(tenantID)

	// Insert a project
	var projectID uuid.UUID
	var createdAt, updatedAt time.Time
	err = tdb.db.QueryRow(fmt.Sprintf(`
		INSERT INTO "%s".projects (name)
		VALUES ($1)
		RETURNING id, created_at, updated_at
	`, schemaName), "Trigger Test").Scan(&projectID, &createdAt, &updatedAt)
	if err != nil {
		t.Fatalf("Failed to insert project: %v", err)
	}

	// Wait a moment
	time.Sleep(10 * time.Millisecond)

	// Update the project
	_, err = tdb.db.Exec(fmt.Sprintf(`
		UPDATE "%s".projects SET name = $1 WHERE id = $2
	`, schemaName), "Updated Trigger Test", projectID)
	if err != nil {
		t.Fatalf("Failed to update project: %v", err)
	}

	// Check that updated_at changed
	var newUpdatedAt time.Time
	err = tdb.db.QueryRow(fmt.Sprintf(`
		SELECT updated_at FROM "%s".projects WHERE id = $1
	`, schemaName), projectID).Scan(&newUpdatedAt)
	if err != nil {
		t.Fatalf("Failed to get updated_at: %v", err)
	}

	if !newUpdatedAt.After(updatedAt) {
		t.Errorf("Trigger should have updated updated_at. Original: %v, New: %v", updatedAt, newUpdatedAt)
	}
}

func TestDatabase_SchemaDrop_CleansUpCompletely(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)
	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	defer cleanupTestData(tdb.db, []uuid.UUID{tenantID})

	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: tenantID, Name: "Test Tenant", Subdomain: "drop-" + tenantID.String()[:8]}); err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)

	// Create schema and apply the fixture migrations
	if err := sm.CreateTenantSchema(ctx, tenantID); err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
	}
	if err := mt.Migrations.ApplyPending(ctx, tenantID); err != nil {
		t.Fatalf("Failed to apply pending migrations: %v", err)
	}

	schemaName := sm.GetSchemaName(tenantID)

	// Verify it exists
	exists, err := tdb.schemaExists(schemaName)
	if err != nil {
		t.Fatalf("Failed to check schema existence: %v", err)
	}
	if !exists {
		t.Fatal("Schema should exist after creation")
	}

	// Insert some data
	_, err = tdb.db.Exec(fmt.Sprintf(`INSERT INTO "%s".projects (name) VALUES ($1)`, schemaName), "Test")
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}

	// Drop schema
	err = sm.DropTenantSchema(ctx, tenantID)
	if err != nil {
		t.Fatalf("Failed to drop tenant schema: %v", err)
	}

	// Verify schema no longer exists
	exists, err = tdb.schemaExists(schemaName)
	if err != nil {
		t.Fatalf("Failed to check schema existence after drop: %v", err)
	}
	if exists {
		t.Errorf("Schema %s should not exist after drop", schemaName)
	}

	// Verify tables don't exist
	tables, err := tdb.listTablesInSchema(schemaName)
	if err == nil && len(tables) > 0 {
		t.Errorf("Tables should not exist after schema drop: %v", tables)
	}
}

// ============================================================================
// FULL LIFECYCLE TESTS WITH MULTITENANT INSTANCE
// These tests use the full MultiTenant struct to test real-world usage
// ============================================================================

func TestDatabase_FullLifecycle_WithMultiTenant(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()

	// Cleanup
	defer cleanupTestData(tdb.db, []uuid.UUID{tenantID})

	// Create tenant
	testTenant := &tenant.Tenant{
		ID:        tenantID,
		Name:      "Full Lifecycle Test",
		Subdomain: "full-lifecycle-test",
	}

	err = mt.Manager.CreateTenant(ctx, testTenant)
	if err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}

	// Record public tables before provisioning
	var publicTablesBefore []string
	rows, err := tdb.db.Query(`
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
	`)
	if err != nil {
		t.Fatalf("Failed to list public tables: %v", err)
	}
	for rows.Next() {
		var name string
		rows.Scan(&name)
		publicTablesBefore = append(publicTablesBefore, name)
	}
	rows.Close()

	publicTablesBeforeMap := make(map[string]bool)
	for _, tbl := range publicTablesBefore {
		publicTablesBeforeMap[tbl] = true
	}

	// Provision tenant (creates schema)
	err = mt.Manager.ProvisionTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ProvisionTenant failed: %v", err)
	}

	// Check public tables after provisioning
	var publicTablesAfter []string
	rows, err = tdb.db.Query(`
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
	`)
	if err != nil {
		t.Fatalf("Failed to list public tables after provision: %v", err)
	}
	for rows.Next() {
		var name string
		rows.Scan(&name)
		publicTablesAfter = append(publicTablesAfter, name)
	}
	rows.Close()

	// Check for leaked tables
	tenantSpecificTables := []string{"projects", "tasks", "documents", "tenant_users"}
	for _, table := range publicTablesAfter {
		if publicTablesBeforeMap[table] {
			continue // Existed before
		}
		for _, tenantTable := range tenantSpecificTables {
			if table == tenantTable {
				t.Errorf("SCHEMA LEAKAGE in MultiTenant.ProvisionTenant: table %s was created in public schema", table)
			}
		}
	}

	// Test WithTenantTx - the safest way to execute tenant-scoped queries
	err = mt.Manager.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO projects (name) VALUES ($1)", "Test Project via WithTenantTx")
		return err
	})
	if err != nil {
		t.Fatalf("WithTenantTx failed: %v", err)
	}

	// Test GetTenantConn - dedicated connection with search_path set
	conn, err := mt.Manager.GetTenantConn(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenantConn failed: %v", err)
	}
	defer conn.Close()

	// Insert data through tenant connection
	_, err = conn.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", "Test Project via TenantConn")
	if err != nil {
		t.Fatalf("Failed to insert via tenant connection: %v", err)
	}

	// Verify data exists using the same connection
	var count int
	err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count projects: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 projects (one from WithTenantTx, one from GetTenantConn), got %d", count)
	}

	// Delete tenant
	err = mt.Manager.DeleteTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("DeleteTenant failed: %v", err)
	}

	t.Log("Full lifecycle test completed successfully")
}

func TestDatabase_ConcurrentTenantCreation_NoSchemaLeakage(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()

	// Record public tables before
	publicTablesBefore := make(map[string]bool)
	rows, _ := tdb.db.Query(`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'`)
	for rows.Next() {
		var name string
		rows.Scan(&name)
		publicTablesBefore[name] = true
	}
	rows.Close()

	// Create multiple tenants concurrently
	const numTenants = 5
	tenantIDs := make([]uuid.UUID, numTenants)
	results := make(chan error, numTenants)

	defer cleanupTestData(tdb.db, tenantIDs)

	for i := 0; i < numTenants; i++ {
		go func(index int) {
			id := uuid.New()
			tenantIDs[index] = id

			tnt := &tenant.Tenant{
				ID:        id,
				Name:      fmt.Sprintf("Concurrent Tenant %d", index),
				Subdomain: fmt.Sprintf("concurrent-test-%d-%s", index, id.String()[:8]),
			}

			if err := mt.Manager.CreateTenant(ctx, tnt); err != nil {
				results <- fmt.Errorf("create tenant %d: %w", index, err)
				return
			}

			if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
				results <- fmt.Errorf("provision tenant %d: %w", index, err)
				return
			}

			results <- nil
		}(i)
	}

	// Wait for all
	for i := 0; i < numTenants; i++ {
		if err := <-results; err != nil {
			t.Errorf("Concurrent operation failed: %v", err)
		}
	}

	// Check for schema leakage
	tenantSpecificTables := []string{"projects", "tasks", "documents", "tenant_users"}
	rows, _ = tdb.db.Query(`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'`)
	for rows.Next() {
		var name string
		rows.Scan(&name)
		if publicTablesBefore[name] {
			continue
		}
		for _, tt := range tenantSpecificTables {
			if name == tt {
				t.Errorf("SCHEMA LEAKAGE during concurrent creation: table %s leaked to public schema", name)
			}
		}
	}
	rows.Close()

	t.Logf("Created %d tenants concurrently without schema leakage", numTenants)
}

// ============================================================================
// CONNECTION ISOLATION TESTS
// These tests verify the new GetTenantConn and WithTenantTx methods work correctly
// ============================================================================

func TestDatabase_GetTenantConn_SearchPath(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()

	defer cleanupTestData(tdb.db, []uuid.UUID{tenantID})

	// Create and provision tenant
	testTenant := &tenant.Tenant{
		ID:        tenantID,
		Name:      "SearchPath Test Tenant",
		Subdomain: fmt.Sprintf("searchpath-test-%s", tenantID.String()[:8]),
	}

	if err := mt.Manager.CreateTenant(ctx, testTenant); err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, tenantID); err != nil {
		t.Fatalf("ProvisionTenant failed: %v", err)
	}

	// Get tenant connection
	conn, err := mt.Manager.GetTenantConn(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenantConn failed: %v", err)
	}
	defer conn.Close()

	// Verify search_path is set correctly
	var searchPath string
	err = conn.QueryRowContext(ctx, "SHOW search_path").Scan(&searchPath)
	if err != nil {
		t.Fatalf("Failed to get search_path: %v", err)
	}

	expectedSchema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(tenantID.String(), "-", "_"))
	if !strings.Contains(searchPath, expectedSchema) {
		t.Errorf("search_path should contain tenant schema %s, got: %s", expectedSchema, searchPath)
	}

	t.Logf("GetTenantConn search_path correctly set to: %s", searchPath)
}

func TestDatabase_GetTenantConn_Isolation(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenant1ID := uuid.New()
	tenant2ID := uuid.New()

	defer cleanupTestData(tdb.db, []uuid.UUID{tenant1ID, tenant2ID})

	// Create and provision both tenants
	for i, id := range []uuid.UUID{tenant1ID, tenant2ID} {
		tnt := &tenant.Tenant{
			ID:        id,
			Name:      fmt.Sprintf("Isolation Test Tenant %d", i+1),
			Subdomain: fmt.Sprintf("isolation-test-%d-%s", i+1, id.String()[:8]),
		}
		if err := mt.Manager.CreateTenant(ctx, tnt); err != nil {
			t.Fatalf("CreateTenant %d failed: %v", i+1, err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatalf("ProvisionTenant %d failed: %v", i+1, err)
		}
	}

	// Get connection for tenant1 and insert data
	conn1, err := mt.Manager.GetTenantConn(ctx, tenant1ID)
	if err != nil {
		t.Fatalf("GetTenantConn for tenant1 failed: %v", err)
	}
	defer conn1.Close()

	_, err = conn1.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", "Tenant1 Secret Project")
	if err != nil {
		t.Fatalf("Failed to insert into tenant1: %v", err)
	}

	// Get connection for tenant2 and insert different data
	conn2, err := mt.Manager.GetTenantConn(ctx, tenant2ID)
	if err != nil {
		t.Fatalf("GetTenantConn for tenant2 failed: %v", err)
	}
	defer conn2.Close()

	_, err = conn2.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", "Tenant2 Secret Project")
	if err != nil {
		t.Fatalf("Failed to insert into tenant2: %v", err)
	}

	// Verify tenant1's connection only sees tenant1's data
	var count1 int
	err = conn1.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&count1)
	if err != nil {
		t.Fatalf("Failed to count tenant1 projects: %v", err)
	}
	if count1 != 1 {
		t.Errorf("Tenant1 connection should see exactly 1 project, got %d", count1)
	}

	var name1 string
	err = conn1.QueryRowContext(ctx, "SELECT name FROM projects LIMIT 1").Scan(&name1)
	if err != nil {
		t.Fatalf("Failed to get tenant1 project name: %v", err)
	}
	if name1 != "Tenant1 Secret Project" {
		t.Errorf("Tenant1 should see its own project, got: %s", name1)
	}

	// Verify tenant2's connection only sees tenant2's data
	var count2 int
	err = conn2.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&count2)
	if err != nil {
		t.Fatalf("Failed to count tenant2 projects: %v", err)
	}
	if count2 != 1 {
		t.Errorf("Tenant2 connection should see exactly 1 project, got %d", count2)
	}

	var name2 string
	err = conn2.QueryRowContext(ctx, "SELECT name FROM projects LIMIT 1").Scan(&name2)
	if err != nil {
		t.Fatalf("Failed to get tenant2 project name: %v", err)
	}
	if name2 != "Tenant2 Secret Project" {
		t.Errorf("Tenant2 should see its own project, got: %s", name2)
	}

	// CRITICAL: Verify cross-tenant isolation - tenant1 should NOT see tenant2's data
	var crossCheck int
	err = conn1.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE name LIKE 'Tenant2%'").Scan(&crossCheck)
	if err != nil {
		t.Fatalf("Failed cross-tenant check: %v", err)
	}
	if crossCheck > 0 {
		t.Errorf("DATA LEAKAGE: Tenant1's connection can see %d of Tenant2's projects", crossCheck)
	}

	t.Log("GetTenantConn correctly isolates data between tenants")
}

func TestDatabase_WithTenantTx_Rollback(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()

	defer cleanupTestData(tdb.db, []uuid.UUID{tenantID})

	// Create and provision tenant
	testTenant := &tenant.Tenant{
		ID:        tenantID,
		Name:      "Rollback Test Tenant",
		Subdomain: fmt.Sprintf("rollback-test-%s", tenantID.String()[:8]),
	}

	if err := mt.Manager.CreateTenant(ctx, testTenant); err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, tenantID); err != nil {
		t.Fatalf("ProvisionTenant failed: %v", err)
	}

	// First, insert a project that should be committed
	err = mt.Manager.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", "Committed Project")
		return err
	})
	if err != nil {
		t.Fatalf("WithTenantTx (commit) failed: %v", err)
	}

	// Now attempt a transaction that should be rolled back
	expectedErr := fmt.Errorf("intentional rollback error")
	err = mt.Manager.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", "Should Be Rolled Back")
		if err != nil {
			return err
		}
		return expectedErr // This should trigger rollback
	})

	if err != expectedErr {
		t.Errorf("WithTenantTx should return the error from fn, got: %v", err)
	}

	// Verify only the committed project exists
	conn, err := mt.Manager.GetTenantConn(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenantConn failed: %v", err)
	}
	defer conn.Close()

	var count int
	err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count projects: %v", err)
	}

	if count != 1 {
		t.Errorf("Should have exactly 1 project (the committed one), got %d", count)
	}

	var name string
	err = conn.QueryRowContext(ctx, "SELECT name FROM projects").Scan(&name)
	if err != nil {
		t.Fatalf("Failed to get project name: %v", err)
	}

	if name != "Committed Project" {
		t.Errorf("Only 'Committed Project' should exist, got: %s", name)
	}

	// Verify the rolled back project doesn't exist
	var rolledBackCount int
	err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE name = 'Should Be Rolled Back'").Scan(&rolledBackCount)
	if err != nil {
		t.Fatalf("Failed to check rolled back project: %v", err)
	}

	if rolledBackCount > 0 {
		t.Errorf("ROLLBACK FAILED: 'Should Be Rolled Back' project still exists")
	}

	t.Log("WithTenantTx correctly rolls back on error")
}

func TestDatabase_GetTenantConn_ConcurrentIsolation(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()

	// Create multiple tenants
	const numTenants = 5
	tenantIDs := make([]uuid.UUID, numTenants)

	defer cleanupTestData(tdb.db, tenantIDs)

	for i := 0; i < numTenants; i++ {
		id := uuid.New()
		tenantIDs[i] = id

		tnt := &tenant.Tenant{
			ID:        id,
			Name:      fmt.Sprintf("Concurrent Isolation Tenant %d", i),
			Subdomain: fmt.Sprintf("concurrent-iso-%d-%s", i, id.String()[:8]),
		}

		if err := mt.Manager.CreateTenant(ctx, tnt); err != nil {
			t.Fatalf("CreateTenant %d failed: %v", i, err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatalf("ProvisionTenant %d failed: %v", i, err)
		}
	}

	// Run concurrent operations - each goroutine gets a connection,
	// inserts data, reads it back, and verifies isolation
	type result struct {
		tenantIndex int
		err         error
		sawOwnData  bool
		sawOthers   bool
	}

	results := make(chan result, numTenants)

	for i := 0; i < numTenants; i++ {
		go func(index int) {
			res := result{tenantIndex: index}

			// Get connection for this tenant
			conn, err := mt.Manager.GetTenantConn(ctx, tenantIDs[index])
			if err != nil {
				res.err = fmt.Errorf("GetTenantConn failed: %w", err)
				results <- res
				return
			}
			defer conn.Close()

			// Insert tenant-specific data
			uniqueValue := fmt.Sprintf("Tenant%d-UniqueData-%s", index, uuid.New().String()[:8])
			_, err = conn.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", uniqueValue)
			if err != nil {
				res.err = fmt.Errorf("insert failed: %w", err)
				results <- res
				return
			}

			// Small delay to allow other goroutines to interleave
			time.Sleep(10 * time.Millisecond)

			// Read back and verify we only see our own data
			rows, err := conn.QueryContext(ctx, "SELECT name FROM projects")
			if err != nil {
				res.err = fmt.Errorf("query failed: %w", err)
				results <- res
				return
			}
			defer rows.Close()

			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					res.err = fmt.Errorf("scan failed: %w", err)
					results <- res
					return
				}

				if strings.HasPrefix(name, fmt.Sprintf("Tenant%d-", index)) {
					res.sawOwnData = true
				} else if strings.HasPrefix(name, "Tenant") {
					// Saw another tenant's data - this is a leak!
					res.sawOthers = true
				}
			}

			results <- res
		}(i)
	}

	// Collect results
	var failures []string
	for i := 0; i < numTenants; i++ {
		res := <-results
		if res.err != nil {
			failures = append(failures, fmt.Sprintf("Tenant %d error: %v", res.tenantIndex, res.err))
		}
		if !res.sawOwnData {
			failures = append(failures, fmt.Sprintf("Tenant %d didn't see its own data", res.tenantIndex))
		}
		if res.sawOthers {
			failures = append(failures, fmt.Sprintf("DATA LEAKAGE: Tenant %d saw other tenants' data", res.tenantIndex))
		}
	}

	if len(failures) > 0 {
		for _, f := range failures {
			t.Error(f)
		}
	} else {
		t.Logf("All %d tenants correctly isolated during concurrent GetTenantConn operations", numTenants)
	}
}

func TestDatabase_WithTenantTx_ConcurrentIsolation(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()

	// Create multiple tenants
	const numTenants = 5
	tenantIDs := make([]uuid.UUID, numTenants)

	defer cleanupTestData(tdb.db, tenantIDs)

	for i := 0; i < numTenants; i++ {
		id := uuid.New()
		tenantIDs[i] = id

		tnt := &tenant.Tenant{
			ID:        id,
			Name:      fmt.Sprintf("TxConcurrent Tenant %d", i),
			Subdomain: fmt.Sprintf("txconcurrent-%d-%s", i, id.String()[:8]),
		}

		if err := mt.Manager.CreateTenant(ctx, tnt); err != nil {
			t.Fatalf("CreateTenant %d failed: %v", i, err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatalf("ProvisionTenant %d failed: %v", i, err)
		}
	}

	// Run concurrent transactions
	type result struct {
		tenantIndex int
		err         error
		count       int
		sawOthers   bool
	}

	results := make(chan result, numTenants)

	for i := 0; i < numTenants; i++ {
		go func(index int) {
			res := result{tenantIndex: index}

			// Use WithTenantTx to insert and verify in same transaction
			err := mt.Manager.WithTenantTx(ctx, tenantIDs[index], func(tx *sql.Tx) error {
				// Insert tenant-specific data
				uniqueValue := fmt.Sprintf("TxTenant%d-Data", index)
				_, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", uniqueValue)
				if err != nil {
					return err
				}

				// Small delay
				time.Sleep(10 * time.Millisecond)

				// Count projects in this transaction
				err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&res.count)
				if err != nil {
					return err
				}

				// Check for other tenants' data
				var otherCount int
				for j := 0; j < numTenants; j++ {
					if j == index {
						continue
					}
					var c int
					err := tx.QueryRowContext(ctx,
						"SELECT COUNT(*) FROM projects WHERE name LIKE $1",
						fmt.Sprintf("TxTenant%d%%", j)).Scan(&c)
					if err != nil {
						return err
					}
					otherCount += c
				}
				res.sawOthers = otherCount > 0

				return nil
			})

			if err != nil {
				res.err = err
			}

			results <- res
		}(i)
	}

	// Collect results
	var failures []string
	for i := 0; i < numTenants; i++ {
		res := <-results
		if res.err != nil {
			failures = append(failures, fmt.Sprintf("Tenant %d error: %v", res.tenantIndex, res.err))
		}
		if res.count != 1 {
			failures = append(failures, fmt.Sprintf("Tenant %d expected 1 project, saw %d", res.tenantIndex, res.count))
		}
		if res.sawOthers {
			failures = append(failures, fmt.Sprintf("DATA LEAKAGE: Tenant %d saw other tenants' data in transaction", res.tenantIndex))
		}
	}

	if len(failures) > 0 {
		for _, f := range failures {
			t.Error(f)
		}
	} else {
		t.Logf("All %d tenants correctly isolated during concurrent WithTenantTx operations", numTenants)
	}
}

// TestDatabase_GetTenantConn_ResetsSearchPathOnClose verifies that a connection
// returned to the pool after GetTenantConn does not keep the tenant search_path.
func TestDatabase_GetTenantConn_ResetsSearchPathOnClose(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}

	config := testConfig(connStr)
	// Force every query through the same underlying connection so a leaked
	// session setting is guaranteed to be observed.
	config.Database.MaxOpenConns = 1
	config.Database.MaxIdleConns = 1

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()
	defer cleanupTestData(tdb.db, []uuid.UUID{tenantID})

	testTenant := &tenant.Tenant{
		ID:        tenantID,
		Name:      "Reset SearchPath Tenant",
		Subdomain: fmt.Sprintf("reset-sp-%s", tenantID.String()[:8]),
	}
	if err := mt.Manager.CreateTenant(ctx, testTenant); err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, tenantID); err != nil {
		t.Fatalf("ProvisionTenant failed: %v", err)
	}

	conn, err := mt.Manager.GetTenantConn(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenantConn failed: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close failed: %v", err)
	}

	var searchPath string
	if err := mt.GetDatabase().QueryRowContext(ctx, "SHOW search_path").Scan(&searchPath); err != nil {
		t.Fatalf("Failed to read pool search_path: %v", err)
	}

	tenantSchema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(tenantID.String(), "-", "_"))
	if strings.Contains(searchPath, tenantSchema) {
		t.Errorf("pool connection still has tenant search_path after GetTenantConn close: %s", searchPath)
	}
}

// ---------------------------------------------------------------------------
// Migration tests (pure-Go migration manager)
// ---------------------------------------------------------------------------

// migrationTestEnv provisions n active tenants and returns the MultiTenant plus their IDs.
func migrationTestEnv(t *testing.T, tdb *testDB, n int) (*MultiTenant, []uuid.UUID) {
	t.Helper()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	config := testConfig(connStr)
	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	ctx := context.Background()
	var ids []uuid.UUID
	for i := 0; i < n; i++ {
		id := uuid.New()
		tt := &tenant.Tenant{
			ID:        id,
			Name:      fmt.Sprintf("Migration Tenant %d", i),
			Subdomain: fmt.Sprintf("mig-%s", id.String()[:8]),
		}
		// CreateTenant no longer defaults an unset plan to basic (that SaaS
		// opinion moved out of the core); set it explicitly so downstream
		// limit-enforcement tests can resolve plan limits.
		tt.SetPlan(limits.PlanBasic)
		if err := mt.Manager.CreateTenant(ctx, tt); err != nil {
			t.Fatalf("CreateTenant failed: %v", err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatalf("ProvisionTenant failed: %v", err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		cleanupTestData(tdb.db, ids)
		mt.Close()
	})
	return mt, ids
}

// bareMigrationTestEnv is like migrationTestEnv but provisions tenants with
// no MigrationsDir, so their schema has no migrations recorded. It is used
// by tests below that apply their own ad-hoc "001"/"002" migrations directly
// and would otherwise collide with the fixture migrations' version numbers.
func bareMigrationTestEnv(t *testing.T, tdb *testDB, n int) (*MultiTenant, []uuid.UUID) {
	t.Helper()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	config := testConfig(connStr)
	config.Database.MigrationsDir = ""
	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	ctx := context.Background()
	var ids []uuid.UUID
	for i := 0; i < n; i++ {
		id := uuid.New()
		tt := &tenant.Tenant{
			ID:        id,
			Name:      fmt.Sprintf("Migration Tenant %d", i),
			Subdomain: fmt.Sprintf("mig-%s", id.String()[:8]),
		}
		tt.SetPlan(limits.PlanBasic)
		if err := mt.Manager.CreateTenant(ctx, tt); err != nil {
			t.Fatalf("CreateTenant failed: %v", err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatalf("ProvisionTenant failed: %v", err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		cleanupTestData(tdb.db, ids)
		mt.Close()
	})
	return mt, ids
}

func widgetsMigration() *tenant.Migration {
	rollback := "DROP TABLE widgets"
	return &tenant.Migration{
		Version:     "001",
		Name:        "create_widgets",
		SQL:         "CREATE TABLE widgets (id SERIAL PRIMARY KEY, name TEXT NOT NULL)",
		RollbackSQL: &rollback,
	}
}

func TestDatabase_Migrations_ApplyCreatesTableInTenantSchemaAndRecordsIt(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := bareMigrationTestEnv(t, tdb, 1)
	ctx := context.Background()
	tenantID := ids[0]
	schema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(tenantID.String(), "-", "_"))

	if err := mt.Migrations.ApplyMigration(ctx, tenantID, widgetsMigration()); err != nil {
		t.Fatalf("ApplyMigration failed: %v", err)
	}

	inTenant, _ := tdb.tableExistsInSchema(schema, "widgets")
	if !inTenant {
		t.Errorf("widgets table should exist in %s", schema)
	}
	inPublic, _ := tdb.tableExistsInSchema("public", "widgets")
	if inPublic {
		t.Errorf("widgets table must not leak into public")
	}

	applied, err := mt.Migrations.IsMigrationApplied(ctx, tenantID, "001")
	if err != nil || !applied {
		t.Errorf("IsMigrationApplied = %v, %v; want true, nil", applied, err)
	}

	list, err := mt.Migrations.GetAppliedMigrations(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetAppliedMigrations failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 applied migration, got %d", len(list))
	}
	if list[0].Version != "001" || list[0].Name != "create_widgets" {
		t.Errorf("unexpected migration record: %+v", list[0])
	}
	if list[0].Checksum == nil || len(*list[0].Checksum) != 64 {
		t.Errorf("expected sha256 checksum to be recorded, got %v", list[0].Checksum)
	}
	if list[0].AppliedAt.IsZero() {
		t.Errorf("expected applied_at to be set")
	}
}

func TestDatabase_Migrations_ApplyIsIdempotent(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := bareMigrationTestEnv(t, tdb, 1)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := mt.Migrations.ApplyMigration(ctx, ids[0], widgetsMigration()); err != nil {
			t.Fatalf("ApplyMigration #%d failed: %v", i+1, err)
		}
	}
	list, _ := mt.Migrations.GetAppliedMigrations(ctx, ids[0])
	if len(list) != 1 {
		t.Errorf("expected exactly 1 record after re-apply, got %d", len(list))
	}
}

func TestDatabase_Migrations_FailedMigrationIsNotRecorded(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := bareMigrationTestEnv(t, tdb, 1)
	ctx := context.Background()

	bad := &tenant.Migration{Version: "002", Name: "broken", SQL: "CREATE TABLE ("}
	if err := mt.Migrations.ApplyMigration(ctx, ids[0], bad); err == nil {
		t.Fatalf("expected error applying invalid SQL")
	}
	applied, _ := mt.Migrations.IsMigrationApplied(ctx, ids[0], "002")
	if applied {
		t.Errorf("failed migration must not be recorded as applied")
	}
}

func TestDatabase_Migrations_ApplyFailsWhenSchemaMissing(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, _ := bareMigrationTestEnv(t, tdb, 1)
	ctx := context.Background()

	unprovisioned := uuid.New()
	tt := &tenant.Tenant{ID: unprovisioned, Name: "Unprovisioned", Subdomain: fmt.Sprintf("unprov-%s", unprovisioned.String()[:8])}
	if err := mt.Manager.CreateTenant(ctx, tt); err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}
	if err := mt.Migrations.ApplyMigration(ctx, unprovisioned, widgetsMigration()); err == nil {
		t.Fatalf("expected error when tenant schema does not exist")
	}
}

func TestDatabase_Migrations_RollbackRunsRollbackSQLAndRemovesRecord(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := bareMigrationTestEnv(t, tdb, 1)
	ctx := context.Background()
	schema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(ids[0].String(), "-", "_"))

	if err := mt.Migrations.ApplyMigration(ctx, ids[0], widgetsMigration()); err != nil {
		t.Fatalf("ApplyMigration failed: %v", err)
	}
	if err := mt.Migrations.RollbackMigration(ctx, ids[0], "001"); err != nil {
		t.Fatalf("RollbackMigration failed: %v", err)
	}
	exists, _ := tdb.tableExistsInSchema(schema, "widgets")
	if exists {
		t.Errorf("widgets table should have been dropped by rollback")
	}
	applied, _ := mt.Migrations.IsMigrationApplied(ctx, ids[0], "001")
	if applied {
		t.Errorf("migration should no longer be recorded after rollback")
	}
}

func TestDatabase_Migrations_RollbackWithoutRollbackSQLFails(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := bareMigrationTestEnv(t, tdb, 1)
	ctx := context.Background()

	m := widgetsMigration()
	m.RollbackSQL = nil
	if err := mt.Migrations.ApplyMigration(ctx, ids[0], m); err != nil {
		t.Fatalf("ApplyMigration failed: %v", err)
	}
	if err := mt.Migrations.RollbackMigration(ctx, ids[0], "001"); err == nil {
		t.Fatalf("expected error rolling back a migration with no rollback SQL")
	}
	applied, _ := mt.Migrations.IsMigrationApplied(ctx, ids[0], "001")
	if !applied {
		t.Errorf("migration record must remain when rollback fails")
	}
}

func TestDatabase_Migrations_ApplyToAllTenants_SkipsInactiveAndReportsFailures(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := bareMigrationTestEnv(t, tdb, 3)
	ctx := context.Background()
	okID, suspendedID, brokenID := ids[0], ids[1], ids[2]

	if err := mt.Manager.SuspendTenant(ctx, suspendedID); err != nil {
		t.Fatalf("SuspendTenant failed: %v", err)
	}
	// Make brokenID fail by removing its schema out from under the manager.
	brokenSchema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(brokenID.String(), "-", "_"))
	if _, err := tdb.db.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, brokenSchema)); err != nil {
		t.Fatalf("failed to drop schema: %v", err)
	}

	err := mt.Migrations.ApplyToAllTenants(ctx, widgetsMigration())
	if err == nil {
		t.Fatalf("expected an error because one tenant's schema is missing")
	}
	if !strings.Contains(err.Error(), brokenID.String()) {
		t.Errorf("error should name the failing tenant %s, got: %v", brokenID, err)
	}

	if applied, _ := mt.Migrations.IsMigrationApplied(ctx, okID, "001"); !applied {
		t.Errorf("active tenant should have received the migration")
	}
	if applied, _ := mt.Migrations.IsMigrationApplied(ctx, suspendedID, "001"); applied {
		t.Errorf("suspended tenant should not receive the migration")
	}
	if applied, _ := mt.Migrations.IsMigrationApplied(ctx, brokenID, "001"); applied {
		t.Errorf("tenant without schema must not be recorded as migrated")
	}
}

// ---------------------------------------------------------------------------
// Limit enforcement tests
// ---------------------------------------------------------------------------

func TestDatabase_Limits_ProjectCountAboveBasicPlanIsRejected(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()
	tenantID := ids[0]

	// Basic plan allows 10 projects; insert 11.
	err := mt.Manager.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		for i := 0; i < 11; i++ {
			if _, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", fmt.Sprintf("p%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to seed projects: %v", err)
	}

	_, err = mt.Limits.CheckTenant(ctx, tenantID)
	if err == nil {
		t.Fatalf("expected CheckTenant to fail with 11 projects on the basic plan")
	}
	var tenantErr *tenant.TenantError
	if !errors.As(err, &tenantErr) || tenantErr.Code != "LIMIT_EXCEEDED" {
		t.Errorf("expected LIMIT_EXCEEDED TenantError, got: %v", err)
	}
}

func TestDatabase_Limits_ProjectCountAtBasicPlanLimitIsAllowed(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()
	tenantID := ids[0]

	err := mt.Manager.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		for i := 0; i < 10; i++ {
			if _, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", fmt.Sprintf("p%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to seed projects: %v", err)
	}

	if _, err := mt.Limits.CheckTenant(ctx, tenantID); err != nil {
		t.Errorf("10 projects should be within the basic plan limit, got: %v", err)
	}
}

func TestDatabase_Limits_CheckerIsExposedAndSwappable(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()

	checker := mt.Limits
	if checker == nil {
		t.Fatalf("MultiTenant.Limits is nil")
	}
	if checker.GetUsageTracker() == nil {
		t.Fatalf("New() should wire a default usage tracker")
	}

	// Tighten the basic plan at runtime and verify it takes effect.
	if err := checker.UpdateLimit(limits.PlanBasic, "max_projects", 0); err != nil {
		t.Fatalf("UpdateLimit failed: %v", err)
	}
	err := mt.Manager.WithTenantTx(ctx, ids[0], func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ('only')")
		return err
	})
	if err != nil {
		t.Fatalf("failed to seed project: %v", err)
	}
	if _, err := mt.Limits.CheckTenant(ctx, ids[0]); err == nil {
		t.Errorf("expected failure after lowering max_projects to 0")
	}
}

// ---------------------------------------------------------------------------
// Schema listing
// ---------------------------------------------------------------------------

func TestDatabase_ListTenantSchemas_OnlyMatchesPrefixLiterally(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()
	want := fmt.Sprintf("tenant_%s", strings.ReplaceAll(ids[0].String(), "-", "_"))

	// "_" is a LIKE wildcard; a schema named tenantXdecoy must not be listed
	// for the prefix "tenant_".
	decoy := fmt.Sprintf("tenantXdecoy_%s", ids[0].String()[:8])
	if _, err := tdb.db.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, decoy)); err != nil {
		t.Fatalf("failed to create decoy schema: %v", err)
	}
	t.Cleanup(func() { _, _ = tdb.db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, decoy)) })

	sm := database.NewSchemaManager(mt.GetDatabase(), tdb.logger, "tenant_")
	schemas, err := sm.ListTenantSchemas(ctx)
	if err != nil {
		t.Fatalf("ListTenantSchemas failed: %v", err)
	}

	found := false
	for _, s := range schemas {
		if s == want {
			found = true
		}
		if s == decoy {
			t.Errorf("ListTenantSchemas returned %q, which does not start with the literal prefix tenant_", s)
		}
	}
	if !found {
		t.Errorf("ListTenantSchemas did not include provisioned schema %s (got %v)", want, schemas)
	}
}

// ---------------------------------------------------------------------------
// SetTenantDB middleware
// ---------------------------------------------------------------------------

// tenantHandler wraps handler with ResolveTenant (header strategy) and SetTenantDB.
func tenantHandler(mt *MultiTenant, handler http.Handler) http.Handler {
	mw := httpmw.New(mt.Manager, mt.Resolver, mt.GetLogger(), httpmw.Config{})
	return httpmw.Chain(handler, mw.ResolveTenant(), mw.SetTenantDB())
}

var countProjectsHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	conn, ok := tenant.GetTenantConnFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"no tenant conn"}`, http.StatusInternalServerError)
		return
	}
	var n int
	if err := conn.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"count":%d}`, n)
})

func seedProjects(t *testing.T, mt *MultiTenant, tenantID uuid.UUID, n int) {
	t.Helper()
	err := mt.Manager.WithTenantTx(context.Background(), tenantID, func(tx *sql.Tx) error {
		for i := 0; i < n; i++ {
			if _, err := tx.ExecContext(context.Background(), "INSERT INTO projects (name) VALUES ($1)", fmt.Sprintf("p%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to seed projects for %s: %v", tenantID, err)
	}
}

func TestDatabase_SetTenantDB_HandlerSeesOnlyResolvedTenantsRows(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	config := testConfig(connStr)
	config.Resolver.Strategy = tenant.ResolverHeader
	config.Resolver.HeaderName = "X-Tenant"
	mt, err := New(config)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer mt.Close()
	ctx := context.Background()

	var ids []uuid.UUID
	var subs []string
	for i, n := range []int{2, 5} {
		id := uuid.New()
		sub := fmt.Sprintf("mw-%d-%s", i, id.String()[:8])
		tt := &tenant.Tenant{ID: id, Name: sub, Subdomain: sub}
		if err := mt.Manager.CreateTenant(ctx, tt); err != nil {
			t.Fatalf("CreateTenant failed: %v", err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatalf("ProvisionTenant failed: %v", err)
		}
		seedProjects(t, mt, id, n)
		ids = append(ids, id)
		subs = append(subs, sub)
	}
	defer cleanupTestData(tdb.db, ids)

	h := tenantHandler(mt, countProjectsHandler)
	for i, want := range []int{2, 5} {
		req := httptest.NewRequest(http.MethodGet, "/count", nil)
		req.Header.Set("X-Tenant", subs[i])
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		var body struct{ Count int }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusOK || body.Count != want {
			t.Errorf("tenant %s: got %d %s, want 200 count=%d", subs[i], rec.Code, rec.Body.String(), want)
		}
	}
}

func TestDatabase_SetTenantDB_ReleasesConnectionWithCleanSearchPath(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	config := testConfig(connStr)
	config.Database.MaxOpenConns = 1
	config.Database.MaxIdleConns = 1
	config.Resolver.Strategy = tenant.ResolverHeader
	config.Resolver.HeaderName = "X-Tenant"
	mt, err := New(config)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer mt.Close()
	ctx := context.Background()

	id := uuid.New()
	sub := fmt.Sprintf("mw-rel-%s", id.String()[:8])
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: sub, Subdomain: sub}); err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("ProvisionTenant failed: %v", err)
	}
	defer cleanupTestData(tdb.db, []uuid.UUID{id})

	h := tenantHandler(mt, countProjectsHandler)
	req := httptest.NewRequest(http.MethodGet, "/count", nil)
	req.Header.Set("X-Tenant", sub)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("request failed: %d %s", rec.Code, rec.Body.String())
	}

	// With a single-connection pool, the request's connection is the only one.
	// If it were still held or still carried the tenant search_path, this
	// query would block or see the tenant schema.
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var searchPath string
	if err := mt.GetDatabase().QueryRowContext(qctx, "SHOW search_path").Scan(&searchPath); err != nil {
		t.Fatalf("pool query after request failed (connection not released?): %v", err)
	}
	schema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(id.String(), "-", "_"))
	if strings.Contains(searchPath, schema) {
		t.Errorf("pool connection still carries tenant search_path after request: %s", searchPath)
	}
}

// SetTenantDB must pass requests through when no tenant was resolved, so
// skipped paths such as /health keep working; the handler simply gets no conn.
func TestDatabase_SetTenantDB_WithoutResolvedTenantPassesThroughWithoutConn(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, _ := migrationTestEnv(t, tdb, 0)

	mw := httpmw.New(mt.Manager, mt.Resolver, mt.GetLogger(), httpmw.Config{SkipPaths: []string{"/health"}})
	var hadConn, reached bool
	h := httpmw.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, hadConn = tenant.GetTenantConnFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}), mw.ResolveTenant(), mw.SetTenantDB())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || !reached {
		t.Errorf("skipped path should reach the handler, got %d", rec.Code)
	}
	if hadConn {
		t.Errorf("no tenant connection should be set when no tenant was resolved")
	}
}

// ---------------------------------------------------------------------------
// File-based migration helpers
// ---------------------------------------------------------------------------

func writeMigrationFiles(t *testing.T, dir, version, name, up, down string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s_%s.up.sql", version, name)), []byte(up), 0o644); err != nil {
		t.Fatalf("write up file: %v", err)
	}
	if down != "" {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s_%s.down.sql", version, name)), []byte(down), 0o644); err != nil {
			t.Fatalf("write down file: %v", err)
		}
	}
}

func fileMigrationEnv(t *testing.T, tdb *testDB, n int) (*MultiTenant, *database.MigrationManager, []uuid.UUID, string) {
	t.Helper()
	dir := t.TempDir()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	config := testConfig(connStr)
	config.Database.MigrationsDir = dir
	mt, err := New(config)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	ctx := context.Background()
	var ids []uuid.UUID
	for i := 0; i < n; i++ {
		id := uuid.New()
		sub := fmt.Sprintf("fmig-%d-%s", i, id.String()[:8])
		if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: sub, Subdomain: sub}); err != nil {
			t.Fatalf("CreateTenant failed: %v", err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatalf("ProvisionTenant failed: %v", err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() { cleanupTestData(tdb.db, ids); mt.Close() })
	mm, ok := mt.Migrations.(*database.MigrationManager)
	if !ok {
		t.Fatalf("Migrations is %T, want *database.MigrationManager", mt.Migrations)
	}
	return mt, mm, ids, dir
}

func TestDatabase_MigrationFiles_ApplyFromFileAppliesUpAndRecordsDown(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	_, mm, ids, dir := fileMigrationEnv(t, tdb, 1)
	ctx := context.Background()
	writeMigrationFiles(t, dir, "001", "create_gadgets",
		"CREATE TABLE gadgets (id SERIAL PRIMARY KEY)", "DROP TABLE gadgets")
	schema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(ids[0].String(), "-", "_"))

	if err := mm.ApplyMigrationFromFile(ctx, ids[0], "001", "create_gadgets"); err != nil {
		t.Fatalf("ApplyMigrationFromFile failed: %v", err)
	}

	if exists, _ := tdb.tableExistsInSchema(schema, "gadgets"); !exists {
		t.Errorf("gadgets table should exist in %s", schema)
	}
	applied, err := mm.GetAppliedMigrations(ctx, ids[0])
	if err != nil || len(applied) != 1 {
		t.Fatalf("GetAppliedMigrations = %v, %v; want one record", applied, err)
	}
	if applied[0].RollbackSQL == nil || *applied[0].RollbackSQL != "DROP TABLE gadgets" {
		t.Errorf("rollback SQL from .down.sql should be recorded, got %v", applied[0].RollbackSQL)
	}
	if err := mm.RollbackMigration(ctx, ids[0], "001"); err != nil {
		t.Fatalf("RollbackMigration failed: %v", err)
	}
	if exists, _ := tdb.tableExistsInSchema(schema, "gadgets"); exists {
		t.Errorf("gadgets table should be dropped after rollback")
	}
}

func TestDatabase_MigrationFiles_ApplyFromFileMissingFileFails(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	_, mm, ids, _ := fileMigrationEnv(t, tdb, 1)

	err := mm.ApplyMigrationFromFile(context.Background(), ids[0], "999", "does_not_exist")
	if err == nil {
		t.Fatalf("expected error for missing migration file")
	}
	if applied, _ := mm.IsMigrationApplied(context.Background(), ids[0], "999"); applied {
		t.Errorf("nothing should be recorded for a missing file")
	}
}

func TestDatabase_MigrationFiles_ApplyToAllTenantsFromFile(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	_, mm, ids, dir := fileMigrationEnv(t, tdb, 2)
	ctx := context.Background()
	writeMigrationFiles(t, dir, "002", "create_gizmos", "CREATE TABLE gizmos (id SERIAL PRIMARY KEY)", "")

	if err := mm.ApplyMigrationToAllTenantsFromFile(ctx, "002", "create_gizmos"); err != nil {
		t.Fatalf("ApplyMigrationToAllTenantsFromFile failed: %v", err)
	}

	for _, id := range ids {
		schema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(id.String(), "-", "_"))
		if exists, _ := tdb.tableExistsInSchema(schema, "gizmos"); !exists {
			t.Errorf("gizmos table missing in %s", schema)
		}
		if applied, _ := mm.IsMigrationApplied(ctx, id, "002"); !applied {
			t.Errorf("migration 002 not recorded for %s", id)
		}
	}
	files, err := mm.ListMigrationFiles()
	if err != nil || len(files) != 1 || files[0] != "002_create_gizmos" {
		t.Errorf("ListMigrationFiles = %v, %v; want [002_create_gizmos]", files, err)
	}
}

func TestDatabase_ApplyPending_AppliesFilesInOrderAndIsIdempotent(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	_, mm, ids, dir := fileMigrationEnv(t, tdb, 1)
	ctx := context.Background()
	writeMigrationFiles(t, dir, "002", "add_col", "ALTER TABLE things ADD COLUMN note TEXT", "")
	writeMigrationFiles(t, dir, "001", "create_things", "CREATE TABLE things (id SERIAL PRIMARY KEY)", "DROP TABLE things")

	if err := mm.ApplyPending(ctx, ids[0]); err != nil {
		t.Fatalf("ApplyPending: %v", err)
	}
	applied, _ := mm.GetAppliedMigrations(ctx, ids[0])
	if len(applied) != 2 || applied[0].Version != "001" || applied[1].Version != "002" {
		t.Fatalf("expected 001 then 002, got %+v", applied)
	}

	// Second run applies nothing new.
	if err := mm.ApplyPending(ctx, ids[0]); err != nil {
		t.Fatalf("second ApplyPending: %v", err)
	}
	if applied, _ = mm.GetAppliedMigrations(ctx, ids[0]); len(applied) != 2 {
		t.Errorf("re-run should not add records, got %d", len(applied))
	}

	// A file added later is picked up.
	writeMigrationFiles(t, dir, "003", "add_more", "ALTER TABLE things ADD COLUMN more TEXT", "")
	if err := mm.ApplyPending(ctx, ids[0]); err != nil {
		t.Fatalf("third ApplyPending: %v", err)
	}
	if ok, _ := mm.IsMigrationApplied(ctx, ids[0], "003"); !ok {
		t.Errorf("003 should be applied")
	}
}

func TestDatabase_ApplyPendingToAllTenants_BringsOlderTenantUpToDate(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, mm, ids, dir := fileMigrationEnv(t, tdb, 2)
	ctx := context.Background()
	writeMigrationFiles(t, dir, "001", "create_things", "CREATE TABLE things (id SERIAL PRIMARY KEY)", "")
	if err := mm.ApplyPending(ctx, ids[0]); err != nil { // only the first tenant is current
		t.Fatalf("ApplyPending: %v", err)
	}
	if err := mt.Manager.SuspendTenant(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	third := uuid.New()
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: third, Name: "third", Subdomain: "third-" + third.String()[:8]}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, third); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupTestData(tdb.db, []uuid.UUID{third}) })

	if err := mm.ApplyPendingToAllTenants(ctx); err != nil {
		t.Fatalf("ApplyPendingToAllTenants: %v", err)
	}
	if ok, _ := mm.IsMigrationApplied(ctx, third, "001"); !ok {
		t.Errorf("active tenant should have been migrated")
	}
	if ok, _ := mm.IsMigrationApplied(ctx, ids[1], "001"); ok {
		t.Errorf("suspended tenant must not be migrated")
	}
}

func TestDatabase_Provision_EmptyMigrationsDirYieldsEmptySchema(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	config := testConfig(connStr)
	config.Database.MigrationsDir = ""
	mt, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer mt.Close()
	ctx := context.Background()
	id := uuid.New()
	defer cleanupTestData(tdb.db, []uuid.UUID{id})
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "empty", Subdomain: "empty-" + id.String()[:8]}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	schema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(id.String(), "-", "_"))
	tables, _ := tdb.listTablesInSchema(schema)
	if len(tables) != 0 {
		t.Errorf("expected no tables, got %v", tables)
	}
	got, _ := mt.Manager.GetTenant(ctx, id)
	if got.Status != tenant.StatusActive {
		t.Errorf("status = %s, want active", got.Status)
	}
}

func TestDatabase_Provision_ResumesAfterFailingMigration(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, mm, _, dir := fileMigrationEnv(t, tdb, 0)
	ctx := context.Background()
	writeMigrationFiles(t, dir, "001", "ok", "CREATE TABLE ok_table (id INT)", "")
	writeMigrationFiles(t, dir, "002", "broken", "CREATE TABLE (", "")

	id := uuid.New()
	t.Cleanup(func() { cleanupTestData(tdb.db, []uuid.UUID{id}) })
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "r", Subdomain: "resume-" + id.String()[:8]}); err != nil {
		t.Fatal(err)
	}

	err := mt.Manager.ProvisionTenant(ctx, id)
	if err == nil || !strings.Contains(err.Error(), "002") {
		t.Fatalf("expected failure naming version 002, got %v", err)
	}
	got, _ := mt.Manager.GetTenant(ctx, id)
	if got.Status != tenant.StatusPending {
		t.Errorf("status after failure = %s, want pending", got.Status)
	}
	if ok, _ := mm.IsMigrationApplied(ctx, id, "001"); !ok {
		t.Errorf("001 should remain applied")
	}

	// Fix the migration and retry.
	writeMigrationFiles(t, dir, "002", "broken", "CREATE TABLE fixed_table (id INT)", "")
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("retry: %v", err)
	}
	got, _ = mt.Manager.GetTenant(ctx, id)
	if got.Status != tenant.StatusActive {
		t.Errorf("status after retry = %s, want active", got.Status)
	}
	if ok, _ := mm.IsMigrationApplied(ctx, id, "002"); !ok {
		t.Errorf("002 should be applied after retry")
	}
}

func TestDatabase_UsageTracker_CountsConfiguredTableAndSkipsOthers(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()
	seedProjects(t, mt, ids[0], 3)

	tracker := mt.Limits.GetUsageTracker()
	v, err := tracker.GetCurrentUsage(ctx, ids[0], "max_projects")
	if err != nil || v != 3 {
		t.Errorf("max_projects usage = %v, %v; want 3", v, err)
	}
	v, err = tracker.GetCurrentUsage(ctx, ids[0], "api_calls_per_month")
	if err != nil || v != nil {
		t.Errorf("unmapped limit should report nil, got %v, %v", v, err)
	}
}

func TestDatabase_GetStats_ReportsMigrationsAndLimitsUsage(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()
	seedProjects(t, mt, ids[0], 2)

	stats, err := mt.Manager.GetStats(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if !stats.SchemaExists {
		t.Error("SchemaExists should be true")
	}
	if stats.AppliedMigrations != 2 { // fixture has 001 and 002
		t.Errorf("AppliedMigrations = %d, want 2", stats.AppliedMigrations)
	}
	usage, err := mt.Limits.Usage(ctx, ids[0])
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage["max_projects"] != 2 || usage["max_users"] != 0 {
		t.Errorf("Usage = %v, want max_projects=2 max_users=0", usage)
	}
}

func TestDatabase_Metadata_RoundTripsThroughRepositoryAndFindByMetadata(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 2)
	ctx := context.Background()

	first, _ := mt.Manager.GetTenant(ctx, ids[0])
	if first.Metadata == nil {
		t.Fatal("Metadata should never be nil after a read")
	}
	tenant.NewStripeExtension(first.Metadata).SetCustomerID("cus_abc")
	first.Metadata.SetInt("seats", 7)
	if err := mt.Manager.UpdateTenant(ctx, first); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}

	got, _ := mt.Manager.GetTenant(ctx, ids[0])
	if id, _ := tenant.NewStripeExtension(got.Metadata).GetCustomerID(); id != "cus_abc" {
		t.Errorf("customer id = %q", id)
	}
	if n, _ := got.Metadata.GetInt("seats"); n != 7 {
		t.Errorf("seats = %d", n)
	}
	bySub, _ := mt.Manager.GetTenantBySubdomain(ctx, got.Subdomain)
	if bySub.Metadata["seats"] == nil {
		t.Errorf("GetBySubdomain should load metadata")
	}
	list, _, _ := mt.Manager.ListTenants(ctx, 1, 100)
	found := false
	for _, lt := range list {
		if lt.ID == ids[0] && lt.Metadata["seats"] != nil {
			found = true
		}
	}
	if !found {
		t.Errorf("List should load metadata")
	}

	repo := dbpostgres.NewRepository(mt.GetDatabase(), mt.GetLogger())
	matches, err := repo.FindByMetadata(ctx, "stripe_customer_id", "cus_abc")
	if err != nil || len(matches) != 1 || matches[0].ID != ids[0] {
		t.Errorf("FindByMetadata = %v, %v; want only %s", matches, err, ids[0])
	}
}

func TestDatabase_Metadata_ColumnIsAddedToPreExistingTenantsTable(t *testing.T) {
	tdb := newTestDB(t)
	// Registered via t.Cleanup (instead of the usual defer tdb.close()) so we
	// can register a second cleanup below that is guaranteed to run first:
	// t.Cleanup callbacks fire in last-registered-first-called order, and all
	// only after the test function (and its own defers) has returned, so a
	// plain "defer tdb.close()" here would already have closed tdb.db by the
	// time any t.Cleanup ran.
	t.Cleanup(tdb.close)
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	// Simulate a database created by an older version: no metadata column.
	if _, err := tdb.db.Exec(`DROP TABLE IF EXISTS public.tenant_migrations; DROP TABLE IF EXISTS public.tenants`); err != nil {
		t.Fatal(err)
	}
	if _, err := tdb.db.Exec(`CREATE TABLE public.tenants (
		id UUID PRIMARY KEY, name VARCHAR(255) NOT NULL, subdomain VARCHAR(255) UNIQUE NOT NULL,
		plan_type VARCHAR(50) NOT NULL DEFAULT 'basic', status VARCHAR(50) NOT NULL DEFAULT 'pending',
		schema_name VARCHAR(255) NOT NULL,
		created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	// This stand-in table is deliberately missing chk_plan_type/chk_status (a
	// faithful "older version" simulation). Drop it once the test is done so
	// the next test's New() -> CreateMasterTables (CREATE TABLE IF NOT
	// EXISTS, a no-op against a table that already exists) rebuilds the real
	// public.tenants with its constraints, instead of leaving the shared
	// database permanently degraded. Registered after t.Cleanup(tdb.close)
	// above so it runs first, while tdb.db is still open.
	t.Cleanup(func() {
		if _, err := tdb.db.Exec(`DROP TABLE IF EXISTS public.tenant_migrations; DROP TABLE IF EXISTS public.tenants CASCADE`); err != nil {
			t.Errorf("failed to restore public.tenants after test: %v", err)
		}
	})

	mt, err := New(testConfig(connStr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer mt.Close()

	var exists bool
	err = tdb.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema='public' AND table_name='tenants' AND column_name='metadata')`).Scan(&exists)
	if err != nil || !exists {
		t.Errorf("metadata column should have been added, exists=%v err=%v", exists, err)
	}
}

func TestNew_FailsWhenMasterTablesCannotBeCreated(t *testing.T) {
	tdb := newTestDB(t)
	// Registered via t.Cleanup (instead of the usual defer tdb.close()) so we
	// can register a second cleanup below that is guaranteed to run first,
	// following the same ordering pattern as
	// TestDatabase_Metadata_ColumnIsAddedToPreExistingTenantsTable.
	t.Cleanup(tdb.close)
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	// Replace public.tenant_migrations with a VIEW. CreateMasterTables'
	// "CREATE TABLE IF NOT EXISTS public.tenant_migrations" is then a no-op
	// (a relation with that name already exists), but the later
	// "CREATE INDEX ... ON public.tenant_migrations(tenant_id)" fails: you
	// cannot create a plain index on a view. That makes CreateMasterTables
	// fail deterministically without touching public.tenants.
	if _, err := tdb.db.Exec(`DROP TABLE IF EXISTS public.tenant_migrations`); err != nil {
		t.Fatal(err)
	}
	if _, err := tdb.db.Exec(`CREATE VIEW public.tenant_migrations AS SELECT 1 AS tenant_id`); err != nil {
		t.Fatal(err)
	}
	// Restore the real table once the test is done so later tests see the
	// schema they expect. Registered after t.Cleanup(tdb.close) above so it
	// runs first, while tdb.db is still open.
	t.Cleanup(func() {
		if _, err := tdb.db.Exec(`DROP VIEW IF EXISTS public.tenant_migrations`); err != nil {
			t.Errorf("failed to drop stand-in view after test: %v", err)
		}
		if _, err := tdb.db.Exec(`CREATE TABLE IF NOT EXISTS public.tenant_migrations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL,
			version VARCHAR(50) NOT NULL,
			name VARCHAR(255) NOT NULL,
			applied_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			rollback_sql TEXT,
			checksum VARCHAR(64),
			FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE,
			UNIQUE(tenant_id, version)
		)`); err != nil {
			t.Errorf("failed to restore public.tenant_migrations after test: %v", err)
		}
	})

	mt, err := New(testConfig(connStr))
	if err == nil {
		mt.Close()
		t.Fatal("New() should fail when master tables cannot be created")
	}
	if !strings.Contains(err.Error(), "master tables") {
		t.Errorf("error should mention master tables, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle hooks against a real database
// ---------------------------------------------------------------------------

type stampingHook struct {
	tenant.BaseHook
	mgr tenant.Manager
}

func (h *stampingHook) Name() string { return "stamper" }
func (h *stampingHook) OnTenantProvisioned(ctx context.Context, t *tenant.Tenant) error {
	t.Metadata.SetString("provisioned_by", "stamper")
	return h.mgr.UpdateTenant(ctx, t)
}

func TestDatabase_Hooks_ProvisionedHookCanPersistMetadata(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, _ := migrationTestEnv(t, tdb, 0)
	mt.Manager.RegisterHook(&stampingHook{mgr: mt.Manager})
	ctx := context.Background()

	id := uuid.New()
	t.Cleanup(func() { cleanupTestData(tdb.db, []uuid.UUID{id}) })
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "h", Subdomain: "hook-" + id.String()[:8]}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	got, _ := mt.Manager.GetTenant(ctx, id)
	if v, _ := got.Metadata.GetString("provisioned_by"); v != "stamper" {
		t.Errorf("hook-written metadata not persisted: %v", got.Metadata)
	}
	if got.Status != tenant.StatusActive {
		t.Errorf("status = %s", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Pooler-safe tenant connections: no session state, every statement scoped
// ---------------------------------------------------------------------------

// TestDatabase_TenantConn_LeavesSessionSearchPathUntouched pins the invariant
// that makes tenant.Conn safe behind transaction-mode poolers: it never sets
// session-level search_path on the underlying connection.
func TestDatabase_TenantConn_LeavesSessionSearchPathUntouched(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()

	conn, err := mt.Manager.GetTenantConn(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetTenantConn: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "INSERT INTO projects (name) VALUES ('scoped')"); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
		t.Fatalf("QueryRowContext: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}

	// Bypass the wrapper: the session itself must not carry the tenant schema.
	var searchPath string
	if err := conn.Unwrap().QueryRowContext(ctx, "SHOW search_path").Scan(&searchPath); err != nil {
		t.Fatalf("SHOW search_path on underlying conn: %v", err)
	}
	schema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(ids[0].String(), "-", "_"))
	if strings.Contains(searchPath, schema) {
		t.Errorf("session search_path carries tenant schema (%s); tenant.Conn must use SET LOCAL per statement", searchPath)
	}
}

func TestDatabase_TenantConn_EveryOperationIsScopedToItsTenant(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 2)
	ctx := context.Background()
	seedProjects(t, mt, ids[0], 2)
	seedProjects(t, mt, ids[1], 5)

	for i, want := range []int{2, 5} {
		conn, err := mt.Manager.GetTenantConn(ctx, ids[i])
		if err != nil {
			t.Fatalf("GetTenantConn: %v", err)
		}

		// ExecContext
		if _, err := conn.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", "extra"); err != nil {
			t.Fatalf("ExecContext: %v", err)
		}
		want++

		// QueryContext with a Rows that must be closed
		rows, err := conn.QueryContext(ctx, "SELECT name FROM projects ORDER BY name")
		if err != nil {
			t.Fatalf("QueryContext: %v", err)
		}
		got := 0
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			got++
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("rows.Close: %v", err)
		}
		if got != want {
			t.Errorf("tenant %d: QueryContext saw %d rows, want %d", i, got, want)
		}

		// QueryRowContext
		var n int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
			t.Fatalf("QueryRowContext: %v", err)
		}
		if n != want {
			t.Errorf("tenant %d: QueryRowContext = %d, want %d", i, n, want)
		}

		// BeginTx: a transaction that is already scoped
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ('in-tx')"); err != nil {
			t.Fatalf("tx exec: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		want++
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&n); err != nil || n != want {
			t.Errorf("tenant %d: after BeginTx count = %d (%v), want %d", i, n, err, want)
		}

		if err := conn.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	// Nothing leaked into public.
	if exists, _ := tdb.tableExistsInSchema("public", "projects"); exists {
		t.Errorf("projects table must not exist in public")
	}
}

func TestDatabase_TenantConn_FailedStatementRollsBackAndConnStaysUsable(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()

	conn, err := mt.Manager.GetTenantConn(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetTenantConn: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "INSERT INTO nope (x) VALUES (1)"); err == nil {
		t.Fatalf("expected error for missing table")
	}
	// The connection must not be stuck in an aborted transaction.
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
		t.Fatalf("connection unusable after failed statement: %v", err)
	}
}

// assertPlanTypeNeutralized checks that the legacy plan_type column has no
// default and is nullable, and that each given row's plan_type is NULL —
// the state the one-time migration must leave behind so a later restart is
// a no-op instead of re-deriving a plan from a column v0.8 no longer writes.
func assertPlanTypeNeutralized(t *testing.T, tdb *testDB, ids ...uuid.UUID) {
	t.Helper()
	var colDefault sql.NullString
	var nullable string
	if err := tdb.db.QueryRow(`SELECT column_default, is_nullable FROM information_schema.columns
		WHERE table_schema='public' AND table_name='tenants' AND column_name='plan_type'`).Scan(&colDefault, &nullable); err != nil {
		t.Fatal(err)
	}
	if colDefault.Valid {
		t.Errorf("plan_type still has a default: %q", colDefault.String)
	}
	if nullable != "YES" {
		t.Errorf("plan_type is_nullable = %q, want YES", nullable)
	}
	for _, id := range ids {
		var pt sql.NullString
		if err := tdb.db.QueryRow(`SELECT plan_type FROM public.tenants WHERE id = $1`, id).Scan(&pt); err != nil {
			t.Fatal(err)
		}
		if pt.Valid {
			t.Errorf("tenant %s plan_type = %q, want NULL", id, pt.String)
		}
	}
}

// TestDatabase_PlanTypeColumnIsCopiedIntoMetadataOnce simulates a v0.7 table
// whose rows have plan_type and no metadata plan, then starts New. The copy
// must run exactly once: plan_type ends up nullable with no default and
// NULL on every row, so a later restart (or a plan-less CreateTenant after
// the upgrade) can never have a plan re-derived for it from the column.
func TestDatabase_PlanTypeColumnIsCopiedIntoMetadataOnce(t *testing.T) {
	tdb := newTestDB(t)
	t.Cleanup(tdb.close)
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	if _, err := tdb.db.Exec(`DROP TABLE IF EXISTS public.tenant_migrations; DROP TABLE IF EXISTS public.tenants CASCADE`); err != nil {
		t.Fatal(err)
	}
	// v0.7 shape: plan_type column present, metadata present.
	if _, err := tdb.db.Exec(`CREATE TABLE public.tenants (
		id UUID PRIMARY KEY, name VARCHAR(255) NOT NULL, subdomain VARCHAR(255) UNIQUE NOT NULL,
		plan_type VARCHAR(50) NOT NULL DEFAULT 'basic', status VARCHAR(50) NOT NULL DEFAULT 'pending',
		schema_name VARCHAR(255) NOT NULL, metadata JSONB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	// Restore the real table for later tests (LIFO: runs before tdb.close).
	t.Cleanup(func() {
		_, _ = tdb.db.Exec(`DROP TABLE IF EXISTS public.tenant_migrations; DROP TABLE IF EXISTS public.tenants CASCADE`)
	})
	proID, keepID := uuid.New(), uuid.New()
	if _, err := tdb.db.Exec(`INSERT INTO public.tenants (id, name, subdomain, plan_type, schema_name, metadata) VALUES
		($1, 'pro', 'plan-pro-'||$1::text, 'pro', 'tenant_x', '{}'),
		($2, 'keep', 'plan-keep-'||$2::text, 'basic', 'tenant_y', '{"plan":"custom"}')`, proID, keepID); err != nil {
		t.Fatal(err)
	}

	mt, err := New(testConfig(connStr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer mt.Close()

	got, err := mt.Manager.GetTenant(context.Background(), proID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan() != "pro" {
		t.Errorf("plan copied from plan_type = %q, want pro", got.Plan())
	}
	kept, _ := mt.Manager.GetTenant(context.Background(), keepID)
	if kept.Plan() != "custom" {
		t.Errorf("existing metadata plan must not be overwritten, got %q", kept.Plan())
	}

	// Column is left in place for the operator to drop later, but made
	// nullable with no default and emptied on every row.
	var hasCol bool
	if err := tdb.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema='public' AND table_name='tenants' AND column_name='plan_type')`).Scan(&hasCol); err != nil || !hasCol {
		t.Errorf("plan_type column should still exist, exists=%v err=%v", hasCol, err)
	}
	assertPlanTypeNeutralized(t, tdb, proID, keepID)

	// Second start is idempotent and new rows never touch plan_type.
	mt2, err := New(testConfig(connStr))
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer mt2.Close()
	newID := uuid.New()
	nt := &tenant.Tenant{ID: newID, Name: "new", Subdomain: "plan-new-" + newID.String()[:8]}
	nt.SetPlan("enterprise")
	if err := mt2.Manager.CreateTenant(context.Background(), nt); err != nil {
		t.Fatalf("CreateTenant on migrated table: %v", err)
	}
	back, _ := mt2.Manager.GetTenant(context.Background(), newID)
	if back.Plan() != "enterprise" {
		t.Errorf("plan on new tenant = %q", back.Plan())
	}

	// The critical repro of finding #1: a tenant created after the upgrade
	// WITHOUT SetPlan must not silently acquire "basic" (the legacy
	// column's old default) on this or any later restart.
	noPlanID := uuid.New()
	noPlan := &tenant.Tenant{ID: noPlanID, Name: "no-plan", Subdomain: "plan-none-" + noPlanID.String()[:8]}
	if err := mt2.Manager.CreateTenant(context.Background(), noPlan); err != nil {
		t.Fatalf("CreateTenant (no plan): %v", err)
	}
	if got, _ := mt2.Manager.GetTenant(context.Background(), noPlanID); got.Plan() != "" {
		t.Errorf("plan-less tenant right after CreateTenant = %q, want \"\"", got.Plan())
	}

	// A third start (another restart/deploy) must leave it alone.
	mt3, err := New(testConfig(connStr))
	if err != nil {
		t.Fatalf("third New: %v", err)
	}
	defer mt3.Close()
	if got, _ := mt3.Manager.GetTenant(context.Background(), noPlanID); got.Plan() != "" {
		t.Errorf("plan-less tenant after a second restart = %q, want \"\" (finding #1 regression)", got.Plan())
	}

	// Nothing in the table should have a non-NULL plan_type any more.
	var nonNull int
	if err := tdb.db.QueryRow(`SELECT COUNT(*) FROM public.tenants WHERE plan_type IS NOT NULL`).Scan(&nonNull); err != nil {
		t.Fatal(err)
	}
	if nonNull != 0 {
		t.Errorf("rows with non-NULL plan_type = %d, want 0", nonNull)
	}
}

// TestDatabase_PlanTypeSetPlanEmptySurvivesRestart simulates an operator who
// deliberately clears a plan that was copied from the legacy plan_type
// column with SetPlan(""). That must stick across restarts, not be silently
// reinstated by the migration re-running.
func TestDatabase_PlanTypeSetPlanEmptySurvivesRestart(t *testing.T) {
	tdb := newTestDB(t)
	t.Cleanup(tdb.close)
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	if _, err := tdb.db.Exec(`DROP TABLE IF EXISTS public.tenant_migrations; DROP TABLE IF EXISTS public.tenants CASCADE`); err != nil {
		t.Fatal(err)
	}
	// v0.7 shape again.
	if _, err := tdb.db.Exec(`CREATE TABLE public.tenants (
		id UUID PRIMARY KEY, name VARCHAR(255) NOT NULL, subdomain VARCHAR(255) UNIQUE NOT NULL,
		plan_type VARCHAR(50) NOT NULL DEFAULT 'basic', status VARCHAR(50) NOT NULL DEFAULT 'pending',
		schema_name VARCHAR(255) NOT NULL, metadata JSONB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = tdb.db.Exec(`DROP TABLE IF EXISTS public.tenant_migrations; DROP TABLE IF EXISTS public.tenants CASCADE`)
	})
	id := uuid.New()
	if _, err := tdb.db.Exec(`INSERT INTO public.tenants (id, name, subdomain, plan_type, schema_name, metadata) VALUES
		($1, 'clearme', 'plan-clear-'||$1::text, 'basic', 'tenant_z', '{}')`, id); err != nil {
		t.Fatal(err)
	}

	mt, err := New(testConfig(connStr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer mt.Close()

	tn, err := mt.Manager.GetTenant(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if tn.Plan() != "basic" {
		t.Fatalf("plan copied from legacy column = %q, want basic", tn.Plan())
	}

	tn.SetPlan("")
	if err := mt.Manager.UpdateTenant(context.Background(), tn); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	cleared, err := mt.Manager.GetTenant(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Plan() != "" {
		t.Fatalf("plan right after SetPlan(\"\") = %q, want \"\"", cleared.Plan())
	}

	// Restart: the cleared plan must not be reinstated from plan_type,
	// because plan_type was already nulled by the first startup's migration.
	mt2, err := New(testConfig(connStr))
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer mt2.Close()
	after, err := mt2.Manager.GetTenant(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Plan() != "" {
		t.Errorf("plan after restart = %q, want \"\" (finding #1 regression)", after.Plan())
	}
	assertPlanTypeNeutralized(t, tdb, id)
}

func TestDatabase_EnforceLimits_ThroughHTTPMiddleware(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	config := testConfig(connStr)
	config.Resolver.Strategy = tenant.ResolverHeader
	config.Resolver.HeaderName = "X-Tenant"
	mt, err := New(config)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer mt.Close()
	ctx := context.Background()

	id := uuid.New()
	sub := "enforce-" + id.String()[:8]
	tn := &tenant.Tenant{ID: id, Name: sub, Subdomain: sub}
	tn.SetPlan(limits.PlanBasic)
	if err := mt.Manager.CreateTenant(ctx, tn); err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("ProvisionTenant failed: %v", err)
	}
	defer cleanupTestData(tdb.db, []uuid.UUID{id})

	seedProjects(t, mt, id, 11) // basic allows 10

	var seen limits.FlexibleLimits
	h := httpmw.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = limits.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}), mt.HTTPMiddleware.ResolveTenant(), mt.HTTPMiddleware.EnforceLimits())

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Tenant", sub)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("11 projects on basic: got %d %s, want 402", rec.Code, rec.Body.String())
	}

	// Under the limit, the checked limits reach the handler.
	if err := mt.Manager.WithTenantTx(ctx, id, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM projects")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("0 projects: got %d %s", rec.Code, rec.Body.String())
	}
	if n, _ := seen.GetInt("max_projects"); n != 10 {
		t.Errorf("limits in context = %v", seen)
	}
}

func TestDatabase_Limits_UsageCountsConfiguredTables(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	seedProjects(t, mt, ids[0], 4)
	usage, err := mt.Limits.Usage(context.Background(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if usage["max_projects"] != 4 || usage["max_users"] != 0 {
		t.Errorf("usage = %v", usage)
	}
}

func TestDatabase_NoLimitsConfigDisablesEnforcement(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	cfg := testConfig(connStr)
	cfg.Limits = nil
	mt, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer mt.Close()
	if mt.Limits != nil {
		t.Fatal("Limits should be nil when Config.Limits is nil")
	}
	ctx := context.Background()
	id := uuid.New()
	defer cleanupTestData(tdb.db, []uuid.UUID{id})
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "n", Subdomain: "nolimits-" + id.String()[:8]}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatal(err)
	}
	seedProjects(t, mt, id, 50)
	h := httpmw.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		withTenantCtx(id), mt.HTTPMiddleware.EnforceLimits())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("EnforceLimits with no checker must pass through, got %d", rec.Code)
	}
}

// withTenantCtx injects a resolved tenant context for tests that skip ResolveTenant.
func withTenantCtx(id uuid.UUID) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), tenant.ContextKeyTenant, &tenant.Context{TenantID: id, Status: tenant.StatusActive})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

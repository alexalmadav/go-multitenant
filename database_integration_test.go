package multitenant

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alexalmadav/go-multitenant/database"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
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
}

// newTestDB creates a new test database connection using testcontainers
func newTestDB(t *testing.T) *testDB {
	if testing.Short() {
		t.Skip("Skipping database integration test in short mode")
	}

	// Check if we should use an external database
	if dbURL := os.Getenv("TEST_DATABASE_URL"); dbURL != "" {
		db, err := sql.Open("postgres", dbURL)
		if err != nil {
			t.Fatalf("Failed to connect to external database: %v", err)
		}
		if err := db.Ping(); err != nil {
			t.Fatalf("Failed to ping external database: %v", err)
		}
		return &testDB{
			db:     db,
			logger: zaptest.NewLogger(t),
			t:      t,
		}
	}

	// Try default local PostgreSQL first
	defaultURL := "postgres://postgres:postgres@localhost:5432/test_multitenant?sslmode=disable"
	db, err := sql.Open("postgres", defaultURL)
	if err == nil {
		if err := db.Ping(); err == nil {
			t.Log("Using local PostgreSQL database")
			return &testDB{
				db:     db,
				logger: zaptest.NewLogger(t),
				t:      t,
			}
		}
		db.Close()
	}

	// Use testcontainers as fallback
	ctx := context.Background()
	container, err := setupPostgresContainer(ctx)
	if err != nil {
		t.Skipf("Skipping integration test - no database available. Set TEST_DATABASE_URL or ensure Docker is running: %v", err)
	}

	db, err = sql.Open("postgres", container.ConnectionString)
	if err != nil {
		container.Terminate(ctx)
		t.Fatalf("Failed to connect to container database: %v", err)
	}

	if err := db.Ping(); err != nil {
		container.Terminate(ctx)
		t.Fatalf("Failed to ping container database: %v", err)
	}

	return &testDB{
		db:        db,
		logger:    zaptest.NewLogger(t),
		t:         t,
		container: container,
	}
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
	if tdb.container != nil {
		return tdb.container.ConnectionString
	}
	return os.Getenv("TEST_DATABASE_URL")
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

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	// Cleanup before and after
	defer tdb.cleanupSchema(tenantID, schemaPrefix)
	tdb.cleanupSchema(tenantID, schemaPrefix)

	// Record which tables existed in public before we start
	publicTablesBefore, err := tdb.listTablesInSchema("public")
	if err != nil {
		t.Fatalf("Failed to list public tables before test: %v", err)
	}
	publicTablesBeforeMap := make(map[string]bool)
	for _, table := range publicTablesBefore {
		publicTablesBeforeMap[table] = true
	}

	// Create schema manager and create tenant schema
	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)
	err = sm.CreateTenantSchema(ctx, tenantID, "Test Tenant")
	if err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
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

	// Expected tenant tables
	expectedTables := []string{"documents", "projects", "tasks", "tenant_users"}

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

func TestDatabase_SchemaCreation_SearchPathIsolation(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	defer tdb.cleanupSchema(tenantID, schemaPrefix)
	tdb.cleanupSchema(tenantID, schemaPrefix)

	// Create schema manager and tenant schema
	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)
	err := sm.CreateTenantSchema(ctx, tenantID, "Test Tenant")
	if err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
	}

	schemaName := sm.GetSchemaName(tenantID)

	// Set search path to tenant schema
	err = sm.SetSearchPath(tdb.db, tenantID)
	if err != nil {
		t.Fatalf("Failed to set search path: %v", err)
	}

	// Verify search path is set correctly
	searchPath, err := tdb.getCurrentSearchPath()
	if err != nil {
		t.Fatalf("Failed to get search path: %v", err)
	}

	// Search path should include tenant schema
	if !strings.Contains(searchPath, schemaName) {
		t.Errorf("Search path should contain tenant schema %s, got: %s", schemaName, searchPath)
	}

	// Insert data using unqualified table name - should go to tenant schema
	_, err = tdb.db.Exec(`INSERT INTO projects (name, description) VALUES ($1, $2)`, "Test Project", "Description")
	if err != nil {
		t.Fatalf("Failed to insert into projects: %v", err)
	}

	// Verify data is in tenant schema using fully qualified name
	var count int
	err = tdb.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s".projects`, schemaName)).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count projects in tenant schema: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 project in tenant schema, got %d", count)
	}

	// Verify data is NOT in public schema (if table exists there)
	exists, err := tdb.tableExistsInSchema("public", "projects")
	if err != nil {
		t.Fatalf("Failed to check if projects exists in public: %v", err)
	}
	if exists {
		var publicCount int
		err = tdb.db.QueryRow(`SELECT COUNT(*) FROM public.projects`).Scan(&publicCount)
		if err == nil && publicCount > 0 {
			t.Errorf("Data leaked to public.projects: found %d rows", publicCount)
		}
	}
}

func TestDatabase_MultiTenant_DataIsolation(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()

	ctx := context.Background()
	tenant1ID := uuid.New()
	tenant2ID := uuid.New()
	schemaPrefix := "tenant_"

	defer tdb.cleanupSchema(tenant1ID, schemaPrefix)
	defer tdb.cleanupSchema(tenant2ID, schemaPrefix)
	tdb.cleanupSchema(tenant1ID, schemaPrefix)
	tdb.cleanupSchema(tenant2ID, schemaPrefix)

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)

	// Create both tenant schemas
	err := sm.CreateTenantSchema(ctx, tenant1ID, "Tenant One")
	if err != nil {
		t.Fatalf("Failed to create tenant1 schema: %v", err)
	}

	err = sm.CreateTenantSchema(ctx, tenant2ID, "Tenant Two")
	if err != nil {
		t.Fatalf("Failed to create tenant2 schema: %v", err)
	}

	schema1 := sm.GetSchemaName(tenant1ID)
	schema2 := sm.GetSchemaName(tenant2ID)

	// Insert data into tenant1's schema using fully qualified names
	_, err = tdb.db.Exec(fmt.Sprintf(`INSERT INTO "%s".projects (name, description) VALUES ($1, $2)`, schema1),
		"Tenant1 Secret Project", "This belongs to tenant 1")
	if err != nil {
		t.Fatalf("Failed to insert into tenant1 projects: %v", err)
	}

	// Insert data into tenant2's schema
	_, err = tdb.db.Exec(fmt.Sprintf(`INSERT INTO "%s".projects (name, description) VALUES ($1, $2)`, schema2),
		"Tenant2 Secret Project", "This belongs to tenant 2")
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

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	defer tdb.cleanupSchema(tenantID, schemaPrefix)
	tdb.cleanupSchema(tenantID, schemaPrefix)

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)
	err := sm.CreateTenantSchema(ctx, tenantID, "Test Tenant")
	if err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
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
	expectedIndexes := []string{"idx_projects_status", "idx_projects_created_at", "idx_tasks_project_id"}
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

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	defer tdb.cleanupSchema(tenantID, schemaPrefix)
	tdb.cleanupSchema(tenantID, schemaPrefix)

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)
	err := sm.CreateTenantSchema(ctx, tenantID, "Test Tenant")
	if err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
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

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	defer tdb.cleanupSchema(tenantID, schemaPrefix)
	tdb.cleanupSchema(tenantID, schemaPrefix)

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)
	err := sm.CreateTenantSchema(ctx, tenantID, "Test Tenant")
	if err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
	}

	schemaName := sm.GetSchemaName(tenantID)

	// Insert a project
	var projectID uuid.UUID
	var createdAt, updatedAt time.Time
	err = tdb.db.QueryRow(fmt.Sprintf(`
		INSERT INTO "%s".projects (name, description)
		VALUES ($1, $2)
		RETURNING id, created_at, updated_at
	`, schemaName), "Trigger Test", "Testing triggers").Scan(&projectID, &createdAt, &updatedAt)
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

	ctx := context.Background()
	tenantID := uuid.New()
	schemaPrefix := "tenant_"

	sm := database.NewSchemaManager(tdb.db, tdb.logger, schemaPrefix)

	// Create schema
	err := sm.CreateTenantSchema(ctx, tenantID, "Test Tenant")
	if err != nil {
		t.Fatalf("Failed to create tenant schema: %v", err)
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

	config := tenant.DefaultConfig()
	config.Database.DSN = connStr
	config.Database.MigrationsDir = ""

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
		PlanType:  tenant.PlanBasic,
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

	config := tenant.DefaultConfig()
	config.Database.DSN = connStr

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
				PlanType:  tenant.PlanBasic,
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

	config := tenant.DefaultConfig()
	config.Database.DSN = connStr

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
		PlanType:  tenant.PlanBasic,
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

	config := tenant.DefaultConfig()
	config.Database.DSN = connStr

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
			PlanType:  tenant.PlanBasic,
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

	config := tenant.DefaultConfig()
	config.Database.DSN = connStr

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
		PlanType:  tenant.PlanBasic,
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

	config := tenant.DefaultConfig()
	config.Database.DSN = connStr

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
			PlanType:  tenant.PlanBasic,
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

	config := tenant.DefaultConfig()
	config.Database.DSN = connStr

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
			PlanType:  tenant.PlanBasic,
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

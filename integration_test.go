package multitenant

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant/limits"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

// Integration tests use the same database discovery as
// database_integration_test.go: TEST_DATABASE_URL, then a local PostgreSQL,
// then a testcontainers-managed postgres:16 instance.

// testDSN is the DSN of the database most recently opened by setupTestDatabase.
var testDSN string

func getTestDatabaseURL() string {
	return testDSN
}

func setupTestDatabase(t *testing.T) *sql.DB {
	tdb := newTestDB(t)
	t.Cleanup(tdb.close)
	testDSN = tdb.dsn
	return tdb.db
}

func cleanupTestData(db *sql.DB, tenantIDs []uuid.UUID) {
	// Drop tenant schemas
	for _, tenantID := range tenantIDs {
		schemaName := fmt.Sprintf("tenant_%s", strings.ReplaceAll(tenantID.String(), "-", "_"))
		schemaName = "\"" + schemaName + "\""
		_, _ = db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
	}

	// Clean master tables
	_, _ = db.Exec("DELETE FROM tenant_migrations")
	_, _ = db.Exec("DELETE FROM tenants")
}

func TestIntegration_FullTenantLifecycle(t *testing.T) {
	db := setupTestDatabase(t)
	defer db.Close()

	config := testConfig(getTestDatabaseURL())

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()

	// Clean up at end
	defer cleanupTestData(db, []uuid.UUID{tenantID})

	// Test 1: Create tenant
	tenant := &tenant.Tenant{
		ID:        tenantID,
		Name:      "Integration Test Tenant",
		Subdomain: "integration-test",
	}
	tenant.SetPlan(limits.PlanBasic)

	err = mt.Manager.CreateTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}

	// Test 2: Retrieve tenant
	retrievedTenant, err := mt.Manager.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant failed: %v", err)
	}

	if retrievedTenant.Name != tenant.Name {
		t.Errorf("Retrieved tenant name = %v, want %v", retrievedTenant.Name, tenant.Name)
	}

	// Test 3: Provision tenant (create schema)
	err = mt.Manager.ProvisionTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ProvisionTenant failed: %v", err)
	}

	// Test 4: Check tenant status is active
	retrievedTenant, err = mt.Manager.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant after provision failed: %v", err)
	}

	if retrievedTenant.Status != StatusActive {
		t.Errorf("Tenant status after provision = %v, want %v", retrievedTenant.Status, StatusActive)
	}

	// Test 5: Test tenant context
	tenantCtx := mt.Manager.WithTenantContext(ctx, tenantID)
	if retrievedCtx, ok := GetTenantFromContext(tenantCtx); !ok {
		t.Error("WithTenantContext should add tenant to context")
	} else if retrievedCtx.TenantID != tenantID {
		t.Error("Tenant context should have correct tenant ID")
	}

	// Test 7: Suspend tenant
	err = mt.Manager.SuspendTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("SuspendTenant failed: %v", err)
	}

	// Test 8: Activate tenant
	err = mt.Manager.ActivateTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ActivateTenant failed: %v", err)
	}

	// Test 9: List tenants
	tenantsList, total, err := mt.Manager.ListTenants(ctx, 1, 10)
	if err != nil {
		t.Fatalf("ListTenants failed: %v", err)
	}

	if total < 1 {
		t.Error("ListTenants should return at least 1 tenant")
	}

	found := false
	for _, tenantItem := range tenantsList {
		if tenantItem.ID == tenantID {
			found = true
			break
		}
	}
	if !found {
		t.Error("ListTenants should include created tenant")
	}

	// Test 10: Get stats
	stats, err := mt.Manager.GetStats(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.TenantID != tenantID {
		t.Error("Stats should have correct tenant ID")
	}

	// Test 11: Check limits
	planLimits, err := mt.Limits.CheckTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("CheckTenant failed: %v", err)
	}

	if planLimits == nil {
		t.Error("CheckTenant should return the plan's limits")
	}

	// Test 12: Update tenant
	retrievedTenant.Name = "Updated Integration Test Tenant"
	err = mt.Manager.UpdateTenant(ctx, retrievedTenant)
	if err != nil {
		t.Fatalf("UpdateTenant failed: %v", err)
	}

	// Verify update
	updatedTenant, err := mt.Manager.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant after update failed: %v", err)
	}

	if updatedTenant.Name != "Updated Integration Test Tenant" {
		t.Error("Tenant name should be updated")
	}

	// Test 13: Delete tenant
	err = mt.Manager.DeleteTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("DeleteTenant failed: %v", err)
	}

	// Verify deletion (status should be cancelled)
	deletedTenant, err := mt.Manager.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant after delete failed: %v", err)
	}

	if deletedTenant.Status != StatusCancelled {
		t.Errorf("Deleted tenant status = %v, want %v", deletedTenant.Status, StatusCancelled)
	}
}

func TestIntegration_TenantResolver(t *testing.T) {
	db := setupTestDatabase(t)
	defer db.Close()

	config := testConfig(getTestDatabaseURL())

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	tenantID := uuid.New()

	// Clean up at end
	defer cleanupTestData(db, []uuid.UUID{tenantID})

	// Create and provision tenant
	testTenant := &tenant.Tenant{
		ID:        tenantID,
		Name:      "Resolver Test Tenant",
		Subdomain: "resolver-test",
	}

	err = mt.Manager.CreateTenant(ctx, testTenant)
	if err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}

	err = mt.Manager.ProvisionTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ProvisionTenant failed: %v", err)
	}

	// Test resolver with subdomain strategy
	req := CreateHTTPRequest("GET", "/api/projects", "resolver-test.example.com", nil)

	resolvedID, err := mt.Resolver.ResolveTenant(ctx, req)
	if err != nil {
		t.Fatalf("ResolveTenant failed: %v", err)
	}

	if resolvedID != tenantID {
		t.Errorf("ResolveTenant returned %v, want %v", resolvedID, tenantID)
	}

	// Test subdomain extraction
	subdomain, err := mt.Resolver.ExtractFromSubdomain("resolver-test.example.com")
	if err != nil {
		t.Fatalf("ExtractFromSubdomain failed: %v", err)
	}

	if subdomain != "resolver-test" {
		t.Errorf("ExtractFromSubdomain = %v, want resolver-test", subdomain)
	}

	// Test subdomain validation
	err = mt.Resolver.ValidateSubdomain("resolver-test")
	if err != nil {
		t.Errorf("ValidateSubdomain failed for valid subdomain: %v", err)
	}

	// Test invalid subdomain
	err = mt.Resolver.ValidateSubdomain("ab")
	if err == nil {
		t.Error("ValidateSubdomain should fail for too short subdomain")
	}
}

func TestIntegration_ConcurrentTenantOperations(t *testing.T) {
	db := setupTestDatabase(t)
	defer db.Close()

	config := testConfig(getTestDatabaseURL())

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()

	// Create multiple tenants concurrently
	const numTenants = 5
	tenantIDs := make([]uuid.UUID, numTenants)

	// Clean up at end
	defer cleanupTestData(db, tenantIDs)

	// Channel to collect results
	results := make(chan error, numTenants)

	// Create tenants concurrently
	for i := 0; i < numTenants; i++ {
		go func(index int) {
			tenantID := uuid.New()
			tenantIDs[index] = tenantID

			tenant := &tenant.Tenant{
				ID:        tenantID,
				Name:      fmt.Sprintf("Concurrent Test Tenant %d", index),
				Subdomain: fmt.Sprintf("concurrent-test-%d", index),
			}

			err := mt.Manager.CreateTenant(ctx, tenant)
			if err != nil {
				results <- fmt.Errorf("CreateTenant %d failed: %w", index, err)
				return
			}

			err = mt.Manager.ProvisionTenant(ctx, tenantID)
			if err != nil {
				results <- fmt.Errorf("ProvisionTenant %d failed: %w", index, err)
				return
			}

			results <- nil
		}(i)
	}

	// Wait for all operations to complete
	for i := 0; i < numTenants; i++ {
		if err := <-results; err != nil {
			t.Errorf("Concurrent operation failed: %v", err)
		}
	}

	// Verify all tenants were created
	_, total, err := mt.Manager.ListTenants(ctx, 1, 20)
	if err != nil {
		t.Fatalf("ListTenants failed: %v", err)
	}

	if total < numTenants {
		t.Errorf("Expected at least %d tenants, got %d", numTenants, total)
	}

	// Verify we can retrieve each tenant
	for _, tenantID := range tenantIDs {
		if tenantID == uuid.Nil {
			continue // Skip if creation failed
		}

		retrievedTenant, err := mt.Manager.GetTenant(ctx, tenantID)
		if err != nil {
			t.Errorf("Failed to retrieve tenant %v: %v", tenantID, err)
		}

		if retrievedTenant.Status != StatusActive {
			t.Errorf("Tenant %v status = %v, want %v", tenantID, retrievedTenant.Status, StatusActive)
		}
	}
}

func TestIntegration_TenantSchemaIsolation(t *testing.T) {
	db := setupTestDatabase(t)
	defer db.Close()

	config := testConfig(getTestDatabaseURL())

	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()

	// Create two tenants
	tenant1ID := uuid.New()
	tenant2ID := uuid.New()

	// Clean up at end
	defer cleanupTestData(db, []uuid.UUID{tenant1ID, tenant2ID})

	tenant1 := &tenant.Tenant{
		ID:        tenant1ID,
		Name:      "Schema Test Tenant 1",
		Subdomain: "schema-test-1",
	}

	tenant2 := &tenant.Tenant{
		ID:        tenant2ID,
		Name:      "Schema Test Tenant 2",
		Subdomain: "schema-test-2",
	}

	// Create and provision both tenants
	for _, tenantItem := range []*tenant.Tenant{tenant1, tenant2} {
		err = mt.Manager.CreateTenant(ctx, tenantItem)
		if err != nil {
			t.Fatalf("CreateTenant failed: %v", err)
		}

		err = mt.Manager.ProvisionTenant(ctx, tenantItem.ID)
		if err != nil {
			t.Fatalf("ProvisionTenant failed: %v", err)
		}
	}

	// Get tenant-specific connections using the safe GetTenantConn
	conn1, err := mt.Manager.GetTenantConn(ctx, tenant1ID)
	if err != nil {
		t.Fatalf("GetTenantConn for tenant1 failed: %v", err)
	}
	defer conn1.Close()

	conn2, err := mt.Manager.GetTenantConn(ctx, tenant2ID)
	if err != nil {
		t.Fatalf("GetTenantConn for tenant2 failed: %v", err)
	}
	defer conn2.Close()

	// Insert data into tenant1's projects table
	_, err = conn1.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", "Tenant 1 Project")
	if err != nil {
		t.Fatalf("Failed to insert into tenant1 projects: %v", err)
	}

	// Insert data into tenant2's projects table
	_, err = conn2.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", "Tenant 2 Project")
	if err != nil {
		t.Fatalf("Failed to insert into tenant2 projects: %v", err)
	}

	// Verify tenant1 can only see its own data
	var count int
	err = conn1.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE name LIKE 'Tenant 1%'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count tenant1 projects: %v", err)
	}
	if count != 1 {
		t.Errorf("Tenant1 should see 1 project, got %d", count)
	}

	// Verify tenant2 can only see its own data
	err = conn2.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE name LIKE 'Tenant 2%'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count tenant2 projects: %v", err)
	}
	if count != 1 {
		t.Errorf("Tenant2 should see 1 project, got %d", count)
	}

	// Verify tenant1 cannot see tenant2's data
	err = conn1.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE name LIKE 'Tenant 2%'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to check tenant1 isolation: %v", err)
	}
	if count != 0 {
		t.Errorf("Tenant1 should not see tenant2 projects, got %d", count)
	}
}

func TestIntegration_MasterTablesCreation(t *testing.T) {
	db := setupTestDatabase(t)
	defer db.Close()

	config := testConfig(getTestDatabaseURL())

	// Test that master tables are created automatically
	mt, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	// Check that tenants table exists
	var exists bool
	err = db.QueryRow(`
		SELECT EXISTS (
			SELECT FROM information_schema.tables 
			WHERE table_schema = 'public' 
			AND table_name = 'tenants'
		)
	`).Scan(&exists)
	if err != nil {
		t.Fatalf("Failed to check tenants table existence: %v", err)
	}
	if !exists {
		t.Error("Tenants table should be created automatically")
	}

	// Check that tenant_migrations table exists
	err = db.QueryRow(`
		SELECT EXISTS (
			SELECT FROM information_schema.tables 
			WHERE table_schema = 'public' 
			AND table_name = 'tenant_migrations'
		)
	`).Scan(&exists)
	if err != nil {
		t.Fatalf("Failed to check tenant_migrations table existence: %v", err)
	}
	if !exists {
		t.Error("Tenant_migrations table should be created automatically")
	}
}

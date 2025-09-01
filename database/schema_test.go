package database

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap/zaptest"
)

func TestNewSchemaManager(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Test with prefix
	sm := NewSchemaManager(nil, logger, "custom_")
	if sm.schemaPrefix != "custom_" {
		t.Errorf("NewSchemaManager() schemaPrefix = %v, want custom_", sm.schemaPrefix)
	}

	// Test with empty prefix (should use default)
	sm = NewSchemaManager(nil, logger, "")
	if sm.schemaPrefix != "tenant_" {
		t.Errorf("NewSchemaManager() should use default prefix, got %v", sm.schemaPrefix)
	}
}

func TestSchemaManager_GetSchemaName(t *testing.T) {
	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	tenantID := uuid.New()
	expected := "tenant_" + tenantID.String()
	// Replace hyphens with underscores as per implementation
	expected = "tenant_" + tenantID.String()
	expected = expected[:8] + "_" + expected[9:13] + "_" + expected[14:18] + "_" + expected[19:23] + "_" + expected[24:]

	schemaName := sm.GetSchemaName(tenantID)
	if len(schemaName) == 0 {
		t.Error("GetSchemaName() should not return empty string")
	}

	if !contains(schemaName, "tenant_") {
		t.Error("GetSchemaName() should contain prefix")
	}
}

func TestSchemaManager_CreateTenantSchema(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	tenantID := uuid.New()
	err := sm.CreateTenantSchema(context.Background(), tenantID, "Test Tenant")
	if err != nil {
		t.Errorf("CreateTenantSchema() error = %v, want nil", err)
	}
}

func TestSchemaManager_DropTenantSchema(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	tenantID := uuid.New()
	err := sm.DropTenantSchema(context.Background(), tenantID)
	if err != nil {
		t.Errorf("DropTenantSchema() error = %v, want nil", err)
	}
}

func TestSchemaManager_SchemaExists(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	tenantID := uuid.New()
	exists, err := sm.SchemaExists(context.Background(), tenantID)
	if err != nil {
		t.Errorf("SchemaExists() error = %v, want nil", err)
	}

	// Should return false for non-existent schema
	if exists {
		t.Error("SchemaExists() should return false for non-existent schema")
	}
}

func TestSchemaManager_SetSearchPath(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	tenantID := uuid.New()
	err := sm.SetSearchPath(nil, tenantID)
	if err == nil {
		t.Error("SetSearchPath() should error with nil database")
	}
}

func TestSchemaManager_ListTenantSchemas(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	schemas, err := sm.ListTenantSchemas(context.Background())
	if err != nil {
		t.Errorf("ListTenantSchemas() error = %v, want nil", err)
	}

	if schemas == nil {
		t.Error("ListTenantSchemas() should not return nil")
	}
}

func TestSchemaManager_quotedSchemaName(t *testing.T) {
	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	tenantID := uuid.New()
	quoted := sm.quotedSchemaName(tenantID)

	if len(quoted) < 2 {
		t.Error("quotedSchemaName() should return quoted string")
	}

	if quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		t.Error("quotedSchemaName() should start and end with quotes")
	}
}

func TestSchemaManager_createTenantTables(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	// Test with nil transaction should fail
	err := sm.createTenantTables(context.Background(), nil)
	if err == nil {
		t.Error("createTenantTables() should error with nil transaction")
	}
}

// Mock tests that don't require database

func TestSchemaManager_GetSchemaName_Format(t *testing.T) {
	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "test_")

	// Test with known UUID
	testUUID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	schemaName := sm.GetSchemaName(testUUID)

	expected := "test_550e8400_e29b_41d4_a716_446655440000"
	if schemaName != expected {
		t.Errorf("GetSchemaName() = %v, want %v", schemaName, expected)
	}
}

func TestSchemaManager_GetSchemaName_Consistency(t *testing.T) {
	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	tenantID := uuid.New()

	// Call multiple times with same ID
	name1 := sm.GetSchemaName(tenantID)
	name2 := sm.GetSchemaName(tenantID)

	if name1 != name2 {
		t.Error("GetSchemaName() should return consistent results for same tenant ID")
	}
}

func TestSchemaManager_GetSchemaName_DifferentPrefixes(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tenantID := uuid.New()

	sm1 := NewSchemaManager(nil, logger, "prefix1_")
	sm2 := NewSchemaManager(nil, logger, "prefix2_")

	name1 := sm1.GetSchemaName(tenantID)
	name2 := sm2.GetSchemaName(tenantID)

	if name1 == name2 {
		t.Error("Different prefixes should produce different schema names")
	}

	if !contains(name1, "prefix1_") {
		t.Error("Schema name should contain first prefix")
	}

	if !contains(name2, "prefix2_") {
		t.Error("Schema name should contain second prefix")
	}
}

func TestSchemaManager_quotedSchemaName_EscapesCorrectly(t *testing.T) {
	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	tenantID := uuid.New()
	quoted := sm.quotedSchemaName(tenantID)

	// Should be wrapped in double quotes
	if len(quoted) < 3 { // At least 2 quotes + 1 character
		t.Error("quotedSchemaName() result too short")
	}

	if quoted[0] != '"' {
		t.Error("quotedSchemaName() should start with double quote")
	}

	if quoted[len(quoted)-1] != '"' {
		t.Error("quotedSchemaName() should end with double quote")
	}

	// Extract the unquoted part
	unquoted := quoted[1 : len(quoted)-1]
	expected := sm.GetSchemaName(tenantID)

	if unquoted != expected {
		t.Errorf("quotedSchemaName() unquoted part = %v, want %v", unquoted, expected)
	}
}

func TestSchemaManager_Implementation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	sm := NewSchemaManager(nil, logger, "tenant_")

	// Test that it implements the SchemaManager interface
	// This is a compile-time check - if it doesn't implement the interface,
	// this won't compile
	var _ interface {
		GetSchemaName(uuid.UUID) string
	} = sm
}

// Helper function for string contains check
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr ||
		(len(s) > len(substr) && containsInner(s, substr))
}

func containsInner(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

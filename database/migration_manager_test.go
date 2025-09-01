package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap/zaptest"
)

func TestNewMigrationManager(t *testing.T) {
	logger := zaptest.NewLogger(t)
	migrationsDir := "/path/to/migrations"

	mgr := NewMigrationManager(nil, logger, migrationsDir)
	if mgr == nil {
		t.Error("NewMigrationManager() should not return nil")
	}

	// Type assertion to access private fields for testing
	if concreteMgr, ok := mgr.(*MigrationManager); ok {
		if concreteMgr.migrationsDir != migrationsDir {
			t.Errorf("NewMigrationManager() migrationsDir = %v, want %v", concreteMgr.migrationsDir, migrationsDir)
		}
	}
}

func TestMigrationManager_ApplyMigration(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "")

	tenantID := uuid.New()
	migration := &tenant.Migration{
		ID:      uuid.New(),
		Version: "001",
		Name:    "test_migration",
		SQL:     "CREATE TABLE test (id INT);",
	}

	err := mgr.ApplyMigration(context.Background(), tenantID, migration)
	if err != nil {
		t.Errorf("ApplyMigration() error = %v, want nil", err)
	}
}

func TestMigrationManager_ApplyToAllTenants(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "")

	migration := &tenant.Migration{
		ID:      uuid.New(),
		Version: "001",
		Name:    "test_migration",
		SQL:     "CREATE TABLE test (id INT);",
	}

	err := mgr.ApplyToAllTenants(context.Background(), migration)
	if err != nil {
		t.Errorf("ApplyToAllTenants() error = %v, want nil", err)
	}
}

func TestMigrationManager_RollbackMigration(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "")

	tenantID := uuid.New()
	err := mgr.RollbackMigration(context.Background(), tenantID, "001")
	if err != nil {
		t.Errorf("RollbackMigration() error = %v, want nil", err)
	}
}

func TestMigrationManager_GetAppliedMigrations(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "")

	tenantID := uuid.New()
	migrations, err := mgr.GetAppliedMigrations(context.Background(), tenantID)
	if err != nil {
		t.Errorf("GetAppliedMigrations() error = %v, want nil", err)
	}

	if migrations == nil {
		t.Error("GetAppliedMigrations() should not return nil")
	}
}

func TestMigrationManager_IsMigrationApplied(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "")

	tenantID := uuid.New()
	applied, err := mgr.IsMigrationApplied(context.Background(), tenantID, "001")
	if err != nil {
		t.Errorf("IsMigrationApplied() error = %v, want nil", err)
	}

	// Should return false for non-existent migration
	if applied {
		t.Error("IsMigrationApplied() should return false for non-existent migration")
	}
}

func TestMigrationManager_LoadMigrationFromFile(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create temporary migration files
	tempDir := t.TempDir()

	// Create up migration file
	upFile := filepath.Join(tempDir, "001_test_migration.up.sql")
	upSQL := "CREATE TABLE test (id INT PRIMARY KEY);"
	err := os.WriteFile(upFile, []byte(upSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create up migration file: %v", err)
	}

	// Create down migration file
	downFile := filepath.Join(tempDir, "001_test_migration.down.sql")
	downSQL := "DROP TABLE test;"
	err = os.WriteFile(downFile, []byte(downSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create down migration file: %v", err)
	}

	mgr := NewMigrationManager(nil, logger, tempDir)

	migration, err := mgr.(*MigrationManager).LoadMigrationFromFile("001", "test_migration")
	if err != nil {
		t.Errorf("LoadMigrationFromFile() error = %v, want nil", err)
		return
	}

	if migration == nil {
		t.Error("LoadMigrationFromFile() should not return nil")
		return
	}

	if migration.Version != "001" {
		t.Errorf("LoadMigrationFromFile() version = %v, want 001", migration.Version)
	}

	if migration.Name != "test_migration" {
		t.Errorf("LoadMigrationFromFile() name = %v, want test_migration", migration.Name)
	}

	if migration.SQL != upSQL {
		t.Errorf("LoadMigrationFromFile() SQL = %v, want %v", migration.SQL, upSQL)
	}

	if migration.RollbackSQL == nil || *migration.RollbackSQL != downSQL {
		t.Errorf("LoadMigrationFromFile() RollbackSQL = %v, want %v", migration.RollbackSQL, &downSQL)
	}

	if migration.Checksum == nil {
		t.Error("LoadMigrationFromFile() should set checksum")
	}
}

func TestMigrationManager_LoadMigrationFromFile_NoDownFile(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create temporary migration files
	tempDir := t.TempDir()

	// Create only up migration file
	upFile := filepath.Join(tempDir, "002_test_migration.up.sql")
	upSQL := "CREATE TABLE test2 (id INT PRIMARY KEY);"
	err := os.WriteFile(upFile, []byte(upSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create up migration file: %v", err)
	}

	mgr := NewMigrationManager(nil, logger, tempDir)

	migration, err := mgr.(*MigrationManager).LoadMigrationFromFile("002", "test_migration")
	if err != nil {
		t.Errorf("LoadMigrationFromFile() error = %v, want nil", err)
		return
	}

	if migration.RollbackSQL != nil {
		t.Error("LoadMigrationFromFile() should have nil RollbackSQL when down file doesn't exist")
	}
}

func TestMigrationManager_LoadMigrationFromFile_FileNotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Use empty temp directory
	tempDir := t.TempDir()

	mgr := NewMigrationManager(nil, logger, tempDir)

	_, err := mgr.(*MigrationManager).LoadMigrationFromFile("999", "nonexistent")
	if err == nil {
		t.Error("LoadMigrationFromFile() should error for non-existent file")
	}
}

func TestMigrationManager_ApplyMigrationFromFile(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "")

	tenantID := uuid.New()
	err := mgr.(*MigrationManager).ApplyMigrationFromFile(context.Background(), tenantID, "001", "test_migration")
	if err != nil {
		t.Errorf("ApplyMigrationFromFile() error = %v, want nil", err)
	}
}

func TestMigrationManager_ApplyMigrationToAllTenantsFromFile(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "")

	err := mgr.(*MigrationManager).ApplyMigrationToAllTenantsFromFile(context.Background(), "001", "test_migration")
	if err != nil {
		t.Errorf("ApplyMigrationToAllTenantsFromFile() error = %v, want nil", err)
	}
}

func TestMigrationManager_ListMigrationFiles(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create temporary migration files
	tempDir := t.TempDir()

	// Create multiple migration files
	files := []string{
		"001_create_users.up.sql",
		"001_create_users.down.sql",
		"002_create_projects.up.sql",
		"002_create_projects.down.sql",
		"003_add_indexes.up.sql",
		"README.txt",          // Should be ignored
		"not_a_migration.sql", // Should be ignored
	}

	for _, file := range files {
		err := os.WriteFile(filepath.Join(tempDir, file), []byte("-- test"), 0644)
		if err != nil {
			t.Fatalf("Failed to create file %s: %v", file, err)
		}
	}

	mgr := NewMigrationManager(nil, logger, tempDir)

	migrations, err := mgr.(*MigrationManager).ListMigrationFiles()
	if err != nil {
		t.Errorf("ListMigrationFiles() error = %v, want nil", err)
		return
	}

	expectedMigrations := []string{
		"001_create_users",
		"002_create_projects",
		"003_add_indexes",
	}

	if len(migrations) != len(expectedMigrations) {
		t.Errorf("ListMigrationFiles() returned %d migrations, want %d", len(migrations), len(expectedMigrations))
	}

	for _, expected := range expectedMigrations {
		found := false
		for _, migration := range migrations {
			if migration == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ListMigrationFiles() missing migration: %s", expected)
		}
	}
}

func TestMigrationManager_ListMigrationFiles_EmptyDir(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Use empty temp directory
	tempDir := t.TempDir()

	mgr := NewMigrationManager(nil, logger, tempDir)

	migrations, err := mgr.(*MigrationManager).ListMigrationFiles()
	if err != nil {
		t.Errorf("ListMigrationFiles() error = %v, want nil", err)
		return
	}

	if len(migrations) != 0 {
		t.Errorf("ListMigrationFiles() returned %d migrations, want 0", len(migrations))
	}
}

func TestMigrationManager_ListMigrationFiles_NoDir(t *testing.T) {
	logger := zaptest.NewLogger(t)

	mgr := NewMigrationManager(nil, logger, "")

	_, err := mgr.(*MigrationManager).ListMigrationFiles()
	if err == nil {
		t.Error("ListMigrationFiles() should error when migrations directory not configured")
	}
}

func TestMigrationManager_ListMigrationFiles_NonexistentDir(t *testing.T) {
	logger := zaptest.NewLogger(t)

	mgr := NewMigrationManager(nil, logger, "/nonexistent/directory")

	_, err := mgr.(*MigrationManager).ListMigrationFiles()
	if err == nil {
		t.Error("ListMigrationFiles() should error for nonexistent directory")
	}
}

func TestMigrationManager_validateTenantSchema(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping database test - requires PostgreSQL database")

	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "").(*MigrationManager)

	tenantID := uuid.New()
	exists := mgr.validateTenantSchema(context.Background(), tenantID)

	// Should return false for non-existent schema
	if exists {
		t.Error("validateTenantSchema() should return false for non-existent schema")
	}
}

// Mock tests that don't require database

func TestMigrationManager_Migration_Fields(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "/test/path")

	// Verify the manager was created with correct fields
	if concreteMgr, ok := mgr.(*MigrationManager); ok {
		if concreteMgr.migrationsDir != "/test/path" {
			t.Errorf("MigrationManager migrationsDir = %v, want /test/path", concreteMgr.migrationsDir)
		}

		if concreteMgr.db != nil {
			t.Errorf("MigrationManager db = %v, want nil", concreteMgr.db)
		}

		if concreteMgr.logger == nil {
			t.Error("MigrationManager logger should not be nil")
		}
	} else {
		t.Error("NewMigrationManager() should return *MigrationManager")
	}
}

func TestMigrationManager_Migration_WithRollback(t *testing.T) {
	rollbackSQL := "DROP TABLE test;"
	migration := &tenant.Migration{
		ID:          uuid.New(),
		TenantID:    uuid.New(),
		Version:     "001",
		Name:        "test_migration",
		SQL:         "CREATE TABLE test (id INT);",
		RollbackSQL: &rollbackSQL,
		AppliedAt:   time.Now(),
	}

	if migration.RollbackSQL == nil {
		t.Error("Migration should have RollbackSQL")
	}

	if *migration.RollbackSQL != rollbackSQL {
		t.Errorf("Migration RollbackSQL = %v, want %v", *migration.RollbackSQL, rollbackSQL)
	}
}

func TestMigrationManager_Migration_WithoutRollback(t *testing.T) {
	migration := &tenant.Migration{
		ID:        uuid.New(),
		TenantID:  uuid.New(),
		Version:   "001",
		Name:      "test_migration",
		SQL:       "CREATE TABLE test (id INT);",
		AppliedAt: time.Now(),
	}

	if migration.RollbackSQL != nil {
		t.Error("Migration should not have RollbackSQL when not set")
	}
}

func TestMigrationManager_Interface(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewMigrationManager(nil, logger, "")

	// Test that it implements the MigrationManager interface
	var _ tenant.MigrationManager = mgr
}

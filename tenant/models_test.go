package tenant

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidateStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{"valid active", StatusActive, true},
		{"valid suspended", StatusSuspended, true},
		{"valid pending", StatusPending, true},
		{"valid cancelled", StatusCancelled, true},
		{"invalid status", "invalid", false},
		{"empty status", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateStatus(tt.status); got != tt.want {
				t.Errorf("ValidateStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTenant_Validation(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name   string
		tenant Tenant
		valid  bool
	}{
		{
			name: "valid tenant",
			tenant: Tenant{
				ID:         uuid.New(),
				Name:       "Test Tenant",
				Subdomain:  "test-tenant",
				Status:     StatusActive,
				SchemaName: "tenant_123",
				CreatedAt:  now,
				UpdatedAt:  now,
			},
			valid: true,
		},
		{
			name: "empty name",
			tenant: Tenant{
				ID:        uuid.New(),
				Name:      "",
				Subdomain: "test-tenant",
				Status:    StatusActive,
			},
			valid: false,
		},
		{
			name: "empty subdomain",
			tenant: Tenant{
				ID:     uuid.New(),
				Name:   "Test Tenant",
				Status: StatusActive,
			},
			valid: false,
		},
		{
			name: "invalid status",
			tenant: Tenant{
				ID:        uuid.New(),
				Name:      "Test Tenant",
				Subdomain: "test-tenant",
				Status:    "invalid",
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test individual field validations
			nameValid := tt.tenant.Name != ""
			subdomainValid := tt.tenant.Subdomain != ""
			statusValid := ValidateStatus(tt.tenant.Status)

			allValid := nameValid && subdomainValid && statusValid

			if allValid != tt.valid {
				t.Errorf("Tenant validation = %v, want %v", allValid, tt.valid)
				t.Errorf("Name valid: %v, Subdomain valid: %v, Status valid: %v",
					nameValid, subdomainValid, statusValid)
			}
		})
	}
}

func TestValidationError(t *testing.T) {
	err := ValidationError{
		Field:   "name",
		Message: "name is required",
	}

	expected := "name is required"
	if err.Error() != expected {
		t.Errorf("ValidationError.Error() = %v, want %v", err.Error(), expected)
	}
}

func TestTenantError(t *testing.T) {
	tenantID := uuid.New()
	err := TenantError{
		TenantID: tenantID,
		Code:     "LIMIT_EXCEEDED",
		Message:  "Tenant limit exceeded",
	}

	expected := "Tenant limit exceeded"
	if err.Error() != expected {
		t.Errorf("TenantError.Error() = %v, want %v", err.Error(), expected)
	}
}

func TestContext_Fields(t *testing.T) {
	tenantID := uuid.New()
	ctx := Context{
		TenantID:   tenantID,
		Subdomain:  "test-tenant",
		SchemaName: "tenant_123",
		Status:     StatusActive,
	}

	if ctx.TenantID != tenantID {
		t.Errorf("Context.TenantID = %v, want %v", ctx.TenantID, tenantID)
	}
	if ctx.Subdomain != "test-tenant" {
		t.Errorf("Context.Subdomain = %v, want %v", ctx.Subdomain, "test-tenant")
	}
	if ctx.SchemaName != "tenant_123" {
		t.Errorf("Context.SchemaName = %v, want %v", ctx.SchemaName, "tenant_123")
	}
	if ctx.Status != StatusActive {
		t.Errorf("Context.Status = %v, want %v", ctx.Status, StatusActive)
	}
}

func TestStats_Fields(t *testing.T) {
	tenantID := uuid.New()

	stats := Stats{
		TenantID:          tenantID,
		SchemaExists:      true,
		AppliedMigrations: 2,
	}

	if stats.TenantID != tenantID {
		t.Errorf("Stats.TenantID = %v, want %v", stats.TenantID, tenantID)
	}
	if !stats.SchemaExists {
		t.Errorf("Stats.SchemaExists = %v, want %v", stats.SchemaExists, true)
	}
	if stats.AppliedMigrations != 2 {
		t.Errorf("Stats.AppliedMigrations = %v, want %v", stats.AppliedMigrations, 2)
	}
}

func TestMigration_Fields(t *testing.T) {
	id := uuid.New()
	tenantID := uuid.New()
	now := time.Now()
	rollbackSQL := "DROP TABLE test;"
	checksum := "abc123"

	migration := Migration{
		ID:          id,
		TenantID:    tenantID,
		Version:     "001",
		Name:        "create_table",
		SQL:         "CREATE TABLE test (id INT);",
		RollbackSQL: &rollbackSQL,
		AppliedAt:   now,
		Checksum:    &checksum,
	}

	if migration.ID != id {
		t.Errorf("Migration.ID = %v, want %v", migration.ID, id)
	}
	if migration.TenantID != tenantID {
		t.Errorf("Migration.TenantID = %v, want %v", migration.TenantID, tenantID)
	}
	if migration.Version != "001" {
		t.Errorf("Migration.Version = %v, want %v", migration.Version, "001")
	}
	if migration.Name != "create_table" {
		t.Errorf("Migration.Name = %v, want %v", migration.Name, "create_table")
	}
	if migration.SQL != "CREATE TABLE test (id INT);" {
		t.Errorf("Migration.SQL = %v, want %v", migration.SQL, "CREATE TABLE test (id INT);")
	}
	if migration.RollbackSQL == nil || *migration.RollbackSQL != rollbackSQL {
		t.Errorf("Migration.RollbackSQL = %v, want %v", migration.RollbackSQL, &rollbackSQL)
	}
	if migration.Checksum == nil || *migration.Checksum != checksum {
		t.Errorf("Migration.Checksum = %v, want %v", migration.Checksum, &checksum)
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	// Test database config
	if config.Database.MaxOpenConns != 100 {
		t.Errorf("DefaultConfig.Database.MaxOpenConns = %v, want %v", config.Database.MaxOpenConns, 100)
	}
	if config.Database.SchemaPrefix != "tenant_" {
		t.Errorf("DefaultConfig.Database.SchemaPrefix = %v, want %v", config.Database.SchemaPrefix, "tenant_")
	}

	// Test resolver config
	if config.Resolver.Strategy != ResolverSubdomain {
		t.Errorf("DefaultConfig.Resolver.Strategy = %v, want %v", config.Resolver.Strategy, ResolverSubdomain)
	}
	if len(config.Resolver.ReservedSubdomain) == 0 {
		t.Error("DefaultConfig.Resolver.ReservedSubdomain should have reserved subdomains")
	}

	// Test logger config
	if config.Logger.Level != "info" {
		t.Errorf("DefaultConfig.Logger.Level = %v, want %v", config.Logger.Level, "info")
	}
	if config.Logger.Format != "json" {
		t.Errorf("DefaultConfig.Logger.Format = %v, want %v", config.Logger.Format, "json")
	}
}

func TestConstants(t *testing.T) {
	// Test status constants
	if StatusActive != "active" {
		t.Errorf("StatusActive = %v, want %v", StatusActive, "active")
	}
	if StatusSuspended != "suspended" {
		t.Errorf("StatusSuspended = %v, want %v", StatusSuspended, "suspended")
	}
	if StatusPending != "pending" {
		t.Errorf("StatusPending = %v, want %v", StatusPending, "pending")
	}
	if StatusCancelled != "cancelled" {
		t.Errorf("StatusCancelled = %v, want %v", StatusCancelled, "cancelled")
	}

	// Test resolver constants
	if ResolverSubdomain != "subdomain" {
		t.Errorf("ResolverSubdomain = %v, want %v", ResolverSubdomain, "subdomain")
	}
	if ResolverPath != "path" {
		t.Errorf("ResolverPath = %v, want %v", ResolverPath, "path")
	}
	if ResolverHeader != "header" {
		t.Errorf("ResolverHeader = %v, want %v", ResolverHeader, "header")
	}
}

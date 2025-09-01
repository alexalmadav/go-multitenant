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

func TestValidatePlanType(t *testing.T) {
	tests := []struct {
		name     string
		planType string
		want     bool
	}{
		{"valid basic", PlanBasic, true},
		{"valid pro", PlanPro, true},
		{"valid enterprise", PlanEnterprise, true},
		{"invalid plan", "invalid", false},
		{"empty plan", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidatePlanType(tt.planType); got != tt.want {
				t.Errorf("ValidatePlanType() = %v, want %v", got, tt.want)
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
				PlanType:   PlanBasic,
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
				PlanType:  PlanBasic,
				Status:    StatusActive,
			},
			valid: false,
		},
		{
			name: "empty subdomain",
			tenant: Tenant{
				ID:       uuid.New(),
				Name:     "Test Tenant",
				PlanType: PlanBasic,
				Status:   StatusActive,
			},
			valid: false,
		},
		{
			name: "invalid plan type",
			tenant: Tenant{
				ID:        uuid.New(),
				Name:      "Test Tenant",
				Subdomain: "test-tenant",
				PlanType:  "invalid",
				Status:    StatusActive,
			},
			valid: false,
		},
		{
			name: "invalid status",
			tenant: Tenant{
				ID:        uuid.New(),
				Name:      "Test Tenant",
				Subdomain: "test-tenant",
				PlanType:  PlanBasic,
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
			planValid := ValidatePlanType(tt.tenant.PlanType)
			statusValid := ValidateStatus(tt.tenant.Status)

			allValid := nameValid && subdomainValid && planValid && statusValid

			if allValid != tt.valid {
				t.Errorf("Tenant validation = %v, want %v", allValid, tt.valid)
				t.Errorf("Name valid: %v, Subdomain valid: %v, Plan valid: %v, Status valid: %v",
					nameValid, subdomainValid, planValid, statusValid)
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
		PlanType:   PlanPro,
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
	if ctx.PlanType != PlanPro {
		t.Errorf("Context.PlanType = %v, want %v", ctx.PlanType, PlanPro)
	}
	if ctx.Status != StatusActive {
		t.Errorf("Context.Status = %v, want %v", ctx.Status, StatusActive)
	}
}

func TestLimits_Fields(t *testing.T) {
	limits := Limits{
		MaxUsers:     10,
		MaxProjects:  20,
		MaxStorageGB: 5,
	}

	if limits.MaxUsers != 10 {
		t.Errorf("Limits.MaxUsers = %v, want %v", limits.MaxUsers, 10)
	}
	if limits.MaxProjects != 20 {
		t.Errorf("Limits.MaxProjects = %v, want %v", limits.MaxProjects, 20)
	}
	if limits.MaxStorageGB != 5 {
		t.Errorf("Limits.MaxStorageGB = %v, want %v", limits.MaxStorageGB, 5)
	}
}

func TestStats_Fields(t *testing.T) {
	tenantID := uuid.New()
	now := time.Now()

	stats := Stats{
		TenantID:      tenantID,
		UserCount:     5,
		ProjectCount:  3,
		StorageUsedGB: 2.5,
		LastActivity:  now,
		SchemaExists:  true,
	}

	if stats.TenantID != tenantID {
		t.Errorf("Stats.TenantID = %v, want %v", stats.TenantID, tenantID)
	}
	if stats.UserCount != 5 {
		t.Errorf("Stats.UserCount = %v, want %v", stats.UserCount, 5)
	}
	if stats.ProjectCount != 3 {
		t.Errorf("Stats.ProjectCount = %v, want %v", stats.ProjectCount, 3)
	}
	if stats.StorageUsedGB != 2.5 {
		t.Errorf("Stats.StorageUsedGB = %v, want %v", stats.StorageUsedGB, 2.5)
	}
	if !stats.SchemaExists {
		t.Errorf("Stats.SchemaExists = %v, want %v", stats.SchemaExists, true)
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
	if config.Database.Driver != "postgres" {
		t.Errorf("DefaultConfig.Database.Driver = %v, want %v", config.Database.Driver, "postgres")
	}
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

	// Test limits config
	if !config.Limits.EnforceLimits {
		t.Error("DefaultConfig.Limits.EnforceLimits should be true")
	}
	if config.Limits.DefaultPlan != PlanBasic {
		t.Errorf("DefaultConfig.Limits.DefaultPlan = %v, want %v", config.Limits.DefaultPlan, PlanBasic)
	}

	// Test plan limits exist
	if config.Limits.PlanLimits[PlanBasic] == nil {
		t.Error("DefaultConfig should have basic plan limits")
	}
	if config.Limits.PlanLimits[PlanPro] == nil {
		t.Error("DefaultConfig should have pro plan limits")
	}
	if config.Limits.PlanLimits[PlanEnterprise] == nil {
		t.Error("DefaultConfig should have enterprise plan limits")
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

	// Test plan constants
	if PlanBasic != "basic" {
		t.Errorf("PlanBasic = %v, want %v", PlanBasic, "basic")
	}
	if PlanPro != "pro" {
		t.Errorf("PlanPro = %v, want %v", PlanPro, "pro")
	}
	if PlanEnterprise != "enterprise" {
		t.Errorf("PlanEnterprise = %v, want %v", PlanEnterprise, "enterprise")
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

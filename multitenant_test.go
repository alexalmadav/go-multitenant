package multitenant

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap/zaptest"
)

func TestNew(t *testing.T) {
	// Skip this test as it requires actual database connection
	// In a real environment, you'd use testcontainers or similar
	t.Skip("Skipping integration test - requires database")

	config := tenant.DefaultConfig()
	config.Database.DSN = "postgres://test:test@localhost:5432/test?sslmode=disable"

	mt, err := New(config)
	if err != nil {
		t.Errorf("New() error = %v, want nil", err)
		return
	}

	if mt == nil {
		t.Error("New() should not return nil")
	}

	defer mt.Close()
}

func TestNew_InvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config tenant.Config
	}{
		{
			name: "invalid database DSN",
			config: tenant.Config{
				Database: tenant.DatabaseConfig{
					Driver: "postgres",
					DSN:    "invalid-dsn",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.config)
			if err == nil {
				t.Error("New() should return error for invalid config")
			}
		})
	}
}

func TestMultiTenant_GetDatabase(t *testing.T) {
	mt := &MultiTenant{
		db: &sql.DB{}, // Mock DB
	}

	db := mt.GetDatabase()
	if db != mt.db {
		t.Error("GetDatabase() should return the same database instance")
	}
}

func TestMultiTenant_GetLogger(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mt := &MultiTenant{
		logger: logger,
	}

	retrievedLogger := mt.GetLogger()
	if retrievedLogger != logger {
		t.Error("GetLogger() should return the same logger instance")
	}
}

func TestMultiTenant_Close(t *testing.T) {
	// Create a mock manager that implements the Close method
	mockManager := &MockMultiTenantManager{}

	mt := &MultiTenant{
		Manager: mockManager,
		db:      nil, // Set to nil to avoid panic
		logger:  zaptest.NewLogger(t),
	}

	// Since db is nil, Close will return an error from db.Close(), but manager.Close() should still be called
	_ = mt.Close()

	if !mockManager.CloseCalled {
		t.Error("Close() should call manager.Close()")
	}
}

func TestSetupLogger(t *testing.T) {
	tests := []struct {
		name   string
		config tenant.LoggerConfig
		want   string // Expected logger type/format indication
	}{
		{
			name: "console format",
			config: tenant.LoggerConfig{
				Format: "console",
				Level:  "info",
			},
			want: "console",
		},
		{
			name: "json format",
			config: tenant.LoggerConfig{
				Format: "json",
				Level:  "info",
			},
			want: "json",
		},
		{
			name: "debug level",
			config: tenant.LoggerConfig{
				Format: "console",
				Level:  "debug",
			},
			want: "debug",
		},
		{
			name: "warn level",
			config: tenant.LoggerConfig{
				Format: "json",
				Level:  "warn",
			},
			want: "warn",
		},
		{
			name: "error level",
			config: tenant.LoggerConfig{
				Format: "json",
				Level:  "error",
			},
			want: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := setupLogger(tt.config)
			if err != nil {
				t.Errorf("setupLogger() error = %v, want nil", err)
				return
			}

			if logger == nil {
				t.Error("setupLogger() should not return nil logger")
			}
		})
	}
}

func TestSetupDatabase(t *testing.T) {
	// Skip this test as it requires actual database connection
	t.Skip("Skipping integration test - requires database")

	config := tenant.DatabaseConfig{
		Driver:          "postgres",
		DSN:             "postgres://test:test@localhost:5432/test?sslmode=disable",
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: 15 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	}

	db, err := setupDatabase(config)
	if err != nil {
		t.Errorf("setupDatabase() error = %v, want nil", err)
		return
	}

	if db == nil {
		t.Error("setupDatabase() should not return nil")
	}

	defer db.Close()

	// Test connection
	if err := db.Ping(); err != nil {
		t.Errorf("Database ping failed: %v", err)
	}
}

func TestSetupDatabase_InvalidDriver(t *testing.T) {
	config := tenant.DatabaseConfig{
		Driver: "invalid-driver",
		DSN:    "invalid-dsn",
	}

	_, err := setupDatabase(config)
	if err == nil {
		t.Error("setupDatabase() should return error for invalid driver")
	}
}

func TestReExportedTypes(t *testing.T) {
	// Test that re-exported types are accessible
	var tenant Tenant
	var context Context
	var config Config
	var limits Limits
	var stats Stats
	var migration Migration

	// Test that we can create instances of re-exported types
	_ = tenant
	_ = context
	_ = config
	_ = limits
	_ = stats
	_ = migration
}

func TestReExportedConstants(t *testing.T) {
	// Test status constants
	if StatusActive != "active" {
		t.Errorf("StatusActive = %v, want active", StatusActive)
	}
	if StatusSuspended != "suspended" {
		t.Errorf("StatusSuspended = %v, want suspended", StatusSuspended)
	}
	if StatusPending != "pending" {
		t.Errorf("StatusPending = %v, want pending", StatusPending)
	}
	if StatusCancelled != "cancelled" {
		t.Errorf("StatusCancelled = %v, want cancelled", StatusCancelled)
	}

	// Test plan constants
	if PlanBasic != "basic" {
		t.Errorf("PlanBasic = %v, want basic", PlanBasic)
	}
	if PlanPro != "pro" {
		t.Errorf("PlanPro = %v, want pro", PlanPro)
	}
	if PlanEnterprise != "enterprise" {
		t.Errorf("PlanEnterprise = %v, want enterprise", PlanEnterprise)
	}

	// Test resolver constants
	if ResolverSubdomain != "subdomain" {
		t.Errorf("ResolverSubdomain = %v, want subdomain", ResolverSubdomain)
	}
	if ResolverPath != "path" {
		t.Errorf("ResolverPath = %v, want path", ResolverPath)
	}
	if ResolverHeader != "header" {
		t.Errorf("ResolverHeader = %v, want header", ResolverHeader)
	}
}

func TestReExportedFunctions(t *testing.T) {
	// Test DefaultConfig
	config := DefaultConfig()
	if config.Database.Driver != "postgres" {
		t.Errorf("DefaultConfig().Database.Driver = %v, want postgres", config.Database.Driver)
	}

	// Test context helper functions
	ctx := context.Background()
	tenantID := uuid.New()

	// Test GetTenantIDFromContext with empty context
	if _, ok := GetTenantIDFromContext(ctx); ok {
		t.Error("GetTenantIDFromContext() should return false for empty context")
	}

	// Test GetTenantFromContext with empty context
	if _, ok := GetTenantFromContext(ctx); ok {
		t.Error("GetTenantFromContext() should return false for empty context")
	}

	// Test with populated context
	ctxWithTenant := context.WithValue(ctx, tenant.ContextKeyTenantID, tenantID)
	if retrievedID, ok := GetTenantIDFromContext(ctxWithTenant); !ok || retrievedID != tenantID {
		t.Error("GetTenantIDFromContext() should return tenant ID from context")
	}

	tenantCtx := &tenant.Context{
		TenantID:   tenantID,
		Subdomain:  "test",
		SchemaName: "tenant_test",
		PlanType:   "basic",
		Status:     "active",
	}
	ctxWithTenantCtx := context.WithValue(ctx, tenant.ContextKeyTenant, tenantCtx)
	if retrievedCtx, ok := GetTenantFromContext(ctxWithTenantCtx); !ok || retrievedCtx.TenantID != tenantID {
		t.Error("GetTenantFromContext() should return tenant context from context")
	}
}

func TestMultiTenant_InterfaceImplementation(t *testing.T) {
	// Test that MultiTenant properly exposes the required interfaces
	mt := &MultiTenant{
		Manager:       &MockMultiTenantManager{},
		Resolver:      &MockMultiTenantResolver{},
		GinMiddleware: nil, // Skip gin middleware for this test
		logger:        zaptest.NewLogger(t),
	}

	// Test that interfaces are accessible
	if mt.Manager == nil {
		t.Error("MultiTenant.Manager should not be nil")
	}
	if mt.Resolver == nil {
		t.Error("MultiTenant.Resolver should not be nil")
	}
}

// Mock implementations for testing

type MockMultiTenantManager struct {
	CloseCalled bool
}

func (m *MockMultiTenantManager) CreateTenant(ctx context.Context, tenant *tenant.Tenant) error {
	return nil
}

func (m *MockMultiTenantManager) GetTenant(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	return &tenant.Tenant{ID: id}, nil
}

func (m *MockMultiTenantManager) GetTenantBySubdomain(ctx context.Context, subdomain string) (*tenant.Tenant, error) {
	return &tenant.Tenant{Subdomain: subdomain}, nil
}

func (m *MockMultiTenantManager) UpdateTenant(ctx context.Context, tenant *tenant.Tenant) error {
	return nil
}

func (m *MockMultiTenantManager) DeleteTenant(ctx context.Context, id uuid.UUID) error {
	return nil
}

func (m *MockMultiTenantManager) ListTenants(ctx context.Context, page, perPage int) ([]*tenant.Tenant, int, error) {
	return []*tenant.Tenant{}, 0, nil
}

func (m *MockMultiTenantManager) ProvisionTenant(ctx context.Context, id uuid.UUID) error {
	return nil
}

func (m *MockMultiTenantManager) SuspendTenant(ctx context.Context, id uuid.UUID) error {
	return nil
}

func (m *MockMultiTenantManager) ActivateTenant(ctx context.Context, id uuid.UUID) error {
	return nil
}

func (m *MockMultiTenantManager) ValidateAccess(ctx context.Context, userID, tenantID uuid.UUID) error {
	return nil
}

func (m *MockMultiTenantManager) CheckLimits(ctx context.Context, tenantID uuid.UUID) (*tenant.Limits, error) {
	return &tenant.Limits{}, nil
}

func (m *MockMultiTenantManager) GetStats(ctx context.Context, tenantID uuid.UUID) (*tenant.Stats, error) {
	return &tenant.Stats{}, nil
}

func (m *MockMultiTenantManager) GetTenantDB(ctx context.Context, tenantID uuid.UUID) (*sql.DB, error) {
	return &sql.DB{}, nil
}

func (m *MockMultiTenantManager) WithTenantContext(ctx context.Context, tenantID uuid.UUID) context.Context {
	return ctx
}

func (m *MockMultiTenantManager) Close() error {
	m.CloseCalled = true
	return nil
}

type MockMultiTenantResolver struct{}

func (m *MockMultiTenantResolver) ResolveTenant(ctx context.Context, req *http.Request) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (m *MockMultiTenantResolver) ExtractFromSubdomain(host string) (string, error) {
	return "test", nil
}

func (m *MockMultiTenantResolver) ExtractFromPath(path string) (string, error) {
	return "test", nil
}

func (m *MockMultiTenantResolver) ExtractFromHeader(req *http.Request) (string, error) {
	return "test", nil
}

func (m *MockMultiTenantResolver) ValidateSubdomain(subdomain string) error {
	return nil
}

// MockGinMiddleware removed as it's not needed for these tests

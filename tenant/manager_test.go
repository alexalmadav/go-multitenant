package tenant

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestNewManager(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	// Create mocks
	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	// Create database connection (mock)
	var db *sql.DB // In real tests, you'd use a test database

	manager := NewManager(config, db, mockRepo, mockSchema, mockMigration, mockLimits, logger)
	if manager == nil {
		t.Error("NewManager() should not return nil")
	}
}

func TestManager_CreateTenant(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	tests := []struct {
		name    string
		tenant  *Tenant
		wantErr bool
	}{
		{
			name: "valid tenant",
			tenant: &Tenant{
				Name:      "Test Tenant",
				Subdomain: "test-tenant",
			},
			wantErr: false,
		},
		{
			name: "empty name",
			tenant: &Tenant{
				Subdomain: "test-tenant",
			},
			wantErr: true,
		},
		{
			name: "empty subdomain",
			tenant: &Tenant{
				Name: "Test Tenant",
			},
			wantErr: true,
		},
		{
			name: "invalid subdomain - too short",
			tenant: &Tenant{
				Name:      "Test Tenant",
				Subdomain: "ab",
			},
			wantErr: true,
		},
		{
			name: "invalid subdomain - reserved",
			tenant: &Tenant{
				Name:      "Test Tenant",
				Subdomain: "www",
			},
			wantErr: true,
		},
		{
			name: "invalid plan type",
			tenant: &Tenant{
				Name:      "Test Tenant",
				Subdomain: "test-tenant",
				PlanType:  "invalid",
			},
			wantErr: true,
		},
		{
			name: "invalid status",
			tenant: &Tenant{
				Name:      "Test Tenant",
				Subdomain: "test-tenant",
				Status:    "invalid",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.CreateTenant(context.Background(), tt.tenant)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateTenant() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify defaults were set
				if tt.tenant.ID == uuid.Nil {
					t.Error("CreateTenant() should set ID if not provided")
				}
				if tt.tenant.Status == "" || tt.tenant.Status != StatusPending {
					t.Error("CreateTenant() should set default status to pending")
				}
				if tt.tenant.PlanType == "" || tt.tenant.PlanType != PlanBasic {
					t.Error("CreateTenant() should set default plan type to basic")
				}
				if tt.tenant.SchemaName == "" {
					t.Error("CreateTenant() should set schema name")
				}
			}
		})
	}
}

func TestManager_CreateTenant_PreservesID(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	tenantID := uuid.New()
	tenant := &Tenant{
		ID:        tenantID,
		Name:      "Test Tenant",
		Subdomain: "test-tenant",
	}

	err := manager.CreateTenant(context.Background(), tenant)
	if err != nil {
		t.Errorf("CreateTenant() error = %v, want nil", err)
		return
	}

	if tenant.ID != tenantID {
		t.Error("CreateTenant() should preserve existing ID")
	}
}

func TestManager_GetTenant(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Create a test tenant
	tenantID := uuid.New()
	tenant := &Tenant{
		ID:        tenantID,
		Name:      "Test Tenant",
		Subdomain: "test-tenant",
		PlanType:  PlanBasic,
		Status:    StatusActive,
	}

	// Add to mock repository
	mockRepo.tenants[tenantID] = tenant

	// Test getting existing tenant
	retrieved, err := manager.GetTenant(context.Background(), tenantID)
	if err != nil {
		t.Errorf("GetTenant() error = %v, want nil", err)
		return
	}
	if retrieved.ID != tenantID {
		t.Error("GetTenant() returned wrong tenant")
	}

	// Test getting non-existing tenant
	nonExistentID := uuid.New()
	_, err = manager.GetTenant(context.Background(), nonExistentID)
	if err == nil {
		t.Error("GetTenant() should return error for non-existing tenant")
	}
}

func TestManager_GetTenantBySubdomain(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Create a test tenant
	tenantID := uuid.New()
	tenant := &Tenant{
		ID:        tenantID,
		Name:      "Test Tenant",
		Subdomain: "test-tenant",
		PlanType:  PlanBasic,
		Status:    StatusActive,
	}

	// Add to mock repository
	mockRepo.tenants[tenantID] = tenant

	// Test getting existing tenant
	retrieved, err := manager.GetTenantBySubdomain(context.Background(), "test-tenant")
	if err != nil {
		t.Errorf("GetTenantBySubdomain() error = %v, want nil", err)
		return
	}
	if retrieved.ID != tenantID {
		t.Error("GetTenantBySubdomain() returned wrong tenant")
	}

	// Test getting non-existing tenant
	_, err = manager.GetTenantBySubdomain(context.Background(), "non-existent")
	if err == nil {
		t.Error("GetTenantBySubdomain() should return error for non-existing tenant")
	}
}

func TestManager_UpdateTenant(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Create a test tenant
	tenantID := uuid.New()
	tenant := &Tenant{
		ID:        tenantID,
		Name:      "Test Tenant",
		Subdomain: "test-tenant",
		PlanType:  PlanBasic,
		Status:    StatusActive,
	}

	// Add to mock repository
	mockRepo.tenants[tenantID] = tenant

	// Test valid update
	tenant.Name = "Updated Tenant"
	err := manager.UpdateTenant(context.Background(), tenant)
	if err != nil {
		t.Errorf("UpdateTenant() error = %v, want nil", err)
	}

	// Test invalid update
	tenant.Name = "" // Invalid name
	err = manager.UpdateTenant(context.Background(), tenant)
	if err == nil {
		t.Error("UpdateTenant() should return error for invalid tenant")
	}
}

func TestManager_DeleteTenant(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Create a test tenant
	tenantID := uuid.New()
	tenant := &Tenant{
		ID:        tenantID,
		Name:      "Test Tenant",
		Subdomain: "test-tenant",
		PlanType:  PlanBasic,
		Status:    StatusActive,
	}

	// Add to mock repository
	mockRepo.tenants[tenantID] = tenant

	// Test delete
	err := manager.DeleteTenant(context.Background(), tenantID)
	if err != nil {
		t.Errorf("DeleteTenant() error = %v, want nil", err)
	}

	// Verify tenant status was changed to cancelled
	if tenant.Status != StatusCancelled {
		t.Error("DeleteTenant() should set status to cancelled")
	}

	// Test deleting non-existing tenant
	nonExistentID := uuid.New()
	err = manager.DeleteTenant(context.Background(), nonExistentID)
	if err == nil {
		t.Error("DeleteTenant() should return error for non-existing tenant")
	}
}

func TestManager_ListTenants(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Create test tenants
	for i := 0; i < 5; i++ {
		tenantID := uuid.New()
		tenant := &Tenant{
			ID:        tenantID,
			Name:      "Test Tenant",
			Subdomain: "test-tenant",
			PlanType:  PlanBasic,
			Status:    StatusActive,
		}
		mockRepo.tenants[tenantID] = tenant
	}

	// Test listing
	tenants, total, err := manager.ListTenants(context.Background(), 1, 10)
	if err != nil {
		t.Errorf("ListTenants() error = %v, want nil", err)
		return
	}

	if len(tenants) != 5 {
		t.Errorf("ListTenants() returned %d tenants, want 5", len(tenants))
	}
	if total != 5 {
		t.Errorf("ListTenants() returned total %d, want 5", total)
	}
}

func TestManager_ProvisionTenant_CreatesSchemaAppliesPendingAndActivates(t *testing.T) {
	mockRepo, mockSchema, mockMig := newManagerMocks()
	manager := newTestManager(mockRepo, mockSchema, mockMig)
	tenantID := uuid.New()
	mockRepo.tenants[tenantID] = &Tenant{ID: tenantID, Name: "T", Subdomain: "ttt", Status: StatusPending}

	if err := manager.ProvisionTenant(context.Background(), tenantID); err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if exists, _ := mockSchema.SchemaExists(context.Background(), tenantID); !exists {
		t.Error("schema should exist")
	}
	if mockMig.applyPendingCalls != 1 {
		t.Errorf("ApplyPending calls = %d, want 1", mockMig.applyPendingCalls)
	}
	if mockRepo.tenants[tenantID].Status != StatusActive {
		t.Errorf("status = %s, want active", mockRepo.tenants[tenantID].Status)
	}
}

func TestManager_ProvisionTenant_LeavesPendingOnMigrationFailureAndResumes(t *testing.T) {
	mockRepo, mockSchema, mockMig := newManagerMocks()
	manager := newTestManager(mockRepo, mockSchema, mockMig)
	tenantID := uuid.New()
	mockRepo.tenants[tenantID] = &Tenant{ID: tenantID, Name: "T", Subdomain: "ttt", Status: StatusPending}
	mockMig.applyPendingErr = errors.New("migration 002 failed")

	err := manager.ProvisionTenant(context.Background(), tenantID)
	if err == nil || !strings.Contains(err.Error(), "migration 002 failed") {
		t.Fatalf("expected migration error, got %v", err)
	}
	if mockRepo.tenants[tenantID].Status != StatusPending {
		t.Errorf("status after failure = %s, want pending", mockRepo.tenants[tenantID].Status)
	}
	if exists, _ := mockSchema.SchemaExists(context.Background(), tenantID); !exists {
		t.Error("schema should be kept for a resumable retry")
	}

	if err := manager.ProvisionTenant(context.Background(), tenantID); err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if mockRepo.tenants[tenantID].Status != StatusActive {
		t.Errorf("status after retry = %s, want active", mockRepo.tenants[tenantID].Status)
	}
}

func TestManager_ProvisionTenant_RejectsCancelledTenant(t *testing.T) {
	mockRepo, mockSchema, mockMig := newManagerMocks()
	manager := newTestManager(mockRepo, mockSchema, mockMig)
	tenantID := uuid.New()
	mockRepo.tenants[tenantID] = &Tenant{ID: tenantID, Name: "T", Subdomain: "ttt", Status: StatusCancelled}

	if err := manager.ProvisionTenant(context.Background(), tenantID); err == nil {
		t.Fatal("expected error provisioning a cancelled tenant")
	}
}

// newManagerMocks creates a fresh set of mocks for manager tests.
func newManagerMocks() (*MockManagerRepository, *MockManagerSchemaManager, *MockManagerMigrationManager) {
	return &MockManagerRepository{tenants: make(map[uuid.UUID]*Tenant)},
		&MockManagerSchemaManager{schemas: make(map[uuid.UUID]bool)},
		&MockManagerMigrationManager{}
}

// newTestManager builds a manager wired to the given mocks.
func newTestManager(repo *MockManagerRepository, schema *MockManagerSchemaManager, mig *MockManagerMigrationManager) Manager {
	return NewManager(DefaultConfig(), nil, repo, schema, mig, &MockManagerLimitChecker{}, zap.NewNop())
}

func TestManager_SuspendTenant(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Create a test tenant
	tenantID := uuid.New()
	tenant := &Tenant{
		ID:        tenantID,
		Name:      "Test Tenant",
		Subdomain: "test-tenant",
		PlanType:  PlanBasic,
		Status:    StatusActive,
	}

	// Add to mock repository
	mockRepo.tenants[tenantID] = tenant

	// Test suspending
	err := manager.SuspendTenant(context.Background(), tenantID)
	if err != nil {
		t.Errorf("SuspendTenant() error = %v, want nil", err)
		return
	}

	// Verify tenant status was updated
	if tenant.Status != StatusSuspended {
		t.Error("SuspendTenant() should set status to suspended")
	}
}

func TestManager_ActivateTenant(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Create a test tenant
	tenantID := uuid.New()
	tenant := &Tenant{
		ID:        tenantID,
		Name:      "Test Tenant",
		Subdomain: "test-tenant",
		PlanType:  PlanBasic,
		Status:    StatusSuspended,
	}

	// Add to mock repository
	mockRepo.tenants[tenantID] = tenant

	// Test activating
	err := manager.ActivateTenant(context.Background(), tenantID)
	if err != nil {
		t.Errorf("ActivateTenant() error = %v, want nil", err)
		return
	}

	// Verify tenant status was updated
	if tenant.Status != StatusActive {
		t.Error("ActivateTenant() should set status to active")
	}
}

func TestManager_ValidateAccess(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	userID := uuid.New()
	tenantID := uuid.New()

	// Create active tenant
	activeTenant := &Tenant{
		ID:        tenantID,
		Name:      "Active Tenant",
		Subdomain: "active-tenant",
		PlanType:  PlanBasic,
		Status:    StatusActive,
	}
	mockRepo.tenants[tenantID] = activeTenant

	// Test access to active tenant
	err := manager.ValidateAccess(context.Background(), userID, tenantID)
	if err != nil {
		t.Errorf("ValidateAccess() error = %v, want nil for active tenant", err)
	}

	// Create suspended tenant
	suspendedTenantID := uuid.New()
	suspendedTenant := &Tenant{
		ID:        suspendedTenantID,
		Name:      "Suspended Tenant",
		Subdomain: "suspended-tenant",
		PlanType:  PlanBasic,
		Status:    StatusSuspended,
	}
	mockRepo.tenants[suspendedTenantID] = suspendedTenant

	// Test access to suspended tenant
	err = manager.ValidateAccess(context.Background(), userID, suspendedTenantID)
	if err == nil {
		t.Error("ValidateAccess() should return error for suspended tenant")
	}

	// Test access to non-existing tenant
	nonExistentID := uuid.New()
	err = manager.ValidateAccess(context.Background(), userID, nonExistentID)
	if err == nil {
		t.Error("ValidateAccess() should return error for non-existing tenant")
	}
}

func TestManager_CheckLimits(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Create a test tenant
	tenantID := uuid.New()
	tenant := &Tenant{
		ID:        tenantID,
		Name:      "Test Tenant",
		Subdomain: "test-tenant",
		PlanType:  PlanBasic,
		Status:    StatusActive,
	}

	// Add to mock repository
	mockRepo.tenants[tenantID] = tenant

	// Test checking limits
	limits, err := manager.CheckLimits(context.Background(), tenantID)
	if err != nil {
		t.Errorf("CheckLimits() error = %v, want nil", err)
		return
	}

	if limits == nil {
		t.Error("CheckLimits() should return limits")
	}
}

func TestManager_GetStats(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	tenantID := uuid.New()
	mockRepo.tenants[tenantID] = &Tenant{ID: tenantID, Name: "T", Subdomain: "get-stats-t"}
	if err := mockSchema.CreateTenantSchema(context.Background(), tenantID); err != nil {
		t.Fatalf("CreateTenantSchema: %v", err)
	}
	mockMigration.applied = []*Migration{
		{ID: uuid.New(), TenantID: tenantID, Version: "001"},
		{ID: uuid.New(), TenantID: tenantID, Version: "002"},
	}

	stats, err := manager.GetStats(context.Background(), tenantID)
	if err != nil {
		t.Errorf("GetStats() error = %v, want nil", err)
		return
	}

	if stats.TenantID != tenantID {
		t.Error("GetStats() should return stats for correct tenant")
	}
	if !stats.SchemaExists {
		t.Error("GetStats() should report SchemaExists = true")
	}
	if stats.AppliedMigrations != 2 {
		t.Errorf("GetStats() AppliedMigrations = %d, want 2", stats.AppliedMigrations)
	}
	if len(stats.Usage) != 0 {
		t.Errorf("GetStats() Usage = %v, want empty (no usage tracker)", stats.Usage)
	}
}

func TestManager_WithTenantContext(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Create a test tenant
	tenantID := uuid.New()
	tenant := &Tenant{
		ID:         tenantID,
		Name:       "Test Tenant",
		Subdomain:  "test-tenant",
		PlanType:   PlanBasic,
		Status:     StatusActive,
		SchemaName: "tenant_123",
	}

	// Add to mock repository
	mockRepo.tenants[tenantID] = tenant

	ctx := context.Background()
	tenantCtx := manager.WithTenantContext(ctx, tenantID)

	// Verify context contains tenant information
	if retrievedCtx, ok := GetTenantFromContext(tenantCtx); !ok {
		t.Error("WithTenantContext() should add tenant context")
	} else {
		if retrievedCtx.TenantID != tenantID {
			t.Error("WithTenantContext() should set correct tenant ID")
		}
		if retrievedCtx.Subdomain != "test-tenant" {
			t.Error("WithTenantContext() should set correct subdomain")
		}
	}

	// Verify context contains tenant ID
	if retrievedID, ok := GetTenantIDFromContext(tenantCtx); !ok {
		t.Error("WithTenantContext() should add tenant ID to context")
	} else if retrievedID != tenantID {
		t.Error("WithTenantContext() should set correct tenant ID")
	}
}

func TestManager_Close(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := DefaultConfig()

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	manager := NewManager(config, (*sql.DB)(nil), mockRepo, mockSchema, mockMigration, mockLimits, logger)

	// Test close
	err := manager.Close()
	if err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
}

// Helper mock implementations for manager tests

// NewMockRepository creates a mock repository for testing
func NewMockRepository() *MockManagerRepository {
	return &MockManagerRepository{
		tenants: make(map[uuid.UUID]*Tenant),
	}
}

// MockManagerRepository implements Repository interface for testing
type MockManagerRepository struct {
	tenants map[uuid.UUID]*Tenant
}

func (m *MockManagerRepository) Create(ctx context.Context, t *Tenant) error {
	if _, exists := m.tenants[t.ID]; exists {
		return &TenantError{TenantID: t.ID, Code: "DUPLICATE", Message: "tenant already exists"}
	}
	for _, existing := range m.tenants {
		if existing.Subdomain == t.Subdomain {
			return &TenantError{TenantID: t.ID, Code: "DUPLICATE_SUBDOMAIN", Message: "subdomain already exists"}
		}
	}
	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now
	m.tenants[t.ID] = t
	return nil
}

func (m *MockManagerRepository) GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	t, exists := m.tenants[id]
	if !exists {
		return nil, &TenantError{TenantID: id, Code: "NOT_FOUND", Message: "tenant not found"}
	}
	return t, nil
}

func (m *MockManagerRepository) GetBySubdomain(ctx context.Context, subdomain string) (*Tenant, error) {
	for _, t := range m.tenants {
		if t.Subdomain == subdomain {
			return t, nil
		}
	}
	return nil, &TenantError{Code: "NOT_FOUND", Message: "tenant not found"}
}

func (m *MockManagerRepository) Update(ctx context.Context, t *Tenant) error {
	existing, exists := m.tenants[t.ID]
	if !exists {
		return &TenantError{TenantID: t.ID, Code: "NOT_FOUND", Message: "tenant not found"}
	}

	// Check for duplicate subdomain (excluding self)
	for id, other := range m.tenants {
		if id != t.ID && other.Subdomain == t.Subdomain {
			return &TenantError{TenantID: t.ID, Code: "DUPLICATE_SUBDOMAIN", Message: "subdomain already exists"}
		}
	}

	t.CreatedAt = existing.CreatedAt
	t.UpdatedAt = time.Now()
	m.tenants[t.ID] = t
	return nil
}

func (m *MockManagerRepository) Delete(ctx context.Context, id uuid.UUID) error {
	t, exists := m.tenants[id]
	if !exists {
		return &TenantError{TenantID: id, Code: "NOT_FOUND", Message: "tenant not found"}
	}
	t.Status = StatusCancelled
	t.UpdatedAt = time.Now()
	return nil
}

func (m *MockManagerRepository) List(ctx context.Context, page, perPage int) ([]*Tenant, int, error) {
	var activeTenants []*Tenant
	for _, t := range m.tenants {
		if t.Status != StatusCancelled {
			activeTenants = append(activeTenants, t)
		}
	}

	total := len(activeTenants)
	start := (page - 1) * perPage
	end := start + perPage

	if start >= total {
		return []*Tenant{}, total, nil
	}
	if end > total {
		end = total
	}

	return activeTenants[start:end], total, nil
}

// NewMockSchemaManager creates a mock schema manager for testing
func NewMockSchemaManager(prefix string) *MockManagerSchemaManager {
	if prefix == "" {
		prefix = "tenant_"
	}
	return &MockManagerSchemaManager{
		schemas: make(map[uuid.UUID]bool),
		prefix:  prefix,
	}
}

// MockManagerSchemaManager implements SchemaManager interface for testing
type MockManagerSchemaManager struct {
	schemas map[uuid.UUID]bool
	prefix  string
}

// CreateTenantSchema mirrors the real SchemaManager's "CREATE SCHEMA IF NOT
// EXISTS" behaviour: creating an already-existing schema is not an error, so
// a resumable ProvisionTenant retry can call it again safely.
func (m *MockManagerSchemaManager) CreateTenantSchema(ctx context.Context, tenantID uuid.UUID) error {
	m.schemas[tenantID] = true
	return nil
}

func (m *MockManagerSchemaManager) DropTenantSchema(ctx context.Context, tenantID uuid.UUID) error {
	delete(m.schemas, tenantID)
	return nil
}

func (m *MockManagerSchemaManager) SchemaExists(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	return m.schemas[tenantID], nil
}

func (m *MockManagerSchemaManager) GetSchemaName(tenantID uuid.UUID) string {
	return m.prefix + tenantID.String()
}

func (m *MockManagerSchemaManager) ListTenantSchemas(ctx context.Context) ([]string, error) {
	var schemas []string
	for tenantID := range m.schemas {
		schemas = append(schemas, m.GetSchemaName(tenantID))
	}
	return schemas, nil
}

// NewMockMigrationManager creates a mock migration manager for testing
func NewMockMigrationManager() *MockManagerMigrationManager {
	return &MockManagerMigrationManager{
		appliedMigrations: make(map[uuid.UUID]map[string]*Migration),
	}
}

// MockManagerMigrationManager implements MigrationManager interface for testing
type MockManagerMigrationManager struct {
	appliedMigrations map[uuid.UUID]map[string]*Migration
	// applyPendingErr, when set, is returned once by ApplyPending and then cleared.
	applyPendingErr   error
	applyPendingCalls int
	// applied, when set, is returned directly by GetAppliedMigrations,
	// regardless of tenant ID.
	applied []*Migration
}

func (m *MockManagerMigrationManager) ApplyMigration(ctx context.Context, tenantID uuid.UUID, migration *Migration) error {
	if m.appliedMigrations[tenantID] == nil {
		m.appliedMigrations[tenantID] = make(map[string]*Migration)
	}

	if _, exists := m.appliedMigrations[tenantID][migration.Version]; exists {
		return &TenantError{TenantID: tenantID, Code: "MIGRATION_EXISTS", Message: "migration already applied"}
	}

	migration.AppliedAt = time.Now()
	m.appliedMigrations[tenantID][migration.Version] = migration
	return nil
}

func (m *MockManagerMigrationManager) RollbackMigration(ctx context.Context, tenantID uuid.UUID, version string) error {
	if m.appliedMigrations[tenantID] == nil {
		return &TenantError{TenantID: tenantID, Code: "NO_MIGRATIONS", Message: "no migrations applied"}
	}

	if _, exists := m.appliedMigrations[tenantID][version]; !exists {
		return &TenantError{TenantID: tenantID, Code: "MIGRATION_NOT_FOUND", Message: "migration not applied"}
	}

	delete(m.appliedMigrations[tenantID], version)
	return nil
}

func (m *MockManagerMigrationManager) ApplyToAllTenants(ctx context.Context, migration *Migration) error {
	for tenantID := range m.appliedMigrations {
		if err := m.ApplyMigration(ctx, tenantID, migration); err != nil {
			return err
		}
	}
	return nil
}

// applyPendingErr, when set, is returned once by ApplyPending and then cleared.
func (m *MockManagerMigrationManager) ApplyPending(ctx context.Context, tenantID uuid.UUID) error {
	m.applyPendingCalls++
	if m.applyPendingErr != nil {
		err := m.applyPendingErr
		m.applyPendingErr = nil
		return err
	}
	return nil
}
func (m *MockManagerMigrationManager) ApplyPendingToAllTenants(ctx context.Context) error { return nil }

func (m *MockManagerMigrationManager) GetAppliedMigrations(ctx context.Context, tenantID uuid.UUID) ([]*Migration, error) {
	if m.applied != nil {
		return m.applied, nil
	}

	migrations := m.appliedMigrations[tenantID]
	if migrations == nil {
		return []*Migration{}, nil
	}

	var result []*Migration
	for _, migration := range migrations {
		result = append(result, migration)
	}
	return result, nil
}

func (m *MockManagerMigrationManager) IsMigrationApplied(ctx context.Context, tenantID uuid.UUID, version string) (bool, error) {
	migrations := m.appliedMigrations[tenantID]
	if migrations == nil {
		return false, nil
	}
	_, exists := migrations[version]
	return exists, nil
}

// NewMockLimitChecker creates a mock limit checker for testing
func NewMockLimitChecker(config LimitsConfig) *MockManagerLimitChecker {
	return &MockManagerLimitChecker{
		config:     config,
		planLimits: config.PlanLimits,
	}
}

// MockManagerLimitChecker implements LimitChecker interface for testing
type MockManagerLimitChecker struct {
	config     LimitsConfig
	planLimits map[string]FlexibleLimits
}

func (m *MockManagerLimitChecker) CheckLimit(ctx context.Context, tenantID uuid.UUID, limitName string, currentValue interface{}) error {
	return nil // Mock always passes
}

func (m *MockManagerLimitChecker) CheckLimitByDefinition(ctx context.Context, tenantID uuid.UUID, def *LimitDefinition, currentValue interface{}) error {
	return nil
}

func (m *MockManagerLimitChecker) CheckAllLimits(ctx context.Context, tenantID uuid.UUID) error {
	return nil
}

func (m *MockManagerLimitChecker) GetLimitSchema() *LimitSchema {
	return m.config.LimitSchema
}

func (m *MockManagerLimitChecker) SetLimitSchema(schema *LimitSchema) {
	m.config.LimitSchema = schema
}

func (m *MockManagerLimitChecker) GetLimitsForPlan(planType string) FlexibleLimits {
	return m.planLimits[planType]
}

func (m *MockManagerLimitChecker) SetLimitsForPlan(planType string, limits FlexibleLimits) {
	m.planLimits[planType] = limits
}

func (m *MockManagerLimitChecker) AddLimit(planType, limitName string, limitType LimitType, value interface{}) error {
	if m.planLimits[planType] == nil {
		m.planLimits[planType] = make(FlexibleLimits)
	}
	m.planLimits[planType][limitName] = &LimitValue{Type: limitType, Value: value}
	return nil
}

func (m *MockManagerLimitChecker) RemoveLimit(planType, limitName string) error {
	if m.planLimits[planType] != nil {
		delete(m.planLimits[planType], limitName)
	}
	return nil
}

func (m *MockManagerLimitChecker) UpdateLimit(planType, limitName string, value interface{}) error {
	if m.planLimits[planType] == nil || m.planLimits[planType][limitName] == nil {
		return &TenantError{Code: "LIMIT_NOT_FOUND", Message: "limit not found"}
	}
	m.planLimits[planType][limitName].Value = value
	return nil
}

func (m *MockManagerLimitChecker) ValidateLimits(planType string, limits FlexibleLimits) error {
	return nil
}

func (m *MockManagerLimitChecker) SetUsageTracker(tracker UsageTracker) {
	// Mock implementation
}

func (m *MockManagerLimitChecker) GetUsageTracker() UsageTracker {
	return nil
}

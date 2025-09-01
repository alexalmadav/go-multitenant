package tenant

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap/zaptest"
)

func TestNewLimitChecker(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{
		EnforceLimits: true,
		DefaultPlan:   PlanBasic,
		PlanLimits:    make(map[string]FlexibleLimits),
	}

	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}

	checker := NewLimitChecker(config, mockRepo, logger)
	if checker == nil {
		t.Error("NewLimitChecker() should not return nil")
	}
}

func TestLimitChecker_CheckLimit(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create test plan limits
	basicLimits := make(FlexibleLimits)
	basicLimits.Set("max_users", LimitTypeInt, 10)
	basicLimits.Set("max_storage_gb", LimitTypeFloat, 5.0)
	basicLimits.Set("advanced_features", LimitTypeBool, false)
	basicLimits.Set("region", LimitTypeString, "us-east")
	basicLimits.Set("unlimited_api", LimitTypeInt, -1) // unlimited

	config := LimitsConfig{
		EnforceLimits: true,
		DefaultPlan:   PlanBasic,
		PlanLimits: map[string]FlexibleLimits{
			PlanBasic: basicLimits,
		},
	}

	tenantID := uuid.New()
	mockRepo := &MockLimitCheckerRepository{
		tenants: map[uuid.UUID]*Tenant{
			tenantID: {
				ID:       tenantID,
				PlanType: PlanBasic,
				Status:   StatusActive,
			},
		},
	}

	checker := NewLimitChecker(config, mockRepo, logger)

	tests := []struct {
		name         string
		limitName    string
		currentValue interface{}
		wantErr      bool
	}{
		{
			name:         "int limit within bounds",
			limitName:    "max_users",
			currentValue: 5,
			wantErr:      false,
		},
		{
			name:         "int limit at bounds",
			limitName:    "max_users",
			currentValue: 10,
			wantErr:      false,
		},
		{
			name:         "int limit exceeded",
			limitName:    "max_users",
			currentValue: 15,
			wantErr:      true,
		},
		{
			name:         "float limit within bounds",
			limitName:    "max_storage_gb",
			currentValue: 3.5,
			wantErr:      false,
		},
		{
			name:         "float limit exceeded",
			limitName:    "max_storage_gb",
			currentValue: 7.5,
			wantErr:      true,
		},
		{
			name:         "bool limit allowed",
			limitName:    "advanced_features",
			currentValue: false,
			wantErr:      false,
		},
		{
			name:         "bool limit denied",
			limitName:    "advanced_features",
			currentValue: true,
			wantErr:      true,
		},
		{
			name:         "unlimited limit",
			limitName:    "unlimited_api",
			currentValue: 1000000,
			wantErr:      false,
		},
		{
			name:         "non-existent limit",
			limitName:    "non_existent",
			currentValue: 5,
			wantErr:      false, // Should not error for non-existent limits
		},
		{
			name:         "nil current value",
			limitName:    "max_users",
			currentValue: nil,
			wantErr:      false, // Should not error with nil current value
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checker.CheckLimit(context.Background(), tenantID, tt.limitName, tt.currentValue)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckLimit() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLimitChecker_CheckLimit_EnforcementDisabled(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := LimitsConfig{
		EnforceLimits: false, // Disabled
		DefaultPlan:   PlanBasic,
	}

	tenantID := uuid.New()
	mockRepo := &MockLimitCheckerRepository{
		tenants: map[uuid.UUID]*Tenant{
			tenantID: {
				ID:       tenantID,
				PlanType: PlanBasic,
				Status:   StatusActive,
			},
		},
	}

	checker := NewLimitChecker(config, mockRepo, logger)

	// Should not error even with exceeded limits when enforcement is disabled
	err := checker.CheckLimit(context.Background(), tenantID, "max_users", 1000)
	if err != nil {
		t.Errorf("CheckLimit() should not error when enforcement is disabled, got: %v", err)
	}
}

func TestLimitChecker_CheckAllLimits(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create test plan limits
	basicLimits := make(FlexibleLimits)
	basicLimits.Set("max_users", LimitTypeInt, 10)
	basicLimits.Set("max_storage_gb", LimitTypeFloat, 5.0)

	config := LimitsConfig{
		EnforceLimits: true,
		DefaultPlan:   PlanBasic,
		PlanLimits: map[string]FlexibleLimits{
			PlanBasic: basicLimits,
		},
	}

	tenantID := uuid.New()
	mockRepo := &MockLimitCheckerRepository{
		tenants: map[uuid.UUID]*Tenant{
			tenantID: {
				ID:       tenantID,
				PlanType: PlanBasic,
				Status:   StatusActive,
			},
		},
	}

	checker := NewLimitChecker(config, mockRepo, logger)

	// Test with valid limits
	err := checker.CheckAllLimits(context.Background(), tenantID)
	if err != nil {
		t.Errorf("CheckAllLimits() error = %v, want nil", err)
	}

	// Test with non-existent tenant
	nonExistentID := uuid.New()
	err = checker.CheckAllLimits(context.Background(), nonExistentID)
	if err == nil {
		t.Error("CheckAllLimits() should error for non-existent tenant")
	}
}

func TestLimitChecker_PlanLimitManagement(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{
		EnforceLimits: true,
		DefaultPlan:   PlanBasic,
		PlanLimits:    make(map[string]FlexibleLimits),
	}

	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger)

	// Test getting non-existent plan limits
	limits := checker.GetLimitsForPlan("non-existent")
	if limits != nil {
		t.Error("GetLimitsForPlan() should return nil for non-existent plan")
	}

	// Test setting plan limits
	testLimits := make(FlexibleLimits)
	testLimits.Set("max_users", LimitTypeInt, 10)
	checker.SetLimitsForPlan(PlanBasic, testLimits)

	// Test getting plan limits
	retrievedLimits := checker.GetLimitsForPlan(PlanBasic)
	if retrievedLimits == nil {
		t.Error("GetLimitsForPlan() should return limits after setting")
	}

	if val, err := retrievedLimits.GetInt("max_users"); err != nil || val != 10 {
		t.Errorf("Plan limits not set correctly: got %v, want 10", val)
	}
}

func TestLimitChecker_LimitManagement(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{
		EnforceLimits: true,
		DefaultPlan:   PlanBasic,
		PlanLimits:    make(map[string]FlexibleLimits),
		LimitSchema:   DefaultLimitSchema(),
	}

	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger)

	// Test adding limit
	err := checker.AddLimit(PlanBasic, "test_limit", LimitTypeInt, 5)
	if err != nil {
		t.Errorf("AddLimit() error = %v, want nil", err)
	}

	// Verify limit was added
	limits := checker.GetLimitsForPlan(PlanBasic)
	if limits == nil {
		t.Error("Plan limits should exist after adding limit")
	}

	if val, err := limits.GetInt("test_limit"); err != nil || val != 5 {
		t.Errorf("Added limit not found: got %v, want 5", val)
	}

	// Test updating limit
	err = checker.UpdateLimit(PlanBasic, "test_limit", 10)
	if err != nil {
		t.Errorf("UpdateLimit() error = %v, want nil", err)
	}

	if val, err := limits.GetInt("test_limit"); err != nil || val != 10 {
		t.Errorf("Updated limit incorrect: got %v, want 10", val)
	}

	// Test removing limit
	err = checker.RemoveLimit(PlanBasic, "test_limit")
	if err != nil {
		t.Errorf("RemoveLimit() error = %v, want nil", err)
	}

	if limits.Has("test_limit") {
		t.Error("Limit should be removed after RemoveLimit()")
	}

	// Test updating non-existent limit
	err = checker.UpdateLimit(PlanBasic, "non_existent", 5)
	if err == nil {
		t.Error("UpdateLimit() should error for non-existent limit")
	}
}

func TestLimitChecker_SchemaManagement(t *testing.T) {
	logger := zaptest.NewLogger(t)
	schema := DefaultLimitSchema()
	config := LimitsConfig{
		EnforceLimits: true,
		DefaultPlan:   PlanBasic,
		PlanLimits:    make(map[string]FlexibleLimits),
		LimitSchema:   schema,
	}

	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger)

	// Test getting schema
	retrievedSchema := checker.GetLimitSchema()
	if retrievedSchema != schema {
		t.Error("GetLimitSchema() should return the same schema")
	}

	// Test setting schema
	newSchema := &LimitSchema{}
	checker.SetLimitSchema(newSchema)

	retrievedSchema = checker.GetLimitSchema()
	if retrievedSchema != newSchema {
		t.Error("SetLimitSchema() should update the schema")
	}
}

func TestLimitChecker_ValidateLimits(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{
		EnforceLimits: true,
		DefaultPlan:   PlanBasic,
		PlanLimits:    make(map[string]FlexibleLimits),
		LimitSchema:   DefaultLimitSchema(),
	}

	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger)

	testLimits := make(FlexibleLimits)
	testLimits.Set("max_users", LimitTypeInt, 10)
	testLimits.Set("max_projects", LimitTypeInt, 5)
	testLimits.Set("max_storage_gb", LimitTypeInt, 1)

	err := checker.ValidateLimits(PlanBasic, testLimits)
	if err != nil {
		t.Errorf("ValidateLimits() error = %v, want nil", err)
	}
}

func TestLimitChecker_UsageTracker(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{
		EnforceLimits: true,
		DefaultPlan:   PlanBasic,
		PlanLimits:    make(map[string]FlexibleLimits),
	}

	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger)

	// Test getting usage tracker (should be nil initially)
	tracker := checker.GetUsageTracker()
	if tracker != nil {
		t.Error("GetUsageTracker() should return nil initially")
	}

	// Test setting usage tracker
	mockTracker := &MockUsageTracker{}
	checker.SetUsageTracker(mockTracker)

	retrievedTracker := checker.GetUsageTracker()
	if retrievedTracker == nil {
		t.Error("SetUsageTracker() should set the usage tracker")
	}
}

func TestLimitChecker_validateIntLimit(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{EnforceLimits: true}
	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger).(*limitChecker)

	tenantID := uuid.New()
	limit := &LimitValue{Type: LimitTypeInt, Value: 10}

	tests := []struct {
		name         string
		currentValue interface{}
		wantErr      bool
	}{
		{"valid int within limit", 5, false},
		{"valid int at limit", 10, false},
		{"valid int over limit", 15, true},
		{"valid int64 within limit", int64(5), false},
		{"valid float64 within limit", float64(5), false},
		{"invalid type", "invalid", true},
		{"nil value", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checker.validateLimit(tenantID, "max_users", limit, tt.currentValue)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateIntLimit() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLimitChecker_validateFloatLimit(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{EnforceLimits: true}
	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger).(*limitChecker)

	tenantID := uuid.New()
	limit := &LimitValue{Type: LimitTypeFloat, Value: 10.5}

	tests := []struct {
		name         string
		currentValue interface{}
		wantErr      bool
	}{
		{"valid float64 within limit", 5.5, false},
		{"valid float64 at limit", 10.5, false},
		{"valid float64 over limit", 15.5, true},
		{"valid float32 within limit", float32(5.5), false},
		{"valid int within limit", 5, false},
		{"valid int64 within limit", int64(5), false},
		{"invalid type", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checker.validateLimit(tenantID, "max_storage", limit, tt.currentValue)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFloatLimit() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLimitChecker_validateBoolLimit(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{EnforceLimits: true}
	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger).(*limitChecker)

	tenantID := uuid.New()

	tests := []struct {
		name         string
		limitValue   bool
		currentValue interface{}
		wantErr      bool
	}{
		{"feature allowed and used", true, true, false},
		{"feature allowed and not used", true, false, false},
		{"feature not allowed but used", false, true, true},
		{"feature not allowed and not used", false, false, false},
		{"invalid type", true, "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := &LimitValue{Type: LimitTypeBool, Value: tt.limitValue}
			err := checker.validateLimit(tenantID, "advanced_features", limit, tt.currentValue)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBoolLimit() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLimitChecker_validateStringLimit(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{EnforceLimits: true}
	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger).(*limitChecker)

	tenantID := uuid.New()
	limit := &LimitValue{Type: LimitTypeString, Value: "short"}

	tests := []struct {
		name         string
		currentValue interface{}
		wantErr      bool
	}{
		{"valid string shorter", "abc", false},
		{"valid string same length", "short", false},
		{"valid string longer", "verylongstring", true},
		{"unlimited string", "unlimited", false},
		{"empty limit string", "", false},
		{"invalid type", 123, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "unlimited string" {
				limit = &LimitValue{Type: LimitTypeString, Value: "unlimited"}
			} else if tt.name == "empty limit string" {
				limit = &LimitValue{Type: LimitTypeString, Value: ""}
			} else {
				limit = &LimitValue{Type: LimitTypeString, Value: "short"}
			}

			err := checker.validateLimit(tenantID, "region", limit, tt.currentValue)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStringLimit() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLimitChecker_validateDurationLimit(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := LimitsConfig{EnforceLimits: true}
	mockRepo := &MockLimitCheckerRepository{tenants: make(map[uuid.UUID]*Tenant)}
	checker := NewLimitChecker(config, mockRepo, logger).(*limitChecker)

	tenantID := uuid.New()
	limit := &LimitValue{Type: LimitTypeDuration, Value: "5m"}

	// Duration validation is not fully implemented, so it should not error
	err := checker.validateLimit(tenantID, "timeout", limit, time.Minute)
	if err != nil {
		t.Errorf("validateDurationLimit() error = %v, want nil (not implemented)", err)
	}
}

// Mock implementations for limit checker tests

type MockLimitCheckerRepository struct {
	tenants map[uuid.UUID]*Tenant
}

func (m *MockLimitCheckerRepository) Create(ctx context.Context, tenant *Tenant) error {
	m.tenants[tenant.ID] = tenant
	return nil
}

func (m *MockLimitCheckerRepository) GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	tenant, exists := m.tenants[id]
	if !exists {
		return nil, &TenantError{TenantID: id, Code: "NOT_FOUND", Message: "tenant not found"}
	}
	return tenant, nil
}

func (m *MockLimitCheckerRepository) GetBySubdomain(ctx context.Context, subdomain string) (*Tenant, error) {
	for _, tenant := range m.tenants {
		if tenant.Subdomain == subdomain {
			return tenant, nil
		}
	}
	return nil, &TenantError{Code: "NOT_FOUND", Message: "tenant not found"}
}

func (m *MockLimitCheckerRepository) Update(ctx context.Context, tenant *Tenant) error {
	m.tenants[tenant.ID] = tenant
	return nil
}

func (m *MockLimitCheckerRepository) Delete(ctx context.Context, id uuid.UUID) error {
	delete(m.tenants, id)
	return nil
}

func (m *MockLimitCheckerRepository) List(ctx context.Context, page, perPage int) ([]*Tenant, int, error) {
	var tenants []*Tenant
	for _, tenant := range m.tenants {
		tenants = append(tenants, tenant)
	}
	return tenants, len(tenants), nil
}

func (m *MockLimitCheckerRepository) GetStats(ctx context.Context, tenantID uuid.UUID) (*Stats, error) {
	return &Stats{TenantID: tenantID}, nil
}

type MockUsageTracker struct{}

func (m *MockUsageTracker) GetCurrentUsage(ctx context.Context, tenantID uuid.UUID, limitName string) (interface{}, error) {
	// Return mock usage data
	switch limitName {
	case "max_users":
		return 5, nil
	case "max_storage_gb":
		return 2.5, nil
	case "advanced_features":
		return false, nil
	default:
		return nil, &TenantError{Code: "UNKNOWN_LIMIT", Message: "unknown limit"}
	}
}

func (m *MockUsageTracker) IncrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error {
	return nil // Mock implementation
}

func (m *MockUsageTracker) DecrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error {
	return nil // Mock implementation
}

func (m *MockUsageTracker) ResetUsage(ctx context.Context, tenantID uuid.UUID, limitName string) error {
	return nil // Mock implementation
}

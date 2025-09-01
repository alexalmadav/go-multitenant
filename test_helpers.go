package multitenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// TestHelpers provides utilities for testing
type TestHelpers struct {
	Logger *zap.Logger
}

// NewTestHelpers creates new test helpers
func NewTestHelpers() *TestHelpers {
	logger := zaptest.NewLogger(nil)
	return &TestHelpers{
		Logger: logger,
	}
}

// MockRepository implements tenant.Repository for testing
type MockRepository struct {
	tenants map[uuid.UUID]*tenant.Tenant
	stats   map[uuid.UUID]*tenant.Stats
}

// NewMockRepository creates a new mock repository
func NewMockRepository() *MockRepository {
	return &MockRepository{
		tenants: make(map[uuid.UUID]*tenant.Tenant),
		stats:   make(map[uuid.UUID]*tenant.Stats),
	}
}

func (m *MockRepository) Create(ctx context.Context, t *tenant.Tenant) error {
	if t.ID == uuid.Nil {
		return errors.New("tenant ID is required")
	}
	if _, exists := m.tenants[t.ID]; exists {
		return errors.New("tenant already exists")
	}

	// Check for duplicate subdomain
	for _, existing := range m.tenants {
		if existing.Subdomain == t.Subdomain {
			return errors.New("subdomain already exists")
		}
	}

	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now
	m.tenants[t.ID] = t
	return nil
}

func (m *MockRepository) GetByID(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	t, exists := m.tenants[id]
	if !exists {
		return nil, sql.ErrNoRows
	}
	return t, nil
}

func (m *MockRepository) GetBySubdomain(ctx context.Context, subdomain string) (*tenant.Tenant, error) {
	for _, t := range m.tenants {
		if t.Subdomain == subdomain {
			return t, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (m *MockRepository) Update(ctx context.Context, t *tenant.Tenant) error {
	existing, exists := m.tenants[t.ID]
	if !exists {
		return errors.New("tenant not found")
	}

	// Check for duplicate subdomain (excluding self)
	for id, other := range m.tenants {
		if id != t.ID && other.Subdomain == t.Subdomain {
			return errors.New("subdomain already exists")
		}
	}

	t.CreatedAt = existing.CreatedAt
	t.UpdatedAt = time.Now()
	m.tenants[t.ID] = t
	return nil
}

func (m *MockRepository) Delete(ctx context.Context, id uuid.UUID) error {
	t, exists := m.tenants[id]
	if !exists {
		return errors.New("tenant not found")
	}
	t.Status = tenant.StatusCancelled
	t.UpdatedAt = time.Now()
	return nil
}

func (m *MockRepository) List(ctx context.Context, page, perPage int) ([]*tenant.Tenant, int, error) {
	var activeTenants []*tenant.Tenant
	for _, t := range m.tenants {
		if t.Status != tenant.StatusCancelled {
			activeTenants = append(activeTenants, t)
		}
	}

	total := len(activeTenants)
	start := (page - 1) * perPage
	end := start + perPage

	if start >= total {
		return []*tenant.Tenant{}, total, nil
	}
	if end > total {
		end = total
	}

	return activeTenants[start:end], total, nil
}

func (m *MockRepository) GetStats(ctx context.Context, tenantID uuid.UUID) (*tenant.Stats, error) {
	stats, exists := m.stats[tenantID]
	if !exists {
		// Return default stats
		stats = &tenant.Stats{
			TenantID:      tenantID,
			UserCount:     0,
			ProjectCount:  0,
			StorageUsedGB: 0.0,
			LastActivity:  time.Now(),
			SchemaExists:  true,
		}
		m.stats[tenantID] = stats
	}
	return stats, nil
}

// SetStats allows setting stats for testing
func (m *MockRepository) SetStats(tenantID uuid.UUID, stats *tenant.Stats) {
	m.stats[tenantID] = stats
}

// MockSchemaManager implements tenant.SchemaManager for testing
type MockSchemaManager struct {
	schemas map[uuid.UUID]bool
	prefix  string
}

// NewMockSchemaManager creates a new mock schema manager
func NewMockSchemaManager(prefix string) *MockSchemaManager {
	if prefix == "" {
		prefix = "tenant_"
	}
	return &MockSchemaManager{
		schemas: make(map[uuid.UUID]bool),
		prefix:  prefix,
	}
}

func (m *MockSchemaManager) CreateTenantSchema(ctx context.Context, tenantID uuid.UUID, name string) error {
	if m.schemas[tenantID] {
		return errors.New("schema already exists")
	}
	m.schemas[tenantID] = true
	return nil
}

func (m *MockSchemaManager) DropTenantSchema(ctx context.Context, tenantID uuid.UUID) error {
	delete(m.schemas, tenantID)
	return nil
}

func (m *MockSchemaManager) SchemaExists(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	return m.schemas[tenantID], nil
}

func (m *MockSchemaManager) GetSchemaName(tenantID uuid.UUID) string {
	return fmt.Sprintf("%s%s", m.prefix, tenantID.String())
}

func (m *MockSchemaManager) SetSearchPath(db *sql.DB, tenantID uuid.UUID) error {
	if !m.schemas[tenantID] {
		return errors.New("schema does not exist")
	}
	return nil
}

func (m *MockSchemaManager) ListTenantSchemas(ctx context.Context) ([]string, error) {
	var schemas []string
	for tenantID := range m.schemas {
		schemas = append(schemas, m.GetSchemaName(tenantID))
	}
	return schemas, nil
}

// MockMigrationManager implements tenant.MigrationManager for testing
type MockMigrationManager struct {
	appliedMigrations map[uuid.UUID]map[string]*tenant.Migration
}

// NewMockMigrationManager creates a new mock migration manager
func NewMockMigrationManager() *MockMigrationManager {
	return &MockMigrationManager{
		appliedMigrations: make(map[uuid.UUID]map[string]*tenant.Migration),
	}
}

func (m *MockMigrationManager) ApplyMigration(ctx context.Context, tenantID uuid.UUID, migration *tenant.Migration) error {
	if m.appliedMigrations[tenantID] == nil {
		m.appliedMigrations[tenantID] = make(map[string]*tenant.Migration)
	}

	if _, exists := m.appliedMigrations[tenantID][migration.Version]; exists {
		return errors.New("migration already applied")
	}

	migration.AppliedAt = time.Now()
	m.appliedMigrations[tenantID][migration.Version] = migration
	return nil
}

func (m *MockMigrationManager) RollbackMigration(ctx context.Context, tenantID uuid.UUID, version string) error {
	if m.appliedMigrations[tenantID] == nil {
		return errors.New("no migrations applied")
	}

	if _, exists := m.appliedMigrations[tenantID][version]; !exists {
		return errors.New("migration not applied")
	}

	delete(m.appliedMigrations[tenantID], version)
	return nil
}

func (m *MockMigrationManager) ApplyToAllTenants(ctx context.Context, migration *tenant.Migration) error {
	for tenantID := range m.appliedMigrations {
		if err := m.ApplyMigration(ctx, tenantID, migration); err != nil {
			return err
		}
	}
	return nil
}

func (m *MockMigrationManager) GetAppliedMigrations(ctx context.Context, tenantID uuid.UUID) ([]*tenant.Migration, error) {
	migrations := m.appliedMigrations[tenantID]
	if migrations == nil {
		return []*tenant.Migration{}, nil
	}

	var result []*tenant.Migration
	for _, migration := range migrations {
		result = append(result, migration)
	}
	return result, nil
}

func (m *MockMigrationManager) IsMigrationApplied(ctx context.Context, tenantID uuid.UUID, version string) (bool, error) {
	migrations := m.appliedMigrations[tenantID]
	if migrations == nil {
		return false, nil
	}
	_, exists := migrations[version]
	return exists, nil
}

// MockLimitChecker implements tenant.LimitChecker for testing
type MockLimitChecker struct {
	config     tenant.LimitsConfig
	planLimits map[string]tenant.FlexibleLimits
}

// NewMockLimitChecker creates a new mock limit checker
func NewMockLimitChecker(config tenant.LimitsConfig) *MockLimitChecker {
	return &MockLimitChecker{
		config:     config,
		planLimits: config.PlanLimits,
	}
}

func (m *MockLimitChecker) CheckLimit(ctx context.Context, tenantID uuid.UUID, limitName string, currentValue interface{}) error {
	if !m.config.EnforceLimits {
		return nil
	}
	// Simple mock implementation - always pass
	return nil
}

func (m *MockLimitChecker) CheckLimitByDefinition(ctx context.Context, tenantID uuid.UUID, def *tenant.LimitDefinition, currentValue interface{}) error {
	return m.CheckLimit(ctx, tenantID, def.Name, currentValue)
}

func (m *MockLimitChecker) CheckAllLimits(ctx context.Context, tenantID uuid.UUID) error {
	if !m.config.EnforceLimits {
		return nil
	}
	return nil
}

func (m *MockLimitChecker) GetLimitSchema() *tenant.LimitSchema {
	return m.config.LimitSchema
}

func (m *MockLimitChecker) SetLimitSchema(schema *tenant.LimitSchema) {
	m.config.LimitSchema = schema
}

func (m *MockLimitChecker) GetLimitsForPlan(planType string) tenant.FlexibleLimits {
	return m.planLimits[planType]
}

func (m *MockLimitChecker) SetLimitsForPlan(planType string, limits tenant.FlexibleLimits) {
	m.planLimits[planType] = limits
}

func (m *MockLimitChecker) AddLimit(planType, limitName string, limitType tenant.LimitType, value interface{}) error {
	if m.planLimits[planType] == nil {
		m.planLimits[planType] = make(tenant.FlexibleLimits)
	}
	m.planLimits[planType][limitName] = &tenant.LimitValue{Type: limitType, Value: value}
	return nil
}

func (m *MockLimitChecker) RemoveLimit(planType, limitName string) error {
	if m.planLimits[planType] != nil {
		delete(m.planLimits[planType], limitName)
	}
	return nil
}

func (m *MockLimitChecker) UpdateLimit(planType, limitName string, value interface{}) error {
	if m.planLimits[planType] == nil || m.planLimits[planType][limitName] == nil {
		return errors.New("limit not found")
	}
	m.planLimits[planType][limitName].Value = value
	return nil
}

func (m *MockLimitChecker) ValidateLimits(planType string, limits tenant.FlexibleLimits) error {
	return nil
}

func (m *MockLimitChecker) SetUsageTracker(tracker tenant.UsageTracker) {
	// Mock implementation
}

func (m *MockLimitChecker) GetUsageTracker() tenant.UsageTracker {
	return nil
}

// TestData provides common test data
type TestData struct {
	TenantID      uuid.UUID
	Tenant        *tenant.Tenant
	Config        tenant.Config
	MockRepo      *MockRepository
	MockSchema    *MockSchemaManager
	MockMigration *MockMigrationManager
	MockLimits    *MockLimitChecker
}

// NewTestData creates a complete test data setup
func NewTestData() *TestData {
	tenantID := uuid.New()

	config := tenant.DefaultConfig()
	config.Database.DSN = "test://localhost"

	mockRepo := NewMockRepository()
	mockSchema := NewMockSchemaManager(config.Database.SchemaPrefix)
	mockMigration := NewMockMigrationManager()
	mockLimits := NewMockLimitChecker(config.Limits)

	testTenant := &tenant.Tenant{
		ID:         tenantID,
		Name:       "Test Tenant",
		Subdomain:  "test-tenant",
		PlanType:   tenant.PlanBasic,
		Status:     tenant.StatusActive,
		SchemaName: mockSchema.GetSchemaName(tenantID),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	return &TestData{
		TenantID:      tenantID,
		Tenant:        testTenant,
		Config:        config,
		MockRepo:      mockRepo,
		MockSchema:    mockSchema,
		MockMigration: mockMigration,
		MockLimits:    mockLimits,
	}
}

// CreateHTTPRequest creates a test HTTP request
func CreateHTTPRequest(method, url, host string, headers map[string]string) *http.Request {
	req, _ := http.NewRequest(method, url, nil)
	if host != "" {
		req.Host = host
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	return req
}

// AssertTenantEqual compares two tenants for equality (ignoring timestamps)
func AssertTenantEqual(t1, t2 *tenant.Tenant) bool {
	return t1.ID == t2.ID &&
		t1.Name == t2.Name &&
		t1.Subdomain == t2.Subdomain &&
		t1.PlanType == t2.PlanType &&
		t1.Status == t2.Status &&
		t1.SchemaName == t2.SchemaName
}

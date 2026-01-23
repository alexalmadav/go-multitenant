package tenant

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// manager implements the Manager interface
type manager struct {
	config        Config
	db            *sql.DB
	repository    Repository
	schemaManager SchemaManager
	migrationMgr  MigrationManager
	limitChecker  LimitChecker
	logger        *zap.Logger
	connections   map[uuid.UUID]*sql.DB // Tenant-specific connections
}

// NewManager creates a new tenant manager
func NewManager(config Config, db *sql.DB, repository Repository, schemaManager SchemaManager, migrationMgr MigrationManager, limitChecker LimitChecker, logger *zap.Logger) Manager {
	return &manager{
		config:        config,
		db:            db,
		repository:    repository,
		schemaManager: schemaManager,
		migrationMgr:  migrationMgr,
		limitChecker:  limitChecker,
		logger:        logger.Named("tenant_manager"),
		connections:   make(map[uuid.UUID]*sql.DB),
	}
}

// CreateTenant creates a new tenant
func (m *manager) CreateTenant(ctx context.Context, tenant *Tenant) error {
	// Validate tenant data
	if err := m.validateTenant(tenant); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Generate ID if not provided
	if tenant.ID == uuid.Nil {
		tenant.ID = uuid.New()
	}

	// Generate schema name
	tenant.SchemaName = m.schemaManager.GetSchemaName(tenant.ID)

	// Set default values
	if tenant.Status == "" {
		tenant.Status = StatusPending
	}
	if tenant.PlanType == "" {
		tenant.PlanType = PlanBasic
	}

	// Create tenant record
	if err := m.repository.Create(ctx, tenant); err != nil {
		return fmt.Errorf("failed to create tenant: %w", err)
	}

	m.logger.Info("Created tenant",
		zap.String("tenant_id", tenant.ID.String()),
		zap.String("name", tenant.Name),
		zap.String("subdomain", tenant.Subdomain))

	return nil
}

// GetTenant retrieves a tenant by ID
func (m *manager) GetTenant(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	return m.repository.GetByID(ctx, id)
}

// GetTenantBySubdomain retrieves a tenant by subdomain
func (m *manager) GetTenantBySubdomain(ctx context.Context, subdomain string) (*Tenant, error) {
	return m.repository.GetBySubdomain(ctx, subdomain)
}

// UpdateTenant updates a tenant
func (m *manager) UpdateTenant(ctx context.Context, tenant *Tenant) error {
	if err := m.validateTenant(tenant); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	return m.repository.Update(ctx, tenant)
}

// DeleteTenant soft deletes a tenant
func (m *manager) DeleteTenant(ctx context.Context, id uuid.UUID) error {
	return m.repository.Delete(ctx, id)
}

// ListTenants lists tenants with pagination
func (m *manager) ListTenants(ctx context.Context, page, perPage int) ([]*Tenant, int, error) {
	return m.repository.List(ctx, page, perPage)
}

// ProvisionTenant creates the tenant schema and activates the tenant
func (m *manager) ProvisionTenant(ctx context.Context, id uuid.UUID) error {
	// Get tenant
	tenant, err := m.repository.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	// Check if already provisioned
	exists, err := m.schemaManager.SchemaExists(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to check schema existence: %w", err)
	}

	if exists {
		m.logger.Info("Tenant schema already exists",
			zap.String("tenant_id", id.String()))
		return nil
	}

	// Create tenant schema
	if err := m.schemaManager.CreateTenantSchema(ctx, id, tenant.Name); err != nil {
		return fmt.Errorf("failed to create tenant schema: %w", err)
	}

	// Update tenant status to active
	tenant.Status = StatusActive
	if err := m.repository.Update(ctx, tenant); err != nil {
		// Try to clean up schema if update fails
		if dropErr := m.schemaManager.DropTenantSchema(ctx, id); dropErr != nil {
			m.logger.Error("Failed to cleanup schema after provisioning failure",
				zap.String("tenant_id", id.String()),
				zap.Error(dropErr))
		}
		return fmt.Errorf("failed to update tenant status: %w", err)
	}

	m.logger.Info("Successfully provisioned tenant",
		zap.String("tenant_id", id.String()),
		zap.String("name", tenant.Name))

	return nil
}

// SuspendTenant suspends a tenant
func (m *manager) SuspendTenant(ctx context.Context, id uuid.UUID) error {
	tenant, err := m.repository.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	tenant.Status = StatusSuspended
	if err := m.repository.Update(ctx, tenant); err != nil {
		return fmt.Errorf("failed to suspend tenant: %w", err)
	}

	m.logger.Info("Suspended tenant",
		zap.String("tenant_id", id.String()))

	return nil
}

// ActivateTenant activates a tenant
func (m *manager) ActivateTenant(ctx context.Context, id uuid.UUID) error {
	tenant, err := m.repository.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	tenant.Status = StatusActive
	if err := m.repository.Update(ctx, tenant); err != nil {
		return fmt.Errorf("failed to activate tenant: %w", err)
	}

	m.logger.Info("Activated tenant",
		zap.String("tenant_id", id.String()))

	return nil
}

// ValidateAccess validates if a user has access to a tenant
func (m *manager) ValidateAccess(ctx context.Context, userID, tenantID uuid.UUID) error {
	// Basic implementation - in practice you'd check user-tenant relationships
	tenant, err := m.repository.GetByID(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	if tenant.Status != StatusActive {
		return fmt.Errorf("tenant is not active: status=%s", tenant.Status)
	}

	// TODO: Add actual user-tenant relationship validation
	// This would typically involve checking a users table or tenant_users table

	return nil
}

// CheckLimits validates tenant against plan limits
func (m *manager) CheckLimits(ctx context.Context, tenantID uuid.UUID) (*Limits, error) {
	tenant, err := m.repository.GetByID(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}

	// Get flexible plan limits
	flexLimits := m.limitChecker.GetLimitsForPlan(tenant.PlanType)
	if flexLimits == nil {
		return nil, fmt.Errorf("unknown plan type: %s", tenant.PlanType)
	}

	// Check current usage against limits
	if err := m.limitChecker.CheckAllLimits(ctx, tenantID); err != nil {
		return nil, err
	}

	// Convert flexible limits to legacy format for backward compatibility
	limits := &Limits{}
	if maxUsers, err := flexLimits.GetInt("max_users"); err == nil {
		limits.MaxUsers = maxUsers
	}
	if maxProjects, err := flexLimits.GetInt("max_projects"); err == nil {
		limits.MaxProjects = maxProjects
	}
	if maxStorageGB, err := flexLimits.GetInt("max_storage_gb"); err == nil {
		limits.MaxStorageGB = maxStorageGB
	}

	return limits, nil
}

// GetStats retrieves tenant usage statistics
func (m *manager) GetStats(ctx context.Context, tenantID uuid.UUID) (*Stats, error) {
	return m.repository.GetStats(ctx, tenantID)
}

// GetTenantDB returns a database connection with tenant context set.
//
// Deprecated: This method is unsafe with connection pools. The search_path is set on
// one connection, but subsequent queries may use different connections from the pool.
// Use GetTenantConn or WithTenantTx instead for safe tenant-scoped queries.
func (m *manager) GetTenantDB(ctx context.Context, tenantID uuid.UUID) (*sql.DB, error) {
	m.logger.Warn("GetTenantDB is deprecated and unsafe with connection pools. Use GetTenantConn or WithTenantTx instead.",
		zap.String("tenant_id", tenantID.String()))

	// This is fundamentally unsafe but kept for backward compatibility
	if err := m.schemaManager.SetSearchPath(m.db, tenantID); err != nil {
		return nil, fmt.Errorf("failed to set tenant context: %w", err)
	}

	return m.db, nil
}

// GetTenantConn returns a dedicated database connection with search_path set to the tenant's schema.
// The caller MUST close the connection when done to return it to the pool.
func (m *manager) GetTenantConn(ctx context.Context, tenantID uuid.UUID) (*sql.Conn, error) {
	// Get a dedicated connection from the pool
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire connection: %w", err)
	}

	// Set search_path on this specific connection using PostgreSQL identifier quoting
	schemaName := m.schemaManager.GetSchemaName(tenantID)
	quotedSchema := fmt.Sprintf(`"%s"`, schemaName)
	query := fmt.Sprintf("SET search_path TO %s, public", quotedSchema)
	if _, err := conn.ExecContext(ctx, query); err != nil {
		conn.Close() // Release connection on error
		return nil, fmt.Errorf("failed to set search path: %w", err)
	}

	m.logger.Debug("Acquired tenant connection",
		zap.String("tenant_id", tenantID.String()),
		zap.String("schema", schemaName))

	return conn, nil
}

// WithTenantTx executes a function within a transaction with the tenant's search_path set.
// This is the safest way to execute tenant-scoped queries.
func (m *manager) WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(tx *sql.Tx) error) error {
	// Get a dedicated connection
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	// Start transaction
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Set search_path within the transaction using SET LOCAL (scoped to transaction)
	// Using PostgreSQL identifier quoting for the schema name
	schemaName := m.schemaManager.GetSchemaName(tenantID)
	quotedSchema := fmt.Sprintf(`"%s"`, schemaName)
	query := fmt.Sprintf("SET LOCAL search_path TO %s, public", quotedSchema)
	if _, err := tx.ExecContext(ctx, query); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to set search path: %w", err)
	}

	// Execute the user function
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// WithTenantContext adds tenant information to the context
func (m *manager) WithTenantContext(ctx context.Context, tenantID uuid.UUID) context.Context {
	tenant, err := m.repository.GetByID(ctx, tenantID)
	if err != nil {
		m.logger.Error("Failed to get tenant for context",
			zap.String("tenant_id", tenantID.String()),
			zap.Error(err))
		return ctx
	}

	tenantCtx := &Context{
		TenantID:   tenant.ID,
		Subdomain:  tenant.Subdomain,
		SchemaName: tenant.SchemaName,
		PlanType:   tenant.PlanType,
		Status:     tenant.Status,
	}

	ctx = context.WithValue(ctx, ContextKeyTenant, tenantCtx)
	ctx = context.WithValue(ctx, ContextKeyTenantID, tenantID)

	return ctx
}

// Close closes all resources
func (m *manager) Close() error {
	// Close any tenant-specific connections
	for tenantID, conn := range m.connections {
		if err := conn.Close(); err != nil {
			m.logger.Error("Failed to close tenant connection",
				zap.String("tenant_id", tenantID.String()),
				zap.Error(err))
		}
	}

	return nil
}

// validateTenant validates tenant data
func (m *manager) validateTenant(tenant *Tenant) error {
	if tenant.Name == "" {
		return &ValidationError{Field: "name", Message: "name is required"}
	}

	if tenant.Subdomain == "" {
		return &ValidationError{Field: "subdomain", Message: "subdomain is required"}
	}

	if err := m.validateSubdomain(tenant.Subdomain); err != nil {
		return &ValidationError{Field: "subdomain", Message: err.Error()}
	}

	if tenant.PlanType != "" && !ValidatePlanType(tenant.PlanType) {
		return &ValidationError{Field: "plan_type", Message: "invalid plan type"}
	}

	if tenant.Status != "" && !ValidateStatus(tenant.Status) {
		return &ValidationError{Field: "status", Message: "invalid status"}
	}

	return nil
}

// validateSubdomain validates a subdomain format
func (m *manager) validateSubdomain(subdomain string) error {
	if len(subdomain) < 3 || len(subdomain) > 50 {
		return fmt.Errorf("subdomain must be between 3 and 50 characters")
	}

	// Check for valid characters (alphanumeric and hyphens only)
	validSubdomain := regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)
	if !validSubdomain.MatchString(subdomain) {
		return fmt.Errorf("subdomain must contain only lowercase letters, numbers, and hyphens, and cannot start or end with a hyphen")
	}

	// Check for reserved subdomains
	for _, reserved := range m.config.Resolver.ReservedSubdomain {
		if strings.EqualFold(subdomain, reserved) {
			return fmt.Errorf("subdomain '%s' is reserved", subdomain)
		}
	}

	return nil
}

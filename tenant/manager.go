package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

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

	hooksMu sync.RWMutex
	hooks   []Hook
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
	}
}

// RegisterHook adds a lifecycle hook. Safe to call while requests are running.
func (m *manager) RegisterHook(h Hook) {
	m.hooksMu.Lock()
	defer m.hooksMu.Unlock()
	m.hooks = append(m.hooks, h)
}

// snapshotHooks returns the registered hooks in order.
func (m *manager) snapshotHooks() []Hook {
	m.hooksMu.RLock()
	defer m.hooksMu.RUnlock()
	return append([]Hook(nil), m.hooks...)
}

// validateMetadataHooks runs ValidateMetadata on every hook; the first error blocks the write.
func (m *manager) validateMetadataHooks(ctx context.Context, t *Tenant) error {
	for _, h := range m.snapshotHooks() {
		if err := h.ValidateMetadata(ctx, t); err != nil {
			return &ValidationError{Field: "metadata", Message: fmt.Sprintf("%s: %v", h.Name(), err)}
		}
	}
	return nil
}

// runHooks calls fn for every hook, collects failures, and returns a HookError
// if any failed. Every hook runs even when an earlier one fails.
func (m *manager) runHooks(event string, fn func(Hook) error) error {
	var errs []error
	for _, h := range m.snapshotHooks() {
		if err := fn(h); err != nil {
			m.logger.Error("Lifecycle hook failed",
				zap.String("event", event), zap.String("hook", h.Name()), zap.Error(err))
			errs = append(errs, fmt.Errorf("%s: %w", h.Name(), err))
		}
	}
	if len(errs) > 0 {
		return &HookError{Event: event, Errors: errs}
	}
	return nil
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
	if tenant.Metadata == nil {
		tenant.Metadata = TenantMetadata{}
	}

	if err := m.validateMetadataHooks(ctx, tenant); err != nil {
		return err
	}

	// Create tenant record
	if err := m.repository.Create(ctx, tenant); err != nil {
		return fmt.Errorf("failed to create tenant: %w", err)
	}

	m.logger.Info("Created tenant",
		zap.String("tenant_id", tenant.ID.String()),
		zap.String("name", tenant.Name),
		zap.String("subdomain", tenant.Subdomain))

	return m.runHooks("created", func(h Hook) error { return h.OnTenantCreated(ctx, tenant) })
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
	if tenant.Metadata == nil {
		tenant.Metadata = TenantMetadata{}
	}
	if err := m.validateMetadataHooks(ctx, tenant); err != nil {
		return err
	}

	before, err := m.repository.GetByID(ctx, tenant.ID)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}
	if err := m.repository.Update(ctx, tenant); err != nil {
		return err
	}

	var errs []error
	if err := m.runHooks("updated", func(h Hook) error { return h.OnTenantUpdated(ctx, before, tenant) }); err != nil {
		errs = append(errs, err)
	}
	if before.Status != tenant.Status {
		if err := m.runHooks("status_changed", func(h Hook) error { return h.OnTenantStatusChanged(ctx, tenant, before.Status) }); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DeleteTenant soft deletes a tenant
func (m *manager) DeleteTenant(ctx context.Context, id uuid.UUID) error {
	tenant, err := m.repository.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}
	if err := m.repository.Delete(ctx, id); err != nil {
		return err
	}
	tenant.Status = StatusCancelled
	return m.runHooks("deleted", func(h Hook) error { return h.OnTenantDeleted(ctx, tenant) })
}

// ListTenants lists tenants with pagination
func (m *manager) ListTenants(ctx context.Context, page, perPage int) ([]*Tenant, int, error) {
	return m.repository.List(ctx, page, perPage)
}

// ProvisionTenant creates the tenant schema, applies every pending migration
// file, and activates the tenant. It is safe to re-run: a failed provision
// leaves the tenant pending with the work done so far, and the next run
// continues from there.
func (m *manager) ProvisionTenant(ctx context.Context, id uuid.UUID) error {
	tenant, err := m.repository.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}
	previous := tenant.Status
	if tenant.Status == StatusCancelled {
		return fmt.Errorf("cannot provision cancelled tenant %s", id)
	}

	if err := m.schemaManager.CreateTenantSchema(ctx, id); err != nil {
		return fmt.Errorf("failed to create tenant schema: %w", err)
	}

	if err := m.migrationMgr.ApplyPending(ctx, id); err != nil {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	if tenant.Status == StatusActive {
		return nil
	}
	tenant.Status = StatusActive
	if err := m.repository.Update(ctx, tenant); err != nil {
		return fmt.Errorf("failed to activate tenant: %w", err)
	}

	m.logger.Info("Successfully provisioned tenant",
		zap.String("tenant_id", id.String()),
		zap.String("name", tenant.Name))

	var errs []error
	if err := m.runHooks("status_changed", func(h Hook) error { return h.OnTenantStatusChanged(ctx, tenant, previous) }); err != nil {
		errs = append(errs, err)
	}
	if err := m.runHooks("provisioned", func(h Hook) error { return h.OnTenantProvisioned(ctx, tenant) }); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// SuspendTenant suspends a tenant
func (m *manager) SuspendTenant(ctx context.Context, id uuid.UUID) error {
	tenant, err := m.repository.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	previous := tenant.Status
	tenant.Status = StatusSuspended
	if err := m.repository.Update(ctx, tenant); err != nil {
		return fmt.Errorf("failed to suspend tenant: %w", err)
	}

	m.logger.Info("Suspended tenant",
		zap.String("tenant_id", id.String()))

	if previous == StatusSuspended {
		return nil
	}
	return m.runHooks("status_changed", func(h Hook) error { return h.OnTenantStatusChanged(ctx, tenant, previous) })
}

// ActivateTenant activates a tenant
func (m *manager) ActivateTenant(ctx context.Context, id uuid.UUID) error {
	tenant, err := m.repository.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	previous := tenant.Status
	tenant.Status = StatusActive
	if err := m.repository.Update(ctx, tenant); err != nil {
		return fmt.Errorf("failed to activate tenant: %w", err)
	}

	m.logger.Info("Activated tenant",
		zap.String("tenant_id", id.String()))

	if previous == StatusActive {
		return nil
	}
	return m.runHooks("status_changed", func(h Hook) error { return h.OnTenantStatusChanged(ctx, tenant, previous) })
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
	flexLimits := m.limitChecker.GetLimitsForPlan(tenant.Plan())
	if flexLimits == nil {
		return nil, fmt.Errorf("unknown plan type: %s", tenant.Plan())
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

// LimitChecker returns the limit checker used by CheckLimits.
func (m *manager) LimitChecker() LimitChecker {
	return m.limitChecker
}

// GetStats reports whether the schema exists, how many migrations are applied,
// and the current usage for every limit listed in LimitsConfig.UsageTables.
func (m *manager) GetStats(ctx context.Context, tenantID uuid.UUID) (*Stats, error) {
	if _, err := m.repository.GetByID(ctx, tenantID); err != nil {
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}
	exists, err := m.schemaManager.SchemaExists(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to check schema: %w", err)
	}
	stats := &Stats{TenantID: tenantID, SchemaExists: exists, Usage: make(map[string]int)}
	if !exists {
		return stats, nil
	}

	applied, err := m.migrationMgr.GetAppliedMigrations(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to list applied migrations: %w", err)
	}
	stats.AppliedMigrations = len(applied)

	tracker := m.limitChecker.GetUsageTracker()
	if tracker == nil {
		return stats, nil
	}
	for limitName := range m.config.Limits.UsageTables {
		value, err := tracker.GetCurrentUsage(ctx, tenantID, limitName)
		if err != nil {
			m.logger.Warn("Failed to read usage",
				zap.String("tenant_id", tenantID.String()), zap.String("limit", limitName), zap.Error(err))
			continue
		}
		if n, ok := value.(int); ok {
			stats.Usage[limitName] = n
		}
	}
	return stats, nil
}

// GetTenantConn returns a dedicated connection scoped to the tenant's schema.
// Every statement on it runs in its own SET LOCAL transaction, so it is safe
// behind transaction-mode poolers. The caller MUST close it when done.
func (m *manager) GetTenantConn(ctx context.Context, tenantID uuid.UUID) (*Conn, error) {
	// Get a dedicated connection from the pool
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire connection: %w", err)
	}

	// No session state is set here: Conn scopes every statement with
	// SET LOCAL inside its own transaction, which is pooler-safe.
	schemaName := m.schemaManager.GetSchemaName(tenantID)

	m.logger.Debug("Acquired tenant connection",
		zap.String("tenant_id", tenantID.String()),
		zap.String("schema", schemaName))

	return newConn(conn, schemaName), nil
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
		Status:     tenant.Status,
	}

	ctx = context.WithValue(ctx, ContextKeyTenant, tenantCtx)
	ctx = context.WithValue(ctx, ContextKeyTenantID, tenantID)

	return ctx
}

// Close closes all resources
func (m *manager) Close() error {
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
	if !subdomainPattern.MatchString(subdomain) {
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

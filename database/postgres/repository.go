package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

// Repository implements tenant.Repository for PostgreSQL
type Repository struct {
	db     *sql.DB
	logger *zap.Logger
}

// NewRepository creates a new PostgreSQL repository
func NewRepository(db *sql.DB, logger *zap.Logger) *Repository {
	return &Repository{
		db:     db,
		logger: logger.Named("postgres_repo"),
	}
}

// Create creates a new tenant
func (r *Repository) Create(ctx context.Context, t *tenant.Tenant) error {
	query := `
		INSERT INTO tenants (id, name, subdomain, plan_type, status, schema_name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now

	_, err := r.db.ExecContext(ctx, query,
		t.ID,
		t.Name,
		t.Subdomain,
		t.PlanType,
		t.Status,
		t.SchemaName,
		t.CreatedAt,
		t.UpdatedAt,
	)

	if err != nil {
		r.logger.Error("Failed to create tenant",
			zap.String("tenant_id", t.ID.String()),
			zap.Error(err))
		return fmt.Errorf("failed to create tenant: %w", err)
	}

	r.logger.Info("Created tenant",
		zap.String("tenant_id", t.ID.String()),
		zap.String("name", t.Name),
		zap.String("subdomain", t.Subdomain))

	return nil
}

// GetByID retrieves a tenant by ID
func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	query := `
		SELECT id, name, subdomain, plan_type, status, schema_name, created_at, updated_at
		FROM tenants
		WHERE id = $1
	`

	t := &tenant.Tenant{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&t.ID,
		&t.Name,
		&t.Subdomain,
		&t.PlanType,
		&t.Status,
		&t.SchemaName,
		&t.CreatedAt,
		&t.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("tenant not found: %w", err)
		}
		r.logger.Error("Failed to get tenant by ID",
			zap.String("tenant_id", id.String()),
			zap.Error(err))
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}

	return t, nil
}

// GetBySubdomain retrieves a tenant by subdomain
func (r *Repository) GetBySubdomain(ctx context.Context, subdomain string) (*tenant.Tenant, error) {
	query := `
		SELECT id, name, subdomain, plan_type, status, schema_name, created_at, updated_at
		FROM tenants
		WHERE subdomain = $1
	`

	t := &tenant.Tenant{}
	err := r.db.QueryRowContext(ctx, query, subdomain).Scan(
		&t.ID,
		&t.Name,
		&t.Subdomain,
		&t.PlanType,
		&t.Status,
		&t.SchemaName,
		&t.CreatedAt,
		&t.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("tenant not found: %w", err)
		}
		r.logger.Error("Failed to get tenant by subdomain",
			zap.String("subdomain", subdomain),
			zap.Error(err))
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}

	return t, nil
}

// Update updates a tenant
func (r *Repository) Update(ctx context.Context, t *tenant.Tenant) error {
	query := `
		UPDATE tenants 
		SET name = $2, subdomain = $3, plan_type = $4, status = $5, updated_at = $6
		WHERE id = $1
	`

	t.UpdatedAt = time.Now()

	result, err := r.db.ExecContext(ctx, query,
		t.ID,
		t.Name,
		t.Subdomain,
		t.PlanType,
		t.Status,
		t.UpdatedAt,
	)

	if err != nil {
		r.logger.Error("Failed to update tenant",
			zap.String("tenant_id", t.ID.String()),
			zap.Error(err))
		return fmt.Errorf("failed to update tenant: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("tenant not found")
	}

	r.logger.Info("Updated tenant",
		zap.String("tenant_id", t.ID.String()),
		zap.String("name", t.Name))

	return nil
}

// Delete soft deletes a tenant (sets status to cancelled)
func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `
		UPDATE tenants 
		SET status = $2, updated_at = $3
		WHERE id = $1
	`

	result, err := r.db.ExecContext(ctx, query, id, tenant.StatusCancelled, time.Now())
	if err != nil {
		r.logger.Error("Failed to delete tenant",
			zap.String("tenant_id", id.String()),
			zap.Error(err))
		return fmt.Errorf("failed to delete tenant: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("tenant not found")
	}

	r.logger.Info("Deleted tenant",
		zap.String("tenant_id", id.String()))

	return nil
}

// List retrieves tenants with pagination
func (r *Repository) List(ctx context.Context, page, perPage int) ([]*tenant.Tenant, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	offset := (page - 1) * perPage

	// Get total count
	var total int
	countQuery := `SELECT COUNT(*) FROM tenants WHERE status != $1`
	err := r.db.QueryRowContext(ctx, countQuery, tenant.StatusCancelled).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get tenant count: %w", err)
	}

	// Get tenants
	query := `
		SELECT id, name, subdomain, plan_type, status, schema_name, created_at, updated_at
		FROM tenants
		WHERE status != $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`

	rows, err := r.db.QueryContext(ctx, query, tenant.StatusCancelled, perPage, offset)
	if err != nil {
		r.logger.Error("Failed to list tenants", zap.Error(err))
		return nil, 0, fmt.Errorf("failed to list tenants: %w", err)
	}
	defer rows.Close()

	var tenants []*tenant.Tenant
	for rows.Next() {
		t := &tenant.Tenant{}
		err := rows.Scan(
			&t.ID,
			&t.Name,
			&t.Subdomain,
			&t.PlanType,
			&t.Status,
			&t.SchemaName,
			&t.CreatedAt,
			&t.UpdatedAt,
		)
		if err != nil {
			r.logger.Error("Failed to scan tenant", zap.Error(err))
			continue
		}
		tenants = append(tenants, t)
	}

	return tenants, total, nil
}

// GetStats retrieves usage statistics for a tenant
func (r *Repository) GetStats(ctx context.Context, tenantID uuid.UUID) (*tenant.Stats, error) {
	// First get the tenant to get schema name
	t, err := r.GetByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Initialize stats
	stats := &tenant.Stats{
		TenantID:     tenantID,
		LastActivity: time.Now(),
		SchemaExists: true, // Assume exists for now - could be checked
	}

	// Get project count from tenant schema
	projectCountQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s.projects`, t.SchemaName)
	err = r.db.QueryRowContext(ctx, projectCountQuery).Scan(&stats.ProjectCount)
	if err != nil {
		// Schema might not exist or no projects table
		stats.ProjectCount = 0
		stats.SchemaExists = false
	}

	// Get user count from tenant schema
	userCountQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s.tenant_users WHERE is_active = true`, t.SchemaName)
	err = r.db.QueryRowContext(ctx, userCountQuery).Scan(&stats.UserCount)
	if err != nil {
		// Schema might not exist or no users table
		stats.UserCount = 0
	}

	// Storage calculation would be more complex in practice
	stats.StorageUsedGB = 0.0

	return stats, nil
}

// CreateMasterTables creates the master tables needed for tenant management
func (r *Repository) CreateMasterTables(ctx context.Context) error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS tenants (
			id UUID PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			subdomain VARCHAR(255) UNIQUE NOT NULL,
			plan_type VARCHAR(50) NOT NULL DEFAULT 'basic',
			status VARCHAR(50) NOT NULL DEFAULT 'pending',
			schema_name VARCHAR(255) NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			CONSTRAINT chk_plan_type CHECK (plan_type IN ('basic', 'pro', 'enterprise')),
			CONSTRAINT chk_status CHECK (status IN ('active', 'suspended', 'pending', 'cancelled'))
		)`,

		`CREATE TABLE IF NOT EXISTS tenant_migrations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL,
			version VARCHAR(50) NOT NULL,
			name VARCHAR(255) NOT NULL,
			applied_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			rollback_sql TEXT,
			checksum VARCHAR(64),
			FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE,
			UNIQUE(tenant_id, version)
		)`,
	}

	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_tenants_subdomain ON tenants(subdomain)",
		"CREATE INDEX IF NOT EXISTS idx_tenants_status ON tenants(status)",
		"CREATE INDEX IF NOT EXISTS idx_tenant_migrations_tenant_id ON tenant_migrations(tenant_id)",
		"CREATE INDEX IF NOT EXISTS idx_tenant_migrations_version ON tenant_migrations(version)",
	}

	// Create tables
	for _, tableSQL := range tables {
		if _, err := r.db.ExecContext(ctx, tableSQL); err != nil {
			return fmt.Errorf("failed to create master table: %w", err)
		}
	}

	// Create indexes
	for _, indexSQL := range indexes {
		if _, err := r.db.ExecContext(ctx, indexSQL); err != nil {
			return fmt.Errorf("failed to create master index: %w", err)
		}
	}

	r.logger.Info("Created master tables")
	return nil
}

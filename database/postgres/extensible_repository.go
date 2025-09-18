package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ExtensibleRepository implements tenant.ExtensibleRepository for PostgreSQL
type ExtensibleRepository struct {
	*Repository // Embed the base repository
}

// NewExtensibleRepository creates a new extensible PostgreSQL repository
func NewExtensibleRepository(db *sql.DB, logger *zap.Logger) *ExtensibleRepository {
	return &ExtensibleRepository{
		Repository: NewRepository(db, logger),
	}
}

// CreateExtended creates a new tenant with metadata
func (r *ExtensibleRepository) CreateExtended(ctx context.Context, t *tenant.ExtensibleTenant) error {
	query := `
		INSERT INTO tenants (id, name, subdomain, plan_type, status, schema_name, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now

	// Initialize metadata if nil
	if t.Metadata == nil {
		t.Metadata = make(tenant.TenantMetadata)
	}

	_, err := r.db.ExecContext(ctx, query,
		t.ID,
		t.Name,
		t.Subdomain,
		t.PlanType,
		t.Status,
		t.SchemaName,
		t.Metadata,
		t.CreatedAt,
		t.UpdatedAt,
	)

	if err != nil {
		r.logger.Error("Failed to create extended tenant",
			zap.String("tenant_id", t.ID.String()),
			zap.Error(err))
		return fmt.Errorf("failed to create extended tenant: %w", err)
	}

	r.logger.Info("Created extended tenant",
		zap.String("tenant_id", t.ID.String()),
		zap.String("name", t.Name),
		zap.String("subdomain", t.Subdomain),
		zap.Int("metadata_fields", len(t.Metadata)))

	return nil
}

// GetExtendedByID retrieves an extended tenant by ID
func (r *ExtensibleRepository) GetExtendedByID(ctx context.Context, id uuid.UUID) (*tenant.ExtensibleTenant, error) {
	query := `
		SELECT id, name, subdomain, plan_type, status, schema_name, 
		       COALESCE(metadata, '{}') as metadata, created_at, updated_at
		FROM tenants
		WHERE id = $1
	`

	t := &tenant.ExtensibleTenant{}
	t.Metadata = make(tenant.TenantMetadata)

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&t.ID,
		&t.Name,
		&t.Subdomain,
		&t.PlanType,
		&t.Status,
		&t.SchemaName,
		&t.Metadata,
		&t.CreatedAt,
		&t.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("tenant not found: %w", err)
		}
		r.logger.Error("Failed to get extended tenant by ID",
			zap.String("tenant_id", id.String()),
			zap.Error(err))
		return nil, fmt.Errorf("failed to get extended tenant: %w", err)
	}

	return t, nil
}

// GetExtendedBySubdomain retrieves an extended tenant by subdomain
func (r *ExtensibleRepository) GetExtendedBySubdomain(ctx context.Context, subdomain string) (*tenant.ExtensibleTenant, error) {
	query := `
		SELECT id, name, subdomain, plan_type, status, schema_name, 
		       COALESCE(metadata, '{}') as metadata, created_at, updated_at
		FROM tenants
		WHERE subdomain = $1
	`

	t := &tenant.ExtensibleTenant{}
	t.Metadata = make(tenant.TenantMetadata)

	err := r.db.QueryRowContext(ctx, query, subdomain).Scan(
		&t.ID,
		&t.Name,
		&t.Subdomain,
		&t.PlanType,
		&t.Status,
		&t.SchemaName,
		&t.Metadata,
		&t.CreatedAt,
		&t.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("tenant not found: %w", err)
		}
		r.logger.Error("Failed to get extended tenant by subdomain",
			zap.String("subdomain", subdomain),
			zap.Error(err))
		return nil, fmt.Errorf("failed to get extended tenant: %w", err)
	}

	return t, nil
}

// UpdateExtended updates an extended tenant
func (r *ExtensibleRepository) UpdateExtended(ctx context.Context, t *tenant.ExtensibleTenant) error {
	query := `
		UPDATE tenants 
		SET name = $2, subdomain = $3, plan_type = $4, status = $5, metadata = $6, updated_at = $7
		WHERE id = $1
	`

	t.UpdatedAt = time.Now()

	// Ensure metadata is not nil
	if t.Metadata == nil {
		t.Metadata = make(tenant.TenantMetadata)
	}

	result, err := r.db.ExecContext(ctx, query,
		t.ID,
		t.Name,
		t.Subdomain,
		t.PlanType,
		t.Status,
		t.Metadata,
		t.UpdatedAt,
	)

	if err != nil {
		r.logger.Error("Failed to update extended tenant",
			zap.String("tenant_id", t.ID.String()),
			zap.Error(err))
		return fmt.Errorf("failed to update extended tenant: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("tenant not found")
	}

	r.logger.Info("Updated extended tenant",
		zap.String("tenant_id", t.ID.String()),
		zap.String("name", t.Name),
		zap.Int("metadata_fields", len(t.Metadata)))

	return nil
}

// ListExtended retrieves extended tenants with pagination
func (r *ExtensibleRepository) ListExtended(ctx context.Context, page, perPage int) ([]*tenant.ExtensibleTenant, int, error) {
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
		SELECT id, name, subdomain, plan_type, status, schema_name, 
		       COALESCE(metadata, '{}') as metadata, created_at, updated_at
		FROM tenants
		WHERE status != $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`

	rows, err := r.db.QueryContext(ctx, query, tenant.StatusCancelled, perPage, offset)
	if err != nil {
		r.logger.Error("Failed to list extended tenants", zap.Error(err))
		return nil, 0, fmt.Errorf("failed to list extended tenants: %w", err)
	}
	defer rows.Close()

	var tenants []*tenant.ExtensibleTenant
	for rows.Next() {
		t := &tenant.ExtensibleTenant{}
		t.Metadata = make(tenant.TenantMetadata)

		err := rows.Scan(
			&t.ID,
			&t.Name,
			&t.Subdomain,
			&t.PlanType,
			&t.Status,
			&t.SchemaName,
			&t.Metadata,
			&t.CreatedAt,
			&t.UpdatedAt,
		)
		if err != nil {
			r.logger.Error("Failed to scan extended tenant", zap.Error(err))
			continue
		}
		tenants = append(tenants, t)
	}

	return tenants, total, nil
}

// UpdateMetadata updates only the metadata field for a tenant
func (r *ExtensibleRepository) UpdateMetadata(ctx context.Context, tenantID uuid.UUID, metadata tenant.TenantMetadata) error {
	query := `
		UPDATE tenants 
		SET metadata = $2, updated_at = $3
		WHERE id = $1
	`

	result, err := r.db.ExecContext(ctx, query, tenantID, metadata, time.Now())
	if err != nil {
		r.logger.Error("Failed to update tenant metadata",
			zap.String("tenant_id", tenantID.String()),
			zap.Error(err))
		return fmt.Errorf("failed to update tenant metadata: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("tenant not found")
	}

	return nil
}

// GetMetadata retrieves only the metadata for a tenant
func (r *ExtensibleRepository) GetMetadata(ctx context.Context, tenantID uuid.UUID) (tenant.TenantMetadata, error) {
	query := `SELECT COALESCE(metadata, '{}') FROM tenants WHERE id = $1`

	metadata := make(tenant.TenantMetadata)
	err := r.db.QueryRowContext(ctx, query, tenantID).Scan(&metadata)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("tenant not found: %w", err)
		}
		return nil, fmt.Errorf("failed to get tenant metadata: %w", err)
	}

	return metadata, nil
}

// UpdateMetadataField updates a single metadata field
func (r *ExtensibleRepository) UpdateMetadataField(ctx context.Context, tenantID uuid.UUID, key string, value interface{}) error {
	query := `
		UPDATE tenants 
		SET metadata = COALESCE(metadata, '{}') || jsonb_build_object($2, $3),
		    updated_at = $4
		WHERE id = $1
	`

	result, err := r.db.ExecContext(ctx, query, tenantID, key, value, time.Now())
	if err != nil {
		r.logger.Error("Failed to update tenant metadata field",
			zap.String("tenant_id", tenantID.String()),
			zap.String("key", key),
			zap.Error(err))
		return fmt.Errorf("failed to update tenant metadata field: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("tenant not found")
	}

	return nil
}

// RemoveMetadataField removes a single metadata field
func (r *ExtensibleRepository) RemoveMetadataField(ctx context.Context, tenantID uuid.UUID, key string) error {
	query := `
		UPDATE tenants 
		SET metadata = COALESCE(metadata, '{}') - $2,
		    updated_at = $3
		WHERE id = $1
	`

	result, err := r.db.ExecContext(ctx, query, tenantID, key, time.Now())
	if err != nil {
		r.logger.Error("Failed to remove tenant metadata field",
			zap.String("tenant_id", tenantID.String()),
			zap.String("key", key),
			zap.Error(err))
		return fmt.Errorf("failed to remove tenant metadata field: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("tenant not found")
	}

	return nil
}

// FindByMetadata finds tenants by a specific metadata key-value pair
func (r *ExtensibleRepository) FindByMetadata(ctx context.Context, key string, value interface{}) ([]*tenant.ExtensibleTenant, error) {
	query := `
		SELECT id, name, subdomain, plan_type, status, schema_name, 
		       COALESCE(metadata, '{}') as metadata, created_at, updated_at
		FROM tenants
		WHERE metadata ->> $1 = $2
		AND status != $3
		ORDER BY created_at DESC
	`

	rows, err := r.db.QueryContext(ctx, query, key, fmt.Sprintf("%v", value), tenant.StatusCancelled)
	if err != nil {
		r.logger.Error("Failed to find tenants by metadata",
			zap.String("key", key),
			zap.Any("value", value),
			zap.Error(err))
		return nil, fmt.Errorf("failed to find tenants by metadata: %w", err)
	}
	defer rows.Close()

	var tenants []*tenant.ExtensibleTenant
	for rows.Next() {
		t := &tenant.ExtensibleTenant{}
		t.Metadata = make(tenant.TenantMetadata)

		err := rows.Scan(
			&t.ID,
			&t.Name,
			&t.Subdomain,
			&t.PlanType,
			&t.Status,
			&t.SchemaName,
			&t.Metadata,
			&t.CreatedAt,
			&t.UpdatedAt,
		)
		if err != nil {
			r.logger.Error("Failed to scan tenant", zap.Error(err))
			continue
		}
		tenants = append(tenants, t)
	}

	return tenants, nil
}

// FindByMetadataKeys finds tenants that have any of the specified metadata keys
func (r *ExtensibleRepository) FindByMetadataKeys(ctx context.Context, keys []string) ([]*tenant.ExtensibleTenant, error) {
	if len(keys) == 0 {
		return []*tenant.ExtensibleTenant{}, nil
	}

	// Build the query dynamically based on the number of keys
	query := `
		SELECT id, name, subdomain, plan_type, status, schema_name, 
		       COALESCE(metadata, '{}') as metadata, created_at, updated_at
		FROM tenants
		WHERE status != $1 AND (
	`

	args := []interface{}{tenant.StatusCancelled}
	for i, key := range keys {
		if i > 0 {
			query += " OR "
		}
		query += fmt.Sprintf("metadata ? $%d", len(args)+1)
		args = append(args, key)
	}
	query += ") ORDER BY created_at DESC"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		r.logger.Error("Failed to find tenants by metadata keys",
			zap.Strings("keys", keys),
			zap.Error(err))
		return nil, fmt.Errorf("failed to find tenants by metadata keys: %w", err)
	}
	defer rows.Close()

	var tenants []*tenant.ExtensibleTenant
	for rows.Next() {
		t := &tenant.ExtensibleTenant{}
		t.Metadata = make(tenant.TenantMetadata)

		err := rows.Scan(
			&t.ID,
			&t.Name,
			&t.Subdomain,
			&t.PlanType,
			&t.Status,
			&t.SchemaName,
			&t.Metadata,
			&t.CreatedAt,
			&t.UpdatedAt,
		)
		if err != nil {
			r.logger.Error("Failed to scan tenant", zap.Error(err))
			continue
		}
		tenants = append(tenants, t)
	}

	return tenants, nil
}

// CreateMasterTablesExtended creates the master tables with metadata support
func (r *ExtensibleRepository) CreateMasterTablesExtended(ctx context.Context) error {
	// First create the base tables
	if err := r.Repository.CreateMasterTables(ctx); err != nil {
		return err
	}

	// Add metadata column if it doesn't exist
	alterQuery := `
		ALTER TABLE tenants 
		ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT '{}'
	`

	if _, err := r.db.ExecContext(ctx, alterQuery); err != nil {
		return fmt.Errorf("failed to add metadata column: %w", err)
	}

	// Create indexes for metadata queries
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_tenants_metadata_gin ON tenants USING GIN (metadata)",
		"CREATE INDEX IF NOT EXISTS idx_tenants_metadata_stripe_customer ON tenants USING BTREE ((metadata->>'stripe_customer_id')) WHERE metadata ? 'stripe_customer_id'",
	}

	for _, indexSQL := range indexes {
		if _, err := r.db.ExecContext(ctx, indexSQL); err != nil {
			r.logger.Warn("Failed to create metadata index", zap.String("sql", indexSQL), zap.Error(err))
			// Don't fail on index creation errors
		}
	}

	r.logger.Info("Created master tables with metadata support")
	return nil
}

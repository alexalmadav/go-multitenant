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
		INSERT INTO public.tenants (id, name, subdomain, status, schema_name, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now

	_, err := r.db.ExecContext(ctx, query,
		t.ID,
		t.Name,
		t.Subdomain,
		t.Status,
		t.SchemaName,
		t.Metadata,
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
		SELECT id, name, subdomain, status, schema_name, metadata, created_at, updated_at
		FROM public.tenants
		WHERE id = $1
	`

	t := &tenant.Tenant{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&t.ID,
		&t.Name,
		&t.Subdomain,
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
		SELECT id, name, subdomain, status, schema_name, metadata, created_at, updated_at
		FROM public.tenants
		WHERE subdomain = $1
	`

	t := &tenant.Tenant{}
	err := r.db.QueryRowContext(ctx, query, subdomain).Scan(
		&t.ID,
		&t.Name,
		&t.Subdomain,
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
		UPDATE public.tenants
		SET name = $2, subdomain = $3, status = $4, metadata = $5, updated_at = $6
		WHERE id = $1
	`

	t.UpdatedAt = time.Now()

	result, err := r.db.ExecContext(ctx, query,
		t.ID,
		t.Name,
		t.Subdomain,
		t.Status,
		t.Metadata,
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
		UPDATE public.tenants 
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
	countQuery := `SELECT COUNT(*) FROM public.tenants WHERE status != $1`
	err := r.db.QueryRowContext(ctx, countQuery, tenant.StatusCancelled).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get tenant count: %w", err)
	}

	// Get tenants
	query := `
		SELECT id, name, subdomain, status, schema_name, metadata, created_at, updated_at
		FROM public.tenants
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

	tenants, err := scanTenants(rows)
	if err != nil {
		r.logger.Error("Failed to scan tenants", zap.Error(err))
		return nil, 0, fmt.Errorf("failed to list tenants: %w", err)
	}

	return tenants, total, nil
}

// FindByMetadata returns tenants whose metadata[key] equals value.
func (r *Repository) FindByMetadata(ctx context.Context, key, value string) ([]*tenant.Tenant, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, subdomain, status, schema_name, metadata, created_at, updated_at
		FROM public.tenants
		WHERE metadata ->> $1 = $2
		ORDER BY created_at`, key, value)
	if err != nil {
		return nil, fmt.Errorf("failed to query tenants by metadata: %w", err)
	}
	defer rows.Close()
	return scanTenants(rows)
}

// scanTenants reads every row of a tenants SELECT with the standard column order.
func scanTenants(rows *sql.Rows) ([]*tenant.Tenant, error) {
	var tenants []*tenant.Tenant
	for rows.Next() {
		t := &tenant.Tenant{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Subdomain, &t.Status, &t.SchemaName, &t.Metadata, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	return tenants, rows.Err()
}

// CreateMasterTables creates the master tables needed for tenant management
func (r *Repository) CreateMasterTables(ctx context.Context) error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS public.tenants (
			id UUID PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			subdomain VARCHAR(255) UNIQUE NOT NULL,
			status VARCHAR(50) NOT NULL DEFAULT 'pending',
			schema_name VARCHAR(255) NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}',
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			CONSTRAINT chk_status CHECK (status IN ('active', 'suspended', 'pending', 'cancelled'))
		)`,

		`CREATE TABLE IF NOT EXISTS public.tenant_migrations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL,
			version VARCHAR(50) NOT NULL,
			name VARCHAR(255) NOT NULL,
			applied_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			rollback_sql TEXT,
			checksum VARCHAR(64),
			FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE,
			UNIQUE(tenant_id, version)
		)`,

		`ALTER TABLE public.tenants ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'`,

		// v0.7 -> v0.8: move plan_type into metadata["plan"] and neutralise
		// the column, once. Runs only if the legacy column exists.
		//
		// The legacy column is NOT NULL DEFAULT 'basic', and v0.8 never
		// writes it, so a guard keyed only on "metadata has no plan key"
		// would match every row created after the upgrade too (its
		// plan_type is always 'basic' from the default) and re-copy
		// "basic" into metadata on every restart, undoing SetPlan("") and
		// stamping plan-less tenants with a plan they never asked for.
		// Instead, after copying, the column's DEFAULT and NOT NULL are
		// dropped and every previously non-NULL value is set to NULL, so
		// "plan_type IS NOT NULL" becomes false for every row from then on
		// (post-upgrade inserts get a NULL plan_type naturally, since there
		// is no default any more) and this block is a no-op on later
		// starts. The column itself is left for the operator to drop.
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = 'plan_type'
			) THEN
				IF EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = 'plan_type'
					  AND (column_default IS NOT NULL OR is_nullable = 'NO')
				) THEN
					ALTER TABLE public.tenants ALTER COLUMN plan_type DROP DEFAULT,
					                           ALTER COLUMN plan_type DROP NOT NULL;
				END IF;
				UPDATE public.tenants
				SET metadata = CASE WHEN metadata ? 'plan' THEN metadata
				                     ELSE metadata || jsonb_build_object('plan', plan_type) END,
				    plan_type = NULL
				WHERE plan_type IS NOT NULL;
			END IF;
		END $$`,
	}

	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_tenants_subdomain ON public.tenants(subdomain)",
		"CREATE INDEX IF NOT EXISTS idx_tenants_status ON public.tenants(status)",
		"CREATE INDEX IF NOT EXISTS idx_tenant_migrations_tenant_id ON public.tenant_migrations(tenant_id)",
		"CREATE INDEX IF NOT EXISTS idx_tenant_migrations_version ON public.tenant_migrations(version)",
		"CREATE INDEX IF NOT EXISTS idx_tenants_metadata ON public.tenants USING GIN (metadata)",
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

package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// MigrationManager implements tenant.MigrationManager using PostgreSQL functions
type MigrationManager struct {
	db            *sql.DB
	logger        *zap.Logger
	migrationsDir string
}

// NewMigrationManager creates a new migration manager
func NewMigrationManager(db *sql.DB, logger *zap.Logger, migrationsDir string) tenant.MigrationManager {
	return &MigrationManager{
		db:            db,
		logger:        logger.Named("migration_manager"),
		migrationsDir: migrationsDir,
	}
}

// ApplyMigration applies a migration to a specific tenant using PostgreSQL functions
func (m *MigrationManager) ApplyMigration(ctx context.Context, tenantID uuid.UUID, migration *tenant.Migration) error {
	m.logger.Info("Applying migration to tenant",
		zap.String("tenant_id", tenantID.String()),
		zap.String("migration_version", migration.Version),
		zap.String("migration_name", migration.Name))

	// Validate tenant schema exists
	if !m.validateTenantSchema(ctx, tenantID) {
		return fmt.Errorf("tenant schema does not exist for tenant %s", tenantID.String())
	}

	// Check if migration is already applied
	applied, err := m.IsMigrationApplied(ctx, tenantID, migration.Version)
	if err != nil {
		return fmt.Errorf("failed to check if migration is applied: %w", err)
	}
	if applied {
		m.logger.Info("Migration already applied, skipping",
			zap.String("tenant_id", tenantID.String()),
			zap.String("migration_version", migration.Version))
		return nil
	}

	// Use the PostgreSQL function apply_tenant_migration
	query := `SELECT apply_tenant_migration($1, $2, $3, $4, $5)`

	var rollbackSQL sql.NullString
	if migration.RollbackSQL != nil {
		rollbackSQL.String = *migration.RollbackSQL
		rollbackSQL.Valid = true
	}

	_, err = m.db.ExecContext(ctx, query,
		tenantID,
		migration.Version,
		migration.Name,
		migration.SQL,
		rollbackSQL,
	)

	if err != nil {
		m.logger.Error("Migration failed",
			zap.String("tenant_id", tenantID.String()),
			zap.String("migration_version", migration.Version),
			zap.Error(err))
		return fmt.Errorf("migration failed: %w", err)
	}

	m.logger.Info("Migration applied successfully",
		zap.String("tenant_id", tenantID.String()),
		zap.String("migration_version", migration.Version))

	return nil
}

// ApplyToAllTenants applies migration to all active tenants using PostgreSQL function
func (m *MigrationManager) ApplyToAllTenants(ctx context.Context, migration *tenant.Migration) error {
	m.logger.Info("Applying migration to all tenants",
		zap.String("migration_version", migration.Version),
		zap.String("migration_name", migration.Name))

	// Use the PostgreSQL function apply_migration_to_all_tenants
	query := `SELECT apply_migration_to_all_tenants($1, $2, $3, $4)`

	var rollbackSQL sql.NullString
	if migration.RollbackSQL != nil {
		rollbackSQL.String = *migration.RollbackSQL
		rollbackSQL.Valid = true
	}

	_, err := m.db.ExecContext(ctx, query,
		migration.Version,
		migration.Name,
		migration.SQL,
		rollbackSQL,
	)

	if err != nil {
		m.logger.Error("Bulk migration failed",
			zap.String("migration_version", migration.Version),
			zap.Error(err))
		return fmt.Errorf("bulk migration failed: %w", err)
	}

	m.logger.Info("Migration applied to all tenants successfully",
		zap.String("migration_version", migration.Version))

	return nil
}

// RollbackMigration rolls back a migration for a specific tenant
func (m *MigrationManager) RollbackMigration(ctx context.Context, tenantID uuid.UUID, version string) error {
	m.logger.Info("Rolling back migration",
		zap.String("tenant_id", tenantID.String()),
		zap.String("version", version))

	// Check if migration is applied
	applied, err := m.IsMigrationApplied(ctx, tenantID, version)
	if err != nil {
		return fmt.Errorf("failed to check if migration is applied: %w", err)
	}
	if !applied {
		m.logger.Info("Migration not applied, nothing to rollback",
			zap.String("tenant_id", tenantID.String()),
			zap.String("version", version))
		return nil
	}

	// Use the PostgreSQL function rollback_tenant_migration
	query := `SELECT rollback_tenant_migration($1, $2)`

	_, err = m.db.ExecContext(ctx, query, tenantID, version)
	if err != nil {
		m.logger.Error("Rollback failed",
			zap.String("tenant_id", tenantID.String()),
			zap.String("version", version),
			zap.Error(err))
		return fmt.Errorf("rollback failed: %w", err)
	}

	m.logger.Info("Migration rolled back successfully",
		zap.String("tenant_id", tenantID.String()),
		zap.String("version", version))

	return nil
}

// GetAppliedMigrations returns all applied migrations for a tenant
func (m *MigrationManager) GetAppliedMigrations(ctx context.Context, tenantID uuid.UUID) ([]*tenant.Migration, error) {
	query := `
		SELECT migration_id, migration_version, migration_name, applied_at, checksum
		FROM get_tenant_applied_migrations($1)
	`

	rows, err := m.db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	var migrations []*tenant.Migration
	for rows.Next() {
		migration := &tenant.Migration{
			TenantID: tenantID,
		}
		var appliedAt time.Time
		var checksum sql.NullString

		if err := rows.Scan(
			&migration.ID,
			&migration.Version,
			&migration.Name,
			&appliedAt,
			&checksum,
		); err != nil {
			return nil, fmt.Errorf("failed to scan migration: %w", err)
		}

		migration.AppliedAt = appliedAt
		if checksum.Valid {
			migration.Checksum = &checksum.String
		}

		migrations = append(migrations, migration)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating migration rows: %w", err)
	}

	return migrations, nil
}

// IsMigrationApplied checks if a migration is applied to a tenant
func (m *MigrationManager) IsMigrationApplied(ctx context.Context, tenantID uuid.UUID, version string) (bool, error) {
	query := `SELECT is_tenant_migration_applied($1, $2)`

	var applied bool
	err := m.db.QueryRowContext(ctx, query, tenantID, version).Scan(&applied)
	if err != nil {
		return false, fmt.Errorf("failed to check if migration is applied: %w", err)
	}

	return applied, nil
}

// LoadMigrationFromFile loads a migration from the filesystem
func (m *MigrationManager) LoadMigrationFromFile(version, name string) (*tenant.Migration, error) {
	// Construct file names
	upFile := filepath.Join(m.migrationsDir, fmt.Sprintf("%s_%s.up.sql", version, name))
	downFile := filepath.Join(m.migrationsDir, fmt.Sprintf("%s_%s.down.sql", version, name))

	// Read up migration
	upSQL, err := os.ReadFile(upFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read up migration file %s: %w", upFile, err)
	}

	migration := &tenant.Migration{
		ID:      uuid.New(),
		Version: version,
		Name:    name,
		SQL:     string(upSQL),
	}

	// Calculate checksum
	checksum := fmt.Sprintf("%x", sha256.Sum256(upSQL))
	migration.Checksum = &checksum

	// Read down migration if it exists
	if downSQL, err := os.ReadFile(downFile); err == nil {
		rollbackSQL := string(downSQL)
		migration.RollbackSQL = &rollbackSQL
	} else {
		m.logger.Debug("No rollback SQL file found",
			zap.String("file", downFile),
			zap.String("version", version))
	}

	return migration, nil
}

// ApplyMigrationFromFile loads and applies a migration from file to a specific tenant
func (m *MigrationManager) ApplyMigrationFromFile(ctx context.Context, tenantID uuid.UUID, version, name string) error {
	migration, err := m.LoadMigrationFromFile(version, name)
	if err != nil {
		return err
	}

	migration.TenantID = tenantID
	return m.ApplyMigration(ctx, tenantID, migration)
}

// ApplyMigrationToAllTenantsFromFile loads and applies migration to all tenants
func (m *MigrationManager) ApplyMigrationToAllTenantsFromFile(ctx context.Context, version, name string) error {
	migration, err := m.LoadMigrationFromFile(version, name)
	if err != nil {
		return err
	}

	return m.ApplyToAllTenants(ctx, migration)
}

// ListMigrationFiles returns all available migration files
func (m *MigrationManager) ListMigrationFiles() ([]string, error) {
	if m.migrationsDir == "" {
		return nil, fmt.Errorf("migrations directory not configured")
	}

	files, err := os.ReadDir(m.migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var migrations []string
	seen := make(map[string]bool)

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		name := file.Name()
		if strings.HasSuffix(name, ".up.sql") {
			// Extract version_name from filename
			baseName := strings.TrimSuffix(name, ".up.sql")
			if !seen[baseName] {
				migrations = append(migrations, baseName)
				seen[baseName] = true
			}
		}
	}

	return migrations, nil
}

// validateTenantSchema checks if tenant schema exists
func (m *MigrationManager) validateTenantSchema(ctx context.Context, tenantID uuid.UUID) bool {
	query := `SELECT validate_tenant_schema($1)`

	var exists bool
	err := m.db.QueryRowContext(ctx, query, tenantID).Scan(&exists)
	if err != nil {
		m.logger.Error("Failed to validate tenant schema",
			zap.String("tenant_id", tenantID.String()),
			zap.Error(err))
		return false
	}

	return exists
}

package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// MigrationManager implements tenant.MigrationManager in pure Go.
//
// Each migration runs inside a transaction whose search_path is scoped to the
// tenant schema, and is recorded in public.tenant_migrations in the same
// transaction, so a failed migration leaves no record behind.
type MigrationManager struct {
	db            *sql.DB
	logger        *zap.Logger
	migrationsDir string
	schemaManager tenant.SchemaManager
	repository    tenant.Repository
}

// NewMigrationManager creates a new migration manager.
func NewMigrationManager(db *sql.DB, logger *zap.Logger, migrationsDir string, schemaManager tenant.SchemaManager, repository tenant.Repository) tenant.MigrationManager {
	return &MigrationManager{
		db:            db,
		logger:        logger.Named("migration_manager"),
		migrationsDir: migrationsDir,
		schemaManager: schemaManager,
		repository:    repository,
	}
}

// ApplyMigration applies a migration to a specific tenant.
func (m *MigrationManager) ApplyMigration(ctx context.Context, tenantID uuid.UUID, migration *tenant.Migration) error {
	log := m.logger.With(
		zap.String("tenant_id", tenantID.String()),
		zap.String("migration_version", migration.Version),
		zap.String("migration_name", migration.Name))

	exists, err := m.schemaManager.SchemaExists(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("failed to check tenant schema: %w", err)
	}
	if !exists {
		return fmt.Errorf("tenant schema does not exist for tenant %s", tenantID)
	}

	applied, err := m.IsMigrationApplied(ctx, tenantID, migration.Version)
	if err != nil {
		return err
	}
	if applied {
		log.Info("Migration already applied, skipping")
		return nil
	}

	checksum := migration.Checksum
	if checksum == nil {
		sum := fmt.Sprintf("%x", sha256.Sum256([]byte(migration.SQL)))
		checksum = &sum
	}

	err = m.withTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			return fmt.Errorf("migration SQL failed: %w", err)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO public.tenant_migrations (id, tenant_id, version, name, rollback_sql, checksum, applied_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			uuid.New(), tenantID, migration.Version, migration.Name, migration.RollbackSQL, checksum, time.Now())
		if err != nil {
			return fmt.Errorf("failed to record migration: %w", err)
		}
		return nil
	})
	if err != nil {
		log.Error("Migration failed", zap.Error(err))
		return fmt.Errorf("migration %s failed for tenant %s: %w", migration.Version, tenantID, err)
	}

	log.Info("Migration applied successfully")
	return nil
}

// ApplyToAllTenants applies a migration to every active tenant. Failures are
// collected and returned together after all tenants have been attempted.
func (m *MigrationManager) ApplyToAllTenants(ctx context.Context, migration *tenant.Migration) error {
	m.logger.Info("Applying migration to all active tenants",
		zap.String("migration_version", migration.Version),
		zap.String("migration_name", migration.Name))
	return m.forEachActiveTenant(ctx, "migration "+migration.Version, func(t *tenant.Tenant) error {
		return m.ApplyMigration(ctx, t.ID, migration)
	})
}

// RollbackMigration runs the stored rollback SQL for a migration and removes its record.
func (m *MigrationManager) RollbackMigration(ctx context.Context, tenantID uuid.UUID, version string) error {
	log := m.logger.With(
		zap.String("tenant_id", tenantID.String()),
		zap.String("version", version))

	var rollbackSQL sql.NullString
	err := m.db.QueryRowContext(ctx,
		`SELECT rollback_sql FROM public.tenant_migrations WHERE tenant_id = $1 AND version = $2`,
		tenantID, version).Scan(&rollbackSQL)
	if errors.Is(err, sql.ErrNoRows) {
		log.Info("Migration not applied, nothing to roll back")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to look up migration: %w", err)
	}
	if !rollbackSQL.Valid || rollbackSQL.String == "" {
		return fmt.Errorf("no rollback SQL recorded for migration %s on tenant %s", version, tenantID)
	}

	err = m.withTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, rollbackSQL.String); err != nil {
			return fmt.Errorf("rollback SQL failed: %w", err)
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM public.tenant_migrations WHERE tenant_id = $1 AND version = $2`,
			tenantID, version)
		if err != nil {
			return fmt.Errorf("failed to remove migration record: %w", err)
		}
		return nil
	})
	if err != nil {
		log.Error("Rollback failed", zap.Error(err))
		return fmt.Errorf("rollback of %s failed for tenant %s: %w", version, tenantID, err)
	}

	log.Info("Migration rolled back successfully")
	return nil
}

// GetAppliedMigrations returns all applied migrations for a tenant in application order.
func (m *MigrationManager) GetAppliedMigrations(ctx context.Context, tenantID uuid.UUID) ([]*tenant.Migration, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, version, name, rollback_sql, checksum, applied_at
		FROM public.tenant_migrations
		WHERE tenant_id = $1
		ORDER BY applied_at, version`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	var migrations []*tenant.Migration
	for rows.Next() {
		mig := &tenant.Migration{TenantID: tenantID}
		var rollbackSQL, checksum sql.NullString
		if err := rows.Scan(&mig.ID, &mig.Version, &mig.Name, &rollbackSQL, &checksum, &mig.AppliedAt); err != nil {
			return nil, fmt.Errorf("failed to scan migration: %w", err)
		}
		if rollbackSQL.Valid {
			mig.RollbackSQL = &rollbackSQL.String
		}
		if checksum.Valid {
			mig.Checksum = &checksum.String
		}
		migrations = append(migrations, mig)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating migration rows: %w", err)
	}
	return migrations, nil
}

// IsMigrationApplied reports whether a migration version has been applied to a tenant.
func (m *MigrationManager) IsMigrationApplied(ctx context.Context, tenantID uuid.UUID, version string) (bool, error) {
	var applied bool
	err := m.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM public.tenant_migrations WHERE tenant_id = $1 AND version = $2)`,
		tenantID, version).Scan(&applied)
	if err != nil {
		return false, fmt.Errorf("failed to check if migration is applied: %w", err)
	}
	return applied, nil
}

// withTenantTx runs fn in a transaction whose search_path is the tenant schema.
func (m *MigrationManager) withTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(tx *sql.Tx) error) error {
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	schema := m.schemaManager.GetSchemaName(tenantID)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`SET LOCAL search_path TO "%s"`, schema)); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to set search path: %w", err)
	}

	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// LoadMigrationFromFile loads a migration from the filesystem
func (m *MigrationManager) LoadMigrationFromFile(version, name string) (*tenant.Migration, error) {
	upFile := filepath.Join(m.migrationsDir, fmt.Sprintf("%s_%s.up.sql", version, name))
	downFile := filepath.Join(m.migrationsDir, fmt.Sprintf("%s_%s.down.sql", version, name))

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

	checksum := fmt.Sprintf("%x", sha256.Sum256(upSQL))
	migration.Checksum = &checksum

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

// migrationFile is one parsed <version>_<name>.up.sql entry.
type migrationFile struct {
	Version string
	Name    string
}

func (f migrationFile) base() string { return f.Version + "_" + f.Name }

// migrationFiles returns the migrations in migrationsDir sorted by filename.
// An empty migrationsDir yields no files and no error.
func (m *MigrationManager) migrationFiles() ([]migrationFile, error) {
	if m.migrationsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(m.migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var files []migrationFile
	ups := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".up.sql")
		version, name, ok := strings.Cut(base, "_")
		if !ok || version == "" || name == "" {
			return nil, fmt.Errorf("migration file %q must be named <version>_<name>.up.sql", e.Name())
		}
		files = append(files, migrationFile{Version: version, Name: name})
		ups[base] = true
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		if base := strings.TrimSuffix(e.Name(), ".down.sql"); !ups[base] {
			return nil, fmt.Errorf("rollback file %q has no matching .up.sql", e.Name())
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].base() < files[j].base() })
	return files, nil
}

// ListMigrationFiles returns "<version>_<name>" for every migration file, sorted by filename.
func (m *MigrationManager) ListMigrationFiles() ([]string, error) {
	if m.migrationsDir == "" {
		return nil, fmt.Errorf("migrations directory not configured")
	}
	files, err := m.migrationFiles()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.base())
	}
	return out, nil
}

// ApplyPending applies every migration file not yet recorded for the tenant, in order.
func (m *MigrationManager) ApplyPending(ctx context.Context, tenantID uuid.UUID) error {
	files, err := m.migrationFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := m.ApplyMigrationFromFile(ctx, tenantID, f.Version, f.Name); err != nil {
			return err
		}
	}
	return nil
}

// ApplyPendingToAllTenants runs ApplyPending for every active tenant and reports all failures.
func (m *MigrationManager) ApplyPendingToAllTenants(ctx context.Context) error {
	return m.forEachActiveTenant(ctx, "pending migrations", func(t *tenant.Tenant) error {
		return m.ApplyPending(ctx, t.ID)
	})
}

// forEachActiveTenant pages through active tenants, applies fn to each, and
// returns a joined error naming every tenant that failed.
func (m *MigrationManager) forEachActiveTenant(ctx context.Context, what string, fn func(*tenant.Tenant) error) error {
	var errs []error
	applied := 0
	const perPage = 100
	for page := 1; ; page++ {
		tenants, _, err := m.repository.List(ctx, page, perPage)
		if err != nil {
			return fmt.Errorf("failed to list tenants: %w", err)
		}
		for _, t := range tenants {
			if t.Status != tenant.StatusActive {
				continue
			}
			if err := fn(t); err != nil {
				errs = append(errs, err)
				continue
			}
			applied++
		}
		if len(tenants) < perPage {
			break
		}
	}
	if len(errs) > 0 {
		m.logger.Error("Bulk operation completed with errors",
			zap.String("operation", what), zap.Int("succeeded", applied), zap.Int("failed", len(errs)))
		return fmt.Errorf("%s failed for %d tenant(s): %w", what, len(errs), errors.Join(errs...))
	}
	m.logger.Info("Bulk operation applied to all active tenants", zap.String("operation", what), zap.Int("count", applied))
	return nil
}

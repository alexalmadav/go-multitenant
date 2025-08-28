package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SchemaManager implements tenant.SchemaManager for database schema operations
type SchemaManager struct {
	db           *sql.DB
	logger       *zap.Logger
	schemaPrefix string
}

// NewSchemaManager creates a new schema manager
func NewSchemaManager(db *sql.DB, logger *zap.Logger, schemaPrefix string) *SchemaManager {
	if schemaPrefix == "" {
		schemaPrefix = "tenant_"
	}

	return &SchemaManager{
		db:           db,
		logger:       logger.Named("schema"),
		schemaPrefix: schemaPrefix,
	}
}

// GetSchemaName generates a standardized tenant schema name from tenant ID
func (sm *SchemaManager) GetSchemaName(tenantID uuid.UUID) string {
	return fmt.Sprintf("%s%s", sm.schemaPrefix, strings.ReplaceAll(tenantID.String(), "-", "_"))
}

// CreateTenantSchema creates a new tenant schema with all required tables
func (sm *SchemaManager) CreateTenantSchema(ctx context.Context, tenantID uuid.UUID, name string) error {
	schemaName := sm.GetSchemaName(tenantID)

	sm.logger.Info("Creating tenant schema",
		zap.String("tenant_id", tenantID.String()),
		zap.String("schema_name", schemaName),
		zap.String("tenant_name", name))

	tx, err := sm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Create the schema
	createSchemaSQL := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", sm.quotedSchemaName(tenantID))
	if _, err := tx.ExecContext(ctx, createSchemaSQL); err != nil {
		return fmt.Errorf("failed to create schema %s: %w", schemaName, err)
	}

	// Set search path to the new schema
	setSearchPathSQL := fmt.Sprintf("SET search_path TO %s, public", sm.quotedSchemaName(tenantID))
	if _, err := tx.ExecContext(ctx, setSearchPathSQL); err != nil {
		return fmt.Errorf("failed to set search path: %w", err)
	}

	// Create tenant-specific tables
	if err := sm.createTenantTables(ctx, tx); err != nil {
		return fmt.Errorf("failed to create tenant tables: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	sm.logger.Info("Successfully created tenant schema",
		zap.String("tenant_id", tenantID.String()),
		zap.String("schema_name", schemaName))

	return nil
}

// DropTenantSchema removes a tenant schema and all its data
func (sm *SchemaManager) DropTenantSchema(ctx context.Context, tenantID uuid.UUID) error {
	schemaName := sm.GetSchemaName(tenantID)

	sm.logger.Warn("Dropping tenant schema",
		zap.String("tenant_id", tenantID.String()),
		zap.String("schema_name", schemaName))

	dropSchemaSQL := fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", sm.quotedSchemaName(tenantID))
	if _, err := sm.db.ExecContext(ctx, dropSchemaSQL); err != nil {
		return fmt.Errorf("failed to drop schema %s: %w", schemaName, err)
	}

	sm.logger.Info("Successfully dropped tenant schema",
		zap.String("tenant_id", tenantID.String()),
		zap.String("schema_name", schemaName))

	return nil
}

// SchemaExists checks if a tenant schema exists
func (sm *SchemaManager) SchemaExists(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	schemaName := sm.GetSchemaName(tenantID)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	query := `SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)`

	var exists bool
	err := sm.db.QueryRowContext(ctx, query, schemaName).Scan(&exists)
	if err != nil {
		sm.logger.Error("Failed to check schema existence",
			zap.String("schema_name", schemaName),
			zap.String("tenant_id", tenantID.String()),
			zap.Error(err))
		return false, fmt.Errorf("error checking schema existence: %w", err)
	}

	sm.logger.Debug("Schema existence check completed",
		zap.String("schema_name", schemaName),
		zap.Bool("exists", exists))

	return exists, nil
}

// SetSearchPath sets the PostgreSQL search path to the tenant schema
func (sm *SchemaManager) SetSearchPath(db *sql.DB, tenantID uuid.UUID) error {
	quotedSchemaName := sm.quotedSchemaName(tenantID)
	query := fmt.Sprintf("SET search_path TO %s, public", quotedSchemaName)

	_, err := db.Exec(query)
	if err != nil {
		sm.logger.Error("Failed to set search path",
			zap.String("tenant_id", tenantID.String()),
			zap.String("query", query),
			zap.Error(err))
		return fmt.Errorf("error setting search path: %w", err)
	}

	return nil
}

// ListTenantSchemas returns all tenant schemas found in the database
func (sm *SchemaManager) ListTenantSchemas(ctx context.Context) ([]string, error) {
	query := `
		SELECT schema_name 
		FROM information_schema.schemata 
		WHERE schema_name LIKE $1
		ORDER BY schema_name
	`

	searchPattern := sm.schemaPrefix + "%"
	rows, err := sm.db.QueryContext(ctx, query, searchPattern)
	if err != nil {
		sm.logger.Error("Failed to list tenant schemas", zap.Error(err))
		return nil, fmt.Errorf("error listing tenant schemas: %w", err)
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var schemaName string
		if err := rows.Scan(&schemaName); err != nil {
			sm.logger.Error("Failed to scan schema name", zap.Error(err))
			continue
		}
		schemas = append(schemas, schemaName)
	}

	return schemas, nil
}

// quotedSchemaName returns a properly quoted schema name for SQL queries
func (sm *SchemaManager) quotedSchemaName(tenantID uuid.UUID) string {
	schemaName := sm.GetSchemaName(tenantID)
	return fmt.Sprintf(`"%s"`, schemaName)
}

// createTenantTables creates the standard tenant tables
// This is a basic implementation - in practice, you'd want this to be configurable
func (sm *SchemaManager) createTenantTables(ctx context.Context, tx *sql.Tx) error {
	// Example tenant tables - customize based on your needs
	tables := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name VARCHAR(255) NOT NULL,
			description TEXT,
			status VARCHAR(50) NOT NULL DEFAULT 'active',
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS tasks (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			project_id UUID NOT NULL,
			title VARCHAR(255) NOT NULL,
			description TEXT,
			status VARCHAR(50) NOT NULL DEFAULT 'pending',
			priority VARCHAR(20) NOT NULL DEFAULT 'medium',
			due_date TIMESTAMP WITH TIME ZONE,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
		)`,

		`CREATE TABLE IF NOT EXISTS documents (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			project_id UUID NOT NULL,
			file_name VARCHAR(255) NOT NULL,
			file_path VARCHAR(500) NOT NULL,
			file_type VARCHAR(100),
			file_size BIGINT,
			uploaded_by UUID,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
		)`,

		`CREATE TABLE IF NOT EXISTS tenant_users (
			user_id UUID NOT NULL,
			role VARCHAR(50) NOT NULL DEFAULT 'user',
			permissions JSONB,
			is_active BOOLEAN DEFAULT TRUE,
			joined_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id)
		)`,
	}

	// Create indexes
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status)",
		"CREATE INDEX IF NOT EXISTS idx_projects_created_at ON projects(created_at)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_project_id ON tasks(project_id)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_due_date ON tasks(due_date)",
		"CREATE INDEX IF NOT EXISTS idx_documents_project_id ON documents(project_id)",
		"CREATE INDEX IF NOT EXISTS idx_tenant_users_role ON tenant_users(role)",
	}

	// Create update triggers
	triggers := []string{
		`CREATE OR REPLACE FUNCTION update_updated_at_column()
		RETURNS TRIGGER AS $$
		BEGIN
			NEW.updated_at = CURRENT_TIMESTAMP;
			RETURN NEW;
		END;
		$$ language 'plpgsql'`,

		"CREATE TRIGGER update_projects_updated_at BEFORE UPDATE ON projects FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()",
		"CREATE TRIGGER update_tasks_updated_at BEFORE UPDATE ON tasks FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()",
		"CREATE TRIGGER update_tenant_users_updated_at BEFORE UPDATE ON tenant_users FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()",
	}

	// Execute all table creation statements
	for _, tableSQL := range tables {
		if _, err := tx.ExecContext(ctx, tableSQL); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}

	// Execute all index creation statements
	for _, indexSQL := range indexes {
		if _, err := tx.ExecContext(ctx, indexSQL); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	// Execute all trigger creation statements
	for _, triggerSQL := range triggers {
		if _, err := tx.ExecContext(ctx, triggerSQL); err != nil {
			return fmt.Errorf("failed to create trigger: %w", err)
		}
	}

	return nil
}

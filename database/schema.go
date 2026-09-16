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

// Ensure SchemaManager implements tenant.SchemaManager interface
var _ tenant.SchemaManager = (*SchemaManager)(nil)

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

// CreateTenantSchema creates the tenant's schema. It creates no tables; the
// application's migration files define the schema contents.
func (sm *SchemaManager) CreateTenantSchema(ctx context.Context, tenantID uuid.UUID) error {
	schemaName := sm.GetSchemaName(tenantID)
	sm.logger.Info("Creating tenant schema",
		zap.String("tenant_id", tenantID.String()),
		zap.String("schema_name", schemaName))

	createSchemaSQL := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", sm.quotedSchemaName(tenantID))
	if _, err := sm.db.ExecContext(ctx, createSchemaSQL); err != nil {
		return fmt.Errorf("failed to create schema %s: %w", schemaName, err)
	}
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

// ListTenantSchemas returns all tenant schemas found in the database
func (sm *SchemaManager) ListTenantSchemas(ctx context.Context) ([]string, error) {
	// starts_with is a literal prefix match; LIKE would treat "_" in the
	// prefix as a wildcard.
	query := `
		SELECT schema_name
		FROM information_schema.schemata
		WHERE starts_with(schema_name, $1)
		ORDER BY schema_name
	`

	rows, err := sm.db.QueryContext(ctx, query, sm.schemaPrefix)
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

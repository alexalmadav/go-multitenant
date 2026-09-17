package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// tableNamePattern is the only shape of table name the tracker will interpolate.
var tableNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// UsageTracker implements tenant.UsageTracker by counting rows in tables of
// the tenant schema. Which table backs which limit comes from
// LimitsConfig.UsageTables; limits not listed report nil and are not checked.
//
// Usage is derived from the tables themselves, so Increment, Decrement and
// Reset are no-ops.
type UsageTracker struct {
	db            *sql.DB
	schemaManager tenant.SchemaManager
	usageTables   map[string]string
	logger        *zap.Logger
}

var _ tenant.UsageTracker = (*UsageTracker)(nil)

// NewUsageTracker creates a tracker for the given limit-to-table map. Every
// table name must match ^[a-z_][a-z0-9_]*$.
func NewUsageTracker(db *sql.DB, schemaManager tenant.SchemaManager, usageTables map[string]string, logger *zap.Logger) (*UsageTracker, error) {
	tables := make(map[string]string, len(usageTables))
	for limit, table := range usageTables {
		if !tableNamePattern.MatchString(table) {
			return nil, fmt.Errorf("usage table for limit %q: %q is not a valid table name", limit, table)
		}
		tables[limit] = table
	}
	return &UsageTracker{
		db:            db,
		schemaManager: schemaManager,
		usageTables:   tables,
		logger:        logger.Named("usage_tracker"),
	}, nil
}

// GetCurrentUsage returns the row count of the table mapped to limitName, or
// nil if the limit is not mapped.
func (u *UsageTracker) GetCurrentUsage(ctx context.Context, tenantID uuid.UUID, limitName string) (interface{}, error) {
	table, ok := u.usageTables[limitName]
	if !ok {
		return nil, nil
	}
	schema := u.schemaManager.GetSchemaName(tenantID)
	query := fmt.Sprintf(`SELECT COUNT(*) FROM "%s"."%s"`, schema, table)

	var n int
	if err := u.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return nil, fmt.Errorf("failed to count %s for tenant %s: %w", table, tenantID, err)
	}
	return n, nil
}

// IncrementUsage is a no-op: usage is derived from the tenant's tables.
func (u *UsageTracker) IncrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error {
	return nil
}

// DecrementUsage is a no-op: usage is derived from the tenant's tables.
func (u *UsageTracker) DecrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error {
	return nil
}

// ResetUsage is a no-op: usage is derived from the tenant's tables.
func (u *UsageTracker) ResetUsage(ctx context.Context, tenantID uuid.UUID, limitName string) error {
	return nil
}

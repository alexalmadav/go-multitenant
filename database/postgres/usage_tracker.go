package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// UsageTracker implements tenant.UsageTracker by counting rows in the tenant
// schema on every check. It knows the limits that map onto the tables the
// SchemaManager creates and reports nil for anything else, which leaves those
// limits unchecked.
//
// Because usage is derived from the tables themselves, Increment, Decrement
// and Reset are no-ops.
type UsageTracker struct {
	db            *sql.DB
	schemaManager tenant.SchemaManager
	logger        *zap.Logger
}

// Ensure UsageTracker implements tenant.UsageTracker.
var _ tenant.UsageTracker = (*UsageTracker)(nil)

// NewUsageTracker creates a usage tracker backed by the tenant schema tables.
func NewUsageTracker(db *sql.DB, schemaManager tenant.SchemaManager, logger *zap.Logger) *UsageTracker {
	return &UsageTracker{
		db:            db,
		schemaManager: schemaManager,
		logger:        logger.Named("usage_tracker"),
	}
}

// GetCurrentUsage returns the current value for a limit, or nil if the limit
// has no table-backed measurement.
func (u *UsageTracker) GetCurrentUsage(ctx context.Context, tenantID uuid.UUID, limitName string) (interface{}, error) {
	switch limitName {
	case "max_projects":
		return u.count(ctx, tenantID, "projects", "")
	case "max_users":
		return u.count(ctx, tenantID, "tenant_users", "WHERE is_active = true")
	case "max_storage_gb":
		// Storage accounting is application-specific; report zero so the
		// limit is never spuriously exceeded.
		return 0, nil
	default:
		return nil, nil
	}
}

func (u *UsageTracker) count(ctx context.Context, tenantID uuid.UUID, table, where string) (int, error) {
	schema := u.schemaManager.GetSchemaName(tenantID)
	query := fmt.Sprintf(`SELECT COUNT(*) FROM "%s".%s %s`, schema, table, where)

	var n int
	if err := u.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count %s for tenant %s: %w", table, tenantID, err)
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

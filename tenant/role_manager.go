package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// roleIsolatedManager is a Manager whose tenant connections come from Pools,
// logged in as each tenant's own role. Every other method is the wrapped
// manager's.
type roleIsolatedManager struct {
	Manager
	pools      *Pools
	schemaName func(uuid.UUID) string
	logger     *zap.Logger
}

// NewRoleIsolatedManager wraps m so that GetTenantConn and WithTenantTx draw
// from pools. schemaName maps a tenant to its schema, which search_path is
// still set to, so unqualified names resolve exactly as in the default mode.
// Close closes the pools and then m.
func NewRoleIsolatedManager(m Manager, pools *Pools, schemaName func(uuid.UUID) string, logger *zap.Logger) Manager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &roleIsolatedManager{Manager: m, pools: pools, schemaName: schemaName, logger: logger.Named("role_isolation")}
}

func (r *roleIsolatedManager) GetTenantConn(ctx context.Context, tenantID uuid.UUID) (*Conn, error) {
	conn, release, err := r.pools.Acquire(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return newPooledConn(conn, r.schemaName(tenantID), release, func() {
		r.logger.Warn("A tenant connection was garbage-collected without Close; its pool slots stay held until the process exits",
			zap.String("tenant_id", tenantID.String()))
	}), nil
}

func (r *roleIsolatedManager) WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(tx *sql.Tx) error) error {
	conn, err := r.GetTenantConn(ctx, tenantID)
	if err != nil {
		return err
	}
	defer conn.Close()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Runs on panic too, so the deferred conn.Close does not wait on an open
	// transaction. After a successful Commit it is a no-op (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

func (r *roleIsolatedManager) Close() error {
	return errors.Join(r.pools.Close(), r.Manager.Close())
}

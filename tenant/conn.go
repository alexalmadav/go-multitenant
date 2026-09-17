package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Conn is a dedicated database connection scoped to one tenant's schema.
//
// It never sets session state. Every ExecContext, QueryContext and
// QueryRowContext runs inside its own transaction that begins with
// SET LOCAL search_path, and BeginTx returns a transaction that is already
// scoped. That makes Conn safe behind connection poolers in transaction mode
// (PgBouncer, pgcat, RDS Proxy), where session-level SET would silently apply
// to whichever server connection the pooler hands out next.
//
// The cost is one extra round trip per statement. Use BeginTx or
// Manager.WithTenantTx to run several statements in one transaction.
type Conn struct {
	conn       *sql.Conn
	schemaName string
	searchPath string // the SET LOCAL statement, built once
}

func newConn(conn *sql.Conn, schemaName string) *Conn {
	return &Conn{
		conn:       conn,
		schemaName: schemaName,
		searchPath: fmt.Sprintf(`SET LOCAL search_path TO "%s", public`, schemaName),
	}
}

// SchemaName returns the tenant schema this connection is scoped to.
func (c *Conn) SchemaName() string { return c.schemaName }

// Unwrap returns the underlying *sql.Conn. Statements run on it directly are
// NOT scoped to the tenant.
func (c *Conn) Unwrap() *sql.Conn { return c.conn }

// BeginTx starts a transaction with search_path already set to the tenant
// schema. Commit or Rollback it as usual.
func (c *Conn) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	tx, err := c.conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, c.searchPath); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("failed to set search path: %w", err)
	}
	return tx, nil
}

// ExecContext runs a statement scoped to the tenant schema in its own transaction.
func (c *Conn) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}
	return res, nil
}

// QueryContext runs a query scoped to the tenant schema. The returned Rows
// holds a transaction open until Close is called, so always close it.
func (c *Conn) QueryContext(ctx context.Context, query string, args ...interface{}) (*Rows, error) {
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return &Rows{Rows: rows, tx: tx}, nil
}

// QueryRowContext runs a single-row query scoped to the tenant schema. The
// transaction is committed when Scan is called.
func (c *Conn) QueryRowContext(ctx context.Context, query string, args ...interface{}) *Row {
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return &Row{err: err}
	}
	return &Row{row: tx.QueryRowContext(ctx, query, args...), tx: tx}
}

// Close releases the connection back to the pool.
func (c *Conn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Rows is a *sql.Rows whose Close also commits the scoping transaction.
type Rows struct {
	*sql.Rows
	tx *sql.Tx
}

// Close closes the rows and commits the transaction that scoped the query.
func (r *Rows) Close() error {
	closeErr := r.Rows.Close()
	if r.tx == nil {
		return closeErr
	}
	tx := r.tx
	r.tx = nil
	if closeErr != nil {
		_ = tx.Rollback()
		return closeErr
	}
	return tx.Commit()
}

// Row is the result of QueryRowContext.
type Row struct {
	row *sql.Row
	tx  *sql.Tx
	err error
}

// Scan copies the row's columns into dest and commits the scoping
// transaction. It returns sql.ErrNoRows when the query matched nothing.
func (r *Row) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	if r.tx == nil {
		return errors.New("tenant: Row already scanned")
	}
	tx := r.tx
	r.tx = nil
	scanErr := r.row.Scan(dest...)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		_ = tx.Rollback()
		return scanErr
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}
	return scanErr
}

// Err returns the error, if any, from beginning the scoped transaction or
// from running the query. Unlike sql.Row.Err, it does not consume the row.
func (r *Row) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.row.Err()
}

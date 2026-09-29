package tenant

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
)

// stubDriver is a database/sql driver whose connections accept every
// statement, record it, and count opens and closes. It lets Pools and the
// role-isolated manager be tested without a database.
type stubDriver struct {
	mu       sync.Mutex
	opened   int
	closed   int
	stmts    []string
	failNext error // returned by the next Connect, then cleared
}

func (d *stubDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("stubDriver: open through a connector")
}

// db returns a new *sql.DB over this driver.
func (d *stubDriver) db(maxOpen int) *sql.DB {
	db := sql.OpenDB(stubConnector{d})
	db.SetMaxOpenConns(maxOpen)
	return db
}

func (d *stubDriver) record(s string) {
	d.mu.Lock()
	d.stmts = append(d.stmts, s)
	d.mu.Unlock()
}

func (d *stubDriver) counts() (opened, closed int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opened, d.closed
}

func (d *stubDriver) statements() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.stmts...)
}

type stubConnector struct{ d *stubDriver }

func (c stubConnector) Connect(context.Context) (driver.Conn, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	if err := c.d.failNext; err != nil {
		c.d.failNext = nil
		return nil, err
	}
	c.d.opened++
	return &stubConn{d: c.d}, nil
}

func (c stubConnector) Driver() driver.Driver { return c.d }

type stubConn struct{ d *stubDriver }

func (c *stubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("stubConn: Prepare is not supported")
}

func (c *stubConn) Close() error {
	c.d.mu.Lock()
	c.d.closed++
	c.d.mu.Unlock()
	return nil
}

func (c *stubConn) Begin() (driver.Tx, error) {
	c.d.record("BEGIN")
	return stubTx{c.d}, nil
}

func (c *stubConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.d.record(query)
	return driver.RowsAffected(0), nil
}

type stubTx struct{ d *stubDriver }

func (t stubTx) Commit() error   { t.d.record("COMMIT"); return nil }
func (t stubTx) Rollback() error { t.d.record("ROLLBACK"); return nil }

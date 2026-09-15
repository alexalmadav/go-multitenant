package tenant

import (
	"context"
	"database/sql"
	"errors"
)

// Conn is a dedicated database connection whose search_path is set to a
// tenant's schema. Closing it resets search_path before the underlying
// connection is returned to the pool, so the tenant setting never leaks
// into unrelated queries.
type Conn struct {
	*sql.Conn
}

// Close resets search_path and releases the connection back to the pool.
func (c *Conn) Close() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	_, resetErr := c.Conn.ExecContext(context.Background(), "RESET search_path")
	closeErr := c.Conn.Close()
	return errors.Join(resetErr, closeErr)
}

package tenant

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// innerManager stands in for the real manager; embedding the interface makes
// any method the test does not define panic if called.
type innerManager struct {
	Manager
	closed bool
	hooks  []Hook
}

func (m *innerManager) Close() error        { m.closed = true; return nil }
func (m *innerManager) RegisterHook(h Hook) { m.hooks = append(m.hooks, h) }

func roleManagerForTest(t *testing.T, cfg PoolsConfig, logger *zap.Logger) (Manager, *innerManager, *stubDriver, *Pools) {
	t.Helper()
	d := &stubDriver{}
	pools, err := NewPools(cfg, func(context.Context, uuid.UUID) (*sql.DB, error) {
		return d.db(cfg.PerTenantMaxConns), nil
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	inner := &innerManager{}
	m := NewRoleIsolatedManager(inner, pools, roleNameForTest, logger)
	t.Cleanup(func() { m.Close() })
	return m, inner, d, pools
}

func TestRoleIsolatedGetTenantConnScopesAndReleases(t *testing.T) {
	m, _, d, pools := roleManagerForTest(t, bigLimits, zap.NewNop())
	id := uuid.New()

	conn, err := m.GetTenantConn(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if conn.SchemaName() != roleNameForTest(id) {
		t.Errorf("SchemaName() = %q, want %q", conn.SchemaName(), roleNameForTest(id))
	}
	if _, err := conn.ExecContext(context.Background(), "INSERT INTO projects DEFAULT VALUES"); err != nil {
		t.Fatal(err)
	}
	want := `SET LOCAL search_path TO "` + roleNameForTest(id) + `", public`
	if !slices.Contains(d.statements(), want) {
		t.Errorf("statements %q do not include %q", d.statements(), want)
	}
	if got := pools.Stats().InUse; got != 1 {
		t.Errorf("InUse = %d while the connection is open, want 1", got)
	}

	conn.Close()
	conn.Close()
	if got := pools.Stats().InUse; got != 0 {
		t.Errorf("InUse = %d after Close, want 0", got)
	}
}

func TestRoleIsolatedWithTenantTx(t *testing.T) {
	m, _, d, pools := roleManagerForTest(t, bigLimits, zap.NewNop())
	id := uuid.New()

	if err := m.WithTenantTx(context.Background(), id, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "UPDATE projects SET name = 'x'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stmts := d.statements()
	if n := len(stmts); n < 4 || stmts[n-1] != "COMMIT" || stmts[n-2] != "UPDATE projects SET name = 'x'" {
		t.Errorf("statements = %q, want BEGIN, SET LOCAL, the update, COMMIT", stmts)
	}

	boom := errors.New("boom")
	if err := m.WithTenantTx(context.Background(), id, func(*sql.Tx) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("WithTenantTx = %v, want the callback's error", err)
	}
	if stmts := d.statements(); stmts[len(stmts)-1] != "ROLLBACK" {
		t.Errorf("last statement = %q after a failed callback, want ROLLBACK", stmts[len(stmts)-1])
	}
	if got := pools.Stats().InUse; got != 0 {
		t.Errorf("InUse = %d after WithTenantTx, want 0", got)
	}
}

func TestRoleIsolatedSurfacesPoolExhaustion(t *testing.T) {
	m, _, _, _ := roleManagerForTest(t, PoolsConfig{MaxConns: 1, PerTenantMaxConns: 1, MaxWarmTenants: 5, IdleTimeout: time.Hour}, zap.NewNop())
	held, err := m.GetTenantConn(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := m.GetTenantConn(ctx, uuid.New()); !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("GetTenantConn = %v, want ErrPoolExhausted", err)
	}
}

func TestRoleIsolatedPassesThroughAndClosesBoth(t *testing.T) {
	m, inner, _, pools := roleManagerForTest(t, bigLimits, zap.NewNop())
	m.RegisterHook(BaseHook{})
	if len(inner.hooks) != 1 {
		t.Error("RegisterHook did not reach the wrapped manager")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if !inner.closed {
		t.Error("Close did not close the wrapped manager")
	}
	if _, _, err := pools.Acquire(context.Background(), uuid.New()); err == nil {
		t.Error("pools still served connections after Close")
	}
}

// A connection that is garbage-collected without Close holds its slots
// forever; the finalizer must say so, naming the tenant.
func TestRoleIsolatedReportsLeakedConnections(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	m, _, _, _ := roleManagerForTest(t, bigLimits, zap.New(core))
	id := uuid.New()

	func() {
		if _, err := m.GetTenantConn(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		for _, e := range logs.All() {
			for _, f := range e.Context {
				if f.Key == "tenant_id" && f.String == id.String() {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no leak warning naming the tenant after the unclosed connection was collected")
}

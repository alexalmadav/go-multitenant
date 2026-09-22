package gin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// testDSN returns TEST_DATABASE_URL or the local default, skipping when unreachable.
func testDSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/test_multitenant?sslmode=disable"
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Skipf("bad DSN: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Skipf("no database: %v", err)
	}
	return dsn
}

func TestIntegration_SetTenantDBThroughAdapter(t *testing.T) {
	dsn := testDSN(t)
	config := multitenant.DefaultConfig()
	config.Database.DSN = dsn
	config.Database.MigrationsDir = filepath.Join("..", "..", "testdata", "migrations")
	config.Resolver.Strategy = tenant.ResolverHeader
	config.Resolver.HeaderName = "X-Tenant"
	// This test exercises SetTenantDB through the adapter, not membership.
	config.InsecureSkipMembership = true
	mt, err := multitenant.New(config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Registered before the schema-cleanup below so it runs last (t.Cleanup
	// runs LIFO): the schemas must be dropped while the pool is still open.
	t.Cleanup(func() { _ = mt.Close() })
	ctx := context.Background()

	var ids []uuid.UUID
	var subs []string
	for i, n := range []int{2, 5} {
		id := uuid.New()
		sub := fmt.Sprintf("gin-%d-%s", i, id.String()[:8])
		if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: sub, Subdomain: sub}); err != nil {
			t.Fatal(err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatal(err)
		}
		err := mt.Manager.WithTenantTx(ctx, id, func(tx *sql.Tx) error {
			for j := 0; j < n; j++ {
				if _, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", fmt.Sprintf("p%d", j)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		subs = append(subs, sub)
	}
	t.Cleanup(func() {
		db := mt.GetDatabase()
		for _, id := range ids {
			schema := "tenant_" + strings.ReplaceAll(id.String(), "-", "_")
			_, _ = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema))
			_, _ = db.Exec(`DELETE FROM public.tenants WHERE id = $1`, id)
		}
	})

	mw := NewMiddleware(mt.Manager, mt.Resolver, mt.GetLogger(), Config{})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw.ResolveTenant(), mw.SetTenantDB())
	r.GET("/count", func(c *gin.Context) {
		conn, ok := GetTenantConnFromContext(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no conn"})
			return
		}
		var n int
		if err := conn.QueryRowContext(c.Request.Context(), "SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"count": n})
	})

	for i, want := range []int{2, 5} {
		req := httptest.NewRequest(http.MethodGet, "/count", nil)
		req.Header.Set("X-Tenant", subs[i])
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var body struct{ Count int }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusOK || body.Count != want {
			t.Errorf("tenant %s: got %d %s, want count=%d", subs[i], rec.Code, rec.Body.String(), want)
		}
	}
}

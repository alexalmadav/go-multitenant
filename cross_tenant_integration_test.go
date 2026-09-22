package multitenant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alexalmadav/go-multitenant/middleware/httpmw"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

// recordingManager wraps the real manager and records every tenant whose
// schema connection is requested, so a test can prove not only that a request
// was refused but that no connection to the other tenant was ever acquired.
type recordingManager struct {
	tenant.Manager
	mu    sync.Mutex
	conns []uuid.UUID
}

func (r *recordingManager) GetTenantConn(ctx context.Context, id uuid.UUID) (*tenant.Conn, error) {
	r.mu.Lock()
	r.conns = append(r.conns, id)
	r.mu.Unlock()
	return r.Manager.GetTenantConn(ctx, id)
}

// take returns the tenants connected to since the last call and resets the
// record.
func (r *recordingManager) take() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	got := r.conns
	r.conns = nil
	return got
}

func errorCodeOf(body string) string {
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &parsed)
	return parsed.Error.Code
}

// TestIntegration_CrossTenantAccessIsRefused is the end-to-end check that the
// unit tests cannot make. Those test the gate with stub managers and no
// database, and TestIntegration_TenantSchemaIsolation proves two correctly
// scoped connections cannot see each other's rows — but only after asking for
// the right tenant's connection directly. This test joins the two: a real
// database, two provisioned tenants with their own data, the real resolver and
// manager behind the full middleware chain, and requests authenticated for one
// tenant aimed at the other.
//
// A refusal must mean more than a 403. The recording manager proves that no
// connection to the other tenant's schema was acquired, and the handler proves
// it never ran.
func TestIntegration_CrossTenantAccessIsRefused(t *testing.T) {
	db := setupTestDatabase(t)
	defer db.Close()

	mt, err := New(testConfig(getTestDatabaseURL()))
	if err != nil {
		t.Fatalf("Failed to create MultiTenant: %v", err)
	}
	defer mt.Close()

	ctx := context.Background()
	acmeID, globexID := uuid.New(), uuid.New()
	defer cleanupTestData(db, []uuid.UUID{acmeID, globexID})

	for _, tn := range []*tenant.Tenant{
		{ID: acmeID, Name: "Acme", Subdomain: "acme-e2e"},
		{ID: globexID, Name: "Globex", Subdomain: "globex-e2e"},
	} {
		if err := mt.Manager.CreateTenant(ctx, tn); err != nil {
			t.Fatalf("CreateTenant(%s) failed: %v", tn.Subdomain, err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, tn.ID); err != nil {
			t.Fatalf("ProvisionTenant(%s) failed: %v", tn.Subdomain, err)
		}
	}

	// Each tenant holds one row the other must never see.
	for id, secret := range map[uuid.UUID]string{acmeID: "acme-confidential", globexID: "globex-confidential"} {
		conn, err := mt.Manager.GetTenantConn(ctx, id)
		if err != nil {
			t.Fatalf("GetTenantConn failed: %v", err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", secret); err != nil {
			conn.Close()
			t.Fatalf("seeding %s failed: %v", secret, err)
		}
		conn.Close()
	}

	// The application's membership: alice belongs to Acme, bob to Globex.
	members := map[string]uuid.UUID{"alice": acmeID, "bob": globexID}
	membership := tenant.MembershipFunc(func(_ context.Context, subject string, tenantID uuid.UUID) error {
		if members[subject] != tenantID {
			return tenant.ErrNotMember
		}
		return nil
	})

	// The application's auth, reduced to a header so the test controls who
	// the caller is.
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sub := r.Header.Get("X-Test-Subject"); sub != "" {
				r = r.WithContext(tenant.WithPrincipal(r.Context(), tenant.Principal{Subject: sub}))
			}
			next.ServeHTTP(w, r)
		})
	}

	// The handler reads from whatever schema its connection is scoped to.
	var handlerRuns int
	list := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRuns++
		conn, ok := tenant.GetTenantConnFromContext(r.Context())
		if !ok {
			http.Error(w, "no tenant connection", http.StatusInternalServerError)
			return
		}
		rows, err := conn.QueryContext(r.Context(), "SELECT name FROM projects ORDER BY name")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte(strings.Join(names, ",")))
	})

	rec := &recordingManager{Manager: mt.Manager}
	mw := httpmw.New(rec, mt.Resolver, mt.GetLogger(),
		httpmw.Config{SkipPaths: []string{"/health"}},
		httpmw.WithMembership(membership))

	// The chain the README recommends.
	standard := httpmw.Chain(list, auth, mw.Standard())

	// A chain that rewrites the path after the tenant is resolved. With
	// "/health" in SkipPaths, a request to /api/health resolves the tenant on
	// the way in and then looks like a skipped path to everything after
	// StripPrefix. Every gate after the rewrite must still run.
	rewritten := httpmw.Chain(
		http.StripPrefix("/api", httpmw.Chain(list,
			mw.ValidateTenant(), mw.RequireMembership(), mw.EnforceLimits(), mw.SetTenantDB())),
		auth, mw.ResolveTenant())

	cases := []struct {
		name        string
		handler     http.Handler
		host, path  string
		subject     string
		wantStatus  int
		wantCode    string
		wantBody    string
		wantConnect []uuid.UUID
	}{
		{
			name: "member reads own tenant", handler: standard,
			host: "acme-e2e.app.test", path: "/projects", subject: "alice",
			wantStatus: http.StatusOK, wantBody: "acme-confidential",
			wantConnect: []uuid.UUID{acmeID},
		},
		{
			name: "other member reads own tenant", handler: standard,
			host: "globex-e2e.app.test", path: "/projects", subject: "bob",
			wantStatus: http.StatusOK, wantBody: "globex-confidential",
			wantConnect: []uuid.UUID{globexID},
		},
		{
			name: "member of one tenant is refused at another", handler: standard,
			host: "globex-e2e.app.test", path: "/projects", subject: "alice",
			wantStatus: http.StatusForbidden, wantCode: "ACCESS_DENIED",
		},
		{
			name: "unauthenticated caller is refused", handler: standard,
			host: "globex-e2e.app.test", path: "/projects",
			wantStatus: http.StatusUnauthorized, wantCode: "USER_NOT_AUTHENTICATED",
		},
		{
			name: "unknown subject is refused", handler: standard,
			host: "acme-e2e.app.test", path: "/projects", subject: "mallory",
			wantStatus: http.StatusForbidden, wantCode: "ACCESS_DENIED",
		},
		{
			name: "path rewritten to a skipped prefix is still enforced", handler: rewritten,
			host: "globex-e2e.app.test", path: "/api/health", subject: "alice",
			wantStatus: http.StatusForbidden, wantCode: "ACCESS_DENIED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec.take()
			handlerRuns = 0

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			if tc.subject != "" {
				req.Header.Set("X-Test-Subject", tc.subject)
			}
			w := httptest.NewRecorder()
			tc.handler.ServeHTTP(w, req)
			body := w.Body.String()

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tc.wantStatus, body)
			}
			if tc.wantCode != "" {
				if got := errorCodeOf(body); got != tc.wantCode {
					t.Errorf("code = %q, want %q", got, tc.wantCode)
				}
			}
			if tc.wantBody != "" && body != tc.wantBody {
				t.Errorf("body = %q, want exactly %q", body, tc.wantBody)
			}

			connected := rec.take()
			if len(connected) != len(tc.wantConnect) {
				t.Fatalf("connected to %v, want %v", connected, tc.wantConnect)
			}
			for i := range connected {
				if connected[i] != tc.wantConnect[i] {
					t.Errorf("connected to %v, want %v", connected, tc.wantConnect)
				}
			}

			wantRuns := 0
			if tc.wantStatus == http.StatusOK {
				wantRuns = 1
			}
			if handlerRuns != wantRuns {
				t.Errorf("handler ran %d times, want %d", handlerRuns, wantRuns)
			}
		})
	}
}

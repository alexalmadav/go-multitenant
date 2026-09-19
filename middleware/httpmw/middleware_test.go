package httpmw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// stubManager implements only what a test needs; other methods panic via the nil embedded interface.
type stubManager struct {
	tenant.Manager
	tenants     map[uuid.UUID]*tenant.Tenant
	checkLimits func(ctx context.Context, id uuid.UUID) (*tenant.Limits, error)
}

func (s *stubManager) GetTenant(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	if t, ok := s.tenants[id]; ok {
		return t, nil
	}
	return nil, errors.New("tenant not found")
}

func (s *stubManager) CheckLimits(ctx context.Context, id uuid.UUID) (*tenant.Limits, error) {
	return s.checkLimits(ctx, id)
}

type stubResolver struct {
	tenant.Resolver
	resolve func(ctx context.Context, r *http.Request) (uuid.UUID, error)
}

func (s *stubResolver) ResolveTenant(ctx context.Context, r *http.Request) (uuid.UUID, error) {
	return s.resolve(ctx, r)
}

func resolveTo(id uuid.UUID) *stubResolver {
	return &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) { return id, nil }}
}

func withTenant(id uuid.UUID, status string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), tenant.ContextKeyTenant, &tenant.Context{TenantID: id, Status: status})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func serve(t *testing.T, h http.Handler, req *http.Request) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, body
}

func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// --- EnforceLimits ---------------------------------------------------------

func enforce(t *testing.T, check func(context.Context, uuid.UUID) (*tenant.Limits, error)) (int, map[string]any) {
	t.Helper()
	mw := New(&stubManager{checkLimits: check}, nil, zap.NewNop(), Config{})
	h := Chain(okHandler(), withTenant(uuid.New(), tenant.StatusActive), mw.EnforceLimits())
	return serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestEnforceLimits_LimitExceededReturns402(t *testing.T) {
	status, body := enforce(t, func(ctx context.Context, id uuid.UUID) (*tenant.Limits, error) {
		return nil, fmt.Errorf("limit check failed for max_projects: %w", &tenant.TenantError{
			TenantID: id, Code: "LIMIT_EXCEEDED", Message: "Limit exceeded for max_projects: current=11, limit=10"})
	})
	if status != http.StatusPaymentRequired || errorCode(body) != "PLAN_LIMIT_EXCEEDED" {
		t.Errorf("got %d %v, want 402 PLAN_LIMIT_EXCEEDED", status, body)
	}
}

func TestEnforceLimits_FeatureNotAllowedReturns402(t *testing.T) {
	status, body := enforce(t, func(ctx context.Context, id uuid.UUID) (*tenant.Limits, error) {
		return nil, &tenant.TenantError{TenantID: id, Code: "FEATURE_NOT_ALLOWED", Message: "advanced_features is disabled"}
	})
	if status != http.StatusPaymentRequired || errorCode(body) != "PLAN_LIMIT_EXCEEDED" {
		t.Errorf("got %d %v, want 402 PLAN_LIMIT_EXCEEDED", status, body)
	}
}

func TestEnforceLimits_UnexpectedErrorReturns500(t *testing.T) {
	status, body := enforce(t, func(context.Context, uuid.UUID) (*tenant.Limits, error) {
		return nil, errors.New("database is on fire")
	})
	if status != http.StatusInternalServerError || errorCode(body) != "LIMIT_CHECK_FAILED" {
		t.Errorf("got %d %v, want 500 LIMIT_CHECK_FAILED", status, body)
	}
}

func TestEnforceLimits_WithinLimitsPassesAndStoresLimits(t *testing.T) {
	var seen *tenant.Limits
	mw := New(&stubManager{checkLimits: func(context.Context, uuid.UUID) (*tenant.Limits, error) {
		return &tenant.Limits{MaxProjects: 10}, nil
	}}, nil, zap.NewNop(), Config{})
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = tenant.PlanLimitsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}), withTenant(uuid.New(), tenant.StatusActive), mw.EnforceLimits())
	status, _ := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if seen == nil || seen.MaxProjects != 10 {
		t.Errorf("limits not stored in context: %v", seen)
	}
}

func TestEnforceLimits_MissingTenantContextReturns500(t *testing.T) {
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{})
	status, body := serve(t, mw.EnforceLimits()(okHandler()), httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusInternalServerError || errorCode(body) != "TENANT_CONTEXT_MISSING" {
		t.Errorf("got %d %v, want 500 TENANT_CONTEXT_MISSING", status, body)
	}
}

// --- ResolveTenant ---------------------------------------------------------

func TestResolveTenant_PopulatesContext(t *testing.T) {
	id := uuid.New()
	mgr := &stubManager{tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: tenant.StatusActive, SchemaName: "tenant_x"}}}
	mw := New(mgr, resolveTo(id), zap.NewNop(), Config{})

	var ctxTenant *tenant.Context
	var ctxID uuid.UUID
	var obj *tenant.Tenant
	h := mw.ResolveTenant()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxTenant, _ = tenant.GetTenantFromContext(r.Context())
		ctxID, _ = tenant.GetTenantIDFromContext(r.Context())
		obj, _ = tenant.TenantObjectFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	status, _ := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if ctxTenant == nil || ctxTenant.TenantID != id || ctxTenant.Subdomain != "acme" || ctxTenant.SchemaName != "tenant_x" {
		t.Errorf("tenant context = %+v", ctxTenant)
	}
	if ctxID != id {
		t.Errorf("tenant id = %s, want %s", ctxID, id)
	}
	if obj == nil || obj.ID != id {
		t.Errorf("tenant object = %+v", obj)
	}
}

func TestResolveTenant_UnresolvableReturns404(t *testing.T) {
	res := &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) { return uuid.Nil, errors.New("nope") }}
	mw := New(&stubManager{}, res, zap.NewNop(), Config{})
	status, body := serve(t, mw.ResolveTenant()(okHandler()), httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusNotFound || errorCode(body) != "TENANT_NOT_FOUND" {
		t.Errorf("got %d %v, want 404 TENANT_NOT_FOUND", status, body)
	}
}

func TestResolveTenant_UnknownTenantReturns404WithID(t *testing.T) {
	id := uuid.New()
	mw := New(&stubManager{tenants: map[uuid.UUID]*tenant.Tenant{}}, resolveTo(id), zap.NewNop(), Config{})
	status, body := serve(t, mw.ResolveTenant()(okHandler()), httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusNotFound || errorCode(body) != "TENANT_NOT_FOUND" {
		t.Errorf("got %d %v", status, body)
	}
	if got, _ := body["tenant_id"].(string); got != id.String() {
		t.Errorf("tenant_id = %q, want %s", got, id)
	}
}

func TestResolveTenant_SkipPathsBypassResolution(t *testing.T) {
	called := false
	res := &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) {
		called = true
		return uuid.Nil, errors.New("nope")
	}}
	mw := New(&stubManager{}, res, zap.NewNop(), Config{SkipPaths: []string{"/health"}})
	status, _ := serve(t, mw.ResolveTenant()(okHandler()), httptest.NewRequest(http.MethodGet, "/health", nil))
	if status != http.StatusOK || called {
		t.Errorf("status = %d, resolver called = %v", status, called)
	}
}

// --- ValidateTenant --------------------------------------------------------

func TestValidateTenant_StatusHandling(t *testing.T) {
	cases := []struct {
		status   string
		wantCode int
		wantErr  string
	}{
		{tenant.StatusActive, http.StatusOK, ""},
		{tenant.StatusSuspended, http.StatusForbidden, "TENANT_SUSPENDED"},
		{tenant.StatusPending, http.StatusForbidden, "TENANT_PENDING"},
		{tenant.StatusCancelled, http.StatusForbidden, "TENANT_CANCELLED"},
		{"weird", http.StatusForbidden, "TENANT_INVALID_STATUS"},
	}
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{})
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			h := Chain(okHandler(), withTenant(uuid.New(), tc.status), mw.ValidateTenant())
			code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
			if code != tc.wantCode || errorCode(body) != tc.wantErr {
				t.Errorf("got %d %q, want %d %q", code, errorCode(body), tc.wantCode, tc.wantErr)
			}
		})
	}
}

func TestValidateTenant_MissingContextReturns500(t *testing.T) {
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{})
	code, body := serve(t, mw.ValidateTenant()(okHandler()), httptest.NewRequest(http.MethodGet, "/", nil))
	if code != http.StatusInternalServerError || errorCode(body) != "TENANT_CONTEXT_MISSING" {
		t.Errorf("got %d %v", code, body)
	}
}

// --- LogAccess -------------------------------------------------------------

func TestLogAccess_EmitsOneEntryWithFields(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.New(core), Config{})
	setUser := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(tenant.WithUserID(r.Context(), "user-1")))
		})
	}
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) }),
		withTenant(id, tenant.StatusActive), setUser, mw.LogAccess())
	req := httptest.NewRequest(http.MethodPost, "/projects", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	req.Header.Set("User-Agent", "test-agent")
	code, _ := serve(t, h, req)
	if code != http.StatusCreated {
		t.Fatalf("status = %d", code)
	}
	entries := logs.FilterMessage("Tenant access").All()
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	f := entries[0].ContextMap()
	want := map[string]string{"tenant_id": id.String(), "user_id": "user-1", "method": "POST", "path": "/projects", "client_ip": "203.0.113.9", "user_agent": "test-agent"}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s = %v, want %s", k, f[k], v)
		}
	}
}

func TestLogAccess_WithoutTenantIsSilent(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	mw := New(&stubManager{}, nil, zap.New(core), Config{})
	code, _ := serve(t, mw.LogAccess()(okHandler()), httptest.NewRequest(http.MethodGet, "/", nil))
	if code != http.StatusOK || logs.FilterMessage("Tenant access").Len() != 0 {
		t.Errorf("code = %d, entries = %d", code, logs.Len())
	}
}

// --- SetTenantDB (no DB): pass-through without tenant --------------------

func TestSetTenantDB_WithoutTenantPassesThrough(t *testing.T) {
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{})
	reached := false
	h := mw.SetTenantDB()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if _, ok := tenant.GetTenantConnFromContext(r.Context()); ok {
			t.Error("no conn expected without a tenant")
		}
	}))
	serve(t, h, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !reached {
		t.Error("handler not reached")
	}
}

// --- Chain / Standard / errors / clientIP ---------------------------------

func TestChain_AppliesInOrder(t *testing.T) {
	var order []string
	tag := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	serve(t, Chain(okHandler(), tag("a"), tag("b"), tag("c")), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := fmt.Sprint(order); got != "[a b c]" {
		t.Errorf("order = %s", got)
	}
}

func TestStandard_ResolvesValidatesAndEnforces(t *testing.T) {
	id := uuid.New()
	mgr := &stubManager{
		tenants:     map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: tenant.StatusSuspended}},
		checkLimits: func(context.Context, uuid.UUID) (*tenant.Limits, error) { return &tenant.Limits{}, nil },
	}
	mw := New(mgr, resolveTo(id), zap.NewNop(), Config{})
	code, body := serve(t, mw.Standard()(okHandler()), httptest.NewRequest(http.MethodGet, "/", nil))
	if code != http.StatusForbidden || errorCode(body) != "TENANT_SUSPENDED" {
		t.Errorf("Standard should have run ValidateTenant: got %d %v", code, body)
	}
}

func TestDefaultErrorHandler_ValidationErrorIs400WithField(t *testing.T) {
	rec := httptest.NewRecorder()
	DefaultErrorHandler(rec, httptest.NewRequest(http.MethodGet, "/", nil), &tenant.ValidationError{Field: "subdomain", Message: "bad"})
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	e, _ := body["error"].(map[string]any)
	if rec.Code != http.StatusBadRequest || e["code"] != "VALIDATION_ERROR" || e["field"] != "subdomain" {
		t.Errorf("got %d %v", rec.Code, body)
	}
}

func TestCustomErrorHandlerIsUsed(t *testing.T) {
	called := false
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}})
	code, _ := serve(t, mw.ValidateTenant()(okHandler()), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called || code != http.StatusTeapot {
		t.Errorf("custom handler called=%v code=%d", called, code)
	}
}

func TestClientIP_Precedence(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	if got := clientIP(r); got != "192.0.2.1" {
		t.Errorf("RemoteAddr: %q", got)
	}
	r.Header.Set("X-Real-IP", "198.51.100.7")
	if got := clientIP(r); got != "198.51.100.7" {
		t.Errorf("X-Real-IP: %q", got)
	}
	r.Header.Set("X-Forwarded-For", " 203.0.113.9 , 10.0.0.1")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("X-Forwarded-For: %q", got)
	}
}

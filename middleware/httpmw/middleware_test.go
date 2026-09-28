package httpmw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant/limits"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// stubManager implements only what a test needs; other methods panic via the nil embedded interface.
type stubManager struct {
	tenant.Manager
	tenants       map[uuid.UUID]*tenant.Tenant
	getTenantConn func(ctx context.Context, id uuid.UUID) (*tenant.Conn, error)
}

func (s *stubManager) GetTenant(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	if t, ok := s.tenants[id]; ok {
		return t, nil
	}
	return nil, errors.New("tenant not found")
}

// stubEnforcer is a limits.Enforcer whose CheckTenant is supplied per test.
type stubEnforcer struct {
	check func(ctx context.Context, id uuid.UUID) (limits.FlexibleLimits, error)
}

func (s stubEnforcer) CheckTenant(ctx context.Context, id uuid.UUID) (limits.FlexibleLimits, error) {
	return s.check(ctx, id)
}

func (s *stubManager) GetTenantConn(ctx context.Context, id uuid.UUID) (*tenant.Conn, error) {
	return s.getTenantConn(ctx, id)
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

func enforce(t *testing.T, check func(context.Context, uuid.UUID) (limits.FlexibleLimits, error)) (int, map[string]any) {
	t.Helper()
	h := Chain(okHandler(), withTenant(uuid.New(), tenant.StatusActive), EnforceLimits(stubEnforcer{check}, nil))
	return serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestEnforceLimits_LimitExceededReturns402(t *testing.T) {
	status, body := enforce(t, func(ctx context.Context, id uuid.UUID) (limits.FlexibleLimits, error) {
		return nil, fmt.Errorf("limit check failed for max_projects: %w", &tenant.TenantError{
			TenantID: id, Code: "LIMIT_EXCEEDED", Message: "Limit exceeded for max_projects: current=11, limit=10"})
	})
	if status != http.StatusPaymentRequired || errorCode(body) != "PLAN_LIMIT_EXCEEDED" {
		t.Errorf("got %d %v, want 402 PLAN_LIMIT_EXCEEDED", status, body)
	}
}

func TestEnforceLimits_FeatureNotAllowedReturns402(t *testing.T) {
	status, body := enforce(t, func(ctx context.Context, id uuid.UUID) (limits.FlexibleLimits, error) {
		return nil, &tenant.TenantError{TenantID: id, Code: "FEATURE_NOT_ALLOWED", Message: "advanced_features is disabled"}
	})
	if status != http.StatusPaymentRequired || errorCode(body) != "PLAN_LIMIT_EXCEEDED" {
		t.Errorf("got %d %v, want 402 PLAN_LIMIT_EXCEEDED", status, body)
	}
}

func TestEnforceLimits_UnexpectedErrorReturns500(t *testing.T) {
	status, body := enforce(t, func(context.Context, uuid.UUID) (limits.FlexibleLimits, error) {
		return nil, errors.New("database is on fire")
	})
	if status != http.StatusInternalServerError || errorCode(body) != "LIMIT_CHECK_FAILED" {
		t.Errorf("got %d %v, want 500 LIMIT_CHECK_FAILED", status, body)
	}
}

func TestEnforceLimits_PlanNotConfiguredReturns403(t *testing.T) {
	status, body := enforce(t, func(ctx context.Context, id uuid.UUID) (limits.FlexibleLimits, error) {
		return nil, &tenant.TenantError{TenantID: id, Code: "PLAN_NOT_CONFIGURED", Message: `no limits configured for plan ""`}
	})
	if status != http.StatusForbidden || errorCode(body) != "PLAN_NOT_CONFIGURED" {
		t.Errorf("got %d %v, want 403 PLAN_NOT_CONFIGURED", status, body)
	}
}

func TestEnforceLimits_WithinLimitsPassesAndStoresLimits(t *testing.T) {
	var seen limits.FlexibleLimits
	allow := stubEnforcer{func(context.Context, uuid.UUID) (limits.FlexibleLimits, error) {
		return limits.FlexibleLimits{"max_projects": limits.IntLimit(10)}, nil
	}}
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = limits.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}), withTenant(uuid.New(), tenant.StatusActive), EnforceLimits(allow, nil))
	status, _ := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if got, err := seen.GetInt("max_projects"); err != nil || got != 10 {
		t.Errorf("limits not stored in context: %v (%v)", seen, err)
	}
}

func TestEnforceLimits_MissingTenantContextReturns500(t *testing.T) {
	deny := stubEnforcer{func(context.Context, uuid.UUID) (limits.FlexibleLimits, error) {
		t.Error("CheckTenant must not run without a tenant context")
		return nil, nil
	}}
	status, body := serve(t, EnforceLimits(deny, nil)(okHandler()), httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusInternalServerError || errorCode(body) != "TENANT_CONTEXT_MISSING" {
		t.Errorf("got %d %v, want 500 TENANT_CONTEXT_MISSING", status, body)
	}
}

func TestEnforceLimits_NilEnforcerPassesThrough(t *testing.T) {
	h := Chain(okHandler(), withTenant(uuid.New(), tenant.StatusActive), EnforceLimits(nil, nil))
	if status, _ := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil)); status != http.StatusOK {
		t.Errorf("status = %d", status)
	}
}

func TestEnforceLimits_WithoutOptionPassesThrough(t *testing.T) {
	id := uuid.New()
	mgr := &stubManager{tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: tenant.StatusActive}}}
	mw := New(mgr, resolveTo(id), zap.NewNop(), Config{}) // no WithLimits
	// Standard's chain minus SetTenantDB, which the stub manager has no
	// database for; the point here is that EnforceLimits passes through.
	h := Chain(okHandler(), mw.ResolveTenant(), mw.ValidateTenant(), mw.EnforceLimits())
	if status, _ := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil)); status != http.StatusOK {
		t.Errorf("status = %d", status)
	}
}

func TestEnforceLimits_MethodLogsTheUnderlyingCause(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	id := uuid.New()
	broken := stubEnforcer{func(context.Context, uuid.UUID) (limits.FlexibleLimits, error) {
		return nil, errors.New("db down")
	}}
	mw := New(&stubManager{}, nil, zap.New(core), Config{}, WithLimits(broken))
	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), mw.EnforceLimits())
	status, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusInternalServerError || errorCode(body) != "LIMIT_CHECK_FAILED" {
		t.Fatalf("got %d %v, want 500 LIMIT_CHECK_FAILED", status, body)
	}
	entries := logs.FilterMessage("Plan limits check failed").All()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	f := entries[0].ContextMap()
	if got, _ := f["error"].(string); !strings.Contains(got, "db down") {
		t.Errorf("logged error = %v, want it to mention the cause", f["error"])
	}
	if f["tenant_id"] != id.String() {
		t.Errorf("tenant_id = %v, want %s", f["tenant_id"], id)
	}
}

func TestStandard_WithLimitsOptionEnforces(t *testing.T) {
	id := uuid.New()
	mgr := &stubManager{tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: tenant.StatusActive}}}
	deny := stubEnforcer{func(context.Context, uuid.UUID) (limits.FlexibleLimits, error) {
		return nil, &tenant.TenantError{TenantID: id, Code: "LIMIT_EXCEEDED", Message: "over"}
	}}
	mw := New(mgr, resolveTo(id), zap.NewNop(), Config{}, WithLimits(deny))
	status, body := serve(t, mw.Standard()(okHandler()), httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusPaymentRequired || errorCode(body) != "PLAN_LIMIT_EXCEEDED" {
		t.Errorf("got %d %v", status, body)
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
	want := map[string]string{"tenant_id": id.String(), "user_id": "user-1", "method": "POST", "path": "/projects", "client_ip": "192.0.2.1", "user_agent": "test-agent"}
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

func TestSetTenantDB_ConnErrorReturns500(t *testing.T) {
	mgr := &stubManager{getTenantConn: func(context.Context, uuid.UUID) (*tenant.Conn, error) {
		return nil, errors.New("connection refused")
	}}
	mw := New(mgr, nil, zap.NewNop(), Config{})
	h := Chain(okHandler(), withTenant(uuid.New(), tenant.StatusActive), mw.SetTenantDB())
	status, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if status != http.StatusInternalServerError || errorCode(body) != "DATABASE_ERROR" {
		t.Errorf("got %d %v, want 500 DATABASE_ERROR", status, body)
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

func TestStandard_SkipPathReachesHandler(t *testing.T) {
	res := &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) {
		return uuid.Nil, errors.New("nope")
	}}
	// A limits enforcer is configured so the skip guard in EnforceLimits is
	// load-bearing: without it the skipped request would fail on the missing
	// tenant context.
	never := stubEnforcer{func(context.Context, uuid.UUID) (limits.FlexibleLimits, error) {
		t.Error("EnforceLimits must not run on a skipped path")
		return nil, nil
	}}
	mw := New(&stubManager{}, res, zap.NewNop(), Config{SkipPaths: []string{"/health"}}, WithLimits(never))
	var sawTenant bool
	h := mw.Standard()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawTenant = tenant.GetTenantFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	status, _ := serve(t, h, httptest.NewRequest(http.MethodGet, "/health", nil))
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if sawTenant {
		t.Error("no tenant expected on skipped path")
	}
}

func TestStandard_ResolvesValidatesAndEnforces(t *testing.T) {
	id := uuid.New()
	mgr := &stubManager{
		tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: tenant.StatusSuspended}},
	}
	allow := stubEnforcer{func(context.Context, uuid.UUID) (limits.FlexibleLimits, error) {
		return limits.FlexibleLimits{}, nil
	}}
	mw := New(mgr, resolveTo(id), zap.NewNop(), Config{}, WithLimits(allow))
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
	if got := ForwardedClientIP(r); got != "192.0.2.1" {
		t.Errorf("RemoteAddr: %q", got)
	}
	r.Header.Set("X-Real-IP", "198.51.100.7")
	if got := ForwardedClientIP(r); got != "198.51.100.7" {
		t.Errorf("X-Real-IP: %q", got)
	}
	r.Header.Set("X-Forwarded-For", " 203.0.113.9 , 10.0.0.1")
	if got := ForwardedClientIP(r); got != "203.0.113.9" {
		t.Errorf("X-Forwarded-For: %q", got)
	}
	r.Header.Set("X-Forwarded-For", "evil<script>")
	if got := ForwardedClientIP(r); got != "198.51.100.7" {
		t.Errorf("non-IP X-Forwarded-For should fall through to X-Real-IP: %q", got)
	}
}

func TestLogAccess_DefaultClientIPIsRemoteAddr(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.New(core), Config{})
	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), mw.LogAccess())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	serve(t, h, req)
	entries := logs.FilterMessage("Tenant access").All()
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	if got := entries[0].ContextMap()["client_ip"]; got != "192.0.2.1" {
		t.Errorf("client_ip = %v, want 192.0.2.1", got)
	}
}

func TestLogAccess_ForwardedClientIPOptIn(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.New(core), Config{ClientIP: ForwardedClientIP})
	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), mw.LogAccess())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	serve(t, h, req)
	entries := logs.FilterMessage("Tenant access").All()
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	if got := entries[0].ContextMap()["client_ip"]; got != "203.0.113.9" {
		t.Errorf("client_ip = %v, want 203.0.113.9", got)
	}
}

func TestResolveTenantSkipsConfiguredHost(t *testing.T) {
	failing := &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) {
		return uuid.Nil, errors.New("resolver must not be called for a skipped host")
	}}
	mw := New(&stubManager{}, failing, zap.NewNop(), Config{SkipHosts: []string{"auth.app.com"}})

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Host = "auth.app.com"
	code, _ := serve(t, mw.ResolveTenant()(okHandler()), req)

	if code != http.StatusOK {
		t.Errorf("status = %d, want %d", code, http.StatusOK)
	}
}

// Host matching ignores the port and case, since neither is part of the
// operator's intent.
func TestSkipHostIgnoresPortAndCase(t *testing.T) {
	failing := &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) {
		return uuid.Nil, errors.New("resolver must not be called for a skipped host")
	}}
	mw := New(&stubManager{}, failing, zap.NewNop(), Config{SkipHosts: []string{"auth.app.com"}})

	for _, host := range []string{"auth.app.com:8443", "AUTH.App.com"} {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		req.Host = host
		if code, _ := serve(t, mw.ResolveTenant()(okHandler()), req); code != http.StatusOK {
			t.Errorf("host %q: status = %d, want %d", host, code, http.StatusOK)
		}
	}
}

func TestSkipHostDoesNotSkipOtherHosts(t *testing.T) {
	mw := New(&stubManager{}, &stubResolver{
		resolve: func(context.Context, *http.Request) (uuid.UUID, error) {
			return uuid.Nil, errors.New("no tenant")
		},
	}, zap.NewNop(), Config{SkipHosts: []string{"auth.app.com"}})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Host = "acme.app.com"
	code, body := serve(t, mw.ResolveTenant()(okHandler()), req)

	if code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", code, http.StatusNotFound)
	}
	if got := errorCode(body); got != "TENANT_NOT_FOUND" {
		t.Errorf("code = %q, want TENANT_NOT_FOUND", got)
	}
}

// A skip list must not disable a status check for a request that already
// carries a resolved tenant: shouldSkip re-reads r.URL.Path and r.Host, which
// a rewrite such as http.StripPrefix can change between ResolveTenant and
// here, and SetTenantDB downstream would still scope the request to that
// tenant's schema. Restoring the original skip-first ordering fails this test
// and nothing else.
func TestValidateTenantEnforcesAResolvedTenantOnASkippedPath(t *testing.T) {
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{SkipPaths: []string{"/health"}})

	h := Chain(okHandler(), withTenant(id, tenant.StatusSuspended), mw.ValidateTenant())
	code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/health", nil))

	if code != http.StatusForbidden {
		t.Errorf("status = %d, want %d - a skipped path let a suspended tenant through", code, http.StatusForbidden)
	}
	if got := errorCode(body); got != "TENANT_SUSPENDED" {
		t.Errorf("code = %q, want TENANT_SUSPENDED", got)
	}
}

// The same rule for plan limits: an over-limit tenant that has been resolved
// is refused even on a skipped path.
func TestEnforceLimitsEnforcesAResolvedTenantOnASkippedPath(t *testing.T) {
	id := uuid.New()
	over := stubEnforcer{func(context.Context, uuid.UUID) (limits.FlexibleLimits, error) {
		return nil, &tenant.TenantError{Code: "LIMIT_EXCEEDED", Message: "project limit reached"}
	}}
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{SkipPaths: []string{"/health"}}, WithLimits(over))

	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), mw.EnforceLimits())
	code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/health", nil))

	if code != http.StatusPaymentRequired {
		t.Errorf("status = %d, want %d - a skipped path let an over-limit tenant through", code, http.StatusPaymentRequired)
	}
	if got := errorCode(body); got != "PLAN_LIMIT_EXCEEDED" {
		t.Errorf("code = %q, want PLAN_LIMIT_EXCEEDED", got)
	}
}

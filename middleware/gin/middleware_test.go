package gin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// stubManager implements only the Manager methods the middleware under test
// touches. Everything else panics via the nil embedded interface.
type stubManager struct {
	tenant.Manager
	checkLimits func(ctx context.Context, tenantID uuid.UUID) (*tenant.Limits, error)
}

func (s *stubManager) CheckLimits(ctx context.Context, tenantID uuid.UUID) (*tenant.Limits, error) {
	return s.checkLimits(ctx, tenantID)
}

func withTenant(id uuid.UUID) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("tenant", &tenant.Context{TenantID: id, Status: tenant.StatusActive})
		c.Next()
	}
}

func runEnforceLimits(t *testing.T, mgr tenant.Manager) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mw := NewMiddleware(mgr, nil, zap.NewNop(), Config{})
	r := gin.New()
	r.Use(withTenant(uuid.New()), mw.EnforceLimits())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

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

func TestEnforceLimits_LimitExceededReturns402(t *testing.T) {
	mgr := &stubManager{checkLimits: func(ctx context.Context, id uuid.UUID) (*tenant.Limits, error) {
		return nil, fmt.Errorf("limit check failed for max_projects: %w", &tenant.TenantError{
			TenantID: id,
			Code:     "LIMIT_EXCEEDED",
			Message:  "Limit exceeded for max_projects: current=11, limit=10",
		})
	}}

	status, body := runEnforceLimits(t, mgr)

	if status != http.StatusPaymentRequired {
		t.Errorf("status = %d, want %d", status, http.StatusPaymentRequired)
	}
	if got := errorCode(body); got != "PLAN_LIMIT_EXCEEDED" {
		t.Errorf("error code = %q, want PLAN_LIMIT_EXCEEDED (body: %v)", got, body)
	}
}

func TestEnforceLimits_FeatureNotAllowedReturns402(t *testing.T) {
	mgr := &stubManager{checkLimits: func(ctx context.Context, id uuid.UUID) (*tenant.Limits, error) {
		return nil, &tenant.TenantError{TenantID: id, Code: "FEATURE_NOT_ALLOWED", Message: "advanced_features is disabled"}
	}}

	status, body := runEnforceLimits(t, mgr)

	if status != http.StatusPaymentRequired || errorCode(body) != "PLAN_LIMIT_EXCEEDED" {
		t.Errorf("got %d %v, want 402 PLAN_LIMIT_EXCEEDED", status, body)
	}
}

func TestEnforceLimits_UnexpectedErrorReturns500(t *testing.T) {
	mgr := &stubManager{checkLimits: func(ctx context.Context, id uuid.UUID) (*tenant.Limits, error) {
		return nil, errors.New("database is on fire")
	}}

	status, body := runEnforceLimits(t, mgr)

	if status != http.StatusInternalServerError || errorCode(body) != "LIMIT_CHECK_FAILED" {
		t.Errorf("got %d %v, want 500 LIMIT_CHECK_FAILED", status, body)
	}
}

func TestEnforceLimits_WithinLimitsPasses(t *testing.T) {
	mgr := &stubManager{checkLimits: func(ctx context.Context, id uuid.UUID) (*tenant.Limits, error) {
		return &tenant.Limits{MaxProjects: 10}, nil
	}}

	status, _ := runEnforceLimits(t, mgr)

	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
}

// --- ResolveTenant / ValidateTenant -------------------------------------

type stubResolver struct {
	tenant.Resolver
	resolve func(ctx context.Context, req *http.Request) (uuid.UUID, error)
}

func (s *stubResolver) ResolveTenant(ctx context.Context, req *http.Request) (uuid.UUID, error) {
	return s.resolve(ctx, req)
}

type lookupManager struct {
	tenant.Manager
	tenants map[uuid.UUID]*tenant.Tenant
}

func (m *lookupManager) GetTenant(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	if t, ok := m.tenants[id]; ok {
		return t, nil
	}
	return nil, errors.New("tenant not found")
}

func (m *lookupManager) ValidateAccess(ctx context.Context, userID, tenantID uuid.UUID) error {
	return nil
}

func (m *lookupManager) WithTenantContext(ctx context.Context, tenantID uuid.UUID) context.Context {
	t := m.tenants[tenantID]
	return context.WithValue(ctx, tenant.ContextKeyTenant, &tenant.Context{TenantID: t.ID, Subdomain: t.Subdomain, Status: t.Status})
}

func newResolveRouter(t *testing.T, mgr tenant.Manager, res tenant.Resolver, cfg Config, extra ...gin.HandlerFunc) (*gin.Engine, *tenant.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mw := NewMiddleware(mgr, res, zap.NewNop(), cfg)
	var seen *tenant.Context
	r := gin.New()
	handlers := append([]gin.HandlerFunc{mw.ResolveTenant()}, extra...)
	r.Use(handlers...)
	r.GET("/*path", func(c *gin.Context) {
		if tc, ok := tenant.GetTenantFromContext(c.Request.Context()); ok {
			seen = tc
		}
		c.Status(http.StatusOK)
	})
	return r, seen
}

func TestResolveTenant_PopulatesGinAndRequestContext(t *testing.T) {
	id := uuid.New()
	mgr := &lookupManager{tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: tenant.StatusActive}}}
	res := &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) { return id, nil }}

	gin.SetMode(gin.TestMode)
	mw := NewMiddleware(mgr, res, zap.NewNop(), Config{})
	r := gin.New()
	r.Use(mw.ResolveTenant())
	var ginCtx, reqCtx *tenant.Context
	r.GET("/", func(c *gin.Context) {
		ginCtx, _ = GetTenantFromContext(c)
		reqCtx, _ = tenant.GetTenantFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ginCtx == nil || ginCtx.TenantID != id || ginCtx.Subdomain != "acme" {
		t.Errorf("gin context tenant = %+v, want id %s", ginCtx, id)
	}
	if reqCtx == nil || reqCtx.TenantID != id {
		t.Errorf("request context tenant = %+v, want id %s", reqCtx, id)
	}
}

func TestResolveTenant_UnresolvableReturns404(t *testing.T) {
	mgr := &lookupManager{tenants: map[uuid.UUID]*tenant.Tenant{}}
	res := &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) { return uuid.Nil, errors.New("nope") }}
	r, _ := newResolveRouter(t, mgr, res, Config{})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusNotFound || errorCode(body) != "TENANT_NOT_FOUND" {
		t.Errorf("got %d %v, want 404 TENANT_NOT_FOUND", rec.Code, body)
	}
}

func TestResolveTenant_SkipPathsBypassResolution(t *testing.T) {
	called := false
	res := &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) {
		called = true
		return uuid.Nil, errors.New("nope")
	}}
	r, _ := newResolveRouter(t, &lookupManager{}, res, Config{SkipPaths: []string{"/health"}})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if called {
		t.Errorf("resolver should not run for skipped paths")
	}
}

func runValidate(t *testing.T, status string, cfg Config, setUser bool) (int, map[string]any) {
	t.Helper()
	id := uuid.New()
	mgr := &lookupManager{tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: status}}}
	res := &stubResolver{resolve: func(context.Context, *http.Request) (uuid.UUID, error) { return id, nil }}
	gin.SetMode(gin.TestMode)
	mw := NewMiddleware(mgr, res, zap.NewNop(), cfg)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if setUser {
			c.Set("user_id", uuid.New().String())
		}
		c.Next()
	}, mw.ResolveTenant(), mw.ValidateTenant())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

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
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			code, body := runValidate(t, tc.status, Config{}, false)
			if code != tc.wantCode || errorCode(body) != tc.wantErr {
				t.Errorf("status %s: got %d %q, want %d %q", tc.status, code, errorCode(body), tc.wantCode, tc.wantErr)
			}
		})
	}
}

func TestValidateTenant_RequireAuthentication(t *testing.T) {
	code, body := runValidate(t, tenant.StatusActive, Config{RequireAuthentication: true}, false)
	if code != http.StatusUnauthorized || errorCode(body) != "USER_NOT_AUTHENTICATED" {
		t.Errorf("without user: got %d %v, want 401 USER_NOT_AUTHENTICATED", code, body)
	}
	code, _ = runValidate(t, tenant.StatusActive, Config{RequireAuthentication: true}, true)
	if code != http.StatusOK {
		t.Errorf("with user: got %d, want 200", code)
	}
}

// --- RequireAdmin / LogAccess --------------------------------------------

func runRequireAdmin(t *testing.T, setup func(c *gin.Context)) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mw := NewMiddleware(&lookupManager{}, nil, zap.NewNop(), Config{})
	r := gin.New()
	r.Use(func(c *gin.Context) { setup(c); c.Next() }, mw.RequireAdmin())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestRequireAdmin_AllowsAdminRoleOrTenantAdminFlag(t *testing.T) {
	cases := map[string]func(c *gin.Context){
		"user_role=admin":      func(c *gin.Context) { c.Set("user_role", "admin") },
		"is_tenant_admin=true": func(c *gin.Context) { c.Set("is_tenant_admin", true) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			if code, _ := runRequireAdmin(t, setup); code != http.StatusOK {
				t.Errorf("status = %d, want 200", code)
			}
		})
	}
}

func TestRequireAdmin_RejectsNonAdminWith403(t *testing.T) {
	id := uuid.New()
	code, body := runRequireAdmin(t, func(c *gin.Context) {
		c.Set("user_role", "member")
		c.Set("tenant", &tenant.Context{TenantID: id})
	})
	if code != http.StatusForbidden || errorCode(body) != "ADMIN_REQUIRED" {
		t.Errorf("got %d %v, want 403 ADMIN_REQUIRED", code, body)
	}
	if got, _ := body["tenant_id"].(string); got != id.String() {
		t.Errorf("tenant_id in response = %q, want %s", got, id)
	}
}

func TestLogAccess_EmitsOneEntryWithTenantAndRequestFields(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	id := uuid.New()
	gin.SetMode(gin.TestMode)
	mw := NewMiddleware(&lookupManager{}, nil, zap.New(core), Config{})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("tenant", &tenant.Context{TenantID: id, Subdomain: "acme"})
		c.Set("user_id", "user-1")
		c.Next()
	}, mw.LogAccess())
	r.POST("/projects", func(c *gin.Context) { c.Status(http.StatusCreated) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projects", nil))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	entries := logs.FilterMessage("Tenant access").All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one access log entry, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	want := map[string]string{"tenant_id": id.String(), "subdomain": "acme", "user_id": "user-1", "method": "POST", "path": "/projects"}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("log field %s = %v, want %s", k, fields[k], v)
		}
	}
}

func TestLogAccess_WithoutTenantContextIsSilent(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	gin.SetMode(gin.TestMode)
	mw := NewMiddleware(&lookupManager{}, nil, zap.New(core), Config{})
	r := gin.New()
	r.Use(mw.LogAccess())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := logs.FilterMessage("Tenant access").Len(); n != 0 {
		t.Errorf("expected no access log without tenant context, got %d", n)
	}
}

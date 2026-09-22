package gin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexalmadav/go-multitenant/limits"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type stubManager struct {
	tenant.Manager
	tenants map[uuid.UUID]*tenant.Tenant
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

type stubResolver struct {
	tenant.Resolver
	id  uuid.UUID
	err error
}

func (s *stubResolver) ResolveTenant(ctx context.Context, r *http.Request) (uuid.UUID, error) {
	return s.id, s.err
}

func newRouter(mw *Middleware, handlers ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(handlers...)
	return r
}

func TestAdapter_ResolveTenantPopulatesGinKeysAndRequestContext(t *testing.T) {
	id := uuid.New()
	mgr := &stubManager{tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: tenant.StatusActive}}}
	enforcer := stubEnforcer{func(context.Context, uuid.UUID) (limits.FlexibleLimits, error) {
		return limits.FlexibleLimits{"max_projects": limits.IntLimit(7)}, nil
	}}
	mw := NewMiddleware(mgr, &stubResolver{id: id}, zap.NewNop(), Config{Limits: enforcer})

	var ginTenant, ctxTenant *tenant.Context
	var ginObj *tenant.Tenant
	var ginID string
	var planLimits limits.FlexibleLimits
	r := newRouter(mw, mw.ResolveTenant(), mw.ValidateTenant(), mw.EnforceLimits())
	r.GET("/", func(c *gin.Context) {
		ginTenant, _ = GetTenantFromContext(c)
		ginObj, _ = GetTenantFromGinContext(c)
		ginID = c.GetString("tenant_id")
		planLimits, _ = GetTenantLimitsFromContext(c)
		ctxTenant, _ = tenant.GetTenantFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if ginTenant == nil || ginTenant.TenantID != id || ctxTenant == nil || ctxTenant.TenantID != id {
		t.Errorf("tenant context: gin=%+v ctx=%+v", ginTenant, ctxTenant)
	}
	if ginObj == nil || ginObj.ID != id || ginID != id.String() {
		t.Errorf("tenant_object=%+v tenant_id=%q", ginObj, ginID)
	}
	if got, err := planLimits.GetInt("max_projects"); err != nil || got != 7 {
		t.Errorf("plan_limits = %+v (%v)", planLimits, err)
	}
}

func TestAdapter_ErrorAbortsChainWithCoreResponse(t *testing.T) {
	mw := NewMiddleware(&stubManager{}, &stubResolver{err: errors.New("nope")}, zap.NewNop(), Config{})
	reached := false
	r := newRouter(mw, mw.ResolveTenant())
	r.GET("/", func(c *gin.Context) { reached = true })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound || reached {
		t.Errorf("code = %d reached = %v", rec.Code, reached)
	}
}

func TestAdapter_CustomErrorHandlerReceivesGinContext(t *testing.T) {
	var got *gin.Context
	mw := NewMiddleware(&stubManager{}, &stubResolver{err: errors.New("nope")}, zap.NewNop(), Config{
		ErrorHandler: func(c *gin.Context, err error) {
			got = c
			c.JSON(http.StatusTeapot, gin.H{"custom": true})
			c.Abort()
		},
	})
	r := newRouter(mw, mw.ResolveTenant())
	r.GET("/", func(c *gin.Context) { t.Error("handler must not run") })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got == nil || rec.Code != http.StatusTeapot {
		t.Errorf("custom handler got=%v code=%d", got != nil, rec.Code)
	}
}

func TestAdapter_SkipPathsPassThrough(t *testing.T) {
	mw := NewMiddleware(&stubManager{}, &stubResolver{err: errors.New("nope")}, zap.NewNop(), Config{SkipPaths: []string{"/health"}})
	r := newRouter(mw, mw.ResolveTenant(), mw.SetTenantDB())
	r.GET("/health", func(c *gin.Context) {
		if _, ok := GetTenantFromContext(c); ok {
			t.Error("no tenant expected on skipped path")
		}
		c.Status(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d", rec.Code)
	}
}

func TestAdapter_StandardChainSkipPathReachesHandler(t *testing.T) {
	mw := NewMiddleware(&stubManager{}, &stubResolver{err: errors.New("nope")}, zap.NewNop(), Config{SkipPaths: []string{"/health"}})
	reached := false
	r := newRouter(mw, mw.ResolveTenant(), mw.ValidateTenant(), mw.EnforceLimits(), mw.SetTenantDB(), mw.LogAccess())
	r.GET("/health", func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || !reached {
		t.Errorf("code = %d reached = %v, want 200 true", rec.Code, reached)
	}
}

func TestAdapter_LogAccessReadsGinUserIDKey(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	id := uuid.New()
	mw := NewMiddleware(&stubManager{}, &stubResolver{}, zap.New(core), Config{})
	setTenantAndUser := func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), tenant.ContextKeyTenant, &tenant.Context{TenantID: id, Status: tenant.StatusActive})
		c.Request = c.Request.WithContext(ctx)
		c.Set("user_id", "user-9")
		c.Next()
	}
	r := newRouter(mw, setTenantAndUser, mw.LogAccess())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	entries := logs.FilterMessage("Tenant access").All()
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	if got := entries[0].ContextMap()["user_id"]; got != "user-9" {
		t.Errorf("user_id = %v, want user-9", got)
	}
}

func TestAdapter_DownstreamHandlersRunInsideCoreNext(t *testing.T) {
	// A wrapping middleware that records whether the downstream handler ran
	// before its own deferred cleanup — the property SetTenantDB relies on.
	var order []string
	mw := NewMiddleware(&stubManager{}, &stubResolver{}, zap.NewNop(), Config{})
	wrapped := mw.adapt(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "before")
			defer func() { order = append(order, "cleanup") }()
			next.ServeHTTP(w, r)
		})
	})
	r := newRouter(mw, wrapped)
	r.GET("/", func(c *gin.Context) { order = append(order, "handler"); c.Status(http.StatusOK) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if want := "[before handler cleanup]"; fmt.Sprint(order) != want {
		t.Errorf("order = %v, want %s", order, want)
	}
}

func TestAdapter_RequireMembershipDeniesANonMember(t *testing.T) {
	id := uuid.New()
	mw := NewMiddleware(&stubManager{}, nil, zap.NewNop(), Config{
		Membership: tenant.MembershipFunc(func(context.Context, string, uuid.UUID) error {
			return tenant.ErrNotMember
		}),
	})

	setCaller := func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), tenant.ContextKeyTenant,
			&tenant.Context{TenantID: id, Status: tenant.StatusActive})
		c.Request = c.Request.WithContext(tenant.WithPrincipal(ctx, tenant.Principal{Subject: "u-1"}))
		c.Next()
	}

	r := newRouter(mw, setCaller, mw.RequireMembership())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// The existing c.Set("user_id", ...) bridge satisfies the principal
// requirement, so Gin applications that already set it need no change.
func TestAdapter_RequireMembershipAcceptsTheUserIDBridge(t *testing.T) {
	id := uuid.New()
	var gotSubject string
	mw := NewMiddleware(&stubManager{}, nil, zap.NewNop(), Config{
		Membership: tenant.MembershipFunc(func(_ context.Context, subject string, _ uuid.UUID) error {
			gotSubject = subject
			return nil
		}),
	})

	setCaller := func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(),
			tenant.ContextKeyTenant, &tenant.Context{TenantID: id, Status: tenant.StatusActive}))
		c.Set("user_id", "u-2")
		c.Next()
	}

	r := newRouter(mw, setCaller, mw.RequireMembership())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if gotSubject != "u-2" {
		t.Errorf("subject = %q, want u-2", gotSubject)
	}
}

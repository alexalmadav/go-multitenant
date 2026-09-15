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

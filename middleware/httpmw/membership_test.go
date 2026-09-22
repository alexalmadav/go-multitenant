package httpmw

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// withPrincipal puts a subject in the request context, standing in for the
// application's auth middleware.
func withPrincipal(subject string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := tenant.WithPrincipal(r.Context(), tenant.Principal{Subject: subject})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func allowAll() tenant.Membership {
	return tenant.MembershipFunc(func(context.Context, string, uuid.UUID) error { return nil })
}

func denyAll() tenant.Membership {
	return tenant.MembershipFunc(func(context.Context, string, uuid.UUID) error {
		return tenant.ErrNotMember
	})
}

func TestRequireMembershipAllowsAMember(t *testing.T) {
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{}, WithMembership(allowAll()))

	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), withPrincipal("u-1"), mw.RequireMembership())
	code, _ := serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if code != http.StatusOK {
		t.Errorf("status = %d, want %d", code, http.StatusOK)
	}
}

func TestRequireMembershipDeniesANonMember(t *testing.T) {
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{}, WithMembership(denyAll()))

	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), withPrincipal("u-1"), mw.RequireMembership())
	code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", code, http.StatusForbidden)
	}
	if got := errorCode(body); got != "ACCESS_DENIED" {
		t.Errorf("code = %q, want ACCESS_DENIED", got)
	}
}

// The cause explains which tenant was refused but never reaches the client.
func TestRequireMembershipDoesNotLeakTheCause(t *testing.T) {
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{}, WithMembership(
		tenant.MembershipFunc(func(context.Context, string, uuid.UUID) error {
			return errors.New("SELECT failed on memberships.secret_column")
		}),
	))

	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), withPrincipal("u-1"), mw.RequireMembership())
	_, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))

	e, _ := body["error"].(map[string]any)
	if msg, _ := e["message"].(string); msg != "Access denied to this tenant" {
		t.Errorf("message = %q, want the sanitised message", msg)
	}
}

func TestRequireMembershipRejectsAnUnauthenticatedRequest(t *testing.T) {
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{}, WithMembership(allowAll()))

	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), mw.RequireMembership())
	code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", code, http.StatusUnauthorized)
	}
	if got := errorCode(body); got != "USER_NOT_AUTHENTICATED" {
		t.Errorf("code = %q, want USER_NOT_AUTHENTICATED", got)
	}
}

func TestRequireMembershipNeedsResolveTenantFirst(t *testing.T) {
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{}, WithMembership(allowAll()))

	h := Chain(okHandler(), withPrincipal("u-1"), mw.RequireMembership())
	code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", code, http.StatusInternalServerError)
	}
	if got := errorCode(body); got != "TENANT_CONTEXT_MISSING" {
		t.Errorf("code = %q, want TENANT_CONTEXT_MISSING", got)
	}
}

// Unlike WithLimits, an unconfigured Membership is not a pass-through.
func TestRequireMembershipFailsClosedWhenUnconfigured(t *testing.T) {
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{})

	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), withPrincipal("u-1"), mw.RequireMembership())
	code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", code, http.StatusForbidden)
	}
	if got := errorCode(body); got != "ACCESS_DENIED" {
		t.Errorf("code = %q, want ACCESS_DENIED", got)
	}
}

func TestRequireMembershipPassesSkippedPathsThrough(t *testing.T) {
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{SkipPaths: []string{"/health"}}, WithMembership(denyAll()))

	h := Chain(okHandler(), mw.RequireMembership())
	code, _ := serve(t, h, httptest.NewRequest(http.MethodGet, "/health", nil))

	if code != http.StatusOK {
		t.Errorf("status = %d, want %d", code, http.StatusOK)
	}
}

// SkipHosts exempts an origin that serves no tenant, so a request from one
// must pass through. As with SkipPaths, the exemption only applies because no
// tenant was resolved; the chain here puts none in the context.
func TestRequireMembershipPassesSkippedHostsThrough(t *testing.T) {
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{SkipHosts: []string{"auth.app.com"}}, WithMembership(denyAll()))

	h := Chain(okHandler(), mw.RequireMembership())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Host = "auth.app.com"
	code, _ := serve(t, h, req)

	if code != http.StatusOK {
		t.Errorf("status = %d, want %d", code, http.StatusOK)
	}
}

// A skip list must not disable the check for a request that already carries a
// resolved tenant. shouldSkip re-reads r.URL.Path and r.Host, which a rewrite
// such as http.StripPrefix can change between ResolveTenant and here, and
// SetTenantDB downstream would still scope the request to that tenant's
// schema. Restoring the original skip-first ordering fails this test and
// nothing else.
func TestRequireMembershipEnforcesAResolvedTenantOnASkippedPath(t *testing.T) {
	id := uuid.New()
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{SkipPaths: []string{"/health"}}, WithMembership(denyAll()))

	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), withPrincipal("u-1"), mw.RequireMembership())
	code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/health", nil))

	if code != http.StatusForbidden {
		t.Errorf("status = %d, want %d - a skipped path disabled the check for a resolved tenant", code, http.StatusForbidden)
	}
	if got := errorCode(body); got != "ACCESS_DENIED" {
		t.Errorf("code = %q, want ACCESS_DENIED", got)
	}
}

// An empty subject is never a legitimate authenticated identity, so the
// middleware rejects it rather than passing "" to Allow and leaving every
// implementation to decide whether the empty string is a user. The membership
// here allows everything, so dropping the empty-subject condition turns this
// into a 200 and the recorder proves why.
func TestRequireMembershipRejectsAnEmptySubject(t *testing.T) {
	id := uuid.New()
	var asked bool
	mw := New(&stubManager{}, nil, zap.NewNop(), Config{}, WithMembership(
		tenant.MembershipFunc(func(context.Context, string, uuid.UUID) error {
			asked = true
			return nil
		}),
	))

	h := Chain(okHandler(), withTenant(id, tenant.StatusActive), withPrincipal(""), mw.RequireMembership())
	code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", code, http.StatusUnauthorized)
	}
	if got := errorCode(body); got != "USER_NOT_AUTHENTICATED" {
		t.Errorf("code = %q, want USER_NOT_AUTHENTICATED", got)
	}
	if asked {
		t.Error("Allow was called with an empty subject; the middleware must reject it first")
	}
}

// Standard omits the check when no Membership was configured, so the
// convenience bundle never silently denies everything. The request carries no
// principal at all, so a Standard that enforced membership would stop it with
// USER_NOT_AUTHENTICATED. Instead it must run the whole chain and reach
// SetTenantDB, which fails only because this test has no database.
func TestStandardOmitsMembershipWhenUnconfigured(t *testing.T) {
	id := uuid.New()
	mgr := &stubManager{
		tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: tenant.StatusActive}},
		getTenantConn: func(context.Context, uuid.UUID) (*tenant.Conn, error) {
			return nil, errors.New("no database in this test")
		},
	}
	mw := New(mgr, resolveTo(id), zap.NewNop(), Config{})

	code, body := serve(t, mw.Standard()(okHandler()), httptest.NewRequest(http.MethodGet, "/x", nil))

	if code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", code, http.StatusInternalServerError)
	}
	if got := errorCode(body); got != "DATABASE_ERROR" {
		t.Errorf("code = %q, want DATABASE_ERROR - only SetTenantDB produces it, so anything else means the chain stopped before reaching it", got)
	}
}

// This test pins the ordering as well as the outcome: mgr has a nil
// getTenantConn, so it panics if the chain ever reaches SetTenantDB. A denied
// membership must stop the request before then. Do not "fix" the stub by
// giving it a working getTenantConn — that would silently lose the ordering
// guarantee and leave only the status assertion behind.
func TestStandardEnforcesMembershipWhenConfigured(t *testing.T) {
	id := uuid.New()
	mgr := &stubManager{tenants: map[uuid.UUID]*tenant.Tenant{id: {ID: id, Subdomain: "acme", Status: tenant.StatusActive}}}
	mw := New(mgr, resolveTo(id), zap.NewNop(), Config{}, WithMembership(denyAll()))

	h := Chain(okHandler(), withPrincipal("u-1"), mw.Standard())
	code, body := serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", code, http.StatusForbidden)
	}
	if got := errorCode(body); got != "ACCESS_DENIED" {
		t.Errorf("code = %q, want ACCESS_DENIED", got)
	}
}

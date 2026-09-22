# Membership and Principal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the cross-tenant access hole by adding one application-supplied authorization seam — a `Membership` interface, a claims-carrying `Principal`, and a fail-closed `RequireMembership` middleware.

**Architecture:** The library still authenticates nobody. The application's auth middleware puts a `tenant.Principal` in the request context; `httpmw.RequireMembership` runs after `ResolveTenant` and asks an application-supplied `tenant.Membership` whether that subject may enter the resolved tenant. The one shipped implementation, `ClaimMembership`, answers from a claim already in the token and issues no queries. No membership table, no role model, no new dependencies.

**Tech Stack:** Go 1.24, `database/sql`, `net/http`, `go.uber.org/zap`, `github.com/google/uuid`. Tests are stdlib `testing` only — the core module has no testify dependency.

**Spec:** `docs/superpowers/specs/2026-09-21-membership-design.md`

## Global Constraints

- **No new module dependencies.** The root `go.mod` gains nothing. Adapter packages for specific auth libraries are explicitly out of scope.
- **No master table.** Nothing is added to `Repository.CreateMasterTables` (`database/postgres/repository.go:277`). The existing per-tenant `tenant_users` fixture in `testdata/migrations/` is unrelated and stays as is.
- **No role or permission model.** `Membership.Allow` returns `error`, not a role.
- **Backward compatible.** `tenant.WithUserID`, `tenant.UserIDFromContext`, `tenant.ContextKeyUserID` and the Gin `user_id` bridge must all keep their current behaviour. Existing tests pass unchanged.
- **Tests are `package tenant` / `package httpmw`** (internal), matching `tenant/resolver_test.go:1` and `middleware/httpmw/middleware_test.go:1`.
- **Doc comments are full sentences** ending in a period, matching the surrounding files.
- Run `go build ./... && go vet ./... && go test ./...` from the repo root before each commit. The Gin module (Task 6) is a separate Go module and must be built and tested from `middleware/gin/`.

---

### Task 1: Principal in the request context

Widen the identity the context carries from a bare user-id string to a subject plus claims, without breaking the existing string API.

**Files:**
- Create: `tenant/principal.go`
- Create: `tenant/principal_test.go`
- Modify: `tenant/context.go:10-15` (add the context key), `tenant/context.go:28-36` (`WithUserID` becomes a wrapper)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `tenant.Principal` (struct with fields `Subject string`, `Claims map[string]any`), `func (Principal) Claim(name string) (any, bool)`, `func tenant.WithPrincipal(ctx context.Context, p Principal) context.Context`, `func tenant.PrincipalFromContext(ctx context.Context) (Principal, bool)`, and the constant `tenant.ContextKeyPrincipal`.

- [ ] **Step 1: Write the failing test**

Create `tenant/principal_test.go`:

```go
package tenant

import (
	"context"
	"testing"
)

func TestPrincipalRoundTrip(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{
		Subject: "auth0|abc",
		Claims:  map[string]any{"org_id": "acme"},
	})

	p, ok := PrincipalFromContext(ctx)
	if !ok {
		t.Fatal("PrincipalFromContext returned ok=false")
	}
	if p.Subject != "auth0|abc" {
		t.Errorf("Subject = %q, want %q", p.Subject, "auth0|abc")
	}
	v, ok := p.Claim("org_id")
	if !ok || v != "acme" {
		t.Errorf("Claim(org_id) = %v, %v; want acme, true", v, ok)
	}
}

func TestPrincipalFromContextMissing(t *testing.T) {
	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Error("PrincipalFromContext returned ok=true for an empty context")
	}
}

func TestClaimOnNilClaims(t *testing.T) {
	var p Principal
	if _, ok := p.Claim("anything"); ok {
		t.Error("Claim returned ok=true for a principal with no claims")
	}
}

// WithPrincipal must keep the string user-id API working, because LogAccess
// and the Gin adapter read it.
func TestWithPrincipalSetsUserID(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{Subject: "u-1"})
	if id, ok := UserIDFromContext(ctx); !ok || id != "u-1" {
		t.Errorf("UserIDFromContext = %q, %v; want u-1, true", id, ok)
	}
}

// WithUserID keeps working and is readable as a principal.
func TestWithUserIDIsAPrincipal(t *testing.T) {
	ctx := WithUserID(context.Background(), "u-2")
	if id, ok := UserIDFromContext(ctx); !ok || id != "u-2" {
		t.Errorf("UserIDFromContext = %q, %v; want u-2, true", id, ok)
	}
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.Subject != "u-2" {
		t.Errorf("PrincipalFromContext = %+v, %v; want Subject u-2, true", p, ok)
	}
	if p.Claims != nil {
		t.Errorf("Claims = %v, want nil", p.Claims)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tenant/ -run 'TestPrincipal|TestClaimOnNilClaims|TestWithPrincipal|TestWithUserIDIsAPrincipal' -v`
Expected: FAIL — compilation error, `undefined: Principal`, `undefined: WithPrincipal`, `undefined: PrincipalFromContext`.

- [ ] **Step 3: Add the context key**

In `tenant/context.go`, add to the existing `const` block (after `ContextKeyUserID`):

```go
	// ContextKeyPrincipal carries the authenticated caller as a Principal,
	// set by the application's auth middleware with WithPrincipal.
	ContextKeyPrincipal ContextKey = "principal"
```

- [ ] **Step 4: Create `tenant/principal.go`**

```go
package tenant

import "context"

// Principal is the authenticated caller, as the application's auth
// middleware sees it. Subject is the stable identifier the identity provider
// issues: "auth0|abc", a UUID in string form, a service-account name.
// Claims carries whatever else that provider supplied and may be nil.
//
// This library never authenticates anyone. It only reads a Principal the
// application put in the context.
type Principal struct {
	Subject string
	Claims  map[string]any
}

// Claim returns the named claim. The second result is false when the
// principal carries no such claim.
func (p Principal) Claim(name string) (any, bool) {
	if p.Claims == nil {
		return nil, false
	}
	v, ok := p.Claims[name]
	return v, ok
}

// WithPrincipal returns a context carrying the authenticated caller. It also
// stores the subject under ContextKeyUserID, so UserIDFromContext, LogAccess
// and the Gin adapter's user_id bridge keep working.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	ctx = context.WithValue(ctx, ContextKeyPrincipal, p)
	return context.WithValue(ctx, ContextKeyUserID, p.Subject)
}

// PrincipalFromContext returns the principal set by WithPrincipal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ContextKeyPrincipal).(Principal)
	return p, ok
}
```

- [ ] **Step 5: Make `WithUserID` a wrapper**

In `tenant/context.go`, replace the body of `WithUserID` (currently `tenant/context.go:29-31`) and update its doc comment:

```go
// WithUserID returns a context carrying the authenticated user's id. It is
// shorthand for WithPrincipal with only a subject; use WithPrincipal when the
// caller also has claims.
func WithUserID(ctx context.Context, userID string) context.Context {
	return WithPrincipal(ctx, Principal{Subject: userID})
}
```

Leave `UserIDFromContext` exactly as it is — it still reads `ContextKeyUserID`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./tenant/ -run 'TestPrincipal|TestClaimOnNilClaims|TestWithPrincipal|TestWithUserIDIsAPrincipal' -v`
Expected: PASS, 5 tests.

- [ ] **Step 7: Verify nothing regressed**

Run: `go build ./... && go vet ./... && go test ./tenant/ ./middleware/httpmw/`
Expected: all PASS. The `LogAccess` tests in `middleware/httpmw/middleware_test.go` exercise `WithUserID` and must still pass.

- [ ] **Step 8: Commit**

```bash
git add tenant/principal.go tenant/principal_test.go tenant/context.go
git commit -m "feat(tenant): carry an authenticated Principal with claims in the context

WithUserID becomes shorthand for WithPrincipal and keeps writing the subject
under ContextKeyUserID, so UserIDFromContext, LogAccess and the Gin user_id
bridge are unchanged.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Membership interface and ClaimMembership

The authorization seam itself, plus the one implementation that needs no database.

**Files:**
- Create: `tenant/membership.go`
- Create: `tenant/membership_test.go`

**Interfaces:**
- Consumes: `tenant.Principal`, `tenant.PrincipalFromContext` (Task 1); the existing `tenant.TenantObjectFromContext` (`tenant/context.go:23`).
- Produces: `tenant.Membership` (interface with `Allow(ctx context.Context, subject string, tenantID uuid.UUID) error`), `tenant.MembershipFunc`, `tenant.ErrNotMember`, `func tenant.ClaimMembership(claim string) Membership`.

- [ ] **Step 1: Write the failing test**

Create `tenant/membership_test.go`:

```go
package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// claimCtx builds a context holding a principal with one claim and a resolved
// tenant, which is what ClaimMembership reads.
func claimCtx(claim string, value any, t *Tenant) context.Context {
	ctx := WithPrincipal(context.Background(), Principal{
		Subject: "u-1",
		Claims:  map[string]any{claim: value},
	})
	if t != nil {
		ctx = WithTenantObject(ctx, t)
	}
	return ctx
}

func TestMembershipFuncAdaptsAFunction(t *testing.T) {
	want := errors.New("nope")
	var m Membership = MembershipFunc(func(context.Context, string, uuid.UUID) error {
		return want
	})
	if got := m.Allow(context.Background(), "u-1", uuid.New()); !errors.Is(got, want) {
		t.Errorf("Allow = %v, want %v", got, want)
	}
}

func TestClaimMembershipMatchesTenantID(t *testing.T) {
	id := uuid.New()
	ctx := claimCtx("org_id", id.String(), nil)
	if err := ClaimMembership("org_id").Allow(ctx, "u-1", id); err != nil {
		t.Errorf("Allow = %v, want nil", err)
	}
}

func TestClaimMembershipMatchesSubdomain(t *testing.T) {
	id := uuid.New()
	ctx := claimCtx("org_slug", "acme", &Tenant{ID: id, Subdomain: "acme"})
	if err := ClaimMembership("org_slug").Allow(ctx, "u-1", id); err != nil {
		t.Errorf("Allow = %v, want nil", err)
	}
}

// A user who belongs to several tenants has a list-valued claim. JSON decoding
// produces []any, so both shapes must work.
func TestClaimMembershipMatchesWithinAList(t *testing.T) {
	id := uuid.New()
	for name, value := range map[string]any{
		"[]any":    []any{"globex", "acme"},
		"[]string": []string{"globex", "acme"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := claimCtx("orgs", value, &Tenant{ID: id, Subdomain: "acme"})
			if err := ClaimMembership("orgs").Allow(ctx, "u-1", id); err != nil {
				t.Errorf("Allow = %v, want nil", err)
			}
		})
	}
}

func TestClaimMembershipDeniesAnotherTenant(t *testing.T) {
	ctx := claimCtx("org_slug", "globex", &Tenant{ID: uuid.New(), Subdomain: "acme"})
	err := ClaimMembership("org_slug").Allow(ctx, "u-1", uuid.New())
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("Allow = %v, want ErrNotMember", err)
	}
}

func TestClaimMembershipDeniesMissingClaim(t *testing.T) {
	ctx := claimCtx("something_else", "acme", &Tenant{Subdomain: "acme"})
	err := ClaimMembership("org_slug").Allow(ctx, "u-1", uuid.New())
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("Allow = %v, want ErrNotMember", err)
	}
}

func TestClaimMembershipDeniesWithoutAPrincipal(t *testing.T) {
	err := ClaimMembership("org_id").Allow(context.Background(), "u-1", uuid.New())
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("Allow = %v, want ErrNotMember", err)
	}
}

// A claim of an unexpected type denies rather than panicking.
func TestClaimMembershipDeniesNonStringClaim(t *testing.T) {
	ctx := claimCtx("org_id", 42, nil)
	err := ClaimMembership("org_id").Allow(ctx, "u-1", uuid.New())
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("Allow = %v, want ErrNotMember", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tenant/ -run 'TestMembership|TestClaimMembership' -v`
Expected: FAIL — compilation error, `undefined: Membership`, `undefined: MembershipFunc`, `undefined: ClaimMembership`, `undefined: ErrNotMember`.

- [ ] **Step 3: Create `tenant/membership.go`**

```go
package tenant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Membership answers whether an authenticated subject may act inside a
// tenant. It is the only authorization decision this library makes, and the
// application always supplies the answer: nothing here authenticates anyone
// or records who belongs where.
//
// Apply it with httpmw.RequireMembership, which reads the subject from the
// Principal the application's auth middleware put in the context.
type Membership interface {
	// Allow returns nil when subject may act inside tenantID. Any non-nil
	// error denies the request; wrap ErrNotMember to say why.
	Allow(ctx context.Context, subject string, tenantID uuid.UUID) error
}

// MembershipFunc adapts an ordinary function to Membership. Most
// applications answer from their own table with their own subject type:
//
//	tenant.MembershipFunc(func(ctx context.Context, subject string, id uuid.UUID) error {
//	    var ok bool
//	    err := db.QueryRowContext(ctx,
//	        `SELECT EXISTS (SELECT 1 FROM memberships WHERE user_id = $1 AND tenant_id = $2)`,
//	        subject, id).Scan(&ok)
//	    if err != nil {
//	        return err
//	    }
//	    if !ok {
//	        return tenant.ErrNotMember
//	    }
//	    return nil
//	})
type MembershipFunc func(ctx context.Context, subject string, tenantID uuid.UUID) error

// Allow implements Membership.
func (f MembershipFunc) Allow(ctx context.Context, subject string, tenantID uuid.UUID) error {
	return f(ctx, subject, tenantID)
}

// ErrNotMember reports that the subject does not belong to the tenant.
// Middleware turns it, and any other error, into ACCESS_DENIED; the cause is
// logged rather than returned to the client.
var ErrNotMember = errors.New("subject is not a member of this tenant")

// ClaimMembership allows a request when the named claim on the Principal
// names the resolved tenant, either by id or by subdomain. The claim may hold
// one string or a list of them, so a subject that belongs to several tenants
// works.
//
// It issues no queries: the answer is already in the token. Use it with
// providers that put organisation membership in claims, such as Auth0
// "org_id", Clerk "org_slug" and WorkOS "organization_id", or with your own
// JWT. Matching by subdomain needs the full tenant record, which
// httpmw.ResolveTenant puts in the context.
func ClaimMembership(claim string) Membership {
	return MembershipFunc(func(ctx context.Context, _ string, tenantID uuid.UUID) error {
		p, ok := PrincipalFromContext(ctx)
		if !ok {
			return fmt.Errorf("%w: no principal in context", ErrNotMember)
		}
		raw, ok := p.Claim(claim)
		if !ok {
			return fmt.Errorf("%w: principal has no %q claim", ErrNotMember, claim)
		}

		want := []string{tenantID.String()}
		if t, ok := TenantObjectFromContext(ctx); ok && t.Subdomain != "" {
			want = append(want, t.Subdomain)
		}
		for _, got := range claimStrings(raw) {
			for _, w := range want {
				if strings.EqualFold(got, w) {
					return nil
				}
			}
		}
		return fmt.Errorf("%w: %q claim does not name it", ErrNotMember, claim)
	})
}

// claimStrings normalises a claim value to a list of strings. It accepts a
// string, a []string, and the []any that encoding/json produces. Anything
// else yields no candidates, which denies the request.
func claimStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tenant/ -run 'TestMembership|TestClaimMembership' -v`
Expected: PASS, 8 tests (the list test has two subtests).

- [ ] **Step 5: Verify the package still builds clean**

Run: `go build ./... && go vet ./... && go test ./tenant/`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add tenant/membership.go tenant/membership_test.go
git commit -m "feat(tenant): add the Membership seam and a claim-based implementation

Membership is the single authorization decision the library makes, always
supplied by the application. ClaimMembership answers from a claim already in
the token and issues no queries; it matches the tenant by id or subdomain and
accepts a scalar or list claim.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Host-aware skipping

`SkipPaths` cannot express "this whole vhost is the login origin". Add `SkipHosts` and route every existing skip check through one helper, so Task 4 inherits it.

**Files:**
- Modify: `middleware/httpmw/middleware.go:20-32` (Config), `:87` `:130` (the `shouldSkipPath` call sites in `ResolveTenant` and `ValidateTenant`), `:168` (the call site in `EnforceLimits`), `:293-300` (`shouldSkipPath`)
- Modify: `middleware/httpmw/middleware_test.go` (append tests)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `httpmw.Config.SkipHosts []string`, and the unexported `func (m *Middleware) shouldSkip(r *http.Request) bool`, which Task 4 calls.

- [ ] **Step 1: Write the failing test**

Append to `middleware/httpmw/middleware_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./middleware/httpmw/ -run 'TestResolveTenantSkipsConfiguredHost|TestSkipHost' -v`
Expected: FAIL — compilation error, `unknown field SkipHosts in struct literal of type Config`.

- [ ] **Step 3: Add the config field**

In `middleware/httpmw/middleware.go`, add to `Config` immediately after `SkipPaths`:

```go
	// SkipHosts are hosts that bypass ResolveTenant entirely, matched against
	// r.Host without its port and ignoring case. Use it for an origin that
	// serves no tenant, such as a single sign-on host like "auth.app.com",
	// where resolution would necessarily fail.
	SkipHosts []string
```

- [ ] **Step 4: Replace the skip helper**

In `middleware/httpmw/middleware.go`, replace the existing `shouldSkipPath` method with:

```go
// shouldSkip reports whether the request bypasses tenant handling, either
// because its path is under a SkipPaths prefix or its host is in SkipHosts.
func (m *Middleware) shouldSkip(r *http.Request) bool {
	return m.shouldSkipPath(r.URL.Path) || m.shouldSkipHost(r.Host)
}

func (m *Middleware) shouldSkipPath(path string) bool {
	for _, p := range m.config.SkipPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (m *Middleware) shouldSkipHost(host string) bool {
	if len(m.config.SkipHosts) == 0 {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	for _, sh := range m.config.SkipHosts {
		if strings.EqualFold(host, sh) {
			return true
		}
	}
	return false
}
```

`net` and `strings` are already imported by this file.

- [ ] **Step 5: Update the three call sites**

In `middleware/httpmw/middleware.go`, change each of the three occurrences of

```go
			if m.shouldSkipPath(r.URL.Path) {
```

to

```go
			if m.shouldSkip(r) {
```

They are in `ResolveTenant`, `ValidateTenant` and the closure returned by the `EnforceLimits` method. Confirm with `grep -n 'shouldSkipPath(r.URL.Path)' middleware/httpmw/middleware.go` that none remain.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./middleware/httpmw/ -v`
Expected: PASS, including the pre-existing `SkipPaths` tests.

- [ ] **Step 7: Commit**

```bash
git add middleware/httpmw/middleware.go middleware/httpmw/middleware_test.go
git commit -m "feat(httpmw): add Config.SkipHosts for origins that serve no tenant

A single sign-on origin is usually its own host, where ResolveTenant can only
fail. SkipPaths cannot express that. Host matching ignores port and case.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: RequireMembership middleware

The enforcement point, wired to the seam from Task 2. This is the task that actually closes the hole.

**Files:**
- Create: `middleware/httpmw/membership.go`
- Create: `middleware/httpmw/membership_test.go`
- Modify: `middleware/httpmw/middleware.go:34-47` (the `membership` field and the `WithMembership` option), `:74-80` (`Standard`)
- Modify: `middleware/httpmw/errors.go:38-52` (`statusForCode`)

**Interfaces:**
- Consumes: `tenant.Membership`, `tenant.PrincipalFromContext` (Tasks 1–2); `(*Middleware).shouldSkip` (Task 3).
- Produces: `func httpmw.WithMembership(m tenant.Membership) Option`, `func (*httpmw.Middleware) RequireMembership() func(http.Handler) http.Handler`, and the error codes `USER_NOT_AUTHENTICATED` (401) and `ACCESS_DENIED` (403).

- [ ] **Step 1: Write the failing test**

Create `middleware/httpmw/membership_test.go`:

```go
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
		t.Errorf("code = %q, want DATABASE_ERROR - the chain stopped before SetTenantDB", got)
	}
}

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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./middleware/httpmw/ -run 'TestRequireMembership|TestStandard' -v`
Expected: FAIL — compilation error, `undefined: WithMembership`, `mw.RequireMembership undefined`.

- [ ] **Step 3: Add the field and the option**

In `middleware/httpmw/middleware.go`, add to the `Middleware` struct after `limits`:

```go
	membership tenant.Membership
```

and add the option beside `WithLimits`:

```go
// WithMembership supplies the authorization check RequireMembership applies.
// Without it, RequireMembership denies every request; see its documentation
// for why that differs from WithLimits.
func WithMembership(m tenant.Membership) Option {
	return func(mw *Middleware) { mw.membership = m }
}
```

- [ ] **Step 4: Create `middleware/httpmw/membership.go`**

```go
package httpmw

import (
	"net/http"

	"github.com/alexalmadav/go-multitenant/tenant"
	"go.uber.org/zap"
)

// RequireMembership rejects requests whose authenticated subject does not
// belong to the resolved tenant. Place it after ResolveTenant and after the
// application's own auth middleware, which must put the caller in the request
// context with tenant.WithPrincipal or tenant.WithUserID.
//
// Requests on a skipped path or host pass through untouched. A request with
// no principal is rejected with USER_NOT_AUTHENTICATED (401); a principal the
// Membership refuses is rejected with ACCESS_DENIED (403), and the refusal's
// cause is logged rather than returned to the client.
//
// Unlike WithLimits, a Membership that was never configured does not disable
// the check: it denies every request and logs the misconfiguration. A missed
// limit check costs money; a missed membership check serves one tenant's data
// to another.
func (m *Middleware) RequireMembership() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m.shouldSkip(r) {
				next.ServeHTTP(w, r)
				return
			}

			tc, ok := tenant.GetTenantFromContext(r.Context())
			if !ok {
				m.config.ErrorHandler(w, r, &tenant.TenantError{
					Code: "TENANT_CONTEXT_MISSING", Message: "Tenant context not found - ensure ResolveTenant middleware is applied first"})
				return
			}

			p, ok := tenant.PrincipalFromContext(r.Context())
			if !ok {
				m.config.ErrorHandler(w, r, &tenant.TenantError{
					TenantID: tc.TenantID, Code: "USER_NOT_AUTHENTICATED", Message: "Authentication required"})
				return
			}

			if m.membership == nil {
				m.logger.Error("RequireMembership is applied without WithMembership; denying the request",
					zap.String("tenant_id", tc.TenantID.String()))
				m.deny(w, r, tc.TenantID)
				return
			}

			if err := m.membership.Allow(r.Context(), p.Subject, tc.TenantID); err != nil {
				m.logger.Warn("Membership check denied the request",
					zap.String("tenant_id", tc.TenantID.String()),
					zap.String("subject", p.Subject),
					zap.Error(err))
				m.deny(w, r, tc.TenantID)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// deny writes the sanitised refusal. The cause has already been logged.
func (m *Middleware) deny(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID) {
	m.config.ErrorHandler(w, r, &tenant.TenantError{
		TenantID: tenantID, Code: "ACCESS_DENIED", Message: "Access denied to this tenant"})
}
```

Add `"github.com/google/uuid"` to this file's imports for `deny`.

- [ ] **Step 5: Insert the check into `Standard`**

In `middleware/httpmw/middleware.go`, replace the body of `Standard` and extend its doc comment:

```go
// Standard is ResolveTenant, ValidateTenant, RequireMembership, EnforceLimits
// and SetTenantDB in that order. RequireMembership is included only when New
// was given WithMembership, so the bundle never denies every request because
// an option was forgotten; apply RequireMembership by hand for the
// fail-closed behaviour. EnforceLimits is a pass-through unless New was given
// WithLimits.
func (m *Middleware) Standard() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		mws := []func(http.Handler) http.Handler{m.ResolveTenant(), m.ValidateTenant()}
		if m.membership != nil {
			mws = append(mws, m.RequireMembership())
		}
		mws = append(mws, m.EnforceLimits(), m.SetTenantDB())
		return Chain(next, mws...)
	}
}
```

- [ ] **Step 6: Map the new codes to statuses**

In `middleware/httpmw/errors.go`, add two cases to `statusForCode` before `default`:

```go
	case "USER_NOT_AUTHENTICATED":
		return http.StatusUnauthorized
	case "ACCESS_DENIED":
		return http.StatusForbidden
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./middleware/httpmw/ -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 8: Verify the whole module**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 9: Commit**

```bash
git add middleware/httpmw/membership.go middleware/httpmw/membership_test.go middleware/httpmw/middleware.go middleware/httpmw/errors.go
git commit -m "feat(httpmw): enforce tenant membership with RequireMembership

Closes the cross-tenant gap: a caller authenticated for one tenant could send
the same credential to another tenant's origin and be scoped to its schema.

RequireMembership fails closed when no Membership is configured, unlike the
pass-through WithLimits, because the cost of the two mistakes is not
comparable. Standard includes the check only when WithMembership was given.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Wire membership into `multitenant.New` and document it

**Files:**
- Modify: `multitenant.go:22-30` (Config), `:96-108` (option assembly)
- Modify: `README.md:238-256` (Available Middleware), `README.md:478-492` (Access Control)

**Interfaces:**
- Consumes: `tenant.Membership` (Task 2), `httpmw.WithMembership` (Task 4).
- Produces: `multitenant.Config.Membership tenant.Membership`.

- [ ] **Step 1: Write the failing test**

Create `membership_wiring_test.go` in the repo root:

```go
package multitenant

import "testing"

// Membership must default to nil. A non-nil default would turn enforcement on
// for every application that never asked for it, and Standard would then
// reject callers that carry no principal.
//
// That a configured value reaches the middleware is covered by httpmw's
// TestStandardEnforcesMembershipWhenConfigured; asserting it through New
// would need a live database.
func TestDefaultConfigHasNoMembership(t *testing.T) {
	if got := DefaultConfig().Membership; got != nil {
		t.Errorf("DefaultConfig().Membership = %v, want nil", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run TestDefaultConfigHasNoMembership -v`
Expected: FAIL — compilation error, `DefaultConfig().Membership undefined`.

- [ ] **Step 3: Add the config field**

In `multitenant.go`, add to `Config` after `Limits`:

```go
	// Membership authorises an authenticated subject for the resolved tenant.
	// When set, HTTPMiddleware.Standard enforces it. Nil leaves the check out
	// of Standard; HTTPMiddleware.RequireMembership then denies every
	// request, which is deliberate — see package httpmw.
	Membership tenant.Membership
```

- [ ] **Step 4: Forward it to the middleware**

In `multitenant.go`, immediately after the `if config.Limits != nil { ... }` block that appends to `mwOpts`, add:

```go
	if config.Membership != nil {
		mwOpts = append(mwOpts, httpmw.WithMembership(config.Membership))
	}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test . -run TestDefaultConfigHasNoMembership -v`
Expected: PASS.

- [ ] **Step 6: Rewrite the README's middleware list**

In `README.md`, replace the `### Available Middleware` code block and the paragraph beneath it (currently `README.md:242-256`) with:

````markdown
`mt.HTTPMiddleware` (package `httpmw`, `net/http`) provides six middlewares:

```go
mt.HTTPMiddleware.ResolveTenant()      // Resolves tenant from request
mt.HTTPMiddleware.ValidateTenant()     // Validates tenant status
mt.HTTPMiddleware.RequireMembership()  // Checks the caller belongs to the tenant
mt.HTTPMiddleware.EnforceLimits()      // Enforces plan limits
mt.HTTPMiddleware.SetTenantDB()        // Sets up tenant database context
mt.HTTPMiddleware.LogAccess()          // Logs tenant access
```

This library authenticates nobody. Put your own auth middleware ahead of these
in the chain and have it call `tenant.WithPrincipal` (or `tenant.WithUserID`,
its subject-only shorthand) on the request context. `LogAccess` reads the
subject, and `RequireMembership` authorises it.
````

- [ ] **Step 7: Rewrite the README's Access Control section**

In `README.md`, replace the `### Access Control` paragraph (currently `README.md:482-492`) with:

````markdown
Resolving a tenant is not the same as being entitled to it. Without a
membership check, a caller authenticated at `acme.app.com` can send the same
credential to `globex.app.com` and be scoped to Globex's schema.

`Membership` is the one authorization decision this library makes, and you
supply it:

```go
type Membership interface {
    Allow(ctx context.Context, subject string, tenantID uuid.UUID) error
}
```

When the tenant is already named in the token — Auth0 `org_id`, Clerk
`org_slug`, WorkOS `organization_id` — no query is needed. The claim may hold
one value or a list, and may name the tenant by id or by subdomain:

```go
cfg.Membership = tenant.ClaimMembership("org_id")
```

When the answer lives in your own database, close over it:

```go
cfg.Membership = tenant.MembershipFunc(func(ctx context.Context, subject string, id uuid.UUID) error {
    var ok bool
    err := db.QueryRowContext(ctx,
        `SELECT EXISTS (SELECT 1 FROM memberships WHERE user_id = $1 AND tenant_id = $2)`,
        subject, id).Scan(&ok)
    if err != nil {
        return err
    }
    if !ok {
        return tenant.ErrNotMember
    }
    return nil
})
```

The library owns no membership table: your identity provider or your own
schema already holds that, with your own subject type, and a second copy would
only drift.

`RequireMembership` **fails closed**. With no `Membership` configured it denies
every request rather than passing them through, because a missed limit check
costs money while a missed membership check serves one tenant's data to
another. `Standard()` includes the check only when a `Membership` is
configured, so the convenience bundle cannot silently deny everything.

Role and permission checks stay yours — the library has no role model.

Authentication itself is unaffected by tenancy in the common case: keep your
users in `public`, give Goth, Authboss, go-pkgz/auth or golang-jwt a plain
`*sql.DB`, and let tenancy enter only at `RequireMembership`:

```go
handler := httpmw.Chain(mux,
    authMiddleware,                        // yours; calls tenant.WithPrincipal
    mt.HTTPMiddleware.ResolveTenant(),
    mt.HTTPMiddleware.RequireMembership(),
    mt.HTTPMiddleware.SetTenantDB(),
)
```

If your login lives on its own origin, list it in `Config.SkipHosts` so
`ResolveTenant` does not try to resolve a tenant there:

```go
httpmw.Config{SkipHosts: []string{"auth.app.com"}}
```
````

- [ ] **Step 8: Point the README at the non-default integration patterns**

The spec documents two cases this plan deliberately does not implement. Add
them to `README.md` at the end of the Access Control section, so a reader who
needs them is not left guessing:

````markdown
Two cases need more than the above, and are described in
`docs/superpowers/specs/2026-09-21-membership-design.md`:

- **Users inside the tenant schema.** The auth library's storer must resolve
  the tenant per call, so its storage interface has to take a
  `context.Context` — Authboss's `ServerStorer.Load(ctx, key)` does,
  go-pkgz/auth's `CredChecker.Check(user, password)` does not. Note that
  `tenant.Conn` is not `*sql.DB`-shaped: `QueryContext` returns `*tenant.Rows`
  because the pooler-safe `SET LOCAL` design needs something to own and commit
  the wrapping transaction. Write such storers against `*tenant.Conn`.
- **Per-tenant auth configuration** (tenant A on Okta, tenant B on Google).
  Needs one auth-library instance per tenant, cached. Goth forces this when
  tenants bring their own OAuth apps, because `goth.UseProviders` and
  `gothic.Store` are package-level globals.

Two things bite regardless: a session cookie scoped to `.app.com` gives
cross-tenant SSO but reaches every tenant subdomain and rules out the
`__Host-` prefix; and OAuth redirect URIs must be pre-registered, with
wildcard subdomains mostly unsupported, so the callback belongs on one origin
with the tenant carried in the `state` parameter.
````

- [ ] **Step 9: Verify the whole module**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 10: Commit**

```bash
git add multitenant.go membership_wiring_test.go README.md
git commit -m "feat: configure Membership from multitenant.Config and document it

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Gin adapter

The Gin module is a separate Go module with a `replace` directive to the checkout (`middleware/gin/go.mod:44`), so it builds and tests on its own.

**Files:**
- Modify: `middleware/gin/middleware.go:22-38` (Config), `:51-67` (`NewMiddleware`), `:113-128` (add the handler), `:1-9` (package doc)
- Modify: `middleware/gin/middleware_test.go` (append tests)

**Interfaces:**
- Consumes: `tenant.Membership`, `tenant.WithPrincipal` (Tasks 1–2), `httpmw.WithMembership`, `(*httpmw.Middleware).RequireMembership` (Task 4).
- Produces: `gin.Config.Membership`, `gin.Config.SkipHosts`, `func (*gin.Middleware) RequireMembership() gin.HandlerFunc`.

- [ ] **Step 1: Write the failing test**

Append to `middleware/gin/middleware_test.go`. Every import and helper it uses
(`stubManager`, `newRouter`, `context`, `tenant`, `uuid`, `zap`) is already in
that file.

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd middleware/gin && go test ./... -run TestAdapter_RequireMembership -v`
Expected: FAIL — compilation error, `unknown field Membership in struct literal of type Config`.

- [ ] **Step 3: Add the config fields**

In `middleware/gin/middleware.go`, add to `Config` after `Limits`:

```go
	// Membership authorises the caller for the resolved tenant in
	// RequireMembership. Nil makes RequireMembership deny every request; see
	// package httpmw.
	Membership tenant.Membership
	// SkipHosts are hosts that bypass tenant resolution, such as a single
	// sign-on origin. Matched against the request host without its port,
	// ignoring case.
	SkipHosts []string
```

- [ ] **Step 4: Forward both to the core middleware**

In `middleware/gin/middleware.go`, in `NewMiddleware`, extend the `coreCfg` literal with `SkipHosts` and append the option:

```go
	coreCfg := httpmw.Config{SkipPaths: cfg.SkipPaths, SkipHosts: cfg.SkipHosts, ClientIP: cfg.ClientIP}
```

and, after the `if cfg.Limits != nil { ... }` block:

```go
	if cfg.Membership != nil {
		opts = append(opts, httpmw.WithMembership(cfg.Membership))
	}
```

- [ ] **Step 5: Add the handler**

In `middleware/gin/middleware.go`, add after `ValidateTenant`:

```go
// RequireMembership rejects callers who do not belong to the resolved tenant.
// Set the caller with tenant.WithPrincipal on the request context, or with
// c.Set("user_id", ...) for a subject with no claims.
func (m *Middleware) RequireMembership() gin.HandlerFunc {
	return m.adapt(m.core.RequireMembership())
}
```

- [ ] **Step 6: Update the package doc**

In `middleware/gin/middleware.go`, append to the package comment (after the sentence listing the Gin context keys):

```go
// The adapter promotes a "user_id" set on the Gin context to a
// tenant.Principal when the request context carries none, so existing
// applications keep working; set a principal with claims directly on the
// request context with tenant.WithPrincipal.
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `cd middleware/gin && go build ./... && go vet ./... && go test ./... -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 8: Commit**

```bash
git add middleware/gin/middleware.go middleware/gin/middleware_test.go
git commit -m "feat(gin): expose RequireMembership, Membership and SkipHosts

The existing c.Set(\"user_id\", ...) bridge satisfies the principal
requirement, so applications that already set it need no change.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Final verification

- [ ] Run `go build ./... && go vet ./... && go test ./...` from the repo root. Expected: all PASS.
- [ ] Run `cd middleware/gin && go build ./... && go vet ./... && go test ./...`. Expected: all PASS.
- [ ] Run `cd examples && go build ./...`. Expected: success — no example uses a removed symbol.
- [ ] Run `grep -rn 'shouldSkipPath(r.URL.Path)' middleware/httpmw/`. Expected: no matches; every call site uses `shouldSkip(r)`.
- [ ] Run `grep -rn 'tenant_users' database/ tenant/ multitenant.go`. Expected: no matches — no master membership table was added.
- [ ] Confirm `git diff master --stat -- go.mod go.sum` is empty: no new dependencies.

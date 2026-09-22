# Membership and Principal — design

**Status:** accepted, 2026-09-21
**Supersedes:** the `ValidateAccess` / `RequireAdmin` surface removed in v0.8.

## Problem

The library resolves a tenant from the request and hands the handler a
connection scoped to that tenant's schema. Nothing checks that the caller is
entitled to that tenant. A user authenticated at `acme.app.com` can send the
same credential to `globex.app.com` and `httpmw.SetTenantDB` will scope them
to Globex's schema.

No general-purpose Go auth library closes this. Goth, go-pkgz/auth, Authboss,
Limen and golang-jwt all answer "who is this?". None answers "which tenants
may this subject enter?", because none of them knows tenants exist. That
question is this library's to answer.

The previous attempt (`Manager.ValidateAccess`) failed for three reasons, and
the replacement must not repeat them:

1. It was a method on the 20-method `Manager` interface, so it could not be
   replaced without reimplementing everything else.
2. Its only implementation ignored the user id and returned nil.
3. It hid behind a `RequireAuthentication: true` config default, so it looked
   enforced while enforcing nothing.

## Non-goals

- Authenticating anyone. No passwords, tokens, sessions or cookies.
- Owning a membership table. A `public.tenant_users` master table would
  collide by name with the per-tenant example table in `testdata/migrations`,
  fix the subject type forever (UUID excludes `auth0|abc`; TEXT annoys
  everyone else), duplicate a source of truth that Auth0, Clerk and WorkOS
  already hold, and be created in every adopter's database by
  `CreateMasterTables` whether wanted or not.
- A role or permission model. `RequireAdmin`'s hardcoded `"admin"` string was
  an opinion worth losing; nothing here brings it back.
- Adapter modules for specific auth libraries. If the seam is right, each one
  is ~15 lines of application code.

## Design

### Principal

`tenant.WithUserID(ctx, string)` already carries the caller for the access
log. Widen it to carry claims too, because the cheapest correct membership
check reads a claim that is already in the token.

    type Principal struct {
        Subject string
        Claims  map[string]any
    }

`WithPrincipal` stores the subject under the existing `ContextKeyUserID` as
well, so `UserIDFromContext`, `LogAccess` and the Gin adapter's `user_id`
bridge keep working unchanged. `WithUserID` becomes a wrapper. No breaking
change.

### Membership

One interface, supplied entirely by the application:

    type Membership interface {
        Allow(ctx context.Context, subject string, tenantID uuid.UUID) error
    }

with a `MembershipFunc` adapter, so the common case is a closure over the
application's own table, with the application's own subject type.

The shipped default is `ClaimMembership(claim string)`: the request is allowed
when the named claim names the resolved tenant, by id or by subdomain. It
issues no queries. The claim may hold one string or a list, so a user who
belongs to several tenants works. This covers Auth0 `org_id`, Clerk
`org_slug`, WorkOS `organization_id` and hand-rolled JWTs.

The precedent is `ResolverConfig.ValidateSubdomain`: a hook with a sensible
default, not a table.

### Middleware

`httpmw.RequireMembership()` runs after `ResolveTenant` and after the
application's auth middleware.

- No tenant in context → `TENANT_CONTEXT_MISSING` (500; the chain is
  misconfigured).
- No principal in context → `USER_NOT_AUTHENTICATED` (401).
- `Allow` returns an error → `ACCESS_DENIED` (403), cause logged, not
  returned to the client.

**Fail closed.** `WithLimits(nil)` is a pass-through; `RequireMembership` with
no configured `Membership` denies every request and logs the
misconfiguration. A missed limit check costs money, a missed membership check
leaks another tenant's data.

`Standard()` includes the check only when `WithMembership` was supplied, so
the convenience bundle never silently denies everything. Reaching for
`RequireMembership()` by hand is a statement of intent and is held to the
stricter rule.

### Host-aware skipping

`Config.SkipPaths` matches path prefixes only. A single sign-on origin is
usually a separate host (`auth.app.com`), where `ResolveTenant` necessarily
fails. Add `Config.SkipHosts`, matched case-insensitively against `r.Host`
with any port stripped.

## Integration patterns (documentation deliverable)

**A — global identity (default).** Users live in `public`; the auth library
gets a plain `*sql.DB` and never hears the word tenant. Chain: auth →
`ResolveTenant` → `RequireMembership` → `SetTenantDB`. Works unmodified with
every library named above.

**B — users inside the tenant schema.** The storer must resolve the tenant per
call, so the library's storage interface must take a `context.Context`
(Authboss's `ServerStorer.Load(ctx, key)` does; go-pkgz's
`CredChecker.Check(user, password)` does not). Note that `tenant.Conn` is not
`*sql.DB`-shaped — `QueryContext` returns `*tenant.Rows` because the
pooler-safe `SET LOCAL` design needs something to own and commit the wrapping
transaction. Storers are written against `*tenant.Conn`.

**C — per-tenant auth configuration.** One auth-library instance per tenant,
cached. Required for Goth when tenants bring their own OAuth apps, because
`goth.UseProviders` and `gothic.Store` are package-level globals.

Cross-cutting, documented not solved: session cookies scoped to `.app.com`
give cross-tenant SSO but reach every subdomain and rule out `__Host-`;
OAuth redirect URIs must be pre-registered, so the callback lives on one
origin with the tenant carried in `state`.

## Scope

Pattern A only. Patterns B and C are documented, not implemented.

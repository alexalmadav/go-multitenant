# Go Multi-Tenant

[![Go Version](https://img.shields.io/badge/go-%3E%3D1.22-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/alexalmadav/go-multitenant)](https://goreportcard.com/report/github.com/alexalmadav/go-multitenant)

A comprehensive multi-tenant solution for Go applications using a **schema-per-tenant** PostgreSQL architecture. This library provides complete tenant isolation, middleware integration, and sophisticated tenant management capabilities.

## 🚀 Features

- **Complete Tenant Isolation**: Schema-per-tenant architecture with PostgreSQL
- **Flexible Tenant Resolution**: Support for subdomain, path, and header-based tenant resolution
- **Framework-Neutral Middleware**: Core `net/http` middleware that works with any router, plus a drop-in Gin adapter
- **Plan & Limit Management**: Built-in support for tenant plans and usage limits
- **Database Migration System**: Per-tenant migration tracking and management
- **Comprehensive Logging**: Structured logging with tenant context
- **Production Ready**: Battle-tested patterns from real multi-tenant applications

## 📦 Installation

```bash
go get github.com/alexalmadav/go-multitenant
```

## 🏗️ Architecture

The library implements a **schema-per-tenant** architecture where:

- **Master Database**: Contains tenant registry, users, and global data
- **Tenant Schemas**: Each tenant gets an isolated PostgreSQL schema (`tenant_{uuid}`)
- **Complete Isolation**: No cross-tenant data leakage possible
- **Independent Scaling**: Each tenant can be managed independently

```
Database
├── public (master schema)
│   ├── tenants
│   ├── tenant_migrations
│   └── master tables...
├── tenant_acme-corp-uuid
│   └── (tables from your migrations)
└── tenant_globex-uuid
    └── (tables from your migrations)
```

## 🚀 Quick Start

### Quick Start (net/http)

```go
config := multitenant.DefaultConfig()
config.Database.DSN = "postgres://user:pass@localhost/db?sslmode=disable"
config.Database.MigrationsDir = "./migrations"
config.Resolver.Strategy = multitenant.ResolverSubdomain
config.Resolver.Domain = "myapp.com"

// New requires you to decide whether callers are checked against the tenant
// they reach. This quick start has no auth, so it opts out on the record; set
// config.Membership before serving real traffic (see Access Control).
config.InsecureSkipMembership = true

// Limits are opt-in: config.Limits is nil (no enforcement) unless you set it.
l := limits.ExampleConfig()
config.Limits = &l

mt, err := multitenant.New(config)
if err != nil { log.Fatal(err) }
defer mt.Close()

mux := http.NewServeMux()
mux.HandleFunc("/api/dashboard", func(w http.ResponseWriter, r *http.Request) {
    t, _ := tenant.GetTenantFromContext(r.Context())
    fmt.Fprintf(w, "Welcome to %s", t.Subdomain)
})

// Resolve, validate, enforce limits, and scope a DB connection per request.
handler := mt.HTTPMiddleware.Standard()(mux)
log.Fatal(http.ListenAndServe(":8080", handler))
```

Works with chi, gorilla/mux and anything else that takes `func(http.Handler) http.Handler`.

### Quick Start (Gin)

The Gin adapter is a separate module so the core does not depend on Gin:

```bash
go get github.com/alexalmadav/go-multitenant/middleware/gin@v0.8.0
```

```go
ginMw := ginmiddleware.NewMiddleware(mt.Manager, mt.Resolver, mt.GetLogger(), ginmiddleware.Config{
    SkipPaths: []string{"/health"},
})
api := r.Group("/api")
// Add ginMw.RequireMembership() once you have auth; see Access Control below.
api.Use(ginMw.ResolveTenant(), ginMw.ValidateTenant(), ginMw.EnforceLimits(), ginMw.SetTenantDB())
```

### Advanced Usage with Billing

```go
// Configure custom plan limits (-1 means unlimited)
basic := make(limits.FlexibleLimits)
basic.Set("max_users", limits.LimitTypeInt, 5)
basic.Set("max_projects", limits.LimitTypeInt, 10)
basic.Set("max_storage_gb", limits.LimitTypeInt, 1)

pro := make(limits.FlexibleLimits)
pro.Set("max_users", limits.LimitTypeInt, 25)
pro.Set("max_projects", limits.LimitTypeInt, 100)
pro.Set("max_storage_gb", limits.LimitTypeInt, 10)

config.Limits.PlanLimits = map[string]limits.FlexibleLimits{
    limits.PlanBasic: basic,
    limits.PlanPro:   pro,
}

// Limits are checked against live counts in the tenant schema. Map each
// limit name to the tenant-schema table whose row count is its usage:
config.Limits.UsageTables = map[string]string{"max_projects": "projects", "max_users": "tenant_users"}

// Only limits listed here (or served by a custom UsageTracker) are checked.
// Swap in your own tracker or add limits at runtime through mt.Limits, the
// limits.Checker built from config.Limits:
mt.Limits.SetUsageTracker(myTracker)
mt.Limits.AddLimit(limits.PlanPro, "beta_features", limits.LimitTypeBool, true)

// Create middleware with custom error handling. Pass Limits so EnforceLimits
// checks plan limits instead of being a pass-through.
ginConfig := ginmiddleware.Config{
    SkipPaths: []string{"/health", "/billing/"},
    Limits:    mt.Limits,
    ErrorHandler: func(c *gin.Context, err error) {
        if tenantErr, ok := err.(*multitenant.TenantError); ok {
            if tenantErr.Code == "PLAN_LIMIT_EXCEEDED" {
                c.JSON(http.StatusPaymentRequired, gin.H{
                    "error": tenantErr.Message,
                    "upgrade_url": "/billing/upgrade",
                })
                return
            }
        }
        // Default error handling...
    },
}
```

## 🎯 Tenant Resolution Strategies

### Subdomain Resolution (Recommended)

```go
config.Resolver.Strategy = multitenant.ResolverSubdomain
config.Resolver.Domain = "myapp.com"

// tenant1.myapp.com -> resolves to "tenant1"
// tenant2.myapp.com -> resolves to "tenant2"
```

### Path-based Resolution

```go
config.Resolver.Strategy = multitenant.ResolverPath
config.Resolver.PathPrefix = "/tenant/"

// myapp.com/tenant/tenant1/api -> resolves to "tenant1"
// myapp.com/tenant/tenant2/api -> resolves to "tenant2"
```

### Header-based Resolution

```go
config.Resolver.Strategy = multitenant.ResolverHeader
config.Resolver.HeaderName = "X-Tenant-ID"

// X-Tenant-ID: tenant1 -> resolves to "tenant1"
```

## 🔧 Configuration

### Database Configuration

```go
config.Database = multitenant.DatabaseConfig{
    DSN:                "postgres://user:pass@localhost/db?sslmode=disable",
    MaxOpenConns:        100,
    MaxIdleConns:        50,
    ConnMaxLifetime:     15 * time.Minute,
    SchemaPrefix:        "tenant_",     // Schema naming: tenant_{uuid}
    MigrationsDir:       "./migrations",
}
```

### Resolver Configuration

```go
config.Resolver = multitenant.ResolverConfig{
    Strategy:          multitenant.ResolverSubdomain,
    Domain:            "myapp.com",
    ReservedSubdomain: []string{"www", "api", "admin"},
}
```

### Limits Configuration

Limits are optional. The core package `multitenant` knows nothing about plans
or limits; enforcement lives in the separate `limits` package and only runs
when `config.Limits` is a non-nil `*limits.Config`. Leave it `nil` (the
`multitenant.DefaultConfig()` default) and `mt.Limits` is `nil` and
`EnforceLimits()` middleware becomes a pass-through.

```go
l := limits.Config{
    EnforceLimits: true,
    PlanLimits: map[string]limits.FlexibleLimits{
        limits.PlanBasic: basic, // see FLEXIBLE_LIMITS.md
        // ... more plans
    },
    // Limits are checked against live row counts. Map each limit name to
    // the tenant-schema table whose row count is its usage.
    UsageTables: map[string]string{"max_projects": "projects", "max_users": "tenant_users"},
    // PlanOf resolves a tenant's plan; nil (the default) uses t.Plan(),
    // i.e. metadata["plan"].
}
config.Limits = &l
```

`limits.ExampleConfig()` returns a ready-to-use three-plan (`basic`, `pro`,
`enterprise`) config to copy and adapt.

At runtime, change plan limits through `mt.Limits` (a `limits.Checker`):

```go
mt.Limits.AddLimit(limits.PlanPro, "beta_features", limits.LimitTypeBool, true)
usage, err := mt.Limits.Usage(ctx, tenantID) // map[string]int, one entry per UsageTables key
```

Middleware enforcement follows the same opt-in rule:
`mt.HTTPMiddleware.EnforceLimits()` is a pass-through unless `Config.Limits`
was set when `mt.HTTPMiddleware` was built; for the Gin adapter, pass
`ginmiddleware.Config{Limits: mt.Limits}` to get real enforcement.

## 🛠️ Middleware

### Available Middleware

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

### Middleware Chain Example

```go
handler := httpmw.Chain(mux,
    authMiddleware,                         // Your auth middleware; sets tenant.WithUserID
    mt.HTTPMiddleware.ResolveTenant(),      // Resolve tenant
    mt.HTTPMiddleware.ValidateTenant(),     // Validate tenant status
    mt.HTTPMiddleware.RequireMembership(),  // Authorise the caller for this tenant
    mt.HTTPMiddleware.EnforceLimits(),      // Check limits
    mt.HTTPMiddleware.SetTenantDB(),        // Set database context
    mt.HTTPMiddleware.LogAccess(),          // Log access
)
```

Or use `mt.HTTPMiddleware.Standard()` — `ResolveTenant`, `ValidateTenant`,
`RequireMembership`, `EnforceLimits` and `SetTenantDB` chained as a single
`func(http.Handler) http.Handler` (see [Quick Start](#quick-start-nethttp)).
`RequireMembership` is included only when a `Membership` is configured, so the
bundle never denies every request because an option was forgotten; apply
`RequireMembership` by hand for the fail-closed behaviour:

```go
handler := mt.HTTPMiddleware.Standard()(mux)
```

### Gin

The [Gin adapter](./middleware/gin) wraps the same six middlewares under the
same method names. It stores every value both in the request context (read
with package `tenant`'s helpers) and, for code that prefers it, under Gin
context keys read with `c.Get`:

| `c.Get` key | Type |
|---|---|
| `tenant` | `*tenant.Context` |
| `tenant_id` | tenant UUID string |
| `tenant_object` | `*tenant.Tenant` |
| `tenant_conn` | `*tenant.Conn` |
| `plan_limits` | `limits.FlexibleLimits`, set only when `Config.Limits` is set |

```go
api := r.Group("/api")
api.Use(authMiddleware())                  // Your auth middleware; sets tenant.WithUserID
api.Use(ginMw.ResolveTenant())             // Resolve tenant
api.Use(ginMw.ValidateTenant())            // Validate tenant status
api.Use(ginMw.RequireMembership())         // Authorise the caller for this tenant
api.Use(ginMw.EnforceLimits())             // Check limits
api.Use(ginMw.SetTenantDB())               // Set database context
api.Use(ginMw.LogAccess())                 // Log access
```

The adapter has no `Standard()` and bundles nothing: setting
`Config.Membership` alone enforces nothing, so `RequireMembership()` has to be
in the chain above or the check never runs.

Setting `c.Set("user_id", id)` from a Gin auth middleware still feeds the
access log — the adapter bridges it onto the request context automatically.
`tenant.WithUserID` on the request context is the framework-neutral way and
takes precedence if both are set.

Read the limits stored by `EnforceLimits` with
`ginmiddleware.GetTenantLimitsFromContext(c) (limits.FlexibleLimits, bool)`;
the `net/http` equivalent is `limits.FromContext(ctx) (limits.FlexibleLimits, bool)`.

## 🗄️ Database Operations

### Tenant-Aware Database Operations

With `SetTenantDB` in the chain, each request gets a dedicated connection
scoped to the tenant schema, released when the request ends. In `net/http`
handlers, read it with `tenant.GetTenantConnFromContext`:

```go
func getProjects(w http.ResponseWriter, r *http.Request) {
    conn, _ := tenant.GetTenantConnFromContext(r.Context())

    // This query only sees the current tenant's projects
    rows, err := conn.QueryContext(r.Context(),
        "SELECT * FROM projects WHERE status = $1", "active")
    if err != nil { /* ... */ }
    defer rows.Close() // also ends the statement's scoping transaction
    // ...
}
```

In a Gin handler, the adapter's `ginmiddleware.GetTenantConnFromContext`
does the same thing by reading the `tenant_conn` Gin context key:

```go
func getProjects(c *gin.Context) {
    conn, _ := ginmiddleware.GetTenantConnFromContext(c)
    rows, err := conn.QueryContext(c.Request.Context(),
        "SELECT * FROM projects WHERE status = $1", "active")
    if err != nil { /* ... */ }
    defer rows.Close()
    // ...
}
```

Each `ExecContext`, `QueryContext` and `QueryRowContext` on a `tenant.Conn`
runs in its own transaction that starts with `SET LOCAL search_path`, so the
connection never carries tenant state between statements. Use `conn.BeginTx`
or `Manager.WithTenantTx` to run several statements in one transaction.

### Manual Tenant Context

```go
// For background jobs or non-HTTP contexts, run inside a transaction
// scoped to the tenant schema:
err := mt.Manager.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
    _, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", name)
    return err
})

// Or hold a dedicated connection. Nothing is set on the session, so Close()
// simply returns it to the pool.
conn, err := mt.Manager.GetTenantConn(ctx, tenantID)
defer conn.Close()
```

### Connection poolers

The library never relies on session state: tenant scoping is always
`SET LOCAL` inside a transaction, migrations run in transactions, and the
pgx driver uses the simple protocol (no server-side prepared statements).
CI runs the full integration suite through PgBouncer in transaction mode.

| Deployment | `WithTenantTx` / `Conn.BeginTx` | `tenant.Conn` per-statement | Migrations |
|---|---|---|---|
| Direct connections | yes | yes | yes |
| PgBouncer / pgcat, session mode | yes | yes | yes |
| PgBouncer / pgcat, transaction mode | yes | yes | yes |
| Statement mode | no | no | no |

Statement mode breaks multi-statement transactions themselves and is not supported.

## 📋 Tenant Management

### Creating Tenants

```go
tenant := &multitenant.Tenant{
    ID:        uuid.New(),
    Name:      "Acme Corporation",
    Subdomain: "acme",
    Status:    multitenant.StatusPending,
}
tenant.SetPlan(limits.PlanPro) // set the plan on creation

// Create tenant record
err := mt.Manager.CreateTenant(ctx, tenant)

// Provision tenant schema
err = mt.Manager.ProvisionTenant(ctx, tenant.ID)
```

`CreateTenant` does not default the plan for you — a tenant created without
calling `SetPlan` has `Plan() == ""`, and with limit enforcement on,
`mt.Limits.CheckTenant` (and therefore `EnforceLimits` middleware) errors on
it as an unknown plan. Always set a plan on creation if you enforce limits.

### Managing Tenant Status

```go
// Suspend a tenant
err := mt.Manager.SuspendTenant(ctx, tenantID)

// Activate a tenant
err := mt.Manager.ActivateTenant(ctx, tenantID)

// Get tenant statistics
stats, err := mt.Manager.GetStats(ctx, tenantID)
// Returns: TenantID, SchemaExists, AppliedMigrations. For limit usage, see
// mt.Limits.Usage below.
```

`UpdateTenant` writes the struct you pass it wholesale — every field, not a
diff. Always pass a freshly-read tenant (`GetTenant`, or the one you already
have from an earlier call in the same request) and mutate that; if you pass a
copy with a stale `Status`, `UpdateTenant` reverts the persisted status to
that stale value and fires `OnTenantStatusChanged` for the reversion.

### Metadata

Every tenant has a `Metadata` map stored as JSONB and loaded with the tenant:

```go
t, _ := mt.Manager.GetTenant(ctx, id)
t.Metadata.SetString("custom_domain", "app.acme.com")
tenant.NewStripeExtension(t.Metadata).SetCustomerID("cus_123")
err := mt.Manager.UpdateTenant(ctx, t)
```

### Plan

A tenant's plan lives in `Metadata` too, under the `tenant.PlanKey` key
(`"plan"`), with typed convenience methods:

```go
t.SetPlan("pro")     // writes metadata["plan"]
plan := t.Plan()      // reads it back; "" if never set
err := mt.Manager.UpdateTenant(ctx, t)
```

The core `tenant` package never interprets the plan string — it is an opaque
value. Only the optional `limits` package (or your own code) gives it
meaning, by mapping plan names to `limits.FlexibleLimits` in
`limits.Config.PlanLimits`.

### Lifecycle hooks

Register a `tenant.Hook` to react to tenant events. Embed `tenant.BaseHook` and
override what you need. `ValidateMetadata` runs before a write and can block it;
the other events run after the write commits and their errors come back as a
`*tenant.HookError` while the tenant persists.

```go
type auditHook struct{ tenant.BaseHook }
func (auditHook) Name() string { return "audit" }
func (auditHook) OnTenantStatusChanged(ctx context.Context, t *tenant.Tenant, prev string) error {
    log.Printf("tenant %s: %s -> %s", t.ID, prev, t.Status)
    return nil
}

mt.Manager.RegisterHook(auditHook{})
```

See `examples/stripe-integration` for a hook that keeps an external system in sync.

## 🔒 Security Features

### Complete Tenant Isolation

- **Schema-level isolation**: Each tenant has a completely separate database schema
- **No cross-tenant queries**: Impossible to accidentally query another tenant's data
- **Middleware protection**: Multiple layers of tenant validation

### Access Control

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

`multitenant.New` **will not choose for you**. It returns an error unless the
`Config` sets either a `Membership` or `InsecureSkipMembership: true`, and it
refuses both at once. A forgotten option therefore breaks startup rather than
isolation, and running without the check is always written down:

```go
cfg.InsecureSkipMembership = true // any caller reaching a tenant's origin reaches its data
```

`New` logs a warning at startup when the opt-out is set.

Below `New`, the rule is the same in spirit. `RequireMembership` **fails
closed**: applied with no `Membership` configured, it denies every request
rather than passing them through, because a missed limit check costs money
while a missed membership check serves one tenant's data to another.
`httpmw.Standard()` includes the check only when a `Membership` is configured.
If you assemble `httpmw.New` or the Gin adapter yourself rather than going
through `multitenant.New`, the startup requirement does not apply, so add
`RequireMembership()` to your chain deliberately.

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

The request host is chosen by the client and `net/http`'s `ServeMux` does not
route on it, so only use `SkipHosts` where the front door pins the Host —
virtual-host routing, or a proxy that rejects unknown ones — and make sure the
handlers reachable on a skipped origin tolerate an absent tenant context.

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

### Input Validation

```go
// Automatic subdomain validation
// - Length requirements (3-50 characters)
// - Character restrictions (alphanumeric + hyphens)
// - Reserved subdomain protection
// - Format validation
```

The check itself is pluggable: `tenant.ResolverConfig.ValidateSubdomain`
defaults to `tenant.DefaultSubdomainValidator(ReservedSubdomain)` (the rules
above) but you can set your own `func(subdomain string) error` to change the
policy.

## 📊 Monitoring & Logging

### Structured Logging

```go
// All operations include tenant context in logs
{
  "level": "info",
  "msg": "Tenant access",
  "tenant_id": "123e4567-e89b-12d3-a456-426614174000",
  "subdomain": "acme",
  "user_id": "user-456",
  "method": "GET",
  "path": "/api/projects"
}
```

### Usage Statistics

```go
stats, err := mt.Manager.GetStats(ctx, tenantID)
// Returns: TenantID, SchemaExists, AppliedMigrations

// Limit usage (per limit in config.Limits.UsageTables) comes from the
// limits checker, not GetStats:
usage, err := mt.Limits.Usage(ctx, tenantID) // map[string]int; errors if any tracker read fails
```

## 🧪 Testing

Run the test suite:

```bash
go test ./...
```

Run with coverage:

```bash
go test -race -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## 📚 Examples

Check out the [examples](./examples/) directory:

- **[Basic Example](./examples/basic/)**: Simple multi-tenant setup
- **[Billing Example](./examples/with-billing/)**: Advanced setup with plan limits and billing
- **[Flexible Limits](./examples/flexible-limits/)**: Custom limit definitions and runtime limit management
- **[Stripe Integration](./examples/stripe-integration/)**: lifecycle hook keeping a Stripe customer in sync

### Running Examples

```bash
# Basic example
cd examples/basic
go run main.go

# Advanced billing example  
cd examples/with-billing
go run main.go

# Stripe integration example
cd examples/stripe-integration
go run .
```

## 🗃️ Database Schema

### Master Tables

```sql
-- Tenant registry
CREATE TABLE public.tenants (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    subdomain VARCHAR(255) UNIQUE NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    schema_name VARCHAR(255) NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_status CHECK (status IN ('active', 'suspended', 'pending', 'cancelled'))
);

-- Migration tracking
CREATE TABLE public.tenant_migrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    version VARCHAR(50) NOT NULL,
    name VARCHAR(255) NOT NULL,
    applied_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    rollback_sql TEXT,
    checksum VARCHAR(64),
    FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE,
    UNIQUE(tenant_id, version)
);
```

There is no plan column: the plan lives in `metadata["plan"]` (see
[Plan](#plan) above). Upgrading an existing v0.7 database? See
[Upgrading from v0.7](#upgrading-from-v07) below for what happens to the old
column.

### Tenant schema

The library creates no tables in a tenant schema. Point `config.Database.MigrationsDir`
at a directory of `<version>_<name>.up.sql` files (optional matching `.down.sql`).
`ProvisionTenant` creates the schema and applies every file in filename order,
recording each in `public.tenant_migrations`. Add a file later and run
`mt.Migrations.ApplyPendingToAllTenants(ctx)` to bring every active tenant up to
date; new tenants get it automatically.

If `MigrationsDir` is unset, provisioned tenants have an empty schema and `New`
logs a warning. A path that does not exist is an error from `New`.

`examples/with-billing` and `examples/stripe-integration` both set
`config.Database.MigrationsDir = "./migrations"`, but that directory is not
shipped with the repository — it is up to you to create it with your own
migration files before running either example. A minimal one, defining the
`projects` table those examples count usage against:

```sql
-- migrations/001_create_projects.up.sql
CREATE TABLE projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

## ⬆️ Upgrading from v0.8

v0.9.0 adds a membership check: resolving a tenant from a request is no longer
treated as permission to act inside it. See Access Control for the design.

- **`multitenant.New` requires a membership decision.** It returns an error
  unless `Config.Membership` or `Config.InsecureSkipMembership` is set. To
  keep v0.8 behaviour exactly, set the opt-out:
  ```go
  config.InsecureSkipMembership = true
  ```
  To close the gap instead, set a `Membership`: `tenant.ClaimMembership("org_id")`
  if your token carries the tenant, or a `tenant.MembershipFunc` over your own
  membership table. Your auth middleware must then put the caller in the
  request context with `tenant.WithPrincipal` (or `tenant.WithUserID`, which
  now does the same). `httpmw.New` and the Gin adapter are unchanged; the
  requirement applies only to `multitenant.New`.
- **A resolved tenant is always checked, whatever the skip lists say.**
  `ValidateTenant`, `EnforceLimits` and `RequireMembership` consult `SkipPaths`
  and `SkipHosts` only while no tenant has been resolved. This changes nothing
  for requests `ResolveTenant` skipped too. It changes behaviour only for a
  chain that rewrites the path after resolution, for example with
  `http.StripPrefix`, where a suspended or over-limit tenant previously
  slipped through.
- **`SkipPaths` and `SkipHosts` are now on `multitenant.Config`.** A nil
  `SkipPaths` keeps the previous built-in list (`/health`, `/metrics`,
  `/api/public/`), so an upgrade that sets neither sees no change.
- **Two new error codes:** `USER_NOT_AUTHENTICATED` (401) when
  `RequireMembership` finds no principal, or one with an empty subject, and
  `ACCESS_DENIED` (403) when the `Membership` refuses the caller.

## ⬆️ Upgrading from v0.7

v0.8.0 (part 1) replaces the Gin-only middleware with a framework-neutral
`net/http` core, and moves the Gin integration into its own module. v0.8.0
(part 2) moves plan and limit handling out of the core: the plan now lives in
tenant metadata, limits are an optional package, and `Manager`/`Config` no
longer know about either.

- **Gin is now a separate module.** The core package no longer imports Gin.
  If you use the Gin middleware, add the require line:
  ```bash
  go get github.com/alexalmadav/go-multitenant/middleware/gin@v0.8.0
  ```
  and import it as `ginmiddleware "github.com/alexalmadav/go-multitenant/middleware/gin"`.
- **`MultiTenant.GinMiddleware` is gone; `MultiTenant.HTTPMiddleware` replaces
  it.** `mt.HTTPMiddleware` is the `net/http` middleware (package `httpmw`),
  usable directly with `net/http`, chi, gorilla/mux, or anything that takes
  `func(http.Handler) http.Handler`. For Gin, build the adapter yourself:
  `ginmiddleware.NewMiddleware(mt.Manager, mt.Resolver, mt.GetLogger(), ginmiddleware.Config{...})`.
  It exposes the same method names (`ResolveTenant`, `ValidateTenant`,
  `EnforceLimits`, `SetTenantDB`, `LogAccess`) as `gin.HandlerFunc`s, and the
  same `c.Get` keys (`tenant`, `tenant_id`, `tenant_object`, `tenant_conn`,
  `plan_limits`) as before.
- **`RequireAdmin` and `RequireAuthentication` are removed.** They are gone
  from both `httpmw.Middleware` and the Gin adapter, and `ginmiddleware.Config`
  no longer has a `RequireAuthentication` field. Access and role checks are
  now entirely the application's responsibility: put your own auth middleware
  ahead of the tenant middleware in the chain, and call `tenant.WithUserID` on
  the request context from it if you want the user id to appear in
  `LogAccess`'s output. Note that `multitenant.New` previously constructed
  the Gin middleware with `RequireAuthentication: true`; after upgrading,
  requests that used to be rejected for a missing `user_id` reach your
  handlers until you add your own auth middleware.
- **The `tenant.Middleware` interface lost `RequireAdmin()`.** Any code that
  called it through the interface (rather than the concrete Gin type) no
  longer compiles; remove the call and enforce admin access in your own
  middleware.
- **`TENANT_INVALID_STATUS` now returns 403** (it returned 500 before). If
  you match on status codes rather than the `error.code` field, update that
  check.
- **The access log's logger name changed** from `gin_middleware` to
  `http_middleware` for both the `net/http` core and the Gin adapter (the Gin
  adapter is built on top of the core middleware, so it now shares the
  core's logger name). If you filter or route logs by logger name, update
  that filter.
- **`client_ip` in the access log now defaults to the host part of
  `r.RemoteAddr`**, which the client cannot spoof, instead of trusting
  `X-Forwarded-For`/`X-Real-IP` unconditionally. If you are behind a
  reverse proxy you control that sets `X-Forwarded-For`, opt back in with
  `Config{ClientIP: httpmw.ForwardedClientIP}` (or, for the Gin adapter,
  `ginmiddleware.Config{ClientIP: httpmw.ForwardedClientIP}`); make sure
  that proxy strips or overwrites inbound `X-Forwarded-For`/`X-Real-IP`
  headers before requests reach it, or clients can still choose the logged
  address.
- **Non-breaking:** `ResolveTenant` no longer fetches the tenant twice per
  request.

### Part 2: plan and limits move out of the core

- **`Tenant.PlanType` is gone; the plan lives in metadata.** Use
  `t.SetPlan("pro")` to set it and `t.Plan()` to read it (backed by
  `tenant.PlanKey`, `metadata["plan"]`). `New` moves an existing
  `plan_type` column's values into `metadata["plan"]` once, on first
  startup after the upgrade (for rows that don't already have a `plan`
  key). As part of that same one-time step, the column is made nullable
  with no default and every row's `plan_type` is set to `NULL`, so the
  move genuinely happens once — later restarts, and tenants created after
  the upgrade, never have a plan re-derived from the column. The column
  itself is left in place; nothing reads it after the first startup. Drop
  it yourself whenever you're ready:
  ```sql
  ALTER TABLE public.tenants DROP COLUMN plan_type;
  ```
- **`CreateTenant` no longer defaults the plan to `basic`.** A tenant created
  without calling `SetPlan` has `Plan() == ""`. With limit enforcement on,
  `CheckTenant` (and `EnforceLimits` middleware) on that tenant returns a
  `*tenant.TenantError` with code `PLAN_NOT_CONFIGURED` — an empty string is
  not a configured plan. Call `t.SetPlan(...)` before `CreateTenant` if you
  enforce limits.
- **An unconfigured plan under enforcement is now a 403, not a 500.** A
  tenant whose plan (including the empty plan) has no entry in
  `limits.Config.PlanLimits` is refused with `PLAN_NOT_CONFIGURED` (HTTP 403
  via `EnforceLimits`) rather than the opaque `LIMIT_CHECK_FAILED` 500 of
  earlier v0.8 builds. Fix it by setting the plan on creation with
  `SetPlan`, or by supplying a fallback via `limits.Config.PlanOf`.
- **Limits are now an optional package, `limits`.** `FlexibleLimits`,
  `LimitType`/`LimitTypeInt`/etc., `LimitSchema`, `LimitDefinition`,
  `UsageTracker`, and the checker itself all moved from `tenant` to `limits`
  (`tenant.FlexibleLimits` → `limits.FlexibleLimits`, and so on). The plan
  constants also moved and are now just examples: `multitenant.PlanBasic` →
  `limits.PlanBasic` (`limits.PlanPro`, `limits.PlanEnterprise`). The core
  `tenant` package does not know these names; `limits.ExampleConfig()`
  returns a ready-made three-plan config using them.
- **`multitenant.Config` is now a struct**, embedding `tenant.Config` with an
  added `Limits *limits.Config` field, in place of the old `tenant.Config`
  usage directly. `multitenant.DefaultConfig()` returns it with `Limits: nil`
  — **no enforcement** — so a v0.7 config that enforced limits silently stops
  enforcing them after upgrading unless you set `Limits` explicitly:
  ```go
  config := multitenant.DefaultConfig()
  l := limits.ExampleConfig() // or your own limits.Config
  config.Limits = &l
  ```
- **`Manager.CheckLimits`, `Manager.LimitChecker()`, and
  `Manager.ValidateAccess` are removed.** Use `mt.Limits` (a `limits.Checker`,
  `nil` when `Config.Limits` is unset) instead of `Manager.LimitChecker()`:
  `mt.Limits.CheckTenant`, `mt.Limits.AddLimit`, `mt.Limits.Usage`, etc.
  `ValidateAccess` was already a stub before v0.8; there is no replacement —
  access control is the application's responsibility (see
  [Access Control](#access-control) above).
- **`tenant.NewManager` dropped the limit-checker parameter** and is now
  `NewManager(config Config, db *sql.DB, repository Repository, schemaManager SchemaManager, migrationMgr MigrationManager, logger *zap.Logger) Manager`
  (six arguments). If you constructed a `Manager` directly rather than
  through `multitenant.New`, update the call.
- **`Stats` lost its `Usage` field.** `mt.Manager.GetStats` now returns only
  `TenantID`, `SchemaExists`, `AppliedMigrations`. For per-limit usage, call
  `mt.Limits.Usage(ctx, tenantID)` — note it now returns an error if any
  configured usage-tracker read fails, where the old `GetStats` silently
  skipped a failing one.
- **`limits.Config.EnforceLimits: false` means `CheckTenant` returns an
  empty snapshot without consulting the repository at all** — no tenant
  lookup, no plan check. `limits.FromContext` (and the Gin
  `GetTenantLimitsFromContext`) then yield an empty map, not the plan's
  configured limits. (`multitenant.Config.Limits == nil` is the more common
  case and is stricter still: `mt.Limits` itself is `nil`, so there is no
  checker to call at all.) To read the configured values regardless of
  enforcement, use `mt.Limits.GetLimitsForPlan(t.Plan())`.
- **`DatabaseConfig.Driver` and `DatabaseConfig.MigrationsTable` are
  removed.** The library only ever used pgx, and the migrations table name
  was never actually configurable; drop these fields from your config.
- **Subdomain validation is now pluggable.**
  `tenant.ResolverConfig.ValidateSubdomain` (`func(subdomain string) error`)
  replaces the old hardcoded check; it defaults to
  `tenant.DefaultSubdomainValidator(ReservedSubdomain)`, which is the same
  policy as before. Set it to change the rules.

## ⬆️ Upgrading from v0.6

v0.6.0 provisioned every tenant schema with hardcoded tables
(`projects`, `tasks`, `documents`, `tenant_users`) and had no migration
tracking. v0.7.0 removes that: schemas come entirely from your own migration
files (see [Tenant schema](#tenant-schema) above). This changes behaviour for
tenants that already exist.

- **New tenants** provisioned after upgrading get no tables at all unless you
  set `config.Database.MigrationsDir`. If you relied on the old built-in
  tables, write migration files that create them.
- **Existing (already-provisioned) tenants** keep the tables v0.6 created —
  upgrading does not touch tenant schemas. What they don't have is any row in
  `public.tenant_migrations`, so `ProvisionTenant` (re-run on a tenant whose
  provisioning previously failed) and `ApplyPendingToAllTenants` will try to
  create `projects`/`tasks`/`documents`/`tenant_users` again and fail against
  the tables that already exist. Bring existing tenants under migration
  control one of two ways:
  - Write your first migration files (e.g. `001_create_projects.up.sql`) using
    `CREATE TABLE IF NOT EXISTS` for every table v0.6 created, so applying them
    against an already-populated schema is a no-op; or
  - Backfill baseline rows into `public.tenant_migrations` so the library
    considers those tables already migrated and never tries to recreate them:
    ```sql
    INSERT INTO public.tenant_migrations (id, tenant_id, version, name, checksum, applied_at)
    SELECT gen_random_uuid(), id, '001', 'baseline', NULL, now() FROM public.tenants;
    ```
    Adjust the `version`/`name` to match whatever you name your first real
    migration file, so that file is treated as already applied too.
- **Limits enforcement**: `limits.Config.UsageTables` defaults to empty, so
  `EnforceLimits: true` silently stops enforcing `max_projects`/`max_users`
  (and any other limit) unless you list the table backing it. To keep v0.6
  behaviour:
  ```go
  config.Limits.UsageTables = map[string]string{
      "max_projects": "projects",
      "max_users":    "tenant_users",
  }
  ```
- **Removed APIs**: `Manager.GetTenantDB`, `GetTenantDBFromContext`,
  `ContextKeyTenantDB`, `SchemaManager.SetSearchPath`, `ExtensibleTenant` and
  `ExtensibleRepository` are gone. Replacements:
  - For per-tenant SQL, use `mt.Migrations` (`ApplyPending`,
    `ApplyMigrationFromFile`, `RollbackMigration`, ...) for schema changes, or
    open your own transaction against `mt.GetDatabase()` and set
    `SET LOCAL search_path TO "<tenant schema>"` yourself the way
    `MigrationManager` does internally.
  - For arbitrary per-tenant data that isn't a schema, use `Tenant.Metadata`
    (see [Metadata](#metadata) above) instead of a second tenant type.

## 🤝 Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) for details on our code of conduct and the process for submitting pull requests.

### Development Setup

1. Clone the repository
2. Set up PostgreSQL database
3. Run tests: `go test ./...`
4. Run examples to verify functionality

### Code Style

- Follow Go conventions and idioms
- Write comprehensive tests
- Use meaningful commit messages
- Document public APIs

## 📝 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- Inspired by real-world multi-tenant applications
- Built on proven PostgreSQL patterns
- Designed for production use cases
- Community feedback and contributions

## 🔗 Related Projects

- [Gin Web Framework](https://github.com/gin-gonic/gin)
- [PostgreSQL](https://www.postgresql.org/)
- [Zap Logger](https://github.com/uber-go/zap)

## 📞 Support

- 📖 [Documentation](./docs/)
- 🐛 [Issue Tracker](https://github.com/alexalmadav/go-multitenant/issues)
- 💬 [Discussions](https://github.com/alexalmadav/go-multitenant/discussions)
- 📧 Email: support@example.com

---

**Built with ❤️ for the Go community**



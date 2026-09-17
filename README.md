# Go Multi-Tenant

[![Go Version](https://img.shields.io/badge/go-%3E%3D1.22-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/alexalmadav/go-multitenant)](https://goreportcard.com/report/github.com/alexalmadav/go-multitenant)

A comprehensive multi-tenant solution for Go applications using a **schema-per-tenant** PostgreSQL architecture. This library provides complete tenant isolation, middleware integration, and sophisticated tenant management capabilities.

## 🚀 Features

- **Complete Tenant Isolation**: Schema-per-tenant architecture with PostgreSQL
- **Flexible Tenant Resolution**: Support for subdomain, path, and header-based tenant resolution
- **Gin Middleware Integration**: Ready-to-use middleware for Gin web framework
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

### Basic Usage

```go
package main

import (
    "log"
    "net/http"
    
    "github.com/alexalmadav/go-multitenant"
    "github.com/gin-gonic/gin"
)

func main() {
    // Create configuration
    config := multitenant.DefaultConfig()
    config.Database.DSN = "postgres://user:pass@localhost/mydb?sslmode=disable"
    config.Resolver.Strategy = multitenant.ResolverSubdomain
    config.Resolver.Domain = "myapp.com"

    // Initialize multi-tenant system
    mt, err := multitenant.New(config)
    if err != nil {
        log.Fatal(err)
    }
    defer mt.Close()

    // Setup Gin with multi-tenant middleware
    r := gin.Default()
    
    // Apply tenant middleware
    api := r.Group("/api")
    api.Use(mt.GinMiddleware.ResolveTenant())
    api.Use(mt.GinMiddleware.ValidateTenant())
    api.Use(mt.GinMiddleware.EnforceLimits())
    
    // Your tenant-aware routes
    api.GET("/dashboard", func(c *gin.Context) {
        tenant, _ := multitenant.GetTenantFromContext(c.Request.Context())
        c.JSON(http.StatusOK, gin.H{
            "message": "Welcome to " + tenant.Subdomain,
            "plan": tenant.PlanType,
        })
    })

    log.Fatal(http.ListenAndServe(":8080", r))
}
```

### Advanced Usage with Billing

```go
// Configure custom plan limits (-1 means unlimited)
basic := make(tenant.FlexibleLimits)
basic.Set("max_users", tenant.LimitTypeInt, 5)
basic.Set("max_projects", tenant.LimitTypeInt, 10)
basic.Set("max_storage_gb", tenant.LimitTypeInt, 1)

pro := make(tenant.FlexibleLimits)
pro.Set("max_users", tenant.LimitTypeInt, 25)
pro.Set("max_projects", tenant.LimitTypeInt, 100)
pro.Set("max_storage_gb", tenant.LimitTypeInt, 10)

config.Limits.PlanLimits = map[string]tenant.FlexibleLimits{
    multitenant.PlanBasic: basic,
    multitenant.PlanPro:   pro,
}

// Limits are checked against live counts in the tenant schema. Map each
// limit name to the tenant-schema table whose row count is its usage:
config.Limits.UsageTables = map[string]string{"max_projects": "projects", "max_users": "tenant_users"}

// Only limits listed here (or served by a custom UsageTracker) are checked.
// Swap in your own tracker or add limits at runtime:
mt.Manager.LimitChecker().SetUsageTracker(myTracker)
mt.Manager.LimitChecker().AddLimit(multitenant.PlanPro, "beta_features", tenant.LimitTypeBool, true)

// Create middleware with custom error handling
ginConfig := ginmiddleware.Config{
    SkipPaths: []string{"/health", "/billing/"},
    RequireAuthentication: true,
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
    Driver:              "pgx",
    DSN:                "postgres://user:pass@localhost/db?sslmode=disable",
    MaxOpenConns:        100,
    MaxIdleConns:        50,
    ConnMaxLifetime:     15 * time.Minute,
    SchemaPrefix:        "tenant_",     // Schema naming: tenant_{uuid}
    MigrationsTable:     "tenant_migrations",
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

```go
config.Limits = tenant.LimitsConfig{
    EnforceLimits: true,
    DefaultPlan:   multitenant.PlanBasic,
    PlanLimits: map[string]tenant.FlexibleLimits{
        multitenant.PlanBasic: basic, // see FLEXIBLE_LIMITS.md
        // ... more plans
    },
}
```

## 🛠️ Middleware

### Available Middleware

```go
// Core middleware
mt.GinMiddleware.ResolveTenant()     // Resolves tenant from request
mt.GinMiddleware.ValidateTenant()    // Validates tenant status
mt.GinMiddleware.EnforceLimits()     // Enforces plan limits
mt.GinMiddleware.SetTenantDB()       // Sets up tenant database context

// Additional middleware
mt.GinMiddleware.RequireAdmin()      // Requires admin privileges
mt.GinMiddleware.LogAccess()         // Logs tenant access
```

### Middleware Chain Example

```go
api := r.Group("/api")
api.Use(authMiddleware())                    // Your auth middleware
api.Use(mt.GinMiddleware.ResolveTenant())    // Resolve tenant
api.Use(mt.GinMiddleware.ValidateTenant())   // Validate tenant status
api.Use(mt.GinMiddleware.EnforceLimits())    // Check limits
api.Use(mt.GinMiddleware.SetTenantDB())      // Set database context
api.Use(mt.GinMiddleware.LogAccess())        // Log access

// Admin-only routes
admin := api.Group("/admin")
admin.Use(mt.GinMiddleware.RequireAdmin())
```

## 🗄️ Database Operations

### Tenant-Aware Database Operations

```go
// With SetTenantDB in the chain, each request gets a dedicated connection
// scoped to the tenant schema. It is released when the request ends.
func getProjects(c *gin.Context) {
    conn, _ := ginmiddleware.GetTenantConnFromContext(c)

    // This query only sees the current tenant's projects
    rows, err := conn.QueryContext(c.Request.Context(),
        "SELECT * FROM projects WHERE status = $1", "active")
    if err != nil { /* ... */ }
    defer rows.Close() // also ends the statement's scoping transaction
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
    PlanType:  multitenant.PlanPro,
    Status:    multitenant.StatusPending,
}

// Create tenant record
err := mt.Manager.CreateTenant(ctx, tenant)

// Provision tenant schema
err = mt.Manager.ProvisionTenant(ctx, tenant.ID)
```

### Managing Tenant Status

```go
// Suspend a tenant
err := mt.Manager.SuspendTenant(ctx, tenantID)

// Activate a tenant
err := mt.Manager.ActivateTenant(ctx, tenantID)

// Get tenant statistics
stats, err := mt.Manager.GetStats(ctx, tenantID)
// Returns: SchemaExists, AppliedMigrations, Usage (per limit in UsageTables)
```

### Plan Management

```go
// Update tenant plan
tenant.PlanType = multitenant.PlanEnterprise
err := mt.Manager.UpdateTenant(ctx, tenant)

// Check current limits
limits, err := mt.Manager.CheckLimits(ctx, tenantID)
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

```go
// Validate user access to tenant
err := mt.Manager.ValidateAccess(ctx, userID, tenantID)

// Admin-only operations
api.Use(mt.GinMiddleware.RequireAdmin())
```

### Input Validation

```go
// Automatic subdomain validation
// - Length requirements (3-50 characters)
// - Character restrictions (alphanumeric + hyphens)
// - Reserved subdomain protection
// - Format validation
```

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
// Returns: SchemaExists, AppliedMigrations, Usage (per limit in UsageTables)
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
CREATE TABLE tenants (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    subdomain VARCHAR(255) UNIQUE NOT NULL,
    plan_type VARCHAR(50) NOT NULL DEFAULT 'basic',
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    schema_name VARCHAR(255) NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Migration tracking
CREATE TABLE tenant_migrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    version VARCHAR(50) NOT NULL,
    name VARCHAR(255) NOT NULL,
    applied_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    rollback_sql TEXT,
    checksum VARCHAR(64),
    FOREIGN KEY (tenant_id) REFERENCES tenants(id),
    UNIQUE(tenant_id, version)
);
```

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
- **Limits enforcement**: `LimitsConfig.UsageTables` defaults to empty, so
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



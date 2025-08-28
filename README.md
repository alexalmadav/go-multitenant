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
│   ├── projects
│   ├── tasks
│   ├── documents
│   └── tenant tables...
└── tenant_globex-uuid
    ├── projects
    ├── tasks
    └── tenant tables...
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
// Configure custom plan limits
config.Limits.PlanLimits = map[string]*multitenant.Limits{
    multitenant.PlanBasic: {
        MaxUsers:     5,
        MaxProjects:  10,
        MaxStorageGB: 1,
    },
    multitenant.PlanPro: {
        MaxUsers:     25,
        MaxProjects:  100,
        MaxStorageGB: 10,
    },
}

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
    Driver:              "postgres",
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
config.Limits = multitenant.LimitsConfig{
    EnforceLimits: true,
    PlanLimits: map[string]*multitenant.Limits{
        multitenant.PlanBasic: {
            MaxUsers:     5,
            MaxProjects:  10,
            MaxStorageGB: 1,
        },
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
// In your handlers, the database context is automatically set
func getProjects(c *gin.Context) {
    // Database queries are automatically scoped to the tenant
    db, _ := ginmiddleware.GetTenantDBFromContext(c)
    
    // This query only returns projects for the current tenant
    rows, err := db.Query("SELECT * FROM projects WHERE status = $1", "active")
    // ...
}
```

### Manual Tenant Context

```go
// For background jobs or non-HTTP contexts
ctx := mt.Manager.WithTenantContext(context.Background(), tenantID)
db, err := mt.Manager.GetTenantDB(ctx, tenantID)
```

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
```

### Plan Management

```go
// Update tenant plan
tenant.PlanType = multitenant.PlanEnterprise
err := mt.Manager.UpdateTenant(ctx, tenant)

// Check current limits
limits, err := mt.Manager.CheckLimits(ctx, tenantID)
```

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
// Returns: UserCount, ProjectCount, StorageUsedGB, LastActivity
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

### Running Examples

```bash
# Basic example
cd examples/basic
go run main.go

# Advanced billing example  
cd examples/with-billing
go run main.go
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

### Tenant Schema Tables

Each tenant schema contains:

```sql
-- Projects table (example)
CREATE TABLE projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Tasks table (example)
CREATE TABLE tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id)
);
```

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



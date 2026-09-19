# Flexible User-Definable Limits System

Package `limits` is a completely flexible, user-definable limits system that
allows you to create custom restrictions and features for your multi-tenant
application. It is optional: the core `multitenant`/`tenant` packages know
nothing about plans or limits, so an application that does not enforce
limits never imports `limits` at all.

## Overview

Instead of hardcoded limits like `MaxUsers`, `MaxProjects`, and `MaxStorageGB`, the system supports:

- **Dynamic Limit Types**: Int, Float, String, Bool, Duration
- **Custom Limit Names**: Define any limit name you need
- **Runtime Management**: Add, remove, and update limits at runtime
- **Type Safety**: Strong typing with validation
- **Schema Definition**: Define available limits with metadata
- **Category Organization**: Group limits by category (usage, features, api, etc.)

## Key Components

### 1. Limit Types

```go
const (
    LimitTypeInt     limits.LimitType = "int"
    LimitTypeFloat   limits.LimitType = "float"
    LimitTypeString  limits.LimitType = "string"
    LimitTypeBool    limits.LimitType = "bool"
    LimitTypeDuration limits.LimitType = "duration"
)
```

### 2. Flexible Limits

```go
// Create limits
l := make(limits.FlexibleLimits)
l.Set("max_users", limits.LimitTypeInt, 10)
l.Set("advanced_features", limits.LimitTypeBool, true)
l.Set("api_rate_per_minute", limits.LimitTypeInt, 1000)
l.Set("session_timeout", limits.LimitTypeDuration, "24h")
l.Set("export_formats", limits.LimitTypeString, "csv,json,pdf")

// Access limits
maxUsers, err := l.GetInt("max_users")
hasAdvanced, err := l.GetBool("advanced_features")
```

### 3. Limit Schema

Define available limits with metadata:

```go
schema := limits.NewLimitSchema()
schema.AddDefinition(&limits.LimitDefinition{
    Name:         "video_processing_minutes",
    DisplayName:  "Video Processing Minutes",
    Description:  "Monthly allowance for video processing",
    Type:         limits.LimitTypeInt,
    DefaultValue: &limits.LimitValue{Type: limits.LimitTypeInt, Value: 60},
    Required:     false,
    Category:     "media",
})
```

## Usage Examples

### Basic Setup

```go
// Create config with flexible limits
config := multitenant.DefaultConfig()
l := limits.ExampleConfig() // three example plans, ready to adapt
config.Limits = &l

// Add custom limit definitions
schema := config.Limits.LimitSchema
schema.AddDefinition(&limits.LimitDefinition{
    Name:         "ai_model_calls",
    DisplayName:  "AI Model API Calls",
    Description:  "Monthly AI model API call allowance",
    Type:         limits.LimitTypeInt,
    DefaultValue: &limits.LimitValue{Type: limits.LimitTypeInt, Value: 1000},
    Category:     "ai",
})

// Create plan with custom limits
planLimits := make(limits.FlexibleLimits)
planLimits.Set("max_users", limits.LimitTypeInt, 5)
planLimits.Set("ai_model_calls", limits.LimitTypeInt, 500)
planLimits.Set("advanced_features", limits.LimitTypeBool, false)

config.Limits.PlanLimits["basic"] = planLimits
```

### Choosing a plan per tenant: `PlanOf`

By default the checker resolves a tenant's plan from `t.Plan()`
(`metadata["plan"]`, set with `t.SetPlan("pro")`). Override this with
`Config.PlanOf` if your plan name comes from somewhere else:

```go
config.Limits.PlanOf = func(t *tenant.Tenant) string {
    if v, ok := t.Metadata.GetString("billing_tier"); ok {
        return v
    }
    return "basic"
}
```

### Runtime Limit Management

Once `mt, err := multitenant.New(config)` has built the checker, manage
plans at runtime through `mt.Limits` (a `limits.Checker`):

```go
// Add new limit to existing plan
err := mt.Limits.AddLimit("premium", "custom_api_endpoints", limits.LimitTypeInt, 10)

// Update existing limit
err := mt.Limits.UpdateLimit("premium", "max_users", 50)

// Remove limit
err := mt.Limits.RemoveLimit("basic", "deprecated_feature")
```

### Limit Checking

```go
// Check specific limit
err := mt.Limits.CheckLimit(ctx, tenantID, "ai_model_calls", currentUsage)
if err != nil {
    // Handle limit exceeded
}

// Check all limits for tenant
err := mt.Limits.CheckAllLimits(ctx, tenantID)

// Check feature availability
planLimits := mt.Limits.GetLimitsForPlan(tenant.Plan())
hasFeature, err := planLimits.GetBool("advanced_features")
```

## Common Use Cases

### 1. Usage Limits
```go
l.Set("max_users", limits.LimitTypeInt, 25)
l.Set("max_storage_gb", limits.LimitTypeInt, 100)
l.Set("api_calls_per_month", limits.LimitTypeInt, 50000)
```

### 2. Feature Toggles
```go
l.Set("advanced_analytics", limits.LimitTypeBool, true)
l.Set("custom_branding", limits.LimitTypeBool, false)
l.Set("sso_integration", limits.LimitTypeBool, true)
```

### 3. API Restrictions
```go
l.Set("webhook_endpoints", limits.LimitTypeInt, 5)
l.Set("api_rate_per_minute", limits.LimitTypeInt, 100)
l.Set("batch_export_size", limits.LimitTypeInt, 10000)
```

### 4. Time-based Limits
```go
l.Set("session_timeout", limits.LimitTypeDuration, "8h")
l.Set("backup_retention_days", limits.LimitTypeInt, 30)
```

### 5. Custom Configurations
```go
l.Set("allowed_domains", limits.LimitTypeString, "example.com,company.com")
l.Set("export_formats", limits.LimitTypeString, "csv,json,pdf,xlsx")
```

## Default Limits Schema

`limits.DefaultLimitSchema()` (used when `Config.LimitSchema` is nil) comes
with a comprehensive default schema including:

**Usage Limits:**
- `max_users` - Maximum number of users
- `max_projects` - Maximum number of projects
- `max_storage_gb` - Storage limit in GB
- `max_file_size_mb` - File upload size limit

**API Limits:**
- `api_calls_per_month` - Monthly API call allowance
- `api_rate_per_minute` - API rate limiting
- `webhook_endpoints` - Maximum webhook endpoints

**Features:**
- `advanced_features` - Advanced feature access
- `custom_integrations` - Custom integration support
- `priority_support` - Priority support access
- `custom_branding` - Branding customization

**And many more...**

## Integration with Usage Tracking

The default usage tracker (wired up by `multitenant.New` when `config.Limits`
is set) reads `config.Limits.UsageTables`, a map from limit name to the table
in the tenant schema whose row count is that limit's current usage:

```go
config.Limits.UsageTables = map[string]string{
    "max_projects": "projects",
    "max_users":    "tenant_users",
}
```

**Only limits listed in `UsageTables` are checked against the database.** A
limit defined in `PlanLimits` but absent from `UsageTables` — and not served
by a custom `UsageTracker` — is never enforced; `CheckLimit` reports no usage
for it and it is silently unlimited in practice.

```go
// Set a custom usage tracker for automatic limit checking
mt.Limits.SetUsageTracker(usageTracker)

// Usage tracker interface
type UsageTracker interface {
    GetCurrentUsage(ctx context.Context, tenantID uuid.UUID, limitName string) (interface{}, error)
    IncrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error
    DecrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error
    ResetUsage(ctx context.Context, tenantID uuid.UUID, limitName string) error
}
```

A custom tracker can serve limits `UsageTables` does not cover — for example,
computed or externally-fetched usage — by implementing `GetCurrentUsage` for
those limit names itself.

`mt.Limits.Usage(ctx, tenantID)` returns the current count for every limit in
`UsageTables` as a `map[string]int`, and returns an error if any configured
tracker read fails (it does not silently skip a failing one).

## Error Handling

The system provides detailed error information:

```go
err := mt.Limits.CheckLimit(ctx, tenantID, "max_users", currentUsers)
if err != nil {
    if tenantErr, ok := err.(*tenant.TenantError); ok {
        switch tenantErr.Code {
        case "LIMIT_EXCEEDED":
            // Handle usage limit exceeded
        case "FEATURE_NOT_ALLOWED":
            // Handle feature not available
        }
    }
}
```

## Benefits

1. **Complete Flexibility**: Define any limit type you need
2. **Runtime Management**: Add/remove limits without code changes
3. **Type Safety**: Strong typing prevents configuration errors
4. **Extensible**: Easy to add new limit types and categories
5. **Self-Documenting**: Rich metadata for each limit
6. **Validation**: Automatic validation against schema

## Migrating from v0.7

Every limits identifier moved from `tenant` to the new `limits` package:
`tenant.FlexibleLimits` → `limits.FlexibleLimits`, `tenant.LimitType*` →
`limits.LimitType*`, `tenant.LimitSchema`/`LimitDefinition`/`LimitValue` →
`limits.*`, and `tenant.UsageTracker` → `limits.UsageTracker`. There is no
compatibility shim; update your imports and type references together.

```go
// v0.7
oldLimits := make(tenant.FlexibleLimits)
oldLimits.Set("max_users", tenant.LimitTypeInt, 10)

// v0.8
l := make(limits.FlexibleLimits)
l.Set("max_users", limits.LimitTypeInt, 10)
```

Configuration and the checker also moved:

- `tenant.LimitsConfig` is now `limits.Config`, and it hangs off
  `multitenant.Config.Limits` as a pointer (`*limits.Config`), not a value on
  `tenant.Config`. `nil` means no enforcement at all — `multitenant.DefaultConfig()`
  leaves it `nil`, so limits are opt-in:
  ```go
  config := multitenant.DefaultConfig()
  l := limits.ExampleConfig()
  config.Limits = &l
  ```
- `Manager.LimitChecker()` and `Manager.CheckLimits` are gone. Use
  `mt.Limits`, the `limits.Checker` built from `config.Limits` by
  `multitenant.New` (`nil` when `config.Limits` is `nil`).
- The plan itself no longer has a dedicated field or type
  (`tenant.PlanType` is gone). It lives in tenant metadata; the checker reads
  it with `t.Plan()` by default, configurable via `Config.PlanOf`. See the
  [Plan](./README.md#plan) section of the README.
- `Stats.Usage` is gone; call `mt.Limits.Usage(ctx, tenantID)` instead of
  reading usage off `Manager.GetStats`'s result.
- With enforcement off (`EnforceLimits: false`, the default inside
  `limits.Config`'s zero value), `CheckTenant` returns an empty
  `FlexibleLimits{}` without querying the repository at all — it does not
  fall back to the plan's configured limits. Read those directly with
  `mt.Limits.GetLimitsForPlan(t.Plan())` when you need them regardless of
  enforcement.
- With enforcement on, a tenant whose plan is not a key in `PlanLimits`
  (including the empty plan) is refused with a `*tenant.TenantError` of code
  `PLAN_NOT_CONFIGURED` — `CheckLimit`, `CheckTenant`, and `CheckAllLimits`
  all agree on this — surfaced by `EnforceLimits` middleware as HTTP 403; set
  the plan on creation with `SetPlan`, or supply a fallback via
  `limits.Config.PlanOf`.

## Example Application

See `examples/flexible-limits/main.go` for a complete working example demonstrating:

- Custom limit definitions
- Multiple plan types with different limits
- Runtime limit management
- API endpoints for limit management
- Feature checking and usage tracking simulation

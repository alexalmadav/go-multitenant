# Flexible User-Definable Limits System

The go-multitenant library now supports a completely flexible, user-definable limits system that allows you to create custom restrictions and features for your multi-tenant application.

## Overview

Instead of hardcoded limits like `MaxUsers`, `MaxProjects`, and `MaxStorageGB`, the new system supports:

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
    LimitTypeInt     LimitType = "int"
    LimitTypeFloat   LimitType = "float"
    LimitTypeString  LimitType = "string"
    LimitTypeBool    LimitType = "bool"
    LimitTypeDuration LimitType = "duration"
)
```

### 2. Flexible Limits

```go
// Create limits
limits := make(tenant.FlexibleLimits)
limits.Set("max_users", tenant.LimitTypeInt, 10)
limits.Set("advanced_features", tenant.LimitTypeBool, true)
limits.Set("api_rate_per_minute", tenant.LimitTypeInt, 1000)
limits.Set("session_timeout", tenant.LimitTypeDuration, "24h")
limits.Set("export_formats", tenant.LimitTypeString, "csv,json,pdf")

// Access limits
maxUsers, err := limits.GetInt("max_users")
hasAdvanced, err := limits.GetBool("advanced_features")
```

### 3. Limit Schema

Define available limits with metadata:

```go
schema := tenant.NewLimitSchema()
schema.AddDefinition(&tenant.LimitDefinition{
    Name:         "video_processing_minutes",
    DisplayName:  "Video Processing Minutes", 
    Description:  "Monthly allowance for video processing",
    Type:         tenant.LimitTypeInt,
    DefaultValue: 60,
    Required:     false,
    Category:     "media",
})
```

## Usage Examples

### Basic Setup

```go
// Create config with flexible limits
config := multitenant.DefaultConfig()

// Add custom limit definitions
schema := config.Limits.LimitSchema
schema.AddDefinition(&tenant.LimitDefinition{
    Name:         "ai_model_calls",
    DisplayName:  "AI Model API Calls",
    Description:  "Monthly AI model API call allowance", 
    Type:         tenant.LimitTypeInt,
    DefaultValue: 1000,
    Category:     "ai",
})

// Create plan with custom limits
planLimits := make(tenant.FlexibleLimits)
planLimits.Set("max_users", tenant.LimitTypeInt, 5)
planLimits.Set("ai_model_calls", tenant.LimitTypeInt, 500)
planLimits.Set("advanced_features", tenant.LimitTypeBool, false)

config.Limits.PlanLimits["basic"] = planLimits
```

### Runtime Limit Management

```go
// Add new limit to existing plan
err := limitChecker.AddLimit("premium", "custom_api_endpoints", tenant.LimitTypeInt, 10)

// Update existing limit
err := limitChecker.UpdateLimit("premium", "max_users", 50)

// Remove limit
err := limitChecker.RemoveLimit("basic", "deprecated_feature")
```

### Limit Checking

```go
// Check specific limit
err := limitChecker.CheckLimit(ctx, tenantID, "ai_model_calls", currentUsage)
if err != nil {
    // Handle limit exceeded
}

// Check all limits for tenant
err := limitChecker.CheckAllLimits(ctx, tenantID)

// Check feature availability
hasFeature, err := planLimits.GetBool("advanced_features")
```

## Common Use Cases

### 1. Usage Limits
```go
limits.Set("max_users", tenant.LimitTypeInt, 25)
limits.Set("max_storage_gb", tenant.LimitTypeInt, 100)
limits.Set("api_calls_per_month", tenant.LimitTypeInt, 50000)
```

### 2. Feature Toggles
```go
limits.Set("advanced_analytics", tenant.LimitTypeBool, true)
limits.Set("custom_branding", tenant.LimitTypeBool, false)
limits.Set("sso_integration", tenant.LimitTypeBool, true)
```

### 3. API Restrictions
```go
limits.Set("webhook_endpoints", tenant.LimitTypeInt, 5)
limits.Set("api_rate_per_minute", tenant.LimitTypeInt, 100)
limits.Set("batch_export_size", tenant.LimitTypeInt, 10000)
```

### 4. Time-based Limits
```go
limits.Set("session_timeout", tenant.LimitTypeDuration, "8h")
limits.Set("backup_retention_days", tenant.LimitTypeInt, 30)
```

### 5. Custom Configurations
```go
limits.Set("allowed_domains", tenant.LimitTypeString, "example.com,company.com")
limits.Set("export_formats", tenant.LimitTypeString, "csv,json,pdf,xlsx")
```

## Default Limits Schema

The system comes with a comprehensive default schema including:

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

```go
// Set usage tracker for automatic limit checking
limitChecker.SetUsageTracker(usageTracker)

// Usage tracker interface
type UsageTracker interface {
    GetCurrentUsage(ctx context.Context, tenantID uuid.UUID, limitName string) (interface{}, error)
    IncrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error
    DecrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error
}
```

## Error Handling

The system provides detailed error information:

```go
err := limitChecker.CheckLimit(ctx, tenantID, "max_users", currentUsers)
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
6. **Backward Compatible**: Legacy Limits struct still works
7. **Validation**: Automatic validation against schema

## Migration from Old System

The new system maintains compatibility with existing code through the legacy `Limits` struct, but new implementations should use `FlexibleLimits` for maximum flexibility.

```go
// Old way (still works)
limits := &tenant.Limits{
    MaxUsers: 10,
    MaxProjects: 25,
}

// New way (recommended)
limits := make(tenant.FlexibleLimits)
limits.Set("max_users", tenant.LimitTypeInt, 10)
limits.Set("max_projects", tenant.LimitTypeInt, 25)
limits.Set("advanced_features", tenant.LimitTypeBool, true)
```

## Example Application

See `examples/flexible-limits/main.go` for a complete working example demonstrating:

- Custom limit definitions
- Multiple plan types with different limits
- Runtime limit management
- API endpoints for limit management
- Feature checking and usage tracking simulation

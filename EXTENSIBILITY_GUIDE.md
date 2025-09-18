# Tenant Extensibility Guide

This guide demonstrates how to extend the go-multitenant library to support additional fields like `stripe_customer_id` and other custom metadata on a case-by-case basis.

## Overview

The library provides several approaches for extending tenant data:

1. **JSONB Metadata Field** (Recommended) - Store custom data in a flexible JSONB column
2. **Extension Interfaces** - Strongly typed extensions for common integrations
3. **Schema Registry** - Validate and manage extension schemas
4. **Lifecycle Hooks** - React to tenant lifecycle events

## JSONB Metadata Approach

The recommended approach uses a `metadata` JSONB column that can store arbitrary key-value pairs while maintaining database performance through targeted indexes.

### Benefits

- **Flexible**: Add any custom fields without schema changes
- **Performant**: JSONB with GIN indexes enables fast queries
- **Backward Compatible**: Existing code continues to work
- **Type Safety**: Helper methods provide type-safe access
- **Queryable**: Can query tenants by metadata values

### Database Schema

```sql
-- Add metadata column to existing tenants table
ALTER TABLE tenants 
ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT '{}';

-- Create indexes for efficient queries
CREATE INDEX IF NOT EXISTS idx_tenants_metadata_gin 
ON tenants USING GIN (metadata);

-- Specific indexes for common queries
CREATE INDEX IF NOT EXISTS idx_tenants_metadata_stripe_customer 
ON tenants USING BTREE ((metadata->>'stripe_customer_id')) 
WHERE metadata ? 'stripe_customer_id';
```

## Usage Examples

### Basic Metadata Operations

```go
// Create tenant with metadata
tenant := &tenant.ExtensibleTenant{
    ID:        uuid.New(),
    Name:      "Acme Corp",
    Subdomain: "acme",
    PlanType:  "pro",
    Status:    "active",
    Metadata:  make(tenant.TenantMetadata),
}

// Set custom fields
tenant.Metadata.SetString("stripe_customer_id", "cus_123456")
tenant.Metadata.SetString("custom_domain", "app.acme.com")
tenant.Metadata.SetBool("feature_flags.advanced_analytics", true)
tenant.Metadata.SetInt("max_users", 100)

// Create tenant
err := repo.CreateExtended(ctx, tenant)
```

### Using Extension Helpers

```go
// Stripe integration helper
stripeExt := tenant.NewStripeExtension(tenant.Metadata)
stripeExt.SetCustomerID("cus_123456")
stripeExt.SetSubscriptionID("sub_789012")

// Check if Stripe is configured
if stripeExt.HasStripeIntegration() {
    customerID, _ := stripeExt.GetCustomerID()
    // Process billing...
}

// Branding extension helper
brandingExt := tenant.NewBrandingExtension(tenant.Metadata)
brandingExt.SetLogoURL("https://acme.com/logo.png")
brandingExt.SetTheme("blue")
brandingExt.SetCustomDomain("app.acme.com")
```

### Querying by Metadata

```go
// Find tenant by Stripe customer ID
tenants, err := repo.FindByMetadata(ctx, "stripe_customer_id", "cus_123456")

// Find tenants with specific metadata keys
tenants, err := repo.FindByMetadataKeys(ctx, []string{
    "stripe_customer_id", 
    "custom_domain",
})

// Update single metadata field
err := repo.UpdateMetadataField(ctx, tenantID, "stripe_subscription_id", "sub_new123")
```

## Extension Patterns

### 1. Helper Extensions

Create typed helpers for common integrations:

```go
// StripeExtension provides type-safe Stripe integration
type StripeExtension struct {
    metadata TenantMetadata
}

func (se *StripeExtension) GetCustomerID() (string, bool) {
    return se.metadata.GetString("stripe_customer_id")
}

func (se *StripeExtension) SetCustomerID(customerID string) {
    se.metadata.SetString("stripe_customer_id", customerID)
}

// Usage
stripeExt := NewStripeExtension(tenant.Metadata)
stripeExt.SetCustomerID("cus_123")
```

### 2. Extension Interfaces

For complex integrations, implement the `TenantExtension` interface:

```go
type StripeExtension struct {
    repo ExtensibleRepository
}

func (se *StripeExtension) GetExtensionName() string {
    return "stripe"
}

func (se *StripeExtension) ValidateMetadata(metadata TenantMetadata) error {
    if customerID, _ := metadata.GetString("stripe_customer_id"); customerID != "" {
        if !strings.HasPrefix(customerID, "cus_") {
            return errors.New("invalid Stripe customer ID format")
        }
    }
    return nil
}

func (se *StripeExtension) OnTenantCreated(ctx context.Context, tenant *ExtensibleTenant) error {
    // Create Stripe customer automatically
    if !tenant.Metadata.Has("stripe_customer_id") {
        customer, err := se.createStripeCustomer(tenant)
        if err != nil {
            return err
        }
        return se.repo.UpdateMetadataField(ctx, tenant.ID, "stripe_customer_id", customer.ID)
    }
    return nil
}
```

### 3. Schema Validation

Define schemas for your extensions:

```go
schema := &ExtensionSchema{
    Name:        "stripe",
    Description: "Stripe billing integration",
    Version:     "1.0",
    Fields: []MetadataField{
        {
            Key:         "stripe_customer_id",
            Type:        "string",
            Required:    true,
            Validation:  "^cus_[a-zA-Z0-9]+$",
            Description: "Stripe customer ID",
        },
        {
            Key:          "stripe_subscription_id",
            Type:         "string",
            Required:     false,
            Validation:   "^sub_[a-zA-Z0-9]+$",
            Description:  "Stripe subscription ID",
        },
    },
}

// Register and validate
registry.RegisterSchema(schema)
err := registry.ValidateAgainstSchema("stripe", tenant.Metadata)
```

## Migration Strategy

### Step 1: Add Metadata Column

Run the migration to add the metadata column:

```sql
-- 001_add_tenant_metadata.up.sql
ALTER TABLE tenants 
ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_tenants_metadata_gin 
ON tenants USING GIN (metadata);
```

### Step 2: Gradual Adoption

Update your application code to use the extensible repository:

```go
// Option 1: Replace existing repository
repo := postgres.NewExtensibleRepository(db, logger)

// Option 2: Use both (during transition)
baseRepo := postgres.NewRepository(db, logger)
extRepo := postgres.NewExtensibleRepository(db, logger)
```

### Step 3: Migrate Existing Data

If you need to migrate existing external ID fields:

```sql
-- Migrate existing stripe_customer_id column to metadata
UPDATE tenants 
SET metadata = metadata || jsonb_build_object('stripe_customer_id', stripe_customer_id)
WHERE stripe_customer_id IS NOT NULL;

-- Drop old column after verification
ALTER TABLE tenants DROP COLUMN stripe_customer_id;
```

## Performance Considerations

### Indexing Strategy

```sql
-- General JSONB index (supports all operations)
CREATE INDEX idx_tenants_metadata_gin ON tenants USING GIN (metadata);

-- Specific B-tree indexes for exact matches (faster for equality)
CREATE INDEX idx_tenants_stripe_customer 
ON tenants USING BTREE ((metadata->>'stripe_customer_id'));

-- Partial indexes for better performance
CREATE INDEX idx_tenants_stripe_active 
ON tenants USING BTREE ((metadata->>'stripe_customer_id'))
WHERE status = 'active' AND metadata ? 'stripe_customer_id';
```

### Query Optimization

```go
// Fast: Use specific metadata queries
tenants, err := repo.FindByMetadata(ctx, "stripe_customer_id", "cus_123")

// Slower: Get full tenant then check metadata
tenant, err := repo.GetExtendedByID(ctx, id)
if customerID, _ := tenant.Metadata.GetString("stripe_customer_id"); customerID == "cus_123" {
    // ...
}
```

## Common Integration Examples

### Stripe Billing

```go
// Complete Stripe integration
type StripeTenant struct {
    *tenant.ExtensibleTenant
    stripe *StripeExtension
}

func NewStripeTenant(extTenant *tenant.ExtensibleTenant) *StripeTenant {
    return &StripeTenant{
        ExtensibleTenant: extTenant,
        stripe:          tenant.NewStripeExtension(extTenant.Metadata),
    }
}

func (st *StripeTenant) SyncWithStripe(ctx context.Context) error {
    if !st.stripe.HasStripeIntegration() {
        return errors.New("Stripe not configured for tenant")
    }
    
    customerID, _ := st.stripe.GetCustomerID()
    // Sync with Stripe API...
    return nil
}
```

### Custom Domains

```go
// Custom domain extension
tenant.Metadata.SetString("custom_domain", "app.acme.com")
tenant.Metadata.SetBool("ssl_enabled", true)
tenant.Metadata.SetString("ssl_cert_id", "cert_123")

// Query tenants by custom domain
tenants, err := repo.FindByMetadata(ctx, "custom_domain", domain)
```

### Feature Flags

```go
// Per-tenant feature flags
tenant.Metadata.SetBool("features.advanced_analytics", true)
tenant.Metadata.SetBool("features.api_access", true)
tenant.Metadata.SetString("features.integration_tier", "premium")

// Check feature availability
if hasAnalytics, _ := tenant.Metadata.GetBool("features.advanced_analytics"); hasAnalytics {
    // Enable analytics features
}
```

## Best Practices

1. **Use Consistent Key Naming**: Use dot notation for hierarchical data (`features.analytics`, `stripe.customer_id`)

2. **Index Frequently Queried Fields**: Create specific indexes for fields you query often

3. **Validate Metadata**: Implement validation for critical fields

4. **Use Helper Extensions**: Create typed helpers for better developer experience

5. **Document Your Schema**: Define clear schemas for your extensions

6. **Plan for Migration**: Consider how to migrate existing data when adding new fields

7. **Monitor Performance**: Watch query performance as metadata usage grows

8. **Version Your Extensions**: Use versioned schemas for complex extensions

## Error Handling

```go
// Graceful handling of missing metadata
if customerID, exists := tenant.Metadata.GetString("stripe_customer_id"); exists {
    // Process Stripe integration
} else {
    // Handle case where Stripe is not configured
    log.Info("Stripe not configured for tenant", "tenant_id", tenant.ID)
}

// Validate before using
if err := validateStripeCustomerID(customerID); err != nil {
    return fmt.Errorf("invalid Stripe customer ID: %w", err)
}
```

This extensibility approach provides a flexible foundation for adding external IDs and custom metadata while maintaining performance and type safety where needed.

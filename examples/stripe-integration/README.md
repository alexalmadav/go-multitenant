# Stripe Integration Example

This example demonstrates how to extend your tenant model to include Stripe customer IDs and other external service integrations.

## Quick Start

1. **Add metadata column to your database:**

```sql
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_tenants_metadata_gin ON tenants USING GIN (metadata);
```

2. **Use the extensible repository:**

```go
import (
    "github.com/alexalmadav/go-multitenant/database/postgres"
    "github.com/alexalmadav/go-multitenant/tenant"
)

// Create extensible repository
repo := postgres.NewExtensibleRepository(db, logger)

// Create tenant with Stripe integration
extTenant := &tenant.ExtensibleTenant{
    ID:        uuid.New(),
    Name:      "Acme Corp",
    Subdomain: "acme",
    PlanType:  "pro",
    Status:    "active",
    Metadata:  make(tenant.TenantMetadata),
}

// Add Stripe customer ID
stripeExt := tenant.NewStripeExtension(extTenant.Metadata)
stripeExt.SetCustomerID("cus_123456789")
stripeExt.SetSubscriptionID("sub_987654321")

// Create tenant
err := repo.CreateExtended(ctx, extTenant)
```

3. **Query tenants by Stripe customer ID:**

```go
// Find tenant by Stripe customer ID
tenants, err := repo.FindByMetadata(ctx, "stripe_customer_id", "cus_123456789")
if len(tenants) > 0 {
    tenant := tenants[0]
    // Process billing for this tenant
}
```

## Integration Patterns

### Webhook Handling

```go
func handleStripeWebhook(repo tenant.ExtensibleRepository) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var event stripe.Event
        if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }

        switch event.Type {
        case "customer.subscription.created":
            var subscription stripe.Subscription
            if err := json.Unmarshal(event.Data.Raw, &subscription); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
            }

            // Find tenant by customer ID
            tenants, err := repo.FindByMetadata(r.Context(), 
                "stripe_customer_id", subscription.Customer.ID)
            if err != nil || len(tenants) == 0 {
                http.Error(w, "Tenant not found", http.StatusNotFound)
                return
            }

            // Update subscription ID
            tenant := tenants[0]
            stripeExt := tenant.NewStripeExtension(tenant.Metadata)
            stripeExt.SetSubscriptionID(subscription.ID)
            
            if err := repo.UpdateExtended(r.Context(), tenant); err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
                return
            }

            w.WriteHeader(http.StatusOK)
        }
    }
}
```

### Billing Service Integration

```go
type BillingService struct {
    repo        tenant.ExtensibleRepository
    stripeClient *stripe.Client
}

func (bs *BillingService) CreateStripeCustomer(ctx context.Context, tenantID uuid.UUID) error {
    // Get tenant
    tenant, err := bs.repo.GetExtendedByID(ctx, tenantID)
    if err != nil {
        return err
    }

    // Check if already has Stripe customer
    stripeExt := tenant.NewStripeExtension(tenant.Metadata)
    if stripeExt.HasStripeIntegration() {
        return nil // Already configured
    }

    // Create Stripe customer
    params := &stripe.CustomerParams{
        Name:  stripe.String(tenant.Name),
        Email: stripe.String(fmt.Sprintf("billing@%s.com", tenant.Subdomain)),
    }
    customer, err := bs.stripeClient.Customers.New(params)
    if err != nil {
        return err
    }

    // Save customer ID
    stripeExt.SetCustomerID(customer.ID)
    return bs.repo.UpdateExtended(ctx, tenant)
}

func (bs *BillingService) GetUsageForTenant(ctx context.Context, tenantID uuid.UUID) (*BillingUsage, error) {
    tenant, err := bs.repo.GetExtendedByID(ctx, tenantID)
    if err != nil {
        return nil, err
    }

    stripeExt := tenant.NewStripeExtension(tenant.Metadata)
    customerID, exists := stripeExt.GetCustomerID()
    if !exists {
        return nil, errors.New("no Stripe integration configured")
    }

    // Get usage from Stripe
    // ...implementation details...
    
    return usage, nil
}
```

## Migration Example

If you're migrating from a dedicated `stripe_customer_id` column:

```sql
-- Migrate existing data
UPDATE tenants 
SET metadata = metadata || jsonb_build_object('stripe_customer_id', stripe_customer_id)
WHERE stripe_customer_id IS NOT NULL;

-- Verify migration
SELECT id, name, stripe_customer_id, metadata->>'stripe_customer_id' as metadata_customer_id
FROM tenants 
WHERE stripe_customer_id IS NOT NULL;

-- Drop old column (after verification)
-- ALTER TABLE tenants DROP COLUMN stripe_customer_id;
```

## Performance Tips

1. **Create specific indexes for frequently queried metadata:**

```sql
-- For exact Stripe customer ID lookups
CREATE INDEX idx_tenants_stripe_customer 
ON tenants USING BTREE ((metadata->>'stripe_customer_id'))
WHERE metadata ? 'stripe_customer_id';
```

2. **Use the FindByMetadata method for queries:**

```go
// Fast: Uses metadata indexes
tenants, err := repo.FindByMetadata(ctx, "stripe_customer_id", customerID)

// Slower: Scans all tenants
allTenants, _, err := repo.ListExtended(ctx, 1, 1000)
for _, tenant := range allTenants {
    if cid, _ := tenant.Metadata.GetString("stripe_customer_id"); cid == customerID {
        // Found it
    }
}
```

3. **Batch metadata updates when possible:**

```go
// Update multiple fields at once
tenant.Metadata.SetString("stripe_customer_id", "cus_123")
tenant.Metadata.SetString("stripe_subscription_id", "sub_456")
tenant.Metadata.SetBool("billing_enabled", true)

// Single database update
err := repo.UpdateExtended(ctx, tenant)
```

This approach gives you the flexibility to add Stripe customer IDs and any other external service integrations without changing your database schema, while maintaining good performance through proper indexing.

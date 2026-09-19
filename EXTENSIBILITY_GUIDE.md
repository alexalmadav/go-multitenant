# Tenant Extensibility Guide

The library ships no tenant tables, no extension registry, and no second
tenant type. Extensibility comes from three mechanisms that are wired into
the core `Manager` and actually run:

1. **Tenant schema from migrations** — your application's migration files
   define what lives in each tenant schema.
2. **Metadata** — a JSONB map on the core `Tenant` for arbitrary per-tenant
   data (Stripe customer IDs, branding, feature flags, ...).
3. **Lifecycle hooks** — register a `tenant.Hook` to react to tenant events
   (sync an external system, audit log, ...).

## Tenant schema from migrations

### File naming and ordering

Point `config.Database.MigrationsDir` at a directory of SQL files named
`<version>_<name>.up.sql`, with an optional matching `<version>_<name>.down.sql`
for rollback:

```
migrations/
├── 001_create_projects.up.sql
├── 001_create_projects.down.sql
├── 002_create_tenant_users.up.sql
└── 002_create_tenant_users.down.sql
```

`MigrationManager.ListMigrationFiles()` sorts by filename and parses each name
by splitting on the first underscore, so `version` is everything before it
(`001`, but any string sorts fine as long as filenames sort in the order you
want them applied) and `name` is the rest. A file with no underscore, or a
`.down.sql` with no matching `.up.sql`, is an error. There is no numbering
requirement beyond "sorts in application order" — zero-padded integers are the
simplest choice.

If `MigrationsDir` is empty or unset, `New` logs a warning once and every
provisioned tenant gets an empty schema. A configured directory that does not
exist is an error from `New`.

### `search_path` inside a migration

Each migration file runs in a transaction with `search_path` set to only the
tenant schema — `public` is deliberately not on it. This means anything that
actually lives in `public`, including functions provided by an extension such
as `uuid-ossp`'s `uuid_generate_v4()`, must be schema-qualified in your SQL:
write `public.uuid_generate_v4()`, not `uuid_generate_v4()`. `gen_random_uuid()`
works unqualified because it lives in `pg_catalog`, which is always on the
search path.

### Provisioning and `ApplyPending`

```go
tenant := &multitenant.Tenant{
    ID:        uuid.New(),
    Name:      "Acme Corporation",
    Subdomain: "acme",
    Status:    multitenant.StatusPending,
}
tenant.SetPlan("pro")
err := mt.Manager.CreateTenant(ctx, tenant)
err = mt.Manager.ProvisionTenant(ctx, tenant.ID)
```

`ProvisionTenant`:

1. Loads the tenant (a `cancelled` tenant is rejected).
2. Runs `CREATE SCHEMA IF NOT EXISTS` for the tenant schema — idempotent.
3. Applies every migration file not yet recorded for this tenant, in filename
   order, recording each one in `public.tenant_migrations`.
4. Sets the tenant's status to `active` and saves it.
5. Fires `OnTenantStatusChanged` and `OnTenantProvisioned` hooks, but only on
   the transition to `active` (see [Lifecycle hooks](#lifecycle-hooks)).

### Resumable provisioning

If a migration fails partway through, the tenant stays `pending`; the schema
and the migrations that already applied are left in place; the error names
the failing version. There is no automatic rollback of the schema. Fix the
migration file (or the underlying issue) and call `ProvisionTenant` again — it
picks up from the first migration not yet recorded and continues. Calling
`ProvisionTenant` on an already-active tenant is a no-op beyond applying any
migration files added since it was provisioned.

### Rollout to existing tenants

Adding a migration file after tenants already exist does not touch them until
you say so:

```go
// Applies every not-yet-recorded migration file, in order, to every
// active tenant. New tenants provisioned afterward pick it up automatically.
err := mt.Migrations.ApplyPendingToAllTenants(ctx)
```

Failures are joined and returned together; tenants that succeed are not
rolled back because others failed.

### Rollback

`.down.sql` files are not run automatically. `MigrationManager.RollbackMigration`
runs a specific tenant's recorded migration's rollback SQL and removes its
record — call it directly when you need to undo a migration for one tenant.

## Metadata

### The typed getters

`Tenant.Metadata` is a `tenant.TenantMetadata` (`map[string]interface{}`),
loaded and saved as a JSONB column alongside the rest of the tenant row —
no separate table, no separate repository:

```go
t, err := mt.Manager.GetTenant(ctx, id)

t.Metadata.SetString("custom_domain", "app.acme.com")
t.Metadata.SetInt("seats_purchased", 25)
t.Metadata.SetBool("beta_opt_in", true)

domain, ok := t.Metadata.GetString("custom_domain")
seats, ok := t.Metadata.GetInt("seats_purchased")
betaOptIn, ok := t.Metadata.GetBool("beta_opt_in")
exists := t.Metadata.Has("custom_domain")
t.Metadata.Remove("beta_opt_in")

err = mt.Manager.UpdateTenant(ctx, t)
```

`UpdateTenant` writes the tenant struct you pass it wholesale, not just the
metadata field. Always start from a freshly-read tenant (as above, or one you
already hold from earlier in the same request) and mutate it in place. If you
build a new `*tenant.Tenant` yourself and its `Status` is stale, `UpdateTenant`
reverts the persisted status to that stale value and fires
`OnTenantStatusChanged` for the reversion — nothing warns you this happened.

### `Plan()` and `SetPlan()`

The plan name is likewise just a metadata key, `tenant.PlanKey`
(`"plan"`), with the same typed-getter treatment as a convenience:

```go
t.SetPlan("pro")  // t.Metadata.SetString(tenant.PlanKey, "pro")
plan := t.Plan()   // "" if never set
```

`tenant` never interprets the string — it is opaque to the core. The
optional `limits` package (see [FLEXIBLE_LIMITS.md](./FLEXIBLE_LIMITS.md))
is what maps plan names to actual limits, through
`limits.Config.PlanLimits` and, by default, `t.Plan()` itself
(`Config.PlanOf` to use something else).

### `StripeExtension` and `BrandingExtension`

Typed wrappers over specific metadata keys, for integrations the library
anticipates:

```go
stripe := tenant.NewStripeExtension(t.Metadata)
stripe.SetCustomerID("cus_123456")
stripe.SetSubscriptionID("sub_789012")
if stripe.HasStripeIntegration() {
    customerID, _ := stripe.GetCustomerID()
}

branding := tenant.NewBrandingExtension(t.Metadata)
branding.SetLogoURL("https://acme.com/logo.png")
branding.SetTheme("blue")
branding.SetCustomDomain("app.acme.com")
```

Both wrap the same `t.Metadata` map the typed getters above operate on —
`stripe.SetCustomerID` is equivalent to
`t.Metadata.SetString("stripe_customer_id", "cus_123456")`. Write your own
wrapper the same way for integrations the library doesn't anticipate: hold a
`tenant.TenantMetadata`, expose typed methods over the keys you care about.

### Querying by metadata: `FindByMetadata`

```go
// Tenants whose metadata["stripe_customer_id"] == "cus_123456"
tenants, err := repo.FindByMetadata(ctx, "stripe_customer_id", "cus_123456")
```

`FindByMetadata` is on `tenant.Repository` (the `postgres.Repository`
implementation runs `WHERE metadata ->> $1 = $2`), not on `Manager` — reach it
through whatever `Repository` your application holds, or through a custom
`Manager` method if you want it exposed at that layer.

### Indexing

The repository creates a GIN index on the whole `metadata` column
(`idx_tenants_metadata ON public.tenants USING GIN (metadata)`). A default
(`jsonb_ops`) GIN index on a JSONB column only serves containment and
key-existence queries — `@>`, `?`, `?|`, `?&`, and jsonpath operators. It does
**not** serve `FindByMetadata`: that query is `WHERE metadata ->> $1 = $2`,
text equality on one extracted key, which the GIN index cannot answer, so it
is a sequential scan over `public.tenants` regardless of how many tenants
exist. If you query one key often enough for that to matter, add a B-tree
expression index for that specific key:

```sql
CREATE INDEX idx_tenants_stripe_customer
ON public.tenants ((metadata ->> 'stripe_customer_id'));
```

That index is used only for lookups on `stripe_customer_id` — add one per key
you query this way. The GIN index remains useful if you also query with `@>`
or `?` directly (not currently done by anything in this library, but
available to application code with direct repository/database access).

## Lifecycle hooks

### The interface

```go
type Hook interface {
    Name() string
    // ValidateMetadata runs before create and update and blocks the write on error.
    ValidateMetadata(ctx context.Context, t *Tenant) error
    // The remaining methods run after the database write has committed.
    OnTenantCreated(ctx context.Context, t *Tenant) error
    OnTenantProvisioned(ctx context.Context, t *Tenant) error
    OnTenantUpdated(ctx context.Context, before, after *Tenant) error
    OnTenantStatusChanged(ctx context.Context, t *Tenant, previousStatus string) error
    OnTenantDeleted(ctx context.Context, t *Tenant) error
}
```

Embed `tenant.BaseHook` to implement only the methods you need — every
`BaseHook` method is a no-op, and `BaseHook.Name()` returns `"hook"` (give
your hook a real name by overriding it).

### Registering hooks

```go
mt.Manager.RegisterHook(myHook)
```

Hooks run in registration order. `RegisterHook` is safe to call concurrently
with request handling — a mutex guards the hook slice.

### Which Manager calls fire which events

| Manager call      | Before write         | After write                                   |
|--------------------|----------------------|-----------------------------------------------|
| `CreateTenant`      | `ValidateMetadata`     | `OnTenantCreated`                               |
| `ProvisionTenant`   |                      | `OnTenantStatusChanged` and `OnTenantProvisioned`, only when the tenant transitions to `active`; a re-run on an already-active tenant fires nothing |
| `UpdateTenant`      | `ValidateMetadata`     | `OnTenantUpdated`; `OnTenantStatusChanged` if status differs |
| `SuspendTenant`     |                      | `OnTenantStatusChanged`, only if status actually changes (suspending an already-suspended tenant fires nothing) |
| `ActivateTenant`    |                      | `OnTenantStatusChanged`, only if status actually changes (activating an already-active tenant fires nothing) |
| `DeleteTenant`      |                      | `OnTenantDeleted`                               |

`DeleteTenant` remains a soft delete (status set to `cancelled`); it fires
`OnTenantDeleted`, not `OnTenantStatusChanged`.

### Failure semantics

- **`ValidateMetadata`**: the first hook to return an error aborts — the write
  never happens. The error comes back from the `Manager` call wrapped in
  `ValidationError{Field: "metadata"}`.
- **After-write hooks** (`OnTenantCreated`, `OnTenantProvisioned`,
  `OnTenantUpdated`, `OnTenantStatusChanged`, `OnTenantDeleted`): every
  registered hook for that event runs even if an earlier one failed. Failures
  are collected into

  ```go
  type HookError struct {
      Event  string   // "created", "provisioned", "updated", "status_changed", "deleted"
      Errors []error  // one per failing hook, each wrapped with the hook's Name()
  }
  ```

  and returned from the `Manager` call as a `*tenant.HookError`. The tenant
  row is already persisted at this point and is **not** rolled back — the
  write succeeded; only the notification of it partially failed. Check
  `errors.As(err, &hookErr)` to distinguish this from a write failure.
- Hooks run synchronously, outside any transaction; the `Manager` call does
  not return until every hook for that event has run. The `context.Context`
  passed to hooks is the caller's context.

### Re-entrancy note

A hook that calls back into `Manager` (for example `UpdateTenant`, to persist
something the hook computed) triggers that call's own hooks, including
itself. `examples/stripe-integration`'s `StripeHook.OnTenantCreated` calls
`Manager.UpdateTenant` to store the Stripe customer ID it just created; that
`UpdateTenant` call fires `OnTenantUpdated`/`OnTenantStatusChanged` hooks,
which is why the example's hook does not also implement `OnTenantUpdated` —
implementing it there would need to guard against reacting to its own write.
Design hooks that call back into `Manager` with this in mind, to avoid
infinite loops or duplicated side effects.

### Example

```go
type auditHook struct{ tenant.BaseHook }

func (auditHook) Name() string { return "audit" }

func (auditHook) OnTenantStatusChanged(ctx context.Context, t *tenant.Tenant, prev string) error {
    log.Printf("tenant %s: %s -> %s", t.ID, prev, t.Status)
    return nil
}

mt.Manager.RegisterHook(auditHook{})
```

`examples/stripe-integration` is a complete, runnable program: a `StripeHook`
that creates a Stripe customer through a small `StripeClient` interface on
`OnTenantCreated`, stores the customer ID in metadata via `UpdateTenant`, and
deletes the customer on `OnTenantDeleted`. Its test uses a fake `StripeClient`
to assert both paths, and that a client failure surfaces as a `*tenant.HookError`
while the tenant itself remains created.

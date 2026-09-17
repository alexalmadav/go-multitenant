# Extensibility: provisioning from migrations, tenant metadata, lifecycle hooks

Date: 2026-09-15
Status: approved in discussion, awaiting spec review
Baseline: master at 97d5d57 (v0.6.0 plus PR #4)

## Goal

Replace the current "extensible tenant" layer, which is a second tenant type and a
second repository that the Manager, Resolver and middleware never see plus five
interfaces with no implementation, with three mechanisms that are wired into the
core and actually run:

1. A tenant's schema comes from the application's migration files, never from
   tables hardcoded in the library.
2. Every tenant carries a JSONB metadata map on the core `Tenant` type.
3. Applications react to tenant lifecycle events through registered hooks.

The library is at v0.6.0. Breaking changes to public signatures are acceptable
and are listed at the end.

## Non-goals

- Lifecycle hooks inside the database transaction (rejected: hooks call external
  services and must not hold transactions open).
- Custom domain resolution, the `Storage` interface, or the plan-type CHECK
  constraint. Those remain deferred.
- Any change to the limits engine beyond telling the usage tracker which tables
  to count.

## 1. Provisioning from migration files

### Behaviour

`Manager.ProvisionTenant(ctx, id)`:

1. Load the tenant. If status is `cancelled`, return an error.
2. `SchemaManager.CreateTenantSchema(ctx, id)` runs `CREATE SCHEMA IF NOT EXISTS`
   and nothing else. It is idempotent.
3. `MigrationManager.ApplyPending(ctx, id)` applies every migration file not yet
   recorded for this tenant, in order.
4. Set status to `active` and save.
5. Run `OnTenantProvisioned` hooks (section 4).

The current early return when the schema already exists is removed. Re-running
`ProvisionTenant` on a tenant whose earlier provision failed part-way applies the
remaining migrations and activates it. Provisioning an already-active tenant is a
no-op apart from applying any migration files added since.

If a migration fails, the tenant stays `pending`, the schema and the migrations
applied so far remain, and the returned error names the failing version. No
schema cleanup is attempted (the previous code dropped the schema when the
status update failed; that path goes away because the status update is now the
last step and a failure there leaves a consistent, re-runnable state).

### Migration files

`MigrationManager` gains:

- `ApplyPending(ctx, tenantID) error`
- `ApplyPendingToAllTenants(ctx) error` (active tenants only, errors joined, same
  shape as `ApplyToAllTenants`)
- `ListMigrationFiles()` now returns results sorted by filename and parses each
  `<version>_<name>.up.sql` by splitting on the first underscore. A file without
  an underscore, or a `.down.sql` without a matching `.up.sql`, is an error.

The `Migration` struct is unchanged. `ApplyMigration`, `RollbackMigration`,
`ApplyToAllTenants`, `GetAppliedMigrations`, `IsMigrationApplied` are unchanged.

`MigrationsDir` empty or unset: `New` logs a warning once; provisioning creates
an empty schema. A configured directory that does not exist is an error from
`New`.

### What is deleted

From `database/schema.go`: `createTenantTables` and everything it created
(projects, tasks, documents, tenant_users, their indexes, the
`update_updated_at_column` function and its triggers). `CreateTenantSchema`
loses its `name string` parameter.

The `tenant.SchemaManager` interface becomes:

```go
type SchemaManager interface {
    CreateTenantSchema(ctx context.Context, tenantID uuid.UUID) error
    DropTenantSchema(ctx context.Context, tenantID uuid.UUID) error
    SchemaExists(ctx context.Context, tenantID uuid.UUID) (bool, error)
    GetSchemaName(tenantID uuid.UUID) string
    ListTenantSchemas(ctx context.Context) ([]string, error)
}
```

`SetSearchPath` is removed along with the deprecated `Manager.GetTenantDB`,
`GetTenantDBFromContext` and `ContextKeyTenantDB`. They were documented as
unsafe and have had replacements for two releases.

### Test fixture

`testdata/migrations/` holds the tables the existing integration tests rely on:

- `001_create_projects.up.sql` / `.down.sql`: `projects(id, name, status, created_at)`
- `002_create_tenant_users.up.sql` / `.down.sql`: `tenant_users(user_id, role, is_active)`

Every integration test that calls `New` sets `MigrationsDir` to this fixture
through the shared helper. Tests that exercise migrations themselves keep using
`t.TempDir()`.

## 2. Usage tracking and stats

### Usage tracker

`tenant.LimitsConfig` gains:

```go
// UsageTables maps a limit name to a table in the tenant schema whose row
// count is that limit's current usage. Limits not listed are not checked.
UsageTables map[string]string `json:"usage_tables"`
```

`postgres.NewUsageTracker(db, schemaManager, usageTables, logger)` counts rows in
the mapped table and returns `nil, nil` for any limit not in the map. Table
names are validated against `^[a-z_][a-z0-9_]*$` at construction; anything else
is an error from `New`. Counting a table that does not exist in a tenant's
schema returns an error, which `CheckLimit` already logs and treats as "skip".

`DefaultConfig()` ships `UsageTables` empty. The README states that limits are
enforced only for limits listed in `UsageTables` or supplied by a custom
`UsageTracker`.

### Stats

```go
type Stats struct {
    TenantID          uuid.UUID      `json:"tenant_id"`
    SchemaExists      bool           `json:"schema_exists"`
    AppliedMigrations int            `json:"applied_migrations"`
    Usage             map[string]int `json:"usage"` // keyed by limit name, from UsageTables
}
```

`GetStats` moves from `Repository` to `Manager` (it needs the schema manager,
migration manager and usage tracker). `Repository.GetStats` is removed.

## 3. Metadata on the core tenant

### Model

```go
type Tenant struct {
    // existing fields unchanged
    Metadata TenantMetadata `json:"metadata"`
}
```

`TenantMetadata` (`map[string]interface{}`), its `Value`/`Scan` implementations
and typed getters/setters move from `extensible_models.go` to `metadata.go`
unchanged. `StripeExtension` and `BrandingExtension` stay, also in `metadata.go`.

A nil `Metadata` is written as `{}`. Reads always return a non-nil map.

### Storage

`CreateMasterTables` creates `public.tenants` with
`metadata JSONB NOT NULL DEFAULT '{}'` and, for databases created by earlier
versions, runs `ALTER TABLE public.tenants ADD COLUMN IF NOT EXISTS metadata JSONB
NOT NULL DEFAULT '{}'`. A GIN index `idx_tenants_metadata` is created.

`Repository.Create`, `Update`, `GetByID`, `GetBySubdomain` and `List` read and
write the column. `Repository` gains:

```go
FindByMetadata(ctx context.Context, key string, value string) ([]*Tenant, error)
```

implemented as `WHERE metadata ->> $1 = $2`. The GIN index above serves
containment/key-existence queries (`@>`, `?`), not this one; `FindByMetadata`
is a sequential scan unless the application adds a B-tree expression index on
the specific key it queries.

`Manager.UpdateTenant` continues to write the whole row, metadata included.
There is no separate "update one metadata key" API; callers load, mutate, save.

### What is deleted

`tenant/extensible_interfaces.go` entirely (`ExtensibleRepository`,
`ExtensibleManager`, `TenantExtension`, `ExtensionRegistry`, `SchemaRegistry`,
`ExtensionSchema`, `MetadataField`), `ExtensibleTenant` and its converters,
`database/postgres/extensible_repository.go`, `examples/extensible-tenant/`.

## 4. Lifecycle hooks

### Interface

```go
// Hook receives tenant lifecycle events. Embed BaseHook to implement only
// the methods you need.
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

type BaseHook struct{}
// every method returns nil; Name returns "hook"
```

### Registration and ordering

`Manager.RegisterHook(h Hook)`. Hooks run in registration order. Registration is
safe to call concurrently with request handling (a mutex guards the slice).

### Which Manager calls fire which events

| Manager call      | Before write         | After write                                   |
|-------------------|----------------------|-----------------------------------------------|
| CreateTenant      | ValidateMetadata     | OnTenantCreated                               |
| ProvisionTenant   |                      | OnTenantStatusChanged and OnTenantProvisioned, only when the tenant transitions to active; a re-run on an active tenant fires nothing |
| UpdateTenant      | ValidateMetadata     | OnTenantUpdated; OnTenantStatusChanged if status differs |
| SuspendTenant     |                      | OnTenantStatusChanged, only if status actually changes (suspending an already-suspended tenant fires nothing) |
| ActivateTenant    |                      | OnTenantStatusChanged, only if status actually changes (activating an already-active tenant fires nothing) |
| DeleteTenant      |                      | OnTenantDeleted                               |

`DeleteTenant` remains a soft delete (status `cancelled`); it fires
`OnTenantDeleted`, not `OnTenantStatusChanged`.

### Failure semantics

- `ValidateMetadata`: the first error aborts; the write does not happen; the
  error is returned wrapped in `ValidationError{Field: "metadata"}`.
- After-write hooks: every registered hook runs even if an earlier one failed.
  Errors are collected into

  ```go
  type HookError struct {
      Event  string   // "created", "provisioned", ...
      Errors []error  // one per failing hook, each wrapped with the hook name
  }
  ```

  and returned from the Manager call. The tenant row is already persisted and
  is not rolled back. Hooks are synchronous; the Manager call does not return
  until all have run. The context passed to hooks is the caller's context.

### Example

`examples/stripe-integration/` becomes a runnable program: a `StripeHook` that on
`OnTenantCreated` creates a customer through a small `StripeClient` interface and
stores the ID in metadata via `UpdateTenant`, and on `OnTenantDeleted` deletes
the customer. A unit test with a fake client asserts both paths and that a
client failure surfaces as a `HookError` while the tenant remains.

## Documentation

- `README.md`: architecture diagram no longer lists example tables; new
  "Tenant schema" section on `MigrationsDir` and provisioning; new "Metadata"
  and "Lifecycle hooks" sections; limits section states the `UsageTables`
  requirement; remove the "Tenant Schema Tables" SQL and the `GetTenantDB`
  references.
- `EXTENSIBILITY_GUIDE.md`: rewritten to cover exactly the three mechanisms.
- `FLEXIBLE_LIMITS.md`: add `UsageTables`.
- `README_TESTS.md`: mention the migrations fixture.

## Breaking changes (v0.7.0)

- `SchemaManager.CreateTenantSchema` drops the `name` parameter; `SetSearchPath` removed.
- `Manager.GetTenantDB`, `GetTenantDBFromContext`, `ContextKeyTenantDB` removed.
- `Repository.GetStats` removed; `Manager.GetStats` returns the new `Stats` shape.
- `Stats` fields changed.
- `Tenant` gains `Metadata`; `ExtensibleTenant` and `ExtensibleRepository` removed.
- `postgres.NewUsageTracker` takes a `usageTables` argument.
- Newly provisioned tenants get no tables unless `MigrationsDir` is set.
- Existing tenants provisioned under v0.6 keep their tables (projects, tasks,
  documents, tenant_users) but have no rows in `public.tenant_migrations`;
  bring them under migration control with `CREATE TABLE IF NOT EXISTS` first
  migrations or by backfilling baseline `tenant_migrations` rows.
- `LimitsConfig.UsageTables` defaults to empty, so `EnforceLimits: true` no
  longer enforces any limit unless its table is listed there — v0.6 behaviour
  for `max_projects`/`max_users` requires setting it explicitly.

## Testing strategy

- Unit (mocks, `-short -race`): hook ordering, error collection, ValidateMetadata
  blocking; ListMigrationFiles sorting and parsing; usage tracker table-name
  validation; metadata Value/Scan round trip.
- Integration (Postgres): provision applies fixture migrations in order and
  records them; re-provision after a failing migration resumes; empty
  MigrationsDir yields an empty schema; ApplyPendingToAllTenants brings an
  older tenant up to date; metadata round-trips through Create/Get/Update/List
  and FindByMetadata; the metadata column is added to a pre-existing tenants
  table; usage tracker counts a configured table and skips an unconfigured
  limit; GetStats reports migrations and usage; hooks fire on each Manager call
  against a real database.

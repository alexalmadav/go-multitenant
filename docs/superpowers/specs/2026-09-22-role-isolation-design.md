# Role isolation — design

**Status:** accepted, 2026-09-22
**Builds on:** `feat/membership-seam` (PR #9). Lands as a stacked PR against that branch.

## Problem

Tenant isolation today is `SET LOCAL search_path` on a connection that every
tenant shares. The pool logs in as one database role, and that role can read
and write every tenant schema. `search_path` only chooses which schema an
*unqualified* name resolves to; it is a default, not a permission.

So on a correctly scoped connection for tenant A:

```sql
SELECT * FROM "tenant_<globex-uuid>".projects
```

returns tenant B's rows. That needs either a handler that builds a
schema-qualified name from input, or a SQL injection anywhere in the
application. The database never refuses.

The driver runs pgx in simple protocol (`multitenant.go:251`) so that it works
behind PgBouncer in transaction mode. Parameterized queries remain safe — pgx
escapes them — but application code that concatenates strings can stack
statements. Any defence that an attacker's SQL can undo is therefore not a
boundary.

### Why not `SET LOCAL ROLE` on the shared pool

The obvious fix — a role per tenant, `SET LOCAL ROLE` next to the
`search_path` — was considered and rejected. To switch into a tenant role, the
pool's login role must be a member of *every* tenant role. Injected SQL can
then switch too (`SET ROLE`, `RESET ROLE`, `set_config('role', …)`). It would
stop the honest mistake of a qualified name in handler code, and nothing
more.

The only arrangement an attacker's SQL cannot undo is a connection whose
**login user** has no privileges on other tenants' schemas.

## Goal and threat model

**Threat:** SQL injection, or simply wrong SQL, executed on a tenant-scoped
connection.

**Success:** on its own connection, tenant A

1. is refused a schema-qualified read or write of tenant B's schema,
2. is refused `SET ROLE` to tenant B's role, and `RESET ROLE` gains it nothing,
3. is refused `SELECT` on `public.tenants` and `public.tenant_migrations`,
4. is refused DDL (`CREATE`, `ALTER`, `DROP`) in its own schema and in `public`,
5. can still `SELECT`, `INSERT`, `UPDATE` and `DELETE` its own tables and use
   its own sequences.

Each of these is an integration test.

## Non-goals

- **Hiding catalog metadata.** Postgres system catalogs are readable by every
  role, so tenant A can list other schema and role names, which contain tenant
  ids. It can never read their data.
- **A compromised admin credential or application host.** Whoever holds the
  admin DSN or the role secret has every tenant.
- **Row-level security or shared-schema tenancy.** A different architecture.
- **Runtime DDL on tenant connections.** Tenant roles get DML only;
  migrations remain the admin's job.
- **Thousands of tenants direct to Postgres.** Supported, but each warm tenant
  costs a Postgres backend. At that scale this mode is designed for a
  connection pooler, and the docs say so.
- **Changing the default.** `search_path` isolation stays the default; role
  isolation is opt-in.

## Design

### Mode and configuration

`tenant.DatabaseConfig` gains:

```go
Isolation     IsolationMode       // IsolationSearchPath (default) or IsolationRole
RoleIsolation RoleIsolationConfig // used only when Isolation == IsolationRole
```

```go
type RoleIsolationConfig struct {
	// TenantDSN is where tenant connections go - normally PgBouncer. Its user
	// and password are replaced per tenant.
	TenantDSN string

	// Secret derives every tenant role's password. At least 32 bytes.
	// PreviousSecrets are tried, in order, when a connection fails to
	// authenticate with Secret; they exist only for rotation.
	Secret          []byte   `json:"-"`
	PreviousSecrets [][]byte `json:"-"`

	// Credentials replaces the derived-password scheme when set.
	Credentials func(tenantID uuid.UUID) (user, password string, err error) `json:"-"`

	MaxConns          int           // connections in use at once, all tenants; default 50
	PerTenantMaxConns int           // connections in use at once, one tenant; default 5
	MaxWarmTenants    int           // tenant pools kept open; default 1000
	IdleTimeout       time.Duration // a warm pool with nothing in use closes after this; default 5m
}
```

The defaults assume `TenantDSN` points at a connection pooler, where a warm
pool's idle connection is cheap. Pointed straight at Postgres, each warm pool
holds a real backend, and `MaxConns + MaxWarmTenants` must fit inside
`max_connections` alongside the admin pool — the default 1,050 would not. The
field documentation and README say this plainly, with the arithmetic.

`multitenant.New` rejects role mode when `TenantDSN` is empty, the secret is
shorter than 32 bytes and no `Credentials` hook is set, or the server is older
than PostgreSQL 15 (see *Why PostgreSQL 15*). Secrets carry `json:"-"` so a
logged or serialised config never contains them.

### Units

| Unit | File | Job |
|---|---|---|
| Credentials | `tenant/credentials.go` | tenant id → role name and password |
| Role manager | `database/roles.go` | create, grant, lock, re-key and drop tenant roles, on the admin pool |
| Tenant pools | `tenant/pools.go` | one small `*sql.DB` per tenant, bounded globally |
| Role-isolated manager | `tenant/role_manager.go` | a `Manager` decorator whose `GetTenantConn` and `WithTenantTx` draw from the tenant pools; every other method passes through |
| Isolation hook | `database/role_hook.go` | embeds `tenant.BaseHook` and ties the role manager to the tenant lifecycle |

In role mode `multitenant.New` builds the manager exactly as today, wraps it
in the decorator, registers the isolation hook on it, and exposes the
decorated manager as `mt.Manager` — so `mt.HTTPMiddleware`, the Gin adapter
and application code all get role-scoped connections without knowing it.
`tenant.NewManager` and the `Manager` interface do not change. Operational
entry points — `EnsureTenantRoles`, `RotateTenantCredentials` and
`TenantPoolStats`, which returns the pools' `Stats()` — go on `*MultiTenant`
for the same reason. The decorator lives in package `tenant` because it
constructs `tenant.Conn`, whose constructor is unexported; `newConn` gains a
release callback.

**Credentials.** The role name is the schema name
(`<SchemaPrefix><uuid with underscores>`, 43 characters with the default
prefix; `New` rejects a prefix that would exceed Postgres's 63-character
identifier limit). The derived password is

```
base64url( HMAC-SHA256( secret, "go-multitenant/role-password/v1:" + tenantID ) )
```

The domain-separation label means the same secret can never yield the same
bytes for another purpose, and `v1` leaves room to change the scheme.

When a `Credentials` hook is set it replaces derivation everywhere: the role
manager sets the password the hook returns, rotation re-sets it from the hook,
and `Secret`/`PreviousSecrets` are ignored. A hook may return an empty
password, in which case the role is created with none and authentication is
left to `pg_hba.conf` — client certificates, or a cloud IAM plugin.

**Role manager.** All statements for one tenant run in one admin-pool
transaction; role DDL is transactional in Postgres.

```sql
CREATE ROLE <role> LOGIN NOINHERIT NOCREATEDB NOCREATEROLE PASSWORD '<scram verifier>';
GRANT USAGE ON SCHEMA <schema> TO <role>;
ALTER DEFAULT PRIVILEGES IN SCHEMA <schema>
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO <role>;
ALTER DEFAULT PRIVILEGES IN SCHEMA <schema>
    GRANT USAGE, SELECT ON SEQUENCES TO <role>;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA <schema> TO <role>;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA <schema> TO <role>;
```

The role is a member of no other role. `ALTER DEFAULT PRIVILEGES` omits
`FOR ROLE`, so it applies to the admin role that runs migrations — every table
a later migration creates is granted automatically. The two backfill grants
cover tables that already exist. Every step is idempotent: an existing role
gets `ALTER ROLE … PASSWORD` instead of `CREATE ROLE`.

**No plaintext password reaches the server.** The library computes the
SCRAM-SHA-256 verifier itself — `crypto/pbkdf2` from the standard library,
4096 iterations, a random 16-byte salt — and sends
`PASSWORD 'SCRAM-SHA-256$4096:…'`. With `log_statement = ddl`, the server log
records a verifier that cannot be used to log in.

**Tenant pools.** One `*sql.DB` per tenant, opened on first use against
`TenantDSN` with the tenant's credentials, the same pgx settings as the admin
pool (simple protocol), `MaxOpenConns = PerTenantMaxConns`,
`MaxIdleConns = 1` and `ConnMaxIdleTime = IdleTimeout`.

- A **global slot** is taken before `db.Conn` and returned when the
  `tenant.Conn` closes, exactly once (`sync.Once`). This caps connections in
  use across every tenant at `MaxConns`.
- **Eviction** is least-recently-used, and only ever closes a pool with no
  connection in use. If every warm pool is busy when a new tenant needs one,
  the warm count may exceed `MaxWarmTenants`, by at most `MaxConns`.
- A warm pool with nothing in use for `IdleTimeout` is closed.
- `Stats()` reports warm pools, connections in use, slot waits, hits, cold
  opens and evictions.

### Lifecycle

| Event | Isolation hook action |
|---|---|
| `OnTenantProvisioned` | create the role and grants (after the schema and migrations exist) |
| `OnTenantStatusChanged` to suspended or cancelled | `ALTER ROLE … NOLOGIN`, close the tenant's pool |
| `OnTenantStatusChanged` to active | `ALTER ROLE … LOGIN` |
| `OnTenantDeleted` | close the pool, `DROP OWNED BY <role>`, `DROP ROLE <role>` |

`NOLOGIN` means a suspended tenant is locked out by the database itself, not
only by `ValidateTenant`.

**Failure after activation.** `ProvisionTenant` activates the tenant before it
runs hooks, so a failed role creation leaves an active tenant with no role.
That fails closed — its connections cannot authenticate — and
`ProvisionTenant` returns the error. `EnsureTenantRoles` repairs it.

**Existing tenants.** `(*MultiTenant).EnsureTenantRoles(ctx)` creates any
missing role and reapplies grants and `LOGIN`/`NOLOGIN` for every tenant,
idempotently. The upgrade note says to run it once before switching a
deployment to role mode. It is also the repair tool.

**Rotation, with no downtime.**

1. Deploy with `Secret` set to the new key and `PreviousSecrets` holding the old.
2. When a tenant pool's first connection fails with SQLSTATE `28P01`
   (`invalid_password`), the pool is reopened with each previous key in
   turn, and keeps whichever authenticates. Any other error is returned as is.
3. `(*MultiTenant).RotateTenantCredentials(ctx)` re-keys every role to `Secret`.
4. Remove the old key from `PreviousSecrets`.

Already-open connections stay authenticated throughout.

### Request flow and errors

`GetTenantConn` and `WithTenantTx` in role mode:

1. take a global slot, waiting only as long as `ctx` allows;
2. take the tenant's pool, opening it on first use and marking it recently used;
3. `db.Conn(ctx)`, still bounded by `PerTenantMaxConns`;
4. return a `tenant.Conn` whose `Close` releases the slot.

`search_path` is still set on every statement, so unqualified names behave
exactly as today. `tenant.Conn`, `Conn.Unwrap() *sql.Conn` and
`WithTenantTx(func(*sql.Tx) error)` keep their types; application code does
not change.

| Condition | Result |
|---|---|
| slot wait outlives `ctx` | `ErrPoolExhausted`; `SetTenantDB` answers `TENANT_DB_BUSY`, **503**, `Retry-After: 1` |
| authentication fails with every secret | an error naming the role and pointing at `EnsureTenantRoles` or a secret mismatch; the password is never logged |
| role mode against PostgreSQL < 15 | `New` refuses to start |

The registry, provisioning, migrations and usage counting stay on the admin
pool, unchanged.

### Why PostgreSQL 15

Before 15, `PUBLIC` holds `CREATE` on the `public` schema, and every role
inherits `PUBLIC`. A tenant role could then create tables in `public`, which
breaks success criterion 4 and gives it a place to stage data. From 15 that
grant is gone by default. Rather than have the library revoke a
database-wide privilege other applications may rely on, role mode requires 15
and `New` checks `server_version_num`.

`PUBLIC` still grants `CONNECT` on the database and `TEMP`. Temporary tables
are session-local and disappear with the connection, so they cross no tenant
boundary.

### PgBouncer

Each tenant logs in as its own role, so PgBouncer keeps a pool per role and
authenticates each one. It learns passwords through `auth_query`: a
`SECURITY DEFINER` function, owned by a privileged role, that returns the
SCRAM verifier from `pg_authid`. The README documents the function and the
`auth_user`/`auth_query` settings. PgBouncer cannot reuse a server connection
across roles, so `max_db_connections` and `server_idle_timeout` need sizing
for many small pools; the docs give the arithmetic.

## Rejected alternatives

- **`SET LOCAL ROLE` on the shared pool** — undone by injected SQL; see above.
- **One global pool of tenant-tagged pgx connections** — tighter limits and
  better reuse, but it replaces `database/sql` under `tenant.Conn` and breaks
  `Conn.Unwrap()` and `WithTenantTx`'s `*sql.Tx`, both public.
- **Connect per request, pooling left to PgBouncer** — least code, but every
  request pays a connection and a SCRAM handshake even behind PgBouncer.
- **Random passwords stored on the tenant record** — a credential at rest in
  `public.tenants` that would need encrypting, which brings back a master key.
- **A startup check against `max_connections`** — behind PgBouncer that value
  bounds PgBouncer's server side, not the library's connections, so the check
  would mislead.

## Compatibility

Additive and opt-in. With `Isolation` unset nothing changes: no roles, no new
pools, no new startup checks. `TENANT_DB_BUSY` is emitted only in role mode.

## Testing

**Unit — `tenant/pools.go`**, against a counting stub driver registered for
the test: slot accounting, release exactly once, eviction order, never
evicting a busy pool, the per-tenant cap, a context-cancelled slot wait, idle
close, and a many-goroutine run under `-race` asserting that connections in
use never exceed `MaxConns`.

**Unit — credentials:** derivation is stable per tenant, differs across
tenants and secrets, and the SCRAM verifier Postgres accepts is produced
(checked in integration).

**Integration — real Postgres, and in CI also through PgBouncer:**

- the five success criteria, each **mutation-verified** — for example,
  granting a tenant role `USAGE` on another tenant's schema must make the
  qualified-read test fail;
- a table added by a later migration is usable by the tenant role (default
  privileges);
- suspend makes login fail; activate restores it;
- delete drops the role;
- rotation: connections keep working across all four steps;
- `EnsureTenantRoles` repairs a tenant whose role was dropped;
- the cross-tenant end-to-end test runs again in role mode.

Roles are cluster-wide, not per-database, so the test cleanup drops tenant
roles explicitly.

**CI:** the PgBouncer job gains `auth_user`/`auth_query` and creates the
lookup function, then runs the role-mode suite through PgBouncer in
transaction mode.

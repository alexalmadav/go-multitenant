# Role isolation — design

**Status:** accepted, 2026-09-22; revised 2026-09-27 after re-review;
revised 2026-09-28 with spike results
**Builds on:** v0.9.0 (membership seam, PR #9).

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

The driver runs pgx in simple protocol (`multitenant.go`, `setupDatabase`) so
that it works behind PgBouncer in transaction mode. Parameterized queries
remain safe — pgx escapes them — but application code that concatenates
strings can stack statements. Any defence that an attacker's SQL can undo is
therefore not a boundary.

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
4. is refused DDL (`CREATE`, `ALTER`, `DROP`, `TRUNCATE`) in its own schema
   and in `public`,
5. can still `SELECT`, `INSERT`, `UPDATE` and `DELETE` its own tables and use
   its own sequences.

Each of these is an integration test.

## Non-goals

- **Hiding catalog metadata.** Postgres system catalogs are readable by every
  role, so tenant A can list other schema and role names, which contain tenant
  ids. It can never read their data.
- **A compromised admin credential or application host.** Whoever holds the
  admin DSN or the role secret has every tenant.
- **Extensions that grant `PUBLIC` powerful functions** (for example `dblink`
  or foreign-data wrappers). What an extension grants `PUBLIC`, every tenant
  role inherits. Auditing installed extensions is the operator's job; the
  README says so.
- **Row-level security or shared-schema tenancy.** A different architecture.
- **Runtime DDL on tenant connections.** Tenant roles get DML only;
  migrations remain the admin's job.
- **Thousands of tenants direct to Postgres.** Supported, but each warm tenant
  costs a Postgres backend. At that scale this mode is designed for a
  connection pooler, and the docs say so.
- **Changing the default.** `search_path` isolation stays the default; role
  isolation is opt-in.

## Pre-implementation spike

One question decides whether this design holds at thousands of tenants, and
it is answered before the implementation plan is written.

**Question.** PgBouncer keeps a separate server-connection pool per role and
cannot lend a server connection logged in as one role to another. When
`max_db_connections` is reached, does PgBouncer close another role's *idle*
server connection to serve a waiting role, or does the waiting role wait until
`server_idle_timeout` frees one?

**Method.** PgBouncer in transaction mode in front of Postgres 16, at the top
of the target range: **5,000 roles**, one per simulated tenant, with
`max_db_connections` far below the role count. Measure:

1. **Reclamation.** Fill every server connection with idle connections held
   by other roles, then start a transaction as a role that has none, and time
   it.
2. **Churn.** Concurrent workers run short transactions as roles picked at
   random from all 5,000, and the latency distribution and error count are
   recorded, with `server_idle_timeout` at its default and at a short value.
3. **Per-pool overhead.** PgBouncer's memory with all 5,000 per-role pools
   created.
4. **`auth_file` reload.** How long PgBouncer takes to `RELOAD` a 5,000-entry
   file, which bounds how quickly a new tenant can connect on managed
   Postgres, and confirmation that SCRAM pass-through works with verifiers
   supplied through `auth_file`.

**Decision rule.**

- If PgBouncer reclaims idle connections across roles, churn latency stays
  within a normal request deadline, and overhead and reload time are modest
  at 5,000: the design stands as written.
- If it does not: the docs require a short `server_idle_timeout` (tens of
  seconds, sized from the measured reconnect cost), and the sizing arithmetic
  in the README is written around it. If even a short timeout makes new
  tenants wait longer than a request deadline under the measured load, the
  design returns to review before any code is written.

### Spike results (2026-09-28)

**Environment.** PostgreSQL 16.11 and PgBouncer 1.25.2 in Docker on one
laptop (Docker Desktop), transaction mode, `max_db_connections = 20`,
`default_pool_size = 5`, 5,000 login roles. The load generator ran inside the
same Docker network, since Docker Desktop's host port forwarding could not
sustain the connection churn. Absolute throughput on dedicated hardware will
be higher; the ratios between runs are what the numbers are for.

**1. Reclamation — yes.** With all 20 server connections held idle by 20
other roles, a role with no connection ran its first transaction in 9–16 ms,
login included, and the server-connection count stayed at 20. PgBouncer
closes another role's idle server connection to serve a waiting role. The
central question is answered in the design's favour.

**2. Churn.** 50 concurrent workers, each running `SELECT 1` transactions as
a tenant chosen at random, with one warm client connection kept per tenant:

| Tenants sharing the load | Mode | Transactions/s | p50 | p99 | Timeouts (5 s) |
|---|---|---|---|---|---|
| 1 | plaintext `auth_file` | ~48,000 | 0.8 ms | 4.1 ms | 0 |
| 20 | plaintext `auth_file` | ~830 | 60 ms | 145 ms | 0 |
| 5,000 | plaintext `auth_file` | ~120 | 265 ms | 1.95 s | 2.2% |
| 5,000 | `auth_query` | ~550 | 45 ms | 466 ms | 0.4% |

The cost is tenant switching, not queries. A server connection logged in as
one role cannot serve another, so once traffic spreads across more tenants
than there are server connections, most transactions pay for a fresh
Postgres login. At 5,000 roles Postgres was the busiest process (164% CPU in
`auth_query` mode); with plaintext entries PgBouncer's single thread saturated
first (99%) on SCRAM key derivation. Uniform random access across 5,000
tenants is the worst case; real traffic concentrates on fewer tenants at a
time, which moves results toward the upper rows.

**3. Per-pool overhead — modest.** PgBouncer stayed between 25 and 53 MiB with
5,000 per-role pools and up to ~4,800 client connections.

**4. `auth_file` — works, with plaintext entries.** A real reload of a
5,002-entry file took 2.6 ms, and a newly added tenant could log in
immediately after. SCRAM *verifiers* in the file, however, lock a role out
after a failed first login (see *PgBouncer authentication*).

**Found along the way.** PgBouncer answers a wrong password with SQLSTATE
`08P01`, not class `28`; PgBouncer's descriptor limit, not its connection
limit, capped the first runs.

**Decision.** The design stands, with four changes made above: `auth_file`
carries plaintext passwords, the SCRAM salt is random again, the rotation
retry recognises PgBouncer's `08P01`, and PgBouncer's descriptor limit is a
documented requirement. The README states the throughput shape in the table
above plainly: role isolation trades peak throughput under wide tenant spread
for a database-enforced boundary, and `auth_query` is preferred wherever a
superuser is available.

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

	// Secret derives every tenant role's password. At least 32 bytes, and
	// distinct per environment. PreviousSecrets are tried, in
	// order, when a connection fails to authenticate with Secret; they exist
	// only for rotation.
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

**One admin role.** Every role-management operation — provisioning, status
changes, rotation, repair, deletion — runs on the admin pool as the same
database user. From PostgreSQL 16, a `CREATEROLE` user can alter and drop only
roles it holds `ADMIN OPTION` on, which it receives for the roles it creates.
Running provisioning as one user and rotation as another would fail. The same
user must also run migrations; see *Migrations*.

### Units

| Unit | File | Job |
|---|---|---|
| Credentials | `tenant/credentials.go` | tenant id → role name, password and SCRAM verifier |
| Role manager | `database/roles.go` | ensure, lock, re-key and drop tenant roles, on the admin pool |
| Tenant pools | `tenant/pools.go` | one small `*sql.DB` per tenant, bounded per tenant and globally |
| Role-isolated manager | `tenant/role_manager.go` | a `Manager` decorator whose `GetTenantConn` and `WithTenantTx` draw from the tenant pools; every other method passes through |
| Granting migrations | `database/role_migrations.go` | a `MigrationManager` decorator that reapplies grants after every migration run |
| Isolation hook | `database/role_hook.go` | embeds `tenant.BaseHook` and ties the role manager to the tenant lifecycle |
| PgBouncer auth file | `database/pgbouncer.go` | renders PgBouncer `auth_file` entries for every tenant role |

In role mode `multitenant.New` builds the manager and migration manager
exactly as today, wraps each in its decorator, registers the isolation hook
**before any application hook** — so an application's own
`OnTenantProvisioned` hook can already use tenant connections — and exposes
the decorated values as `mt.Manager` and `mt.Migrations`. `mt.HTTPMiddleware`,
the Gin adapter and application code all get role-scoped connections without
knowing it. `tenant.NewManager` and the `Manager` and `MigrationManager`
interfaces do not change.

Operational entry points go on `*MultiTenant` for the same reason:
`EnsureTenantRoles`, `RotateTenantCredentials`, `PgBouncerAuthFile` and
`TenantPoolStats`, which returns the pools' `Stats()`.

The role-isolated manager lives in package `tenant` because it constructs
`tenant.Conn`, whose constructor is unexported; `newConn` gains a release
callback.

### Credentials

The role name is the schema name (`<SchemaPrefix><uuid with underscores>`, 43
characters with the default prefix; `New` rejects a prefix that would exceed
Postgres's 63-character identifier limit).

The password is derived:

```
password = base64url( HMAC-SHA256( secret, "go-multitenant/role-password/v1:" + tenantID ) )
```

The domain-separation label keeps this derivation from ever producing the
same bytes as any future use of the secret, and `v1` leaves room to change the
scheme.

**The verifier.** The library computes the SCRAM-SHA-256 verifier itself —
`crypto/pbkdf2` from the standard library, 4096 iterations, a random 16-byte
salt — and sends `PASSWORD 'SCRAM-SHA-256$4096:…'`. No plaintext password reaches the
server, so with `log_statement = ddl` the server log records a verifier that
cannot be used to log in. The password alphabet is base64url, which is ASCII,
so the SASLprep normalisation SCRAM specifies is the identity and no Unicode
library is needed.

When a `Credentials` hook is set it replaces derivation everywhere: the role
manager sets the password the hook returns, rotation re-sets it from the hook,
`PgBouncerAuthFile` renders the hook's passwords, and `Secret` and
`PreviousSecrets` are ignored. A hook may return an empty password, in which
case the role is created with none and authentication is left to
`pg_hba.conf` — client certificates, or a cloud IAM plugin. Such a tenant
cannot go through an `auth_file`, so `PgBouncerAuthFile` reports an error
naming it.

### Role manager

The central operation is **ensure**, which brings one tenant's role to the
state its registry record calls for. It runs in one admin-pool transaction
(role DDL is transactional in Postgres) and is idempotent:

```sql
CREATE ROLE <role> NOINHERIT NOCREATEDB NOCREATEROLE;         -- only if missing
ALTER ROLE <role> PASSWORD '<scram verifier>';
ALTER ROLE <role> LOGIN;                                       -- or NOLOGIN; see below
GRANT USAGE ON SCHEMA <schema> TO <role>;
ALTER DEFAULT PRIVILEGES IN SCHEMA <schema>
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO <role>;
ALTER DEFAULT PRIVILEGES IN SCHEMA <schema>
    GRANT USAGE, SELECT ON SEQUENCES TO <role>;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA <schema> TO <role>;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA <schema> TO <role>;
```

`LOGIN` when the tenant is active, `NOLOGIN` when it is suspended or
cancelled. The role is a member of no other role. `ALTER DEFAULT PRIVILEGES`
omits `FOR ROLE`, so it applies to the admin role that runs migrations. The
two backfill grants cover tables that already exist.

Because ensure is idempotent and order-independent, every lifecycle event
calls it rather than a narrower step. That matters: `ProvisionTenant` fires
`OnTenantStatusChanged` *before* `OnTenantProvisioned`
(`tenant/manager.go`), so a design where one event creates the role and
another only toggles `LOGIN` would, on every first provisioning, run
`ALTER ROLE` against a role that does not exist yet.

### Lifecycle

| Event | Isolation hook action |
|---|---|
| `OnTenantStatusChanged` (any transition) | ensure the role for the new status; for suspended or cancelled, then lock it out (below) |
| `OnTenantProvisioned` | ensure the role |
| `OnTenantDeleted` | lock it out, then `DROP OWNED BY <role>` and `DROP ROLE <role>` |

Status can also change through `UpdateTenant`, which fires the same event, so
it is covered.

**Locking a role out is a database-level backstop, not the gate.** The gate is
`ValidateTenant`, which re-reads the tenant's status on every request in every
application instance, so a suspension stops requests immediately everywhere.
Behind it, locking out does three things:

1. `NOLOGIN`, so no new connection can authenticate as the role;
2. `pg_terminate_backend` for every backend running as the role, so existing
   server connections — PgBouncer's included — are dropped rather than left
   authenticated; this needs the admin role to hold `pg_signal_backend`, which
   `New` checks in role mode;
3. closes that tenant's warm pool in this process.

Other application instances still hold warm pool entries for the tenant, but
their connections were just terminated, so any reuse fails; the pool entries
themselves close after `IdleTimeout`. The README states this instead of
claiming an instant lockout.

**Failure after activation.** `ProvisionTenant` activates the tenant before
it runs hooks, so a failed ensure leaves an active tenant whose role is
missing or incomplete. That fails closed — its connections cannot
authenticate — and `ProvisionTenant` returns the error. Hook failures do not
stop later hooks (`runHooks` collects errors), so an application hook that
uses a tenant connection will fail too, and report why.

**Existing tenants.** `(*MultiTenant).EnsureTenantRoles(ctx)` runs ensure for
every provisioned tenant — every tenant whose schema exists — and skips
tenants that are created but not yet provisioned. The upgrade note says to
run it once before switching a deployment to role mode. It is also the repair
tool.

**Rotation.** With `auth_query` or direct connections, rotation has no
downtime. With `auth_file` it has a short window, stated below.

1. Deploy with `Secret` set to the new key and `PreviousSecrets` holding the old.
2. When a tenant pool's first connection fails authentication, the pool is
   reopened with each previous key in turn, and keeps whichever
   authenticates. Any other error is returned as is. "Fails authentication"
   means either a SQLSTATE in class `28` (invalid authorization — `28000`,
   `28P01`), which is what Postgres sends, or `08P01` with a message
   containing `authentication failed`, which is what PgBouncer sends: the
   spike measured PgBouncer 1.25.2 answering a wrong password with
   `08P01 SASL authentication failed`. `08P01` alone is a generic protocol
   violation and is not retried.
3. `(*MultiTenant).RotateTenantCredentials(ctx)` re-keys every role to `Secret`.
4. With `auth_file` (see *PgBouncer authentication*), regenerate the file and
   reload PgBouncer after step 3.
5. Remove the old key from `PreviousSecrets`.

Already-open connections stay authenticated throughout.

**The `auth_file` window.** Postgres stores one password per role. Between
step 3 re-keying a role and step 4 reloading PgBouncer, PgBouncer's file still
holds the old password while Postgres holds the new one, so PgBouncer's own
login to Postgres fails. A tenant that already has a PgBouncer server
connection keeps working through the window; a tenant that needs a new server
connection cannot get one until the reload. The window is as long as the gap
between the two steps, so the README's sidecar reloads immediately after
rotation, and rotation belongs in a low-traffic period. `auth_query` has no
window, because PgBouncer fetches the current verifier on every login.

### Migrations

`ALTER DEFAULT PRIVILEGES` covers tables created *by the admin role*. If
migrations ever run as a different user — a deploy pipeline with its own DSN,
say — tables they create are not granted, and tenants start getting
*permission denied* at runtime. That fails closed, but it is an outage.

The granting-migrations decorator reapplies the two backfill grants for each
tenant after every `ApplyMigration`, `ApplyPending`,
`ApplyToAllTenants` and `ApplyPendingToAllTenants`, whoever created the
tables. Default privileges remain the first line; the decorator is the
guarantee.

### Tenant pools and request flow

One `*sql.DB` per tenant, opened on first use against `TenantDSN` with the
tenant's credentials, the same pgx settings as the admin pool (simple
protocol), `MaxOpenConns = PerTenantMaxConns`, `MaxIdleConns = 1` and
`ConnMaxIdleTime = IdleTimeout`.

`GetTenantConn` and `WithTenantTx` in role mode:

1. take a **per-tenant slot** (at most `PerTenantMaxConns`), waiting only as
   long as `ctx` allows;
2. take a **global slot** (at most `MaxConns`), likewise;
3. take the tenant's pool, opening it on first use and marking it recently used;
4. `db.Conn(ctx)` — never waits on `MaxOpenConns`, since step 1 already bounds it;
5. return a `tenant.Conn` whose `Close` releases both slots, exactly once
   (`sync.Once`).

**The order of steps 1 and 2 is the fairness guarantee.** Taking the global
slot first would let one busy tenant's waiting requests each hold a global
slot while queued on their own tenant's cap, until every global slot belonged
to that tenant and every other tenant starved. Waiting on the per-tenant slot
first means a saturated tenant's queue holds no global capacity.

- **Eviction** is least-recently-used and only ever closes a pool with no
  connection in use. Eviction and acquisition are serialized under the pools'
  lock: a pool is marked in use before the lock is released, so it cannot be
  evicted between being chosen and being used. If every warm pool is busy when
  a new tenant needs one, the warm count may exceed `MaxWarmTenants`, by at
  most `MaxConns`.
- A warm pool with nothing in use for `IdleTimeout` is closed.
- **Leaks.** A `tenant.Conn` that is never closed holds both slots forever,
  and with a global cap of 50 that is far more damaging than a leaked
  connection is today. `Stats()` reports connections in use per tenant, and a
  finalizer on `tenant.Conn` logs a warning naming the tenant when an unclosed
  connection is garbage-collected. The finalizer only reports; it does not
  release, because releasing from a finalizer would hide the bug.
- `Stats()` also reports warm pools, slot waits, hits, cold opens and evictions.

`search_path` is still set on every statement, so unqualified names behave
exactly as today. `tenant.Conn`, `Conn.Unwrap() *sql.Conn` and
`WithTenantTx(func(*sql.Tx) error)` keep their types; application code does
not change.

| Condition | Result |
|---|---|
| a slot wait outlives `ctx` | `ErrPoolExhausted`; `SetTenantDB` answers `TENANT_DB_BUSY`, **503**, `Retry-After: 1` |
| authentication fails with every secret | an error naming the role and pointing at `EnsureTenantRoles`, a secret mismatch, or — with `auth_file` — a stale PgBouncer auth file; the password is never logged |
| role mode against PostgreSQL < 15 | `New` refuses to start |
| role mode without `pg_signal_backend` | `New` refuses to start |

The registry, provisioning, migrations and usage counting stay on the admin
pool.

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

### PgBouncer authentication

Each tenant logs in as its own role, so PgBouncer must authenticate thousands
of roles and, for each, log in to Postgres on the client's behalf. It can
learn tenant credentials two ways, and both are supported, because which one a
deployment can use depends on where Postgres runs. The spike measured both.

**`auth_query` — self-hosted, with a superuser. Preferred.** PgBouncer runs a
query that returns the role's SCRAM verifier when the role logs in, and logs
in to Postgres by SCRAM *pass-through*, reusing the keys from the client's
exchange. The query calls a `SECURITY DEFINER` function that reads
`pg_shadow`, and creating that function takes a superuser. New tenants work
immediately, with no PgBouncer change. PgBouncer logs in to Postgres as the
`auth_user` to run the query, and it cannot do that from a verifier, so the
`auth_user`'s own entry in PgBouncer's `auth_file` must be a plaintext
password. The README gives the function and the
`auth_user`/`auth_query`/`auth_file` settings.

**`auth_file` with plaintext passwords — managed Postgres (RDS, Cloud SQL and
others).** These services grant no superuser, so nothing can read password
verifiers. Instead, PgBouncer reads credentials from a file.
`(*MultiTenant).PgBouncerAuthFile(ctx, w)` renders it — one
`"<role>" "<password>"` line per active provisioned tenant, excluding
suspended and cancelled ones — from the derived passwords, without reading
anything secret from the database. PgBouncer then authenticates clients and
logs in to Postgres itself.

The file holds **plaintext passwords, not SCRAM verifiers**, and that is
deliberate. The spike found that PgBouncer 1.25.2, given verifiers in an
`auth_file`, locks a role out if that role's first login attempt since
PgBouncer started is a failure: every later login with the correct password
fails with `client authentication did not provide SCRAM keys` until
PgBouncer restarts, and `RELOAD` does not clear it. After every restart all
roles are "first login" again, so one bad attempt per role — from a
misconfigured client, the rotation retry, or anyone who can reach PgBouncer —
would lock tenants out. Plaintext entries showed no lockout, nor did
`auth_query`. A test in CI guards this, in both modes.

What plaintext costs:

- **The rendered file is a secret.** It contains every tenant's working
  credentials. Write it with mode `0600` to a volume only PgBouncer and the
  renderer can read, as you would the role secret itself.
- **PgBouncer CPU.** With plaintext entries PgBouncer performs the expensive
  SCRAM key derivation for each client login and each server login, on its
  single thread. The spike measured about 4.5× lower throughput than
  `auth_query` under the worst-case load (see *Spike results*).
- **New tenants wait for a reload.** A new tenant cannot connect through
  PgBouncer until the file is regenerated and PgBouncer reloaded. The library
  cannot write to PgBouncer's filesystem, so the deployment runs the
  regeneration — the README gives a small sidecar loop that renders the file
  on an interval or after a provisioning event and issues `RELOAD` on
  PgBouncer's admin console. The reload itself is cheap: 2.6 ms for 5,002
  entries in the spike. PgBouncer re-reads the file only when it has changed.
  Until the reload, the new tenant's requests fail with the authentication
  error above, which names a stale auth file as a likely cause.

**Operational requirements, either mode.**

- **File descriptors.** Every warm tenant pool in every application instance
  holds a client connection open to PgBouncer. PgBouncer's descriptor limit
  must cover roughly `MaxWarmTenants × application instances` plus its server
  connections. Container defaults are often a soft limit of 1,024; in the
  spike, exceeding it produced `accept() failed: No file descriptors
  available` in PgBouncer's log and only connection timeouts at the client,
  not errors. The README gives the arithmetic and the `ulimit` setting.
- **Sizing.** PgBouncer cannot reuse a server connection across roles, so
  `max_db_connections` and `server_idle_timeout` need sizing for many small
  pools; the README gives the arithmetic, informed by the spike.

## Rejected alternatives

- **`SET LOCAL ROLE` on the shared pool** — undone by injected SQL; see above.
- **One global pool of tenant-tagged pgx connections** — tighter limits and
  better reuse, but it replaces `database/sql` under `tenant.Conn` and breaks
  `Conn.Unwrap()` and `WithTenantTx`'s `*sql.Tx`, both public.
- **Connect per request, pooling left to PgBouncer** — least code, but every
  request pays a connection and a SCRAM handshake even behind PgBouncer.
- **Random passwords stored on the tenant record** — a credential at rest in
  `public.tenants` that would need encrypting, which brings back a master key.
- **SCRAM verifiers in `auth_file`** — would keep plaintext passwords off
  PgBouncer's host, but PgBouncer 1.25.2 then locks a role out after a failed
  first login, until restart. Measured in the spike; see *PgBouncer
  authentication*. Revisit if a later PgBouncer release fixes it.
- **A startup check against `max_connections`** — behind PgBouncer that value
  bounds PgBouncer's server side, not the library's connections, so the check
  would mislead.
- **Releasing a leaked connection's slots from a finalizer** — it would keep
  the service running while hiding the bug that caused the leak.

## Compatibility

Additive and opt-in. With `Isolation` unset nothing changes: no roles, no new
pools, no decorators, no new startup checks. `TENANT_DB_BUSY` is emitted only
in role mode.

## Testing

**Unit — `tenant/pools.go`**, against a counting stub driver registered for
the test: slot accounting, release exactly once, eviction order, never
evicting a busy pool, eviction racing acquisition, the per-tenant cap, a
context-cancelled wait at either slot, idle close, and a many-goroutine run
under `-race` asserting that connections in use never exceed `MaxConns` in
total or `PerTenantMaxConns` for any tenant. **Fairness:** saturate one tenant
far past its cap and assert another tenant still acquires within a short
deadline — the test that fails if the slot order is reversed.

**Unit — credentials:** password derivation is stable per tenant and differs
across tenants and secrets; the verifier for a fixed password and salt
matches a known answer; `PgBouncerAuthFile` renders plaintext entries for
active tenants only, and reports an error naming any tenant whose
`Credentials` hook returned no password.

**Unit — retry classification:** `28000`, `28P01`, and `08P01` with
`authentication failed` in the message are retried with a previous secret;
`08P01` with any other message, and every other code, is not.

**Unit — hook ordering:** a manager that fires `OnTenantStatusChanged` before
`OnTenantProvisioned`, as `ProvisionTenant` does, provisions cleanly.

**Integration — real Postgres, and in CI also through PgBouncer:**

- the five success criteria, each **mutation-verified** — for example,
  granting a tenant role `USAGE` on another tenant's schema must make the
  qualified-read test fail;
- first provisioning succeeds and the role can log in;
- a table added by a later migration run as a *different* database user is
  usable by the tenant role (the granting-migrations decorator);
- suspend makes login fail *and* terminates an already-open session; activate
  restores login;
- delete drops the role;
- rotation: connections keep working across every step;
- `EnsureTenantRoles` repairs a tenant whose role was dropped and skips an
  unprovisioned tenant;
- the verifier the library computes authenticates directly against Postgres;
- the cross-tenant end-to-end test runs again in role mode.

Roles are cluster-wide, not per-database, so the test cleanup drops tenant
roles explicitly.

**CI:** the PgBouncer job runs the role-mode suite twice, once with
`auth_query` (creating the lookup function as the container's superuser) and
once with a rendered plaintext `auth_file`, both in transaction mode, with
PgBouncer's descriptor limit raised. Both runs include:

- **the lockout guard:** restart PgBouncer, make a tenant's first login a
  wrong password, then assert its correct login succeeds. This is the test
  that fails if the `auth_file` is ever switched to SCRAM verifiers;
- the rotation retry against PgBouncer's real `08P01` response.

The `auth_file` run also provisions a tenant, regenerates the file, reloads
PgBouncer, and asserts the tenant can then connect.

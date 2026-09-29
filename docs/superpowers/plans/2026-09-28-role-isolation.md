# Role Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An opt-in isolation mode in which every tenant logs in to PostgreSQL as its own role, with privileges on its own schema only, so the database itself refuses a query that reaches another tenant's data.

**Architecture:** `multitenant.New` builds the manager and migration manager exactly as today, then in role mode wraps each in a decorator, registers a lifecycle hook that keeps each tenant's role in step with its registry record, and routes every tenant connection through a bounded set of per-tenant `*sql.DB` pools that log in as the tenant's role. `tenant.Conn`, `WithTenantTx` and the `Manager`/`MigrationManager` interfaces keep their types, so application code does not change.

**Tech Stack:** Go 1.24, `database/sql`, `github.com/jackc/pgx/v5` (already required), `crypto/pbkdf2` (standard library from Go 1.24), `go.uber.org/zap`, PostgreSQL 15+, PgBouncer 1.25.

**Spec:** `docs/superpowers/specs/2026-09-22-role-isolation-design.md`

## Global Constraints

- **No new module dependencies** in any `go.mod`. pgx is already required; `crypto/pbkdf2` is standard library. Run `go mod tidy -diff` in the root, `middleware/gin` and `examples` modules before each commit that adds an import; it must print nothing.
- **Default behaviour is unchanged.** With `Database.Isolation` unset, no roles, pools, decorators, hooks or startup checks are added. Every existing test must pass unchanged.
- **Role mode requires PostgreSQL 15 or later**, and an admin role with `CREATEROLE` and membership in `pg_signal_backend` (or superuser). `New` checks all three.
- **Role name = schema name** (`<SchemaPrefix><uuid with underscores>`). `New` rejects a prefix that makes it longer than 63 characters.
- **Passwords never reach the server in plaintext** — the library sends a SCRAM-SHA-256 verifier, 4096 iterations, random 16-byte salt — **and are never logged.**
- **The PgBouncer auth file carries plaintext derived passwords, not SCRAM verifiers.** Verifiers there trigger a PgBouncer 1.25.2 lockout (spec, *PgBouncer authentication*).
- **Retry with a previous secret only on an authentication failure:** SQLSTATE class `28`, or `08P01` whose message contains `authentication failed`. Nothing else.
- Tests use stdlib `testing` only in the root module, as internal packages (`package tenant`, `package database`, `package multitenant`, `package httpmw`). Integration tests live in the root package and use the existing `setupTestDatabase`/`getTestDatabaseURL`/`testConfig` helpers.
- Doc comments are full sentences ending in a period. `gofmt -l .` must print nothing (CI enforces it).
- Every commit message ends with exactly: `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `tenant/isolation.go` | create | `IsolationMode`, `RoleIsolationConfig`, defaults, `Validate` |
| `tenant/models.go` | modify | two new `DatabaseConfig` fields |
| `tenant/credentials.go` | create | derived passwords, SCRAM verifiers, `CredentialSource`, `IsAuthFailure` |
| `tenant/pools.go` | create | `Pools`: per-tenant and global slots, LRU warm pools, idle close, `Stats` |
| `tenant/conn.go` | modify | release callback and leak report on pooled connections |
| `tenant/role_manager.go` | create | `Manager` decorator routing tenant connections through `Pools` |
| `tenant/stubdriver_test.go` | create | a `database/sql` stub driver for pool and decorator tests |
| `middleware/httpmw/middleware.go`, `errors.go` | modify | `TENANT_DB_BUSY` → 503 with `Retry-After` |
| `database/roles.go` | create | `RoleManager`: ensure, grant, lock out, drop, prerequisites |
| `database/role_hook.go` | create | `RoleHook`: lifecycle events → role operations |
| `database/role_migrations.go` | create | `MigrationManager` decorator re-granting after every run |
| `database/pgbouncer.go` | create | PgBouncer `auth_file` renderer |
| `role_isolation.go` | create | wiring, tenant pool opener with rotation retry, `*MultiTenant` methods |
| `multitenant.go` | modify | config validation, decorators and hook in role mode |
| `role_isolation_integration_test.go` | create | integration tests against real PostgreSQL, and PgBouncer in CI |
| `cross_tenant_integration_test.go` | modify | run the cross-tenant scenario in both modes |
| `scripts/ci/pgbouncer-role-setup.sh` | create | start PgBouncer in `auth_query` or `auth_file` mode for CI |
| `.github/workflows/go.yml` | modify | role-isolation jobs through PgBouncer |
| `README.md`, spec | modify | documentation; two spec corrections |

---

### Task 1: Isolation configuration

**Files:**
- Create: `tenant/isolation.go`, `tenant/isolation_test.go`
- Modify: `tenant/models.go` (`DatabaseConfig`, after `MigrationsDir`)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type IsolationMode string`; `const IsolationSearchPath IsolationMode = ""`; `const IsolationRole IsolationMode = "role"`
  - `type RoleIsolationConfig struct { TenantDSN string; Secret []byte; PreviousSecrets [][]byte; Credentials func(tenantID uuid.UUID) (password string, err error); MaxConns, PerTenantMaxConns, MaxWarmTenants int; IdleTimeout time.Duration }`
  - `func (c RoleIsolationConfig) WithDefaults() RoleIsolationConfig`
  - `func (c RoleIsolationConfig) Validate(schemaPrefix string) error` — errors wrap `ErrInvalidRoleIsolation`
  - `var ErrInvalidRoleIsolation error`; constants `DefaultRoleMaxConns = 50`, `DefaultRolePerTenantMaxConns = 5`, `DefaultRoleMaxWarmTenants = 1000`, `DefaultRoleIdleTimeout = 5 * time.Minute`, `MinRoleSecretLen = 32`
  - `DatabaseConfig.Isolation IsolationMode`, `DatabaseConfig.RoleIsolation RoleIsolationConfig`

- [ ] **Step 1: Write the failing test**

Create `tenant/isolation_test.go`:

```go
package tenant

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validRoleConfig() RoleIsolationConfig {
	return RoleIsolationConfig{
		TenantDSN: "postgres://localhost:6432/app",
		Secret:    bytes.Repeat([]byte("s"), MinRoleSecretLen),
	}
}

func TestRoleIsolationDefaults(t *testing.T) {
	got := RoleIsolationConfig{}.WithDefaults()
	if got.MaxConns != DefaultRoleMaxConns || got.PerTenantMaxConns != DefaultRolePerTenantMaxConns ||
		got.MaxWarmTenants != DefaultRoleMaxWarmTenants || got.IdleTimeout != DefaultRoleIdleTimeout {
		t.Errorf("WithDefaults() = %+v, want every limit at its default", got)
	}

	set := RoleIsolationConfig{MaxConns: 7, PerTenantMaxConns: 2, MaxWarmTenants: 9, IdleTimeout: time.Second}.WithDefaults()
	if set.MaxConns != 7 || set.PerTenantMaxConns != 2 || set.MaxWarmTenants != 9 || set.IdleTimeout != time.Second {
		t.Errorf("WithDefaults() changed explicit limits: %+v", set)
	}
}

func TestRoleIsolationValidate(t *testing.T) {
	short := []byte("too short")
	for name, tc := range map[string]struct {
		mutate  func(*RoleIsolationConfig)
		prefix  string
		wantErr string
	}{
		"valid":                       {mutate: func(*RoleIsolationConfig) {}, prefix: "tenant_"},
		"no TenantDSN":                {mutate: func(c *RoleIsolationConfig) { c.TenantDSN = "" }, prefix: "tenant_", wantErr: "TenantDSN"},
		"short Secret":                {mutate: func(c *RoleIsolationConfig) { c.Secret = short }, prefix: "tenant_", wantErr: "Secret"},
		"short Secret with a hook":    {mutate: func(c *RoleIsolationConfig) { c.Secret = nil; c.Credentials = func(uuid.UUID) (string, error) { return "pw", nil } }, prefix: "tenant_"},
		"short PreviousSecrets entry": {mutate: func(c *RoleIsolationConfig) { c.PreviousSecrets = [][]byte{short} }, prefix: "tenant_", wantErr: "PreviousSecrets[0]"},
		"prefix too long":             {mutate: func(*RoleIsolationConfig) {}, prefix: strings.Repeat("p", 28), wantErr: "63"},
		"prefix at the limit":         {mutate: func(*RoleIsolationConfig) {}, prefix: strings.Repeat("p", 27)},
		"negative limit":              {mutate: func(c *RoleIsolationConfig) { c.MaxConns = -1 }, prefix: "tenant_", wantErr: "negative"},
		"per-tenant above global":     {mutate: func(c *RoleIsolationConfig) { c.MaxConns = 3; c.PerTenantMaxConns = 4 }, prefix: "tenant_", wantErr: "PerTenantMaxConns"},
	} {
		t.Run(name, func(t *testing.T) {
			c := validRoleConfig()
			tc.mutate(&c)
			err := c.Validate(tc.prefix)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRoleIsolation) {
				t.Fatalf("Validate() = %v, want ErrInvalidRoleIsolation", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// The zero value must be today's behaviour, so every existing Config keeps it.
func TestIsolationDefaultsToSearchPath(t *testing.T) {
	if got := DefaultConfig().Database.Isolation; got != IsolationSearchPath {
		t.Errorf("DefaultConfig().Database.Isolation = %q, want IsolationSearchPath", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./tenant/ -run 'TestRoleIsolation|TestIsolationDefaults' -v`
Expected: FAIL — compilation errors, `undefined: RoleIsolationConfig`, `undefined: MinRoleSecretLen`, `DefaultConfig().Database.Isolation undefined`.

- [ ] **Step 3: Create `tenant/isolation.go`**

```go
package tenant

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// IsolationMode selects how tenant connections are isolated from each other.
type IsolationMode string

const (
	// IsolationSearchPath scopes each connection to its tenant's schema with
	// SET LOCAL search_path, on a pool that logs in as one role for every
	// tenant. It is the default. It guards against mistakes, not against SQL
	// that names another tenant's schema.
	IsolationSearchPath IsolationMode = ""
	// IsolationRole logs every tenant in as its own database role, which has
	// privileges on its own schema only, so the database itself refuses a
	// query that reaches another tenant's data.
	IsolationRole IsolationMode = "role"
)

// Defaults and limits for RoleIsolationConfig.
const (
	DefaultRoleMaxConns          = 50
	DefaultRolePerTenantMaxConns = 5
	DefaultRoleMaxWarmTenants    = 1000
	DefaultRoleIdleTimeout       = 5 * time.Minute
	// MinRoleSecretLen is the shortest Secret role isolation accepts.
	MinRoleSecretLen = 32

	maxIdentifierLen = 63 // PostgreSQL's NAMEDATALEN - 1
	tenantIDLen      = 36 // a UUID in a role or schema name
)

// ErrInvalidRoleIsolation reports a RoleIsolationConfig that cannot run.
var ErrInvalidRoleIsolation = errors.New("tenant: invalid role isolation config")

// RoleIsolationConfig configures IsolationRole.
//
// The pool defaults assume TenantDSN points at a connection pooler such as
// PgBouncer, where a warm tenant's idle connection is cheap. Pointed straight
// at PostgreSQL, every warm tenant holds a real backend, so
// MaxConns + MaxWarmTenants must fit inside max_connections alongside the
// admin pool; the defaults, 1,050 together, would not.
type RoleIsolationConfig struct {
	// TenantDSN is where tenant connections go, normally a pooler. Its user
	// and password are replaced with each tenant's own.
	TenantDSN string `json:"tenant_dsn"`

	// Secret derives every tenant role's password. It must be at least
	// MinRoleSecretLen bytes, and distinct per environment. PreviousSecrets
	// are tried, in order, when a connection fails to authenticate with
	// Secret; they exist only for rotation.
	Secret          []byte   `json:"-"`
	PreviousSecrets [][]byte `json:"-"`

	// Credentials, when set, replaces derived passwords everywhere: role
	// creation, rotation and the PgBouncer auth file. It may return an empty
	// password, leaving authentication to pg_hba.conf. The role name is
	// always the tenant's schema name.
	Credentials func(tenantID uuid.UUID) (password string, err error) `json:"-"`

	// MaxConns caps connections in use at once across all tenants.
	MaxConns int `json:"max_conns"`
	// PerTenantMaxConns caps connections in use at once for one tenant.
	PerTenantMaxConns int `json:"per_tenant_max_conns"`
	// MaxWarmTenants caps the tenant pools kept open.
	MaxWarmTenants int `json:"max_warm_tenants"`
	// IdleTimeout closes a warm pool with nothing in use for this long.
	IdleTimeout time.Duration `json:"idle_timeout"`
}

// WithDefaults returns c with every zero limit replaced by its default.
func (c RoleIsolationConfig) WithDefaults() RoleIsolationConfig {
	if c.MaxConns == 0 {
		c.MaxConns = DefaultRoleMaxConns
	}
	if c.PerTenantMaxConns == 0 {
		c.PerTenantMaxConns = DefaultRolePerTenantMaxConns
	}
	if c.MaxWarmTenants == 0 {
		c.MaxWarmTenants = DefaultRoleMaxWarmTenants
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = DefaultRoleIdleTimeout
	}
	return c
}

// Validate reports whether c can run, given the schema prefix that role
// names are built from. It checks configuration only; the database
// prerequisites are checked when multitenant.New connects.
func (c RoleIsolationConfig) Validate(schemaPrefix string) error {
	if c.TenantDSN == "" {
		return fmt.Errorf("%w: TenantDSN is empty", ErrInvalidRoleIsolation)
	}
	if c.Credentials == nil && len(c.Secret) < MinRoleSecretLen {
		return fmt.Errorf("%w: Secret is %d bytes, need at least %d", ErrInvalidRoleIsolation, len(c.Secret), MinRoleSecretLen)
	}
	for i, s := range c.PreviousSecrets {
		if len(s) < MinRoleSecretLen {
			return fmt.Errorf("%w: PreviousSecrets[%d] is %d bytes, need at least %d", ErrInvalidRoleIsolation, i, len(s), MinRoleSecretLen)
		}
	}
	if n := len(schemaPrefix) + tenantIDLen; n > maxIdentifierLen {
		return fmt.Errorf("%w: schema prefix %q makes role names %d characters, over PostgreSQL's %d", ErrInvalidRoleIsolation, schemaPrefix, n, maxIdentifierLen)
	}
	if c.MaxConns < 0 || c.PerTenantMaxConns < 0 || c.MaxWarmTenants < 0 || c.IdleTimeout < 0 {
		return fmt.Errorf("%w: limits must not be negative", ErrInvalidRoleIsolation)
	}
	if d := c.WithDefaults(); d.PerTenantMaxConns > d.MaxConns {
		return fmt.Errorf("%w: PerTenantMaxConns %d exceeds MaxConns %d", ErrInvalidRoleIsolation, d.PerTenantMaxConns, d.MaxConns)
	}
	return nil
}
```

- [ ] **Step 4: Add the two `DatabaseConfig` fields**

In `tenant/models.go`, add after the `MigrationsDir` field of `DatabaseConfig`:

```go
	// Isolation selects how tenant connections are isolated. The zero value,
	// IsolationSearchPath, is the shared-role behaviour. IsolationRole gives
	// every tenant its own database role; see RoleIsolationConfig.
	Isolation IsolationMode `json:"isolation"`
	// RoleIsolation configures IsolationRole and is ignored otherwise.
	RoleIsolation RoleIsolationConfig `json:"role_isolation"`
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./tenant/ -run 'TestRoleIsolation|TestIsolationDefaults' -v`
Expected: PASS.

- [ ] **Step 6: Verify the module**

Run: `gofmt -l . && go build ./... && go vet ./... && go test -short ./...`
Expected: no gofmt output; all PASS.

- [ ] **Step 7: Commit**

```bash
git add tenant/isolation.go tenant/isolation_test.go tenant/models.go
git commit -m "feat(tenant): add the role isolation configuration

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Credentials

**Files:**
- Create: `tenant/credentials.go`, `tenant/credentials_test.go`

**Interfaces:**
- Consumes: `RoleIsolationConfig` (Task 1).
- Produces:
  - `func DerivePassword(secret []byte, tenantID uuid.UUID) string`
  - `func SCRAMVerifier(password string, salt []byte) (string, error)`; `func NewSCRAMVerifier(password string) (string, error)`
  - `type RoleCredentials struct { User, Password string }`
  - `type CredentialSource`; `func NewCredentialSource(cfg RoleIsolationConfig, roleName func(uuid.UUID) string) *CredentialSource`
  - `func (s *CredentialSource) RoleName(tenantID uuid.UUID) string`
  - `func (s *CredentialSource) Current(tenantID uuid.UUID) (RoleCredentials, error)`
  - `func (s *CredentialSource) Candidates(tenantID uuid.UUID) ([]RoleCredentials, error)`
  - `func IsAuthFailure(err error) bool`

- [ ] **Step 1: Write the failing test**

Create `tenant/credentials_test.go`. The expected values were computed independently with Python's `hashlib.pbkdf2_hmac` and `hmac`, not with this code:

```go
package tenant

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDerivePasswordKnownAnswer(t *testing.T) {
	secret := bytes.Repeat([]byte("k"), 32)
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	if got, want := DerivePassword(secret, id), "3y1jGN_R3_fvFo_TDofAlY4FsI9FpYBVbWSpd-70YeI"; got != want {
		t.Errorf("DerivePassword() = %q, want %q", got, want)
	}
}

func TestDerivePasswordVaries(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	s1, s2 := bytes.Repeat([]byte("1"), 32), bytes.Repeat([]byte("2"), 32)
	if DerivePassword(s1, a) != DerivePassword(s1, a) {
		t.Error("DerivePassword is not stable for the same inputs")
	}
	if DerivePassword(s1, a) == DerivePassword(s1, b) {
		t.Error("two tenants derived the same password")
	}
	if DerivePassword(s1, a) == DerivePassword(s2, a) {
		t.Error("two secrets derived the same password")
	}
}

func TestSCRAMVerifierKnownAnswer(t *testing.T) {
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i)
	}
	got, err := SCRAMVerifier("pencil", salt)
	if err != nil {
		t.Fatal(err)
	}
	want := "SCRAM-SHA-256$4096:AAECAwQFBgcICQoLDA0ODw==$zHCdol2044/ZyWzPLi7oxApCkamKw9Z+E4U/QApd/5Y=:dd5peBOitVnLNFu7VmwP+HiDaaw4OUCv396eVCWhYiE="
	if got != want {
		t.Errorf("SCRAMVerifier() =\n  %s\nwant\n  %s", got, want)
	}
}

func TestNewSCRAMVerifierUsesARandomSalt(t *testing.T) {
	a, err := NewSCRAMVerifier("pw")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSCRAMVerifier("pw")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two verifiers for one password were identical; the salt is not random")
	}
	if !strings.HasPrefix(a, "SCRAM-SHA-256$4096:") {
		t.Errorf("verifier %q is not SCRAM-SHA-256 with 4096 iterations", a)
	}
}

func roleNameForTest(id uuid.UUID) string {
	return "tenant_" + strings.ReplaceAll(id.String(), "-", "_")
}

func TestCredentialSourceDerived(t *testing.T) {
	id := uuid.New()
	cur, old := bytes.Repeat([]byte("c"), 32), bytes.Repeat([]byte("o"), 32)
	s := NewCredentialSource(RoleIsolationConfig{Secret: cur, PreviousSecrets: [][]byte{old}}, roleNameForTest)

	c, err := s.Current(id)
	if err != nil {
		t.Fatal(err)
	}
	if c.User != roleNameForTest(id) || c.Password != DerivePassword(cur, id) {
		t.Errorf("Current() = %+v, want the schema name and the current secret's password", c)
	}

	cands, err := s.Candidates(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 || cands[0] != c || cands[1].Password != DerivePassword(old, id) || cands[1].User != c.User {
		t.Errorf("Candidates() = %+v, want current then previous", cands)
	}
}

func TestCredentialSourceHook(t *testing.T) {
	id := uuid.New()
	s := NewCredentialSource(RoleIsolationConfig{
		PreviousSecrets: [][]byte{bytes.Repeat([]byte("o"), 32)},
		Credentials:     func(got uuid.UUID) (string, error) { return "from-hook-" + got.String()[:4], nil },
	}, roleNameForTest)

	cands, err := s.Candidates(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Password != "from-hook-"+id.String()[:4] {
		t.Errorf("Candidates() = %+v, want only the hook's credentials; PreviousSecrets are ignored with a hook", cands)
	}

	failing := NewCredentialSource(RoleIsolationConfig{
		Credentials: func(uuid.UUID) (string, error) { return "", errors.New("vault down") },
	}, roleNameForTest)
	if _, err := failing.Current(id); err == nil || !strings.Contains(err.Error(), "vault down") {
		t.Errorf("Current() = %v, want the hook's error", err)
	}
}

func TestIsAuthFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"postgres invalid password": {&pgconn.PgError{Code: "28P01", Message: "password authentication failed for user x"}, true},
		"postgres role cannot login": {&pgconn.PgError{Code: "28000", Message: `role "x" is not permitted to log in`}, true},
		"pgbouncer sasl failure":     {&pgconn.PgError{Code: "08P01", Message: "SASL authentication failed"}, true},
		"wrapped pgbouncer failure":  {fmt.Errorf("connect: %w", &pgconn.PgError{Code: "08P01", Message: "SASL authentication failed"}), true},
		"other protocol violation":   {&pgconn.PgError{Code: "08P01", Message: "server login failed: wrong password type"}, false},
		"undefined table":            {&pgconn.PgError{Code: "42P01", Message: "relation does not exist"}, false},
		"not a postgres error":       {errors.New("dial tcp: connection refused"), false},
		"nil":                        {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IsAuthFailure(tc.err); got != tc.want {
				t.Errorf("IsAuthFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
```

The case `other protocol violation` is the message PgBouncer returns when *it* cannot log in to PostgreSQL. It is not the client's password being wrong, so a previous secret cannot help.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./tenant/ -run 'TestDerivePassword|TestSCRAM|TestNewSCRAM|TestCredentialSource|TestIsAuthFailure' -v`
Expected: FAIL — `undefined: DerivePassword`, `undefined: SCRAMVerifier`, `undefined: NewCredentialSource`, `undefined: IsAuthFailure`.

- [ ] **Step 3: Create `tenant/credentials.go`**

```go
package tenant

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	passwordLabel   = "go-multitenant/role-password/v1:"
	scramIterations = 4096
	scramSaltLen    = 16
)

// DerivePassword returns the database password for a tenant's role, derived
// from secret. The result is base64url, so ASCII, which makes the SASLprep
// normalisation SCRAM specifies the identity.
func DerivePassword(secret []byte, tenantID uuid.UUID) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(passwordLabel + tenantID.String()))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SCRAMVerifier returns the SCRAM-SHA-256 verifier PostgreSQL stores for
// password with the given salt, in the form ALTER ROLE ... PASSWORD accepts.
func SCRAMVerifier(password string, salt []byte) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, scramIterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("tenant: derive SCRAM key: %w", err)
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	enc := base64.StdEncoding
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", scramIterations,
		enc.EncodeToString(salt), enc.EncodeToString(storedKey[:]), enc.EncodeToString(serverKey)), nil
}

// NewSCRAMVerifier is SCRAMVerifier with a random 16-byte salt.
func NewSCRAMVerifier(password string) (string, error) {
	salt := make([]byte, scramSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("tenant: SCRAM salt: %w", err)
	}
	return SCRAMVerifier(password, salt)
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

// RoleCredentials is one way to log in as a tenant's role.
type RoleCredentials struct {
	User     string
	Password string
}

// CredentialSource resolves tenant role credentials under a
// RoleIsolationConfig: derived from the secrets, or from its Credentials hook.
type CredentialSource struct {
	cfg      RoleIsolationConfig
	roleName func(uuid.UUID) string
}

// NewCredentialSource returns a CredentialSource for cfg. roleName maps a
// tenant to its role, which is its schema name.
func NewCredentialSource(cfg RoleIsolationConfig, roleName func(uuid.UUID) string) *CredentialSource {
	return &CredentialSource{cfg: cfg, roleName: roleName}
}

// RoleName returns the database role for a tenant.
func (s *CredentialSource) RoleName(tenantID uuid.UUID) string { return s.roleName(tenantID) }

// Current returns the credentials the tenant's role should have now.
func (s *CredentialSource) Current(tenantID uuid.UUID) (RoleCredentials, error) {
	user := s.roleName(tenantID)
	if s.cfg.Credentials != nil {
		password, err := s.cfg.Credentials(tenantID)
		if err != nil {
			return RoleCredentials{}, fmt.Errorf("tenant: credentials for %s: %w", user, err)
		}
		return RoleCredentials{User: user, Password: password}, nil
	}
	return RoleCredentials{User: user, Password: DerivePassword(s.cfg.Secret, tenantID)}, nil
}

// Candidates returns every set of credentials worth trying when connecting:
// the current one first, then one per previous secret. With a Credentials
// hook it returns only the current one.
func (s *CredentialSource) Candidates(tenantID uuid.UUID) ([]RoleCredentials, error) {
	current, err := s.Current(tenantID)
	if err != nil {
		return nil, err
	}
	out := []RoleCredentials{current}
	if s.cfg.Credentials != nil {
		return out, nil
	}
	for _, prev := range s.cfg.PreviousSecrets {
		out = append(out, RoleCredentials{User: current.User, Password: DerivePassword(prev, tenantID)})
	}
	return out, nil
}

// IsAuthFailure reports whether err is a failed login worth retrying with a
// previous secret: a SQLSTATE in class 28, which PostgreSQL sends, or 08P01
// with "authentication failed" in its message, which PgBouncer sends. 08P01
// alone is a generic protocol violation and is not one.
func IsAuthFailure(err error) bool {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return false
	}
	if strings.HasPrefix(pe.Code, "28") {
		return true
	}
	return pe.Code == "08P01" && strings.Contains(strings.ToLower(pe.Message), "authentication failed")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tenant/ -run 'TestDerivePassword|TestSCRAM|TestNewSCRAM|TestCredentialSource|TestIsAuthFailure' -v`
Expected: PASS, including both known-answer tests.

- [ ] **Step 5: Verify the modules and that no dependency was added**

Run: `gofmt -l . && go build ./... && go vet ./... && go test -short ./... && go mod tidy -diff && (cd middleware/gin && go mod tidy -diff && go build ./...) && (cd examples && go mod tidy -diff && go build ./...)`
Expected: no output from gofmt or any `tidy -diff`; all PASS. `tenant` now imports `pgconn`, which every module already requires through pgx.

- [ ] **Step 6: Commit**

```bash
git add tenant/credentials.go tenant/credentials_test.go
git commit -m "feat(tenant): derive tenant role passwords and SCRAM verifiers

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Tenant pools

**Files:**
- Create: `tenant/pools.go`, `tenant/pools_test.go`, `tenant/stubdriver_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `var ErrPoolExhausted error`
  - `type PoolsConfig struct { MaxConns, PerTenantMaxConns, MaxWarmTenants int; IdleTimeout time.Duration }`
  - `type PoolOpener func(ctx context.Context, tenantID uuid.UUID) (*sql.DB, error)`
  - `type PoolStats struct { Warm, InUse int; InUseByTenant map[uuid.UUID]int; Hits, ColdOpens, Evictions, SlotWaits uint64 }`
  - `func NewPools(cfg PoolsConfig, open PoolOpener, logger *zap.Logger) (*Pools, error)`
  - `func (p *Pools) Acquire(ctx context.Context, tenantID uuid.UUID) (conn *sql.Conn, release func(), err error)` — the caller closes `conn`, then calls `release`, which is idempotent
  - `func (p *Pools) Evict(tenantID uuid.UUID)`
  - `func (p *Pools) Stats() PoolStats`
  - `func (p *Pools) Close() error`
  - test-only: `stubDriver` with `db(maxOpen int) *sql.DB`, `counts() (opened, closed int)`, `statements() []string`, field `failNext error`

- [ ] **Step 1: Create the stub driver**

Create `tenant/stubdriver_test.go`:

```go
package tenant

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
)

// stubDriver is a database/sql driver whose connections accept every
// statement, record it, and count opens and closes. It lets Pools and the
// role-isolated manager be tested without a database.
type stubDriver struct {
	mu       sync.Mutex
	opened   int
	closed   int
	stmts    []string
	failNext error // returned by the next Connect, then cleared
}

func (d *stubDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("stubDriver: open through a connector")
}

// db returns a new *sql.DB over this driver.
func (d *stubDriver) db(maxOpen int) *sql.DB {
	db := sql.OpenDB(stubConnector{d})
	db.SetMaxOpenConns(maxOpen)
	return db
}

func (d *stubDriver) record(s string) {
	d.mu.Lock()
	d.stmts = append(d.stmts, s)
	d.mu.Unlock()
}

func (d *stubDriver) counts() (opened, closed int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opened, d.closed
}

func (d *stubDriver) statements() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.stmts...)
}

type stubConnector struct{ d *stubDriver }

func (c stubConnector) Connect(context.Context) (driver.Conn, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	if err := c.d.failNext; err != nil {
		c.d.failNext = nil
		return nil, err
	}
	c.d.opened++
	return &stubConn{d: c.d}, nil
}

func (c stubConnector) Driver() driver.Driver { return c.d }

type stubConn struct{ d *stubDriver }

func (c *stubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("stubConn: Prepare is not supported")
}

func (c *stubConn) Close() error {
	c.d.mu.Lock()
	c.d.closed++
	c.d.mu.Unlock()
	return nil
}

func (c *stubConn) Begin() (driver.Tx, error) {
	c.d.record("BEGIN")
	return stubTx{c.d}, nil
}

func (c *stubConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.d.record(query)
	return driver.RowsAffected(0), nil
}

type stubTx struct{ d *stubDriver }

func (t stubTx) Commit() error   { t.d.record("COMMIT"); return nil }
func (t stubTx) Rollback() error { t.d.record("ROLLBACK"); return nil }
```

- [ ] **Step 2: Write the failing tests**

Create `tenant/pools_test.go`:

```go
package tenant

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// testPools returns Pools over one stub driver, and a per-tenant count of how
// many times each tenant's pool was opened.
func testPools(t *testing.T, cfg PoolsConfig) (*Pools, *stubDriver, func(uuid.UUID) int) {
	t.Helper()
	d := &stubDriver{}
	var mu sync.Mutex
	opens := map[uuid.UUID]int{}
	p, err := NewPools(cfg, func(_ context.Context, id uuid.UUID) (*sql.DB, error) {
		mu.Lock()
		opens[id]++
		mu.Unlock()
		return d.db(cfg.PerTenantMaxConns), nil
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, d, func(id uuid.UUID) int { mu.Lock(); defer mu.Unlock(); return opens[id] }
}

func acquire(t *testing.T, p *Pools, id uuid.UUID) (*sql.Conn, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, release, err := p.Acquire(ctx, id)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", id, err)
	}
	return conn, func() { conn.Close(); release() }
}

func acquireWithin(p *Pools, id uuid.UUID, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	conn, release, err := p.Acquire(ctx, id)
	if err == nil {
		conn.Close()
		release()
	}
	return err
}

var bigLimits = PoolsConfig{MaxConns: 10, PerTenantMaxConns: 10, MaxWarmTenants: 10, IdleTimeout: time.Hour}

func TestPoolsPerTenantCap(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 10, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	a := uuid.New()
	_, r1 := acquire(t, p, a)
	_, r2 := acquire(t, p, a)

	if err := acquireWithin(p, a, 50*time.Millisecond); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("third acquisition for one tenant = %v, want ErrPoolExhausted", err)
	}
	r1()
	if err := acquireWithin(p, a, time.Second); err != nil {
		t.Fatalf("acquisition after a release = %v, want success", err)
	}
	r2()
}

func TestPoolsGlobalCap(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 2, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	_, ra := acquire(t, p, uuid.New())
	_, rb := acquire(t, p, uuid.New())
	defer ra()
	defer rb()

	if err := acquireWithin(p, uuid.New(), 50*time.Millisecond); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("acquisition past MaxConns = %v, want ErrPoolExhausted", err)
	}
}

// A saturated tenant's queued requests must not hold global capacity. If
// Acquire took the global slot before the per-tenant one, the queued requests
// for tenant a would each take a global slot and wait, and b would starve.
func TestPoolsSaturatedTenantDoesNotStarveOthers(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 4, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	a := uuid.New()
	_, r1 := acquire(t, p, a)
	_, r2 := acquire(t, p, a)
	defer r1()
	defer r2()

	queued, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if conn, release, err := p.Acquire(queued, a); err == nil {
				conn.Close()
				release()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond) // let the ten queue on a's slot

	if err := acquireWithin(p, uuid.New(), 200*time.Millisecond); err != nil {
		t.Fatalf("another tenant could not acquire while one tenant was saturated: %v", err)
	}
	cancel()
	wg.Wait()
}

func TestPoolsReleaseIsIdempotent(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 2, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	a := uuid.New()
	conn, release, err := p.Acquire(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	release()

	done := make(chan struct{})
	go func() { release(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a second release blocked; release is not idempotent")
	}

	// Exactly MaxConns must be available again: two succeed, a third waits.
	_, r1 := acquire(t, p, a)
	_, r2 := acquire(t, p, uuid.New())
	defer r1()
	defer r2()
	if err := acquireWithin(p, uuid.New(), 50*time.Millisecond); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("a double release granted extra capacity: third acquisition = %v", err)
	}
}

func TestPoolsEvictLeastRecentlyUsedAndNeverBusy(t *testing.T) {
	p, _, opens := testPools(t, PoolsConfig{MaxConns: 10, PerTenantMaxConns: 2, MaxWarmTenants: 2, IdleTimeout: time.Hour})
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	_, ra := acquire(t, p, a)
	ra()
	time.Sleep(2 * time.Millisecond)
	bConn, rb := acquire(t, p, b) // b stays busy
	defer rb()

	_, rc := acquire(t, p, c) // over MaxWarmTenants: a is idle and oldest; c stays busy until rc
	if got := p.Stats().Warm; got != 2 {
		t.Fatalf("Warm = %d after evicting, want 2", got)
	}

	_, rd := acquire(t, p, d) // b and c both busy: nothing evictable, so exceed
	defer rd()
	if got := p.Stats().Warm; got != 3 {
		t.Errorf("Warm = %d with every pool busy, want 3 (exceeding rather than evicting a busy pool)", got)
	}
	if _, err := bConn.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Errorf("busy pool b was closed: %v", err)
	}
	rc()

	_, ra2 := acquire(t, p, a)
	ra2()
	if got := opens(a); got != 2 {
		t.Errorf("tenant a opened %d times, want 2 (it was evicted once)", got)
	}
}

func TestPoolsCancelledWaitReleasesNothing(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 10, PerTenantMaxConns: 1, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	a := uuid.New()
	_, r := acquire(t, p, a)

	if err := acquireWithin(p, a, 30*time.Millisecond); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("wait = %v, want ErrPoolExhausted", err)
	}
	if got := p.Stats().InUseByTenant[a]; got != 1 {
		t.Errorf("InUseByTenant[a] = %d after a cancelled wait, want 1", got)
	}
	r()
	if err := acquireWithin(p, a, time.Second); err != nil {
		t.Errorf("tenant capacity did not return after release: %v", err)
	}
}

// The fake clock replaces p.now after NewPools. That is safe only because a
// one-minute IdleTimeout keeps the janitor from ticking, and so from reading
// p.now, while the test runs.
func TestPoolsCloseIdle(t *testing.T) {
	p, d, _ := testPools(t, PoolsConfig{MaxConns: 10, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Minute})
	now := time.Now()
	p.now = func() time.Time { return now }

	_, r := acquire(t, p, uuid.New())
	r()
	now = now.Add(2 * time.Minute)
	p.closeIdle()

	if got := p.Stats().Warm; got != 0 {
		t.Errorf("Warm = %d after IdleTimeout, want 0", got)
	}
	if opened, closed := d.counts(); closed != opened {
		t.Errorf("stub connections opened %d, closed %d; the idle pool was not closed", opened, closed)
	}
}

func TestPoolsEvictWhileInUse(t *testing.T) {
	p, d, opens := testPools(t, bigLimits)
	a := uuid.New()
	conn, release, err := p.Acquire(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}

	p.Evict(a)
	if _, err := conn.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("an in-use connection stopped working when its pool was evicted: %v", err)
	}
	conn.Close()
	release()
	if opened, closed := d.counts(); closed != opened {
		t.Errorf("the evicted pool was not closed once its last connection was released (opened %d, closed %d)", opened, closed)
	}

	_, r := acquire(t, p, a)
	r()
	if got := opens(a); got != 2 {
		t.Errorf("tenant a opened %d times, want 2 (a fresh pool after eviction)", got)
	}
}

func TestPoolsOpenerErrorIsNotCached(t *testing.T) {
	d := &stubDriver{}
	var calls atomic.Int32
	p, err := NewPools(bigLimits, func(context.Context, uuid.UUID) (*sql.DB, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("login failed")
		}
		return d.db(10), nil
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	a := uuid.New()
	if err := acquireWithin(p, a, time.Second); err == nil || errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("first acquisition = %v, want the opener's error", err)
	}
	if err := acquireWithin(p, a, time.Second); err != nil {
		t.Fatalf("second acquisition = %v, want success; a failed open must not be cached", err)
	}
}

func TestPoolsNeverExceedLimitsUnderLoad(t *testing.T) {
	cfg := PoolsConfig{MaxConns: 8, PerTenantMaxConns: 3, MaxWarmTenants: 6, IdleTimeout: time.Hour}
	p, _, _ := testPools(t, cfg)
	tenants := make([]uuid.UUID, 20)
	for i := range tenants {
		tenants[i] = uuid.New()
	}

	var inUse, peak atomic.Int64
	perTenant := make([]atomic.Int64, len(tenants))
	perPeak := make([]atomic.Int64, len(tenants))
	raise := func(peak *atomic.Int64, n int64) {
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				return
			}
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < 50; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 200; i++ {
				k := rng.Intn(len(tenants))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				conn, release, err := p.Acquire(ctx, tenants[k])
				cancel()
				if err != nil {
					t.Errorf("Acquire: %v", err)
					return
				}
				raise(&peak, inUse.Add(1))
				raise(&perPeak[k], perTenant[k].Add(1))
				time.Sleep(20 * time.Microsecond)
				perTenant[k].Add(-1)
				inUse.Add(-1)
				conn.Close()
				release()
			}
		}(int64(w))
	}
	wg.Wait()

	if got := peak.Load(); got > int64(cfg.MaxConns) {
		t.Errorf("peak connections in use = %d, over MaxConns %d", got, cfg.MaxConns)
	}
	for k := range perPeak {
		if got := perPeak[k].Load(); got > int64(cfg.PerTenantMaxConns) {
			t.Errorf("tenant %d peaked at %d connections, over PerTenantMaxConns %d", k, got, cfg.PerTenantMaxConns)
		}
	}
	if s := p.Stats(); s.InUse != 0 {
		t.Errorf("InUse = %d after every release, want 0", s.InUse)
	}
}

func TestNewPoolsRejectsNonPositiveLimits(t *testing.T) {
	if _, err := NewPools(PoolsConfig{MaxConns: 0, PerTenantMaxConns: 1, MaxWarmTenants: 1, IdleTimeout: time.Second}, nil, zap.NewNop()); err == nil {
		t.Error("NewPools accepted MaxConns 0")
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./tenant/ -run 'TestPools|TestNewPools' -v`
Expected: FAIL — `undefined: NewPools`, `undefined: PoolsConfig`, `undefined: ErrPoolExhausted`.

- [ ] **Step 4: Create `tenant/pools.go`**

```go
package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ErrPoolExhausted is returned when a tenant connection could not be
// acquired before the request's context ended, because the tenant, or every
// tenant together, was at its connection limit.
var ErrPoolExhausted = errors.New("tenant: connection pool exhausted")

var errPoolsClosed = errors.New("tenant: connection pools are closed")

// PoolsConfig bounds Pools. Every field must be positive; see
// RoleIsolationConfig.WithDefaults.
type PoolsConfig struct {
	MaxConns          int
	PerTenantMaxConns int
	MaxWarmTenants    int
	IdleTimeout       time.Duration
}

// PoolOpener opens the *sql.DB for one tenant. Pools calls it outside any
// lock, once per warm period of a tenant.
type PoolOpener func(ctx context.Context, tenantID uuid.UUID) (*sql.DB, error)

// PoolStats is a snapshot of Pools.
type PoolStats struct {
	Warm          int               // tenant pools open
	InUse         int               // connections in use, all tenants
	InUseByTenant map[uuid.UUID]int // connections in use, for each tenant with any
	Hits          uint64            // acquisitions served by an already-open pool
	ColdOpens     uint64            // pools opened
	Evictions     uint64            // pools closed to stay within MaxWarmTenants
	SlotWaits     uint64            // acquisitions that had to wait for a slot
}

type poolEntry struct {
	db       *sql.DB
	ready    chan struct{} // closed once db or err is set
	err      error
	inUse    int
	lastUsed time.Time
	retired  bool // removed from the map; close db when inUse reaches 0
}

type tenantSlots struct {
	sem  chan struct{}
	refs int // acquisitions holding or waiting for a slot
}

// Pools keeps one small *sql.DB per tenant, bounded per tenant and across all
// tenants.
//
// An acquisition takes a per-tenant slot, then a global slot, then a
// connection. The order is the fairness guarantee: a tenant at its own limit
// queues on its per-tenant slot, holding no global capacity, so it cannot
// starve other tenants.
type Pools struct {
	cfg    PoolsConfig
	open   PoolOpener
	logger *zap.Logger
	global chan struct{}
	now    func() time.Time

	mu      sync.Mutex
	slots   map[uuid.UUID]*tenantSlots
	entries map[uuid.UUID]*poolEntry
	stats   PoolStats
	closed  bool

	done chan struct{}
	wg   sync.WaitGroup
}

// NewPools returns Pools that open tenant pools with open.
func NewPools(cfg PoolsConfig, open PoolOpener, logger *zap.Logger) (*Pools, error) {
	if cfg.MaxConns <= 0 || cfg.PerTenantMaxConns <= 0 || cfg.MaxWarmTenants <= 0 || cfg.IdleTimeout <= 0 {
		return nil, fmt.Errorf("tenant: pool limits must be positive: %+v", cfg)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &Pools{
		cfg:     cfg,
		open:    open,
		logger:  logger.Named("tenant_pools"),
		global:  make(chan struct{}, cfg.MaxConns),
		now:     time.Now,
		slots:   map[uuid.UUID]*tenantSlots{},
		entries: map[uuid.UUID]*poolEntry{},
		done:    make(chan struct{}),
	}
	p.wg.Add(1)
	go p.janitor()
	return p, nil
}

// Acquire returns a connection logged in as the tenant's role, and a release
// function. The caller closes the connection and then calls release, which is
// safe to call more than once.
func (p *Pools) Acquire(ctx context.Context, tenantID uuid.UUID) (*sql.Conn, func(), error) {
	ts := p.takeTenantSlots(tenantID)
	waited := false

	select {
	case ts.sem <- struct{}{}:
	default:
		waited = true
		select {
		case ts.sem <- struct{}{}:
		case <-ctx.Done():
			p.dropTenantSlots(tenantID)
			return nil, nil, fmt.Errorf("%w: tenant %s is at its limit of %d connections: %v", ErrPoolExhausted, tenantID, p.cfg.PerTenantMaxConns, ctx.Err())
		}
	}

	select {
	case p.global <- struct{}{}:
	default:
		waited = true
		select {
		case p.global <- struct{}{}:
		case <-ctx.Done():
			<-ts.sem
			p.dropTenantSlots(tenantID)
			return nil, nil, fmt.Errorf("%w: all tenants are at the limit of %d connections: %v", ErrPoolExhausted, p.cfg.MaxConns, ctx.Err())
		}
	}
	if waited {
		p.mu.Lock()
		p.stats.SlotWaits++
		p.mu.Unlock()
	}
	releaseSlots := func() {
		<-p.global
		<-ts.sem
		p.dropTenantSlots(tenantID)
	}

	e, err := p.entry(ctx, tenantID)
	if err != nil {
		releaseSlots()
		return nil, nil, err
	}
	conn, err := e.db.Conn(ctx)
	if err != nil {
		p.finish(e)
		releaseSlots()
		return nil, nil, fmt.Errorf("tenant: connection for %s: %w", tenantID, err)
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			p.finish(e)
			releaseSlots()
		})
	}
	return conn, release, nil
}

// entry returns the tenant's open pool, opening it if needed, with its
// in-use count already raised so it cannot be evicted before it is used.
func (p *Pools) entry(ctx context.Context, id uuid.UUID) (*poolEntry, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPoolsClosed
	}
	if e, ok := p.entries[id]; ok {
		e.inUse++
		e.lastUsed = p.now()
		select {
		case <-e.ready:
			p.stats.Hits++
		default:
		}
		p.mu.Unlock()

		select {
		case <-e.ready:
		case <-ctx.Done():
			p.finish(e)
			return nil, fmt.Errorf("tenant: waiting for %s's pool to open: %w", id, ctx.Err())
		}
		if e.err != nil {
			p.finish(e)
			return nil, e.err
		}
		return e, nil
	}

	e := &poolEntry{ready: make(chan struct{}), inUse: 1, lastUsed: p.now()}
	p.entries[id] = e
	p.stats.ColdOpens++
	evicted := p.evictLocked(id)
	p.mu.Unlock()
	closeAll(evicted)

	db, err := p.open(ctx, id)

	p.mu.Lock()
	e.db, e.err = db, err
	close(e.ready)
	if err != nil {
		if p.entries[id] == e {
			delete(p.entries, id)
		}
		e.inUse--
	}
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return e, nil
}

// evictLocked removes least-recently-used idle pools until at most
// MaxWarmTenants remain, never the one for keep and never one in use. It
// returns the removed pools' databases for the caller to close outside the
// lock. If nothing is evictable the warm count stays above the limit.
func (p *Pools) evictLocked(keep uuid.UUID) []*sql.DB {
	var out []*sql.DB
	for len(p.entries) > p.cfg.MaxWarmTenants {
		var victim uuid.UUID
		var oldest *poolEntry
		for id, e := range p.entries {
			if id == keep || e.inUse > 0 || e.db == nil {
				continue
			}
			if oldest == nil || e.lastUsed.Before(oldest.lastUsed) {
				victim, oldest = id, e
			}
		}
		if oldest == nil {
			break
		}
		delete(p.entries, victim)
		out = append(out, oldest.db)
		oldest.db = nil
		p.stats.Evictions++
	}
	return out
}

// finish lowers an entry's in-use count, closing it if it was retired.
func (p *Pools) finish(e *poolEntry) {
	p.mu.Lock()
	e.inUse--
	e.lastUsed = p.now()
	var toClose *sql.DB
	if e.retired && e.inUse == 0 {
		toClose, e.db = e.db, nil
	}
	p.mu.Unlock()
	if toClose != nil {
		toClose.Close()
	}
}

// Evict closes a tenant's pool, for example when the tenant is suspended.
// Connections already in use keep working until released; the next
// acquisition opens a fresh pool.
func (p *Pools) Evict(tenantID uuid.UUID) {
	p.mu.Lock()
	e, ok := p.entries[tenantID]
	if !ok {
		p.mu.Unlock()
		return
	}
	delete(p.entries, tenantID)
	var toClose *sql.DB
	if e.inUse == 0 {
		toClose, e.db = e.db, nil
	} else {
		e.retired = true
	}
	p.mu.Unlock()
	if toClose != nil {
		toClose.Close()
	}
}

func (p *Pools) takeTenantSlots(id uuid.UUID) *tenantSlots {
	p.mu.Lock()
	defer p.mu.Unlock()
	ts, ok := p.slots[id]
	if !ok {
		ts = &tenantSlots{sem: make(chan struct{}, p.cfg.PerTenantMaxConns)}
		p.slots[id] = ts
	}
	ts.refs++
	return ts
}

func (p *Pools) dropTenantSlots(id uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ts := p.slots[id]
	ts.refs--
	if ts.refs == 0 {
		delete(p.slots, id)
	}
}

func (p *Pools) janitor() {
	defer p.wg.Done()
	interval := p.cfg.IdleTimeout / 4
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-tick.C:
			p.closeIdle()
		}
	}
}

// closeIdle closes every pool with nothing in use for IdleTimeout.
func (p *Pools) closeIdle() {
	now := p.now()
	var toClose []*sql.DB
	p.mu.Lock()
	for id, e := range p.entries {
		if e.inUse == 0 && e.db != nil && now.Sub(e.lastUsed) >= p.cfg.IdleTimeout {
			delete(p.entries, id)
			toClose = append(toClose, e.db)
			e.db = nil
		}
	}
	p.mu.Unlock()
	closeAll(toClose)
}

// Stats returns a snapshot of the pools.
func (p *Pools) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.InUse = len(p.global)
	s.InUseByTenant = map[uuid.UUID]int{}
	for id, ts := range p.slots {
		if n := len(ts.sem); n > 0 {
			s.InUseByTenant[id] = n
		}
	}
	for _, e := range p.entries {
		if e.db != nil {
			s.Warm++
		}
	}
	return s
}

// Close closes every tenant pool and stops the idle janitor.
func (p *Pools) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.done)
	var dbs []*sql.DB
	for id, e := range p.entries {
		delete(p.entries, id)
		if e.db != nil {
			dbs = append(dbs, e.db)
			e.db = nil
		}
	}
	p.mu.Unlock()
	p.wg.Wait()

	var errs []error
	for _, db := range dbs {
		if err := db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func closeAll(dbs []*sql.DB) {
	for _, db := range dbs {
		db.Close()
	}
}
```

- [ ] **Step 5: Run the tests, with the race detector**

Run: `go test ./tenant/ -run 'TestPools|TestNewPools' -race -count=3 -v`
Expected: PASS on all three runs, no race reports.

- [ ] **Step 6: Mutation-check the fairness test**

In `Acquire`, swap the per-tenant `select` block and the global `select` block, so the global slot is taken first. Run: `go test ./tenant/ -run TestPoolsSaturatedTenantDoesNotStarveOthers -count=1`
Expected: FAIL — "another tenant could not acquire while one tenant was saturated". Restore the original order and confirm the test passes again.

- [ ] **Step 7: Verify and commit**

Run: `gofmt -l . && go vet ./... && go test -short -race ./...`
Expected: no gofmt output; all PASS.

```bash
git add tenant/pools.go tenant/pools_test.go tenant/stubdriver_test.go
git commit -m "feat(tenant): bounded per-tenant connection pools

A per-tenant slot is taken before the global one, so a saturated tenant's
queue holds no global capacity and cannot starve other tenants; the order is
guarded by a test that fails when it is reversed.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Pooled connections and the role-isolated manager

**Files:**
- Modify: `tenant/conn.go` (`Conn` struct, `newConn`, `Close`)
- Create: `tenant/role_manager.go`, `tenant/role_manager_test.go`

**Interfaces:**
- Consumes: `Pools`, `Pools.Acquire`, `ErrPoolExhausted` (Task 3); `stubDriver` (Task 3).
- Produces:
  - `func NewRoleIsolatedManager(m Manager, pools *Pools, schemaName func(uuid.UUID) string, logger *zap.Logger) Manager`
  - unexported `newPooledConn(conn *sql.Conn, schemaName string, release, onLeak func()) *Conn`; `newConn` keeps its signature

- [ ] **Step 1: Write the failing test**

Create `tenant/role_manager_test.go`:

```go
package tenant

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// innerManager stands in for the real manager; embedding the interface makes
// any method the test does not define panic if called.
type innerManager struct {
	Manager
	closed bool
	hooks  []Hook
}

func (m *innerManager) Close() error        { m.closed = true; return nil }
func (m *innerManager) RegisterHook(h Hook) { m.hooks = append(m.hooks, h) }

func roleManagerForTest(t *testing.T, cfg PoolsConfig, logger *zap.Logger) (Manager, *innerManager, *stubDriver, *Pools) {
	t.Helper()
	d := &stubDriver{}
	pools, err := NewPools(cfg, func(context.Context, uuid.UUID) (*sql.DB, error) {
		return d.db(cfg.PerTenantMaxConns), nil
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	inner := &innerManager{}
	m := NewRoleIsolatedManager(inner, pools, roleNameForTest, logger)
	t.Cleanup(func() { m.Close() })
	return m, inner, d, pools
}

func TestRoleIsolatedGetTenantConnScopesAndReleases(t *testing.T) {
	m, _, d, pools := roleManagerForTest(t, bigLimits, zap.NewNop())
	id := uuid.New()

	conn, err := m.GetTenantConn(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if conn.SchemaName() != roleNameForTest(id) {
		t.Errorf("SchemaName() = %q, want %q", conn.SchemaName(), roleNameForTest(id))
	}
	if _, err := conn.ExecContext(context.Background(), "INSERT INTO projects DEFAULT VALUES"); err != nil {
		t.Fatal(err)
	}
	want := `SET LOCAL search_path TO "` + roleNameForTest(id) + `", public`
	if !slices.Contains(d.statements(), want) {
		t.Errorf("statements %q do not include %q", d.statements(), want)
	}
	if got := pools.Stats().InUse; got != 1 {
		t.Errorf("InUse = %d while the connection is open, want 1", got)
	}

	conn.Close()
	conn.Close()
	if got := pools.Stats().InUse; got != 0 {
		t.Errorf("InUse = %d after Close, want 0", got)
	}
}

func TestRoleIsolatedWithTenantTx(t *testing.T) {
	m, _, d, pools := roleManagerForTest(t, bigLimits, zap.NewNop())
	id := uuid.New()

	if err := m.WithTenantTx(context.Background(), id, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "UPDATE projects SET name = 'x'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stmts := d.statements()
	if n := len(stmts); n < 4 || stmts[n-1] != "COMMIT" || stmts[n-2] != "UPDATE projects SET name = 'x'" {
		t.Errorf("statements = %q, want BEGIN, SET LOCAL, the update, COMMIT", stmts)
	}

	boom := errors.New("boom")
	if err := m.WithTenantTx(context.Background(), id, func(*sql.Tx) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("WithTenantTx = %v, want the callback's error", err)
	}
	if stmts := d.statements(); stmts[len(stmts)-1] != "ROLLBACK" {
		t.Errorf("last statement = %q after a failed callback, want ROLLBACK", stmts[len(stmts)-1])
	}
	if got := pools.Stats().InUse; got != 0 {
		t.Errorf("InUse = %d after WithTenantTx, want 0", got)
	}
}

func TestRoleIsolatedSurfacesPoolExhaustion(t *testing.T) {
	m, _, _, _ := roleManagerForTest(t, PoolsConfig{MaxConns: 1, PerTenantMaxConns: 1, MaxWarmTenants: 5, IdleTimeout: time.Hour}, zap.NewNop())
	held, err := m.GetTenantConn(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := m.GetTenantConn(ctx, uuid.New()); !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("GetTenantConn = %v, want ErrPoolExhausted", err)
	}
}

func TestRoleIsolatedPassesThroughAndClosesBoth(t *testing.T) {
	m, inner, _, pools := roleManagerForTest(t, bigLimits, zap.NewNop())
	m.RegisterHook(BaseHook{})
	if len(inner.hooks) != 1 {
		t.Error("RegisterHook did not reach the wrapped manager")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if !inner.closed {
		t.Error("Close did not close the wrapped manager")
	}
	if _, _, err := pools.Acquire(context.Background(), uuid.New()); err == nil {
		t.Error("pools still served connections after Close")
	}
}

// A connection that is garbage-collected without Close holds its slots
// forever; the finalizer must say so, naming the tenant.
func TestRoleIsolatedReportsLeakedConnections(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	m, _, _, _ := roleManagerForTest(t, bigLimits, zap.New(core))
	id := uuid.New()

	func() {
		if _, err := m.GetTenantConn(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		for _, e := range logs.All() {
			for _, f := range e.Context {
				if f.Key == "tenant_id" && f.String == id.String() {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no leak warning naming the tenant after the unclosed connection was collected")
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./tenant/ -run TestRoleIsolated -v`
Expected: FAIL — `undefined: NewRoleIsolatedManager`.

- [ ] **Step 3: Give `tenant.Conn` a release callback and a leak report**

In `tenant/conn.go`, add `"runtime"` and `"sync/atomic"` to the imports, and replace the `Conn` struct, `newConn`, and `Close`:

```go
type Conn struct {
	conn       *sql.Conn
	schemaName string
	searchPath string // the SET LOCAL statement, built once
	release    func() // returns the connection's pool slots; nil for the shared pool
	closed     atomic.Bool
}

func newConn(conn *sql.Conn, schemaName string) *Conn {
	return newPooledConn(conn, schemaName, nil, nil)
}

// newPooledConn is newConn for a connection drawn from Pools: Close also
// calls release, and if the Conn is garbage-collected without Close, onLeak
// reports it.
func newPooledConn(conn *sql.Conn, schemaName string, release, onLeak func()) *Conn {
	c := &Conn{
		conn:       conn,
		schemaName: schemaName,
		searchPath: fmt.Sprintf(`SET LOCAL search_path TO "%s", public`, schemaName),
		release:    release,
	}
	if onLeak != nil {
		runtime.SetFinalizer(c, func(c *Conn) {
			if !c.closed.Load() {
				onLeak()
			}
		})
	}
	return c
}
```

```go
// Close releases the connection back to the pool.
func (c *Conn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	c.closed.Store(true)
	err := c.conn.Close()
	if c.release != nil {
		c.release()
	}
	return err
}
```

`Close` still returns the underlying `*sql.Conn` error on every call, as before; `release` is idempotent, so a second `Close` returns no slot twice.

- [ ] **Step 4: Create `tenant/role_manager.go`**

```go
package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// roleIsolatedManager is a Manager whose tenant connections come from Pools,
// logged in as each tenant's own role. Every other method is the wrapped
// manager's.
type roleIsolatedManager struct {
	Manager
	pools      *Pools
	schemaName func(uuid.UUID) string
	logger     *zap.Logger
}

// NewRoleIsolatedManager wraps m so that GetTenantConn and WithTenantTx draw
// from pools. schemaName maps a tenant to its schema, which search_path is
// still set to, so unqualified names resolve exactly as in the default mode.
// Close closes the pools and then m.
func NewRoleIsolatedManager(m Manager, pools *Pools, schemaName func(uuid.UUID) string, logger *zap.Logger) Manager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &roleIsolatedManager{Manager: m, pools: pools, schemaName: schemaName, logger: logger.Named("role_isolation")}
}

func (r *roleIsolatedManager) GetTenantConn(ctx context.Context, tenantID uuid.UUID) (*Conn, error) {
	conn, release, err := r.pools.Acquire(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return newPooledConn(conn, r.schemaName(tenantID), release, func() {
		r.logger.Warn("A tenant connection was garbage-collected without Close; its pool slots stay held until the process exits",
			zap.String("tenant_id", tenantID.String()))
	}), nil
}

func (r *roleIsolatedManager) WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(tx *sql.Tx) error) error {
	conn, err := r.GetTenantConn(ctx, tenantID)
	if err != nil {
		return err
	}
	defer conn.Close()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

func (r *roleIsolatedManager) Close() error {
	return errors.Join(r.pools.Close(), r.Manager.Close())
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./tenant/ -run TestRoleIsolated -race -v`
Expected: PASS.

- [ ] **Step 6: Verify nothing regressed**

`newConn`'s callers are unchanged, and `TestIntegration_TenantConnIsPoolerSafe`-style tests exercise `Close`. Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: no gofmt output; all PASS, including the integration suite.

- [ ] **Step 7: Commit**

```bash
git add tenant/conn.go tenant/role_manager.go tenant/role_manager_test.go
git commit -m "feat(tenant): route tenant connections through per-tenant pools

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `TENANT_DB_BUSY`

**Files:**
- Modify: `middleware/httpmw/middleware.go` (`SetTenantDB` error branch), `middleware/httpmw/errors.go` (`statusForCode`)
- Test: `middleware/httpmw/middleware_test.go` (append)

**Interfaces:**
- Consumes: `tenant.ErrPoolExhausted` (Task 3).
- Produces: error code `TENANT_DB_BUSY` → 503 with `Retry-After: 1`.

- [ ] **Step 1: Write the failing test**

Append to `middleware/httpmw/middleware_test.go`:

```go
// An exhausted tenant pool is load, not failure: the client should retry.
func TestSetTenantDBReportsAnExhaustedPoolAsBusy(t *testing.T) {
	id := uuid.New()
	mgr := &stubManager{getTenantConn: func(context.Context, uuid.UUID) (*tenant.Conn, error) {
		return nil, fmt.Errorf("%w: tenant is at its limit", tenant.ErrPoolExhausted)
	}}
	mw := New(mgr, nil, zap.NewNop(), Config{})

	rec := httptest.NewRecorder()
	Chain(okHandler(), withTenant(id, tenant.StatusActive), mw.SetTenantDB()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want %q", got, "1")
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if got := errorCode(body); got != "TENANT_DB_BUSY" {
		t.Errorf("code = %q, want TENANT_DB_BUSY", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./middleware/httpmw/ -run TestSetTenantDBReportsAnExhaustedPoolAsBusy -v`
Expected: FAIL — status 500 and code `DATABASE_ERROR`.

- [ ] **Step 3: Implement**

In `middleware/httpmw/middleware.go`, in `SetTenantDB`, replace the `if err != nil { ... }` block after `m.manager.GetTenantConn` with:

```go
			if err != nil {
				if errors.Is(err, tenant.ErrPoolExhausted) {
					m.logger.Warn("Tenant connection pool exhausted", zap.String("tenant_id", tc.TenantID.String()), zap.Error(err))
					w.Header().Set("Retry-After", "1")
					m.config.ErrorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "TENANT_DB_BUSY", Message: "Tenant database is busy; retry shortly"})
					return
				}
				m.logger.Error("Failed to get tenant database connection", zap.String("tenant_id", tc.TenantID.String()), zap.Error(err))
				m.config.ErrorHandler(w, r, &tenant.TenantError{TenantID: tc.TenantID, Code: "DATABASE_ERROR", Message: "Failed to access tenant database"})
				return
			}
```

In `middleware/httpmw/errors.go`, add to `statusForCode` before `default`:

```go
	case "TENANT_DB_BUSY":
		return http.StatusServiceUnavailable
```

- [ ] **Step 4: Run the tests**

Run: `go test ./middleware/httpmw/ -v -count=1 && (cd middleware/gin && go test ./... -count=1)`
Expected: PASS in both modules.

- [ ] **Step 5: Commit**

```bash
git add middleware/httpmw/middleware.go middleware/httpmw/errors.go middleware/httpmw/middleware_test.go
git commit -m "feat(httpmw): answer an exhausted tenant pool with 503 and Retry-After

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Role manager and the isolation guarantees

**Files:**
- Create: `database/roles.go`
- Create: `role_isolation_integration_test.go` (root package)

**Interfaces:**
- Consumes: `tenant.CredentialSource`, `tenant.NewSCRAMVerifier`, `tenant.DerivePassword` (Task 2); `tenant.SchemaManager` (existing).
- Produces:
  - `type TenantRoles interface { Ensure(ctx, *tenant.Tenant) error; Grant(ctx, uuid.UUID) error; LockOut(ctx, uuid.UUID) error; Drop(ctx, uuid.UUID) error }`
  - `type RoleManager`; `func NewRoleManager(db *sql.DB, schemas tenant.SchemaManager, creds *tenant.CredentialSource, logger *zap.Logger) *RoleManager` — implements `TenantRoles`
  - `func (rm *RoleManager) CheckPrerequisites(ctx context.Context) error` — errors wrap `ErrRolePrerequisites`
  - `var ErrRolePrerequisites error`
  - test helpers in `role_isolation_integration_test.go`: `testRoleSecret []byte`, `roleTestTenantDSN() string` (used from Task 9), `testRoleName(uuid.UUID) string`, `connectAsRole(t, dsn string, c tenant.RoleCredentials) (*pgx.Conn, error)`, `dropTestRoles(db *sql.DB, ids []uuid.UUID)`, `wantSQLState(t, what string, err error, code string)`

- [ ] **Step 1: Write the failing integration test**

Create `role_isolation_integration_test.go`:

```go
package multitenant

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant/database"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

var testRoleSecret = bytes.Repeat([]byte("r"), tenant.MinRoleSecretLen)

// roleTestTenantDSN is where tenant roles log in: PgBouncer in the CI jobs
// that set ROLE_TEST_TENANT_DSN, otherwise the test database directly.
func roleTestTenantDSN() string {
	if dsn := os.Getenv("ROLE_TEST_TENANT_DSN"); dsn != "" {
		return dsn
	}
	return getTestDatabaseURL()
}

func testRoleName(id uuid.UUID) string {
	return "tenant_" + strings.ReplaceAll(id.String(), "-", "_")
}

// connectAsRole opens one connection as a tenant role, with the library's
// driver settings.
func connectAsRole(t *testing.T, dsn string, c tenant.RoleCredentials) (*pgx.Conn, error) {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = c.User, c.Password
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	return pgx.ConnectConfig(context.Background(), cfg)
}

// dropTestRoles removes tenant roles, which are cluster-wide and so outlive
// the tenants' schemas.
func dropTestRoles(db *sql.DB, ids []uuid.UUID) {
	for _, id := range ids {
		role := `"` + testRoleName(id) + `"`
		_, _ = db.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1", testRoleName(id))
		_, _ = db.Exec("DROP OWNED BY " + role)
		_, _ = db.Exec("DROP ROLE IF EXISTS " + role)
	}
}

func wantSQLState(t *testing.T, what string, err error, code string) {
	t.Helper()
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != code {
		t.Errorf("%s: got %v, want SQLSTATE %s", what, err, code)
	}
}

// TestIntegration_RoleIsolation_Guarantees checks the spec's five success
// criteria on a tenant role's own connection.
func TestIntegration_RoleIsolation_Guarantees(t *testing.T) {
	db := setupTestDatabase(t)
	ctx := context.Background()
	mt, err := New(testConfig(getTestDatabaseURL()))
	if err != nil {
		t.Fatal(err)
	}
	defer mt.Close()

	a, b := uuid.New(), uuid.New()
	defer cleanupTestData(db, []uuid.UUID{a, b})
	defer dropTestRoles(db, []uuid.UUID{a, b})
	for i, id := range []uuid.UUID{a, b} {
		if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "guarantee", Subdomain: []string{"guarantee-a", "guarantee-b"}[i]}); err != nil {
			t.Fatal(err)
		}
		if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	schemas := database.NewSchemaManager(db, zap.NewNop(), "tenant_")
	creds := tenant.NewCredentialSource(tenant.RoleIsolationConfig{Secret: testRoleSecret}, schemas.GetSchemaName)
	roles := database.NewRoleManager(db, schemas, creds, zap.NewNop())
	if err := roles.CheckPrerequisites(ctx); err != nil {
		t.Fatalf("CheckPrerequisites: %v", err)
	}
	for _, id := range []uuid.UUID{a, b} {
		t.Logf("ensuring %s", testRoleName(id))
		tn, err := mt.Manager.GetTenant(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := roles.Ensure(ctx, tn); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if err := roles.Ensure(ctx, tn); err != nil {
			t.Fatalf("Ensure is not idempotent: %v", err)
		}
	}

	// A table a later migration adds, with a sequence, created by the admin
	// role after the tenant role exists: default privileges must cover it.
	if err := mt.Migrations.ApplyMigration(ctx, a, &tenant.Migration{
		Version: "900", Name: "widgets",
		SQL: "CREATE TABLE widgets (id bigserial PRIMARY KEY, name text NOT NULL)",
	}); err != nil {
		t.Fatal(err)
	}

	credA, err := creds.Current(a)
	if err != nil {
		t.Fatal(err)
	}
	// Straight to PostgreSQL, even in the PgBouncer jobs: these guarantees are
	// enforced by the database, whatever sits in front of it, and this test
	// renders no PgBouncer auth file.
	conn, err := connectAsRole(t, getTestDatabaseURL(), credA)
	if err != nil {
		t.Fatalf("tenant a's role could not log in: %v", err)
	}
	defer conn.Close(ctx)
	sa, sb := `"`+testRoleName(a)+`"`, `"`+testRoleName(b)+`"`
	exec := func(sql string) error { _, err := conn.Exec(ctx, sql); return err }

	// 1. Another tenant's schema, read and written by qualified name.
	wantSQLState(t, "qualified read of b", exec("SELECT count(*) FROM "+sb+".projects"), "42501")
	wantSQLState(t, "qualified write of b", exec("INSERT INTO "+sb+".projects (name) VALUES ('x')"), "42501")
	// 2. Becoming another tenant.
	wantSQLState(t, "SET ROLE b", exec("SET ROLE "+sb), "42501")
	// 3. The registry.
	wantSQLState(t, "read public.tenants", exec("SELECT count(*) FROM public.tenants"), "42501")
	wantSQLState(t, "read public.tenant_migrations", exec("SELECT count(*) FROM public.tenant_migrations"), "42501")
	// 4. DDL, in its own schema and in public.
	wantSQLState(t, "CREATE TABLE in own schema", exec("CREATE TABLE "+sa+".evil (id int)"), "42501")
	wantSQLState(t, "CREATE TABLE in public", exec("CREATE TABLE public.evil (id int)"), "42501")
	wantSQLState(t, "DROP own table", exec("DROP TABLE "+sa+".projects"), "42501")
	wantSQLState(t, "TRUNCATE own table", exec("TRUNCATE "+sa+".projects"), "42501")
	// 5. Its own data, including a sequence and a trigger the admin owns.
	for _, stmt := range []string{
		"INSERT INTO " + sa + ".projects (name) VALUES ('mine')",
		"SELECT count(*) FROM " + sa + ".projects",
		"UPDATE " + sa + ".projects SET name = 'still mine'",
		"DELETE FROM " + sa + ".projects",
		"INSERT INTO " + sa + ".widgets (name) VALUES ('uses the sequence')",
	} {
		if err := exec(stmt); err != nil {
			t.Errorf("own DML %q: %v", stmt, err)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test . -run TestIntegration_RoleIsolation_Guarantees -v -count=1`
Expected: FAIL — compilation error, `undefined: database.NewRoleManager`.

- [ ] **Step 3: Create `database/roles.go`**

```go
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ErrRolePrerequisites reports a database that cannot run role isolation.
var ErrRolePrerequisites = errors.New("database: role isolation prerequisites not met")

// TenantRoles is what the isolation hook and the granting migration manager
// need from RoleManager.
type TenantRoles interface {
	Ensure(ctx context.Context, t *tenant.Tenant) error
	Grant(ctx context.Context, tenantID uuid.UUID) error
	LockOut(ctx context.Context, tenantID uuid.UUID) error
	Drop(ctx context.Context, tenantID uuid.UUID) error
}

// RoleManager creates and maintains one PostgreSQL login role per tenant, on
// the admin connection. Every method is idempotent. All of them must run as
// the same admin role: from PostgreSQL 16 a CREATEROLE role can alter and
// drop only the roles it created.
type RoleManager struct {
	db      *sql.DB
	schemas tenant.SchemaManager
	creds   *tenant.CredentialSource
	logger  *zap.Logger
}

// NewRoleManager returns a RoleManager that issues role DDL on db.
func NewRoleManager(db *sql.DB, schemas tenant.SchemaManager, creds *tenant.CredentialSource, logger *zap.Logger) *RoleManager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RoleManager{db: db, schemas: schemas, creds: creds, logger: logger.Named("role_manager")}
}

var _ TenantRoles = (*RoleManager)(nil)

// CheckPrerequisites reports whether the server and the admin role can run
// role isolation: PostgreSQL 15 or later, and CREATEROLE and pg_signal_backend
// for the admin role, or superuser.
func (rm *RoleManager) CheckPrerequisites(ctx context.Context) error {
	var version int
	if err := rm.db.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		return fmt.Errorf("database: read server version: %w", err)
	}
	if version < 150000 {
		return fmt.Errorf("%w: PostgreSQL %d, need 15 or later: before 15 every role can create tables in public", ErrRolePrerequisites, version/10000)
	}
	var super, createRole, signal bool
	if err := rm.db.QueryRowContext(ctx,
		`SELECT rolsuper, rolcreaterole, pg_has_role(current_user, 'pg_signal_backend', 'USAGE')
		   FROM pg_roles WHERE rolname = current_user`).Scan(&super, &createRole, &signal); err != nil {
		return fmt.Errorf("database: read admin role attributes: %w", err)
	}
	if !super && !createRole {
		return fmt.Errorf("%w: the admin role needs CREATEROLE to manage tenant roles", ErrRolePrerequisites)
	}
	if !super && !signal {
		return fmt.Errorf("%w: the admin role needs membership in pg_signal_backend to end a suspended tenant's sessions", ErrRolePrerequisites)
	}
	return nil
}

// Ensure brings t's role to the state its record calls for: created if
// missing, with the current password, LOGIN if the tenant is active and
// NOLOGIN otherwise, and DML on every table and sequence in its schema, now
// and in future. The tenant's schema must exist.
func (rm *RoleManager) Ensure(ctx context.Context, t *tenant.Tenant) error {
	role := quoteIdent(rm.creds.RoleName(t.ID))
	schema := quoteIdent(rm.schemas.GetSchemaName(t.ID))
	cred, err := rm.creds.Current(t.ID)
	if err != nil {
		return err
	}
	password := "NULL"
	if cred.Password != "" {
		verifier, err := tenant.NewSCRAMVerifier(cred.Password)
		if err != nil {
			return err
		}
		password = quoteLiteral(verifier)
	}
	login := "NOLOGIN"
	if t.Status == tenant.StatusActive {
		login = "LOGIN"
	}

	tx, err := rm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("database: ensure role %s: %w", cred.User, err)
	}
	defer tx.Rollback()

	exists, err := roleExists(ctx, tx, cred.User)
	if err != nil {
		return err
	}
	stmts := []string{}
	if !exists {
		stmts = append(stmts, "CREATE ROLE "+role+" NOINHERIT NOCREATEDB NOCREATEROLE")
	}
	stmts = append(stmts,
		"ALTER ROLE "+role+" PASSWORD "+password,
		"ALTER ROLE "+role+" "+login,
		"GRANT USAGE ON SCHEMA "+schema+" TO "+role,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA "+schema+" GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "+role,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA "+schema+" GRANT USAGE, SELECT ON SEQUENCES TO "+role,
	)
	stmts = append(stmts, backfillGrants(schema, role)...)
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("database: ensure role %s: %w", cred.User, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("database: ensure role %s: %w", cred.User, err)
	}
	return nil
}

// Grant reapplies DML grants on every table and sequence in the tenant's
// schema. It does nothing if the tenant has no role yet.
func (rm *RoleManager) Grant(ctx context.Context, tenantID uuid.UUID) error {
	name := rm.creds.RoleName(tenantID)
	exists, err := roleExists(ctx, rm.db, name)
	if err != nil || !exists {
		return err
	}
	schema, role := quoteIdent(rm.schemas.GetSchemaName(tenantID)), quoteIdent(name)
	for _, s := range backfillGrants(schema, role) {
		if _, err := rm.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("database: grant to %s: %w", name, err)
		}
	}
	return nil
}

// LockOut stops the tenant's role from logging in and ends its sessions,
// including connections a pooler holds on its behalf.
func (rm *RoleManager) LockOut(ctx context.Context, tenantID uuid.UUID) error {
	name := rm.creds.RoleName(tenantID)
	exists, err := roleExists(ctx, rm.db, name)
	if err != nil || !exists {
		return err
	}
	if _, err := rm.db.ExecContext(ctx, "ALTER ROLE "+quoteIdent(name)+" NOLOGIN"); err != nil {
		return fmt.Errorf("database: lock out %s: %w", name, err)
	}
	var ended int
	if err := rm.db.QueryRowContext(ctx,
		`SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE usename = $1`, name).Scan(&ended); err != nil {
		return fmt.Errorf("database: end sessions of %s: %w", name, err)
	}
	rm.logger.Info("Locked out tenant role", zap.String("role", name), zap.Int("sessions_ended", ended))
	return nil
}

// Drop locks the tenant's role out, revokes everything granted to it, and
// drops it. It does nothing if the role does not exist.
func (rm *RoleManager) Drop(ctx context.Context, tenantID uuid.UUID) error {
	name := rm.creds.RoleName(tenantID)
	exists, err := roleExists(ctx, rm.db, name)
	if err != nil || !exists {
		return err
	}
	if err := rm.LockOut(ctx, tenantID); err != nil {
		return err
	}
	role := quoteIdent(name)
	for _, s := range []string{"DROP OWNED BY " + role, "DROP ROLE " + role} {
		if _, err := rm.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("database: drop role %s: %w", name, err)
		}
	}
	return nil
}

func backfillGrants(schema, role string) []string {
	return []string{
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA " + schema + " TO " + role,
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA " + schema + " TO " + role,
	}
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func roleExists(ctx context.Context, q queryRower, name string) (bool, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, name).Scan(&exists); err != nil {
		return false, fmt.Errorf("database: look up role %s: %w", name, err)
	}
	return exists, nil
}

func quoteIdent(s string) string   { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func quoteLiteral(s string) string { return `'` + strings.ReplaceAll(s, `'`, `''`) + `'` }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test . -run TestIntegration_RoleIsolation_Guarantees -v -count=1`
Expected: PASS — every refusal is SQLSTATE `42501`, every own-DML statement succeeds.

- [ ] **Step 5: Mutation-check the qualified-read guarantee**

For the duration of this check, grant tenant a read access to tenant b's schema, by inserting into the test right after the `Ensure` loop:

```go
	_, _ = db.Exec(`GRANT USAGE ON SCHEMA "` + testRoleName(b) + `" TO "` + testRoleName(a) + `"; ` +
		`GRANT SELECT ON ALL TABLES IN SCHEMA "` + testRoleName(b) + `" TO "` + testRoleName(a) + `"`)
```

Run: `go test . -run TestIntegration_RoleIsolation_Guarantees -count=1`
Expected: FAIL at "qualified read of b". That shows the test detects the very grant it forbids. Remove the inserted lines and confirm the test passes again.

- [ ] **Step 6: Verify and commit**

Run: `gofmt -l . && go vet ./... && go test ./... -count=1`
Expected: no gofmt output; all PASS.

```bash
git add database/roles.go role_isolation_integration_test.go
git commit -m "feat(database): manage one login role per tenant

Integration tests check the spec's five guarantees on a tenant role's own
connection: another tenant's schema, SET ROLE, the registry, and DDL are all
refused with 42501, while its own DML, sequences and triggers work.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Isolation hook and granting migrations

**Files:**
- Create: `database/role_hook.go`, `database/role_hook_test.go`, `database/role_migrations.go`, `database/role_migrations_test.go`

**Interfaces:**
- Consumes: `TenantRoles` (Task 6); `tenant.BaseHook`, `tenant.SchemaManager`, `tenant.MigrationManager`, `tenant.Repository` (existing).
- Produces:
  - `type RoleHook`; `func NewRoleHook(roles TenantRoles, schemas tenant.SchemaManager, evict func(uuid.UUID), logger *zap.Logger) *RoleHook`
  - `func NewGrantingMigrationManager(inner tenant.MigrationManager, roles TenantRoles, repo tenant.Repository) tenant.MigrationManager`
  - `func ForEachProvisionedTenant(ctx context.Context, repo tenant.Repository, schemas tenant.SchemaManager, fn func(*tenant.Tenant) error) error` (used by Tasks 8 and 9)
  - unexported `forEachTenant(ctx, repo, fn) error`

- [ ] **Step 1: Write the failing hook test**

Create `database/role_hook_test.go`:

```go
package database

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// fakeRoles records calls, and refuses to act on a role Ensure has not
// created - the failure the original design would have hit on every first
// provisioning.
type fakeRoles struct {
	calls   []string
	created map[uuid.UUID]bool
	err     error
}

func (f *fakeRoles) Ensure(_ context.Context, t *tenant.Tenant) error {
	f.calls = append(f.calls, "ensure:"+t.Status)
	if f.created == nil {
		f.created = map[uuid.UUID]bool{}
	}
	f.created[t.ID] = true
	return f.err
}

func (f *fakeRoles) Grant(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "grant")
	return f.err
}

func (f *fakeRoles) LockOut(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "lockout")
	if !f.created[id] {
		return errors.New("role does not exist")
	}
	return f.err
}

func (f *fakeRoles) Drop(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "drop")
	return f.err
}

// fakeSchemas embeds the interface so unused methods panic.
type fakeSchemas struct {
	tenant.SchemaManager
	exists bool
}

func (f fakeSchemas) SchemaExists(context.Context, uuid.UUID) (bool, error) { return f.exists, nil }

func newTestHook(exists bool) (*RoleHook, *fakeRoles, *[]uuid.UUID) {
	roles := &fakeRoles{}
	var evicted []uuid.UUID
	h := NewRoleHook(roles, fakeSchemas{exists: exists}, func(id uuid.UUID) { evicted = append(evicted, id) }, zap.NewNop())
	return h, roles, &evicted
}

// ProvisionTenant fires OnTenantStatusChanged before OnTenantProvisioned.
func TestRoleHookProvisioningInEitherEventOrder(t *testing.T) {
	h, roles, _ := newTestHook(true)
	tn := &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusActive}
	ctx := context.Background()

	if err := h.OnTenantStatusChanged(ctx, tn, tenant.StatusPending); err != nil {
		t.Fatalf("status change during provisioning: %v", err)
	}
	if err := h.OnTenantProvisioned(ctx, tn); err != nil {
		t.Fatalf("provisioned: %v", err)
	}
	if want := []string{"ensure:active", "ensure:active"}; !slices.Equal(roles.calls, want) {
		t.Errorf("calls = %q, want %q", roles.calls, want)
	}
}

func TestRoleHookSuspensionLocksOutAndEvicts(t *testing.T) {
	h, roles, evicted := newTestHook(true)
	tn := &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusSuspended}

	if err := h.OnTenantStatusChanged(context.Background(), tn, tenant.StatusActive); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ensure:suspended", "lockout"}; !slices.Equal(roles.calls, want) {
		t.Errorf("calls = %q, want %q", roles.calls, want)
	}
	if !slices.Equal(*evicted, []uuid.UUID{tn.ID}) {
		t.Errorf("evicted = %v, want the suspended tenant", *evicted)
	}
}

// A status change for a tenant that was never provisioned has no schema to
// grant on and no role to manage.
func TestRoleHookIgnoresUnprovisionedTenants(t *testing.T) {
	h, roles, _ := newTestHook(false)
	if err := h.OnTenantStatusChanged(context.Background(), &tenant.Tenant{ID: uuid.New(), Status: tenant.StatusSuspended}, tenant.StatusPending); err != nil {
		t.Fatal(err)
	}
	if len(roles.calls) != 0 {
		t.Errorf("calls = %q, want none", roles.calls)
	}
}

func TestRoleHookDeletionEvictsThenDrops(t *testing.T) {
	h, roles, evicted := newTestHook(true)
	tn := &tenant.Tenant{ID: uuid.New()}
	if err := h.OnTenantDeleted(context.Background(), tn); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(roles.calls, []string{"drop"}) || !slices.Equal(*evicted, []uuid.UUID{tn.ID}) {
		t.Errorf("calls = %q, evicted = %v; want drop, and the tenant evicted", roles.calls, *evicted)
	}
}

func TestRoleHookName(t *testing.T) {
	h, _, _ := newTestHook(true)
	if h.Name() != "role_isolation" {
		t.Errorf("Name() = %q", h.Name())
	}
}
```

- [ ] **Step 2: Write the failing migrations test**

Create `database/role_migrations_test.go`:

```go
package database

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

type fakeMigrations struct {
	tenant.MigrationManager
	err error
}

func (f *fakeMigrations) ApplyMigration(context.Context, uuid.UUID, *tenant.Migration) error { return f.err }
func (f *fakeMigrations) ApplyPending(context.Context, uuid.UUID) error                       { return f.err }
func (f *fakeMigrations) ApplyToAllTenants(context.Context, *tenant.Migration) error          { return f.err }
func (f *fakeMigrations) ApplyPendingToAllTenants(context.Context) error                      { return f.err }

type fakeRepo struct {
	tenant.Repository
	tenants []*tenant.Tenant
}

func (f fakeRepo) List(_ context.Context, page, perPage int) ([]*tenant.Tenant, int, error) {
	start := (page - 1) * perPage
	if start >= len(f.tenants) {
		return nil, len(f.tenants), nil
	}
	end := min(start+perPage, len(f.tenants))
	return f.tenants[start:end], len(f.tenants), nil
}

type grantLog struct {
	fakeRoles
	granted []uuid.UUID
}

func (g *grantLog) Grant(_ context.Context, id uuid.UUID) error {
	g.granted = append(g.granted, id)
	return nil
}

func TestGrantingMigrationsGrantAfterEachRun(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	roles := &grantLog{}
	m := NewGrantingMigrationManager(&fakeMigrations{}, roles, fakeRepo{})

	if err := m.ApplyMigration(ctx, id, &tenant.Migration{}); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyPending(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(roles.granted, []uuid.UUID{id, id}) {
		t.Errorf("granted = %v, want the tenant after each run", roles.granted)
	}
}

func TestGrantingMigrationsDoNotGrantAfterAFailedRun(t *testing.T) {
	roles := &grantLog{}
	boom := errors.New("migration failed")
	m := NewGrantingMigrationManager(&fakeMigrations{err: boom}, roles, fakeRepo{})
	if err := m.ApplyPending(context.Background(), uuid.New()); !errors.Is(err, boom) {
		t.Fatalf("ApplyPending = %v, want the migration error", err)
	}
	if len(roles.granted) != 0 {
		t.Errorf("granted after a failed run: %v", roles.granted)
	}
}

// Across all tenants, grants run for every tenant even when the migration
// failed for some, and pagination reaches past the first page.
func TestGrantingMigrationsGrantEveryTenantAcrossPages(t *testing.T) {
	var ts []*tenant.Tenant
	for i := 0; i < 150; i++ {
		ts = append(ts, &tenant.Tenant{ID: uuid.New()})
	}
	roles := &grantLog{}
	boom := errors.New("one tenant failed")
	m := NewGrantingMigrationManager(&fakeMigrations{err: boom}, roles, fakeRepo{tenants: ts})

	if err := m.ApplyPendingToAllTenants(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("ApplyPendingToAllTenants = %v, want the migration error joined in", err)
	}
	if len(roles.granted) != len(ts) {
		t.Errorf("granted %d tenants, want %d", len(roles.granted), len(ts))
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./database/ -run 'TestRoleHook|TestGrantingMigrations' -v`
Expected: FAIL — `undefined: NewRoleHook`, `undefined: NewGrantingMigrationManager`.

- [ ] **Step 4: Create `database/role_hook.go`**

```go
package database

import (
	"context"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// RoleHook keeps each tenant's role in step with its registry record.
//
// Every lifecycle event calls Ensure rather than a narrower step, because
// Ensure is idempotent and order-independent: ProvisionTenant fires
// OnTenantStatusChanged before OnTenantProvisioned, so a hook that created the
// role in one event and toggled LOGIN in the other would fail on every first
// provisioning.
type RoleHook struct {
	tenant.BaseHook
	roles   TenantRoles
	schemas tenant.SchemaManager
	evict   func(uuid.UUID)
	logger  *zap.Logger
}

// NewRoleHook returns the isolation hook. evict closes a tenant's warm
// connection pool in this process.
func NewRoleHook(roles TenantRoles, schemas tenant.SchemaManager, evict func(uuid.UUID), logger *zap.Logger) *RoleHook {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RoleHook{roles: roles, schemas: schemas, evict: evict, logger: logger.Named("role_hook")}
}

// Name implements tenant.Hook.
func (h *RoleHook) Name() string { return "role_isolation" }

// OnTenantProvisioned ensures the new tenant's role.
func (h *RoleHook) OnTenantProvisioned(ctx context.Context, t *tenant.Tenant) error {
	return h.roles.Ensure(ctx, t)
}

// OnTenantStatusChanged ensures the role for the new status, and for a
// suspended or cancelled tenant also ends its sessions and closes its pool.
// A tenant with no schema has not been provisioned, so there is nothing to do.
func (h *RoleHook) OnTenantStatusChanged(ctx context.Context, t *tenant.Tenant, _ string) error {
	provisioned, err := h.schemas.SchemaExists(ctx, t.ID)
	if err != nil {
		return err
	}
	if !provisioned {
		return nil
	}
	if err := h.roles.Ensure(ctx, t); err != nil {
		return err
	}
	if t.Status == tenant.StatusSuspended || t.Status == tenant.StatusCancelled {
		if err := h.roles.LockOut(ctx, t.ID); err != nil {
			return err
		}
		h.evict(t.ID)
	}
	return nil
}

// OnTenantDeleted closes the tenant's pool and drops its role.
func (h *RoleHook) OnTenantDeleted(ctx context.Context, t *tenant.Tenant) error {
	h.evict(t.ID)
	return h.roles.Drop(ctx, t.ID)
}
```

- [ ] **Step 5: Create `database/role_migrations.go`**

```go
package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

// grantingMigrations is a MigrationManager that reapplies tenant role grants
// after every migration run. ALTER DEFAULT PRIVILEGES covers tables the admin
// role creates; this covers tables created any other way, provided the admin
// role owns them or is a member of the role that does.
type grantingMigrations struct {
	tenant.MigrationManager
	roles TenantRoles
	repo  tenant.Repository
}

// NewGrantingMigrationManager wraps inner so that each migration run is
// followed by Grant for the tenants it touched.
func NewGrantingMigrationManager(inner tenant.MigrationManager, roles TenantRoles, repo tenant.Repository) tenant.MigrationManager {
	return &grantingMigrations{MigrationManager: inner, roles: roles, repo: repo}
}

func (g *grantingMigrations) ApplyMigration(ctx context.Context, tenantID uuid.UUID, m *tenant.Migration) error {
	if err := g.MigrationManager.ApplyMigration(ctx, tenantID, m); err != nil {
		return err
	}
	return g.roles.Grant(ctx, tenantID)
}

func (g *grantingMigrations) ApplyPending(ctx context.Context, tenantID uuid.UUID) error {
	if err := g.MigrationManager.ApplyPending(ctx, tenantID); err != nil {
		return err
	}
	return g.roles.Grant(ctx, tenantID)
}

func (g *grantingMigrations) ApplyToAllTenants(ctx context.Context, m *tenant.Migration) error {
	return errors.Join(g.MigrationManager.ApplyToAllTenants(ctx, m), g.grantAll(ctx))
}

func (g *grantingMigrations) ApplyPendingToAllTenants(ctx context.Context) error {
	return errors.Join(g.MigrationManager.ApplyPendingToAllTenants(ctx), g.grantAll(ctx))
}

// grantAll grants for every tenant, even after a partly failed run, since
// the tenants it succeeded for have new tables.
func (g *grantingMigrations) grantAll(ctx context.Context) error {
	return forEachTenant(ctx, g.repo, func(t *tenant.Tenant) error {
		if err := g.roles.Grant(ctx, t.ID); err != nil {
			return fmt.Errorf("tenant %s: %w", t.ID, err)
		}
		return nil
	})
}

const listPageSize = 100

// ForEachProvisionedTenant calls fn for every tenant whose schema exists,
// which is every tenant that has been provisioned. Cancelled tenants are not
// listed by the repository and so are not visited. Errors are collected
// rather than stopping at the first.
func ForEachProvisionedTenant(ctx context.Context, repo tenant.Repository, schemas tenant.SchemaManager, fn func(*tenant.Tenant) error) error {
	return forEachTenant(ctx, repo, func(t *tenant.Tenant) error {
		provisioned, err := schemas.SchemaExists(ctx, t.ID)
		if err != nil || !provisioned {
			return err
		}
		return fn(t)
	})
}

// forEachTenant calls fn for every tenant the repository lists, page by page,
// collecting errors rather than stopping at the first.
func forEachTenant(ctx context.Context, repo tenant.Repository, fn func(*tenant.Tenant) error) error {
	var errs []error
	for page, seen := 1, 0; ; page++ {
		ts, total, err := repo.List(ctx, page, listPageSize)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, t := range ts {
			if err := fn(t); err != nil {
				errs = append(errs, err)
			}
		}
		seen += len(ts)
		if len(ts) == 0 || seen >= total {
			return errors.Join(errs...)
		}
	}
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./database/ -run 'TestRoleHook|TestGrantingMigrations' -v`
Expected: PASS.

- [ ] **Step 7: Verify and commit**

Run: `gofmt -l . && go vet ./... && go test -short ./...`
Expected: no gofmt output; all PASS.

```bash
git add database/role_hook.go database/role_hook_test.go database/role_migrations.go database/role_migrations_test.go
git commit -m "feat(database): keep tenant roles in step with the lifecycle and migrations

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: PgBouncer auth file

**Files:**
- Create: `database/pgbouncer.go`, `database/pgbouncer_test.go`

**Interfaces:**
- Consumes: `ForEachProvisionedTenant` (Task 7); `tenant.CredentialSource` (Task 2).
- Produces: `func WriteAuthFile(ctx context.Context, w io.Writer, repo tenant.Repository, schemas tenant.SchemaManager, creds *tenant.CredentialSource) error`

- [ ] **Step 1: Write the failing test**

Create `database/pgbouncer_test.go`:

```go
package database

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

type provisionedSchemas struct {
	tenant.SchemaManager
	provisioned map[uuid.UUID]bool
}

func (p provisionedSchemas) SchemaExists(_ context.Context, id uuid.UUID) (bool, error) {
	return p.provisioned[id], nil
}

func roleName(id uuid.UUID) string { return "tenant_" + strings.ReplaceAll(id.String(), "-", "_") }

func TestWriteAuthFile(t *testing.T) {
	active, suspended, unprovisioned := uuid.New(), uuid.New(), uuid.New()
	repo := fakeRepo{tenants: []*tenant.Tenant{
		{ID: active, Status: tenant.StatusActive},
		{ID: suspended, Status: tenant.StatusSuspended},
		{ID: unprovisioned, Status: tenant.StatusActive},
	}}
	schemas := provisionedSchemas{provisioned: map[uuid.UUID]bool{active: true, suspended: true}}
	secret := []byte("0123456789abcdef0123456789abcdef")
	creds := tenant.NewCredentialSource(tenant.RoleIsolationConfig{Secret: secret}, roleName)

	var buf bytes.Buffer
	if err := WriteAuthFile(context.Background(), &buf, repo, schemas, creds); err != nil {
		t.Fatal(err)
	}
	want := `"` + roleName(active) + `" "` + tenant.DerivePassword(secret, active) + `"` + "\n"
	if buf.String() != want {
		t.Errorf("auth file =\n%s\nwant only the active, provisioned tenant, in plaintext:\n%s", buf.String(), want)
	}
}

func TestWriteAuthFileRejectsPasswordlessRoles(t *testing.T) {
	id := uuid.New()
	repo := fakeRepo{tenants: []*tenant.Tenant{{ID: id, Status: tenant.StatusActive}}}
	schemas := provisionedSchemas{provisioned: map[uuid.UUID]bool{id: true}}
	creds := tenant.NewCredentialSource(tenant.RoleIsolationConfig{
		Credentials: func(uuid.UUID) (string, error) { return "", nil },
	}, roleName)

	err := WriteAuthFile(context.Background(), &bytes.Buffer{}, repo, schemas, creds)
	if err == nil || !strings.Contains(err.Error(), roleName(id)) {
		t.Errorf("WriteAuthFile = %v, want an error naming the passwordless role", err)
	}
}

func TestAuthFileQuoting(t *testing.T) {
	if got := quoteAuthFile(`a"b`); got != `"a""b"` {
		t.Errorf("quoteAuthFile = %s, want embedded quotes doubled", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./database/ -run 'TestWriteAuthFile|TestAuthFileQuoting' -v`
Expected: FAIL — `undefined: WriteAuthFile`.

- [ ] **Step 3: Create `database/pgbouncer.go`**

```go
package database

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/alexalmadav/go-multitenant/tenant"
)

// WriteAuthFile writes PgBouncer auth_file entries for every active,
// provisioned tenant: one "<role>" "<password>" line each, sorted by role.
// Suspended and cancelled tenants are left out, so PgBouncer refuses them too.
//
// The entries are plaintext passwords, not SCRAM verifiers: PgBouncer 1.25.2
// locks a role out after a failed first login when given verifiers. The
// output is therefore a secret, as sensitive as the role secret itself. It
// contains only tenant entries; a deployment adds its own fixed entries, such
// as the admin user.
func WriteAuthFile(ctx context.Context, w io.Writer, repo tenant.Repository, schemas tenant.SchemaManager, creds *tenant.CredentialSource) error {
	var lines, passwordless []string
	err := ForEachProvisionedTenant(ctx, repo, schemas, func(t *tenant.Tenant) error {
		if t.Status != tenant.StatusActive {
			return nil
		}
		cred, err := creds.Current(t.ID)
		if err != nil {
			return err
		}
		if cred.Password == "" {
			passwordless = append(passwordless, cred.User)
			return nil
		}
		lines = append(lines, quoteAuthFile(cred.User)+" "+quoteAuthFile(cred.Password))
		return nil
	})
	if err != nil {
		return err
	}
	if len(passwordless) > 0 {
		return fmt.Errorf("database: %d tenant role(s) have no password and cannot authenticate through an auth_file: %s",
			len(passwordless), strings.Join(passwordless, ", "))
	}
	sort.Strings(lines)
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return err
		}
	}
	return nil
}

// quoteAuthFile quotes a value the way PgBouncer's auth_file expects, with
// embedded double quotes doubled.
func quoteAuthFile(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
```

- [ ] **Step 4: Run the tests, verify, and commit**

Run: `go test ./database/ -run 'TestWriteAuthFile|TestAuthFileQuoting' -v && gofmt -l . && go vet ./...`
Expected: PASS; no gofmt output.

```bash
git add database/pgbouncer.go database/pgbouncer_test.go
git commit -m "feat(database): render a PgBouncer auth_file for tenant roles

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Wiring role mode into `multitenant.New`

**Files:**
- Create: `role_isolation.go`, `role_isolation_test.go`
- Modify: `multitenant.go` (`MultiTenant` struct, `New`)
- Modify: `role_isolation_integration_test.go` (append), `cross_tenant_integration_test.go` (extract the scenario)

**Interfaces:**
- Consumes: everything from Tasks 1–8.
- Produces:
  - `var ErrRoleIsolationDisabled error`
  - `func (mt *MultiTenant) EnsureTenantRoles(ctx context.Context) error`
  - `func (mt *MultiTenant) RotateTenantCredentials(ctx context.Context) error`
  - `func (mt *MultiTenant) PgBouncerAuthFile(ctx context.Context, w io.Writer) error`
  - `func (mt *MultiTenant) TenantPoolStats() (tenant.PoolStats, bool)`
  - test helpers: `newRoleModeMT(t, secret []byte, previous ...[]byte) *MultiTenant`, `refreshPgBouncerAuth(t, mt *MultiTenant)`, `runCrossTenantScenario(t, cfg Config, afterProvision func(*MultiTenant), cleanup func(*sql.DB, []uuid.UUID))`

- [ ] **Step 1: Write the failing unit test**

Create `role_isolation_test.go`:

```go
package multitenant

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
)

// Role isolation config errors are caught before New touches the database.
func TestNew_RejectsInvalidRoleIsolationBeforeConnecting(t *testing.T) {
	for name, set := range map[string]func(*Config){
		"no TenantDSN": func(c *Config) {
			c.Database.Isolation = tenant.IsolationRole
			c.Database.RoleIsolation = tenant.RoleIsolationConfig{Secret: bytes.Repeat([]byte("s"), 32)}
		},
		"short secret": func(c *Config) {
			c.Database.Isolation = tenant.IsolationRole
			c.Database.RoleIsolation = tenant.RoleIsolationConfig{TenantDSN: "postgres://x", Secret: []byte("short")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := decisionConfig()
			cfg.InsecureSkipMembership = true
			set(&cfg)
			_, err := New(cfg)
			if !errors.Is(err, tenant.ErrInvalidRoleIsolation) {
				t.Fatalf("New() = %v, want ErrInvalidRoleIsolation", err)
			}
			if strings.Contains(err.Error(), "failed to setup database") {
				t.Errorf("New reached the database before rejecting the config: %v", err)
			}
		})
	}
}

func TestNew_RejectsUnknownIsolationMode(t *testing.T) {
	cfg := decisionConfig()
	cfg.InsecureSkipMembership = true
	cfg.Database.Isolation = "rolez"
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "rolez") {
		t.Errorf("New() = %v, want an error naming the unknown mode", err)
	}
}

func TestRoleIsolationMethodsWithoutRoleMode(t *testing.T) {
	mt := &MultiTenant{}
	ctx := context.Background()
	for name, err := range map[string]error{
		"EnsureTenantRoles":       mt.EnsureTenantRoles(ctx),
		"RotateTenantCredentials": mt.RotateTenantCredentials(ctx),
		"PgBouncerAuthFile":       mt.PgBouncerAuthFile(ctx, &bytes.Buffer{}),
	} {
		if !errors.Is(err, ErrRoleIsolationDisabled) {
			t.Errorf("%s = %v, want ErrRoleIsolationDisabled", name, err)
		}
	}
	if _, ok := mt.TenantPoolStats(); ok {
		t.Error("TenantPoolStats reported pools without role mode")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test . -run 'TestNew_RejectsInvalidRoleIsolation|TestNew_RejectsUnknownIsolationMode|TestRoleIsolationMethodsWithoutRoleMode' -v`
Expected: FAIL — `mt.EnsureTenantRoles undefined`, `undefined: ErrRoleIsolationDisabled`.

- [ ] **Step 3: Create `role_isolation.go`**

```go
package multitenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/alexalmadav/go-multitenant/database"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

// ErrRoleIsolationDisabled is returned by the role isolation methods when the
// Config did not select tenant.IsolationRole.
var ErrRoleIsolationDisabled = errors.New("multitenant: role isolation is not enabled")

// roleIsolation holds what role mode adds to a MultiTenant.
type roleIsolation struct {
	roles   *database.RoleManager
	creds   *tenant.CredentialSource
	pools   *tenant.Pools
	repo    tenant.Repository
	schemas tenant.SchemaManager
}

func setupRoleIsolation(ctx context.Context, cfg tenant.RoleIsolationConfig, db *sql.DB, schemas tenant.SchemaManager, repo tenant.Repository, logger *zap.Logger) (*roleIsolation, error) {
	creds := tenant.NewCredentialSource(cfg, schemas.GetSchemaName)
	roles := database.NewRoleManager(db, schemas, creds, logger)
	if err := roles.CheckPrerequisites(ctx); err != nil {
		return nil, err
	}
	open, err := tenantPoolOpener(cfg, creds, logger)
	if err != nil {
		return nil, err
	}
	pools, err := tenant.NewPools(tenant.PoolsConfig{
		MaxConns:          cfg.MaxConns,
		PerTenantMaxConns: cfg.PerTenantMaxConns,
		MaxWarmTenants:    cfg.MaxWarmTenants,
		IdleTimeout:       cfg.IdleTimeout,
	}, open, logger)
	if err != nil {
		return nil, err
	}
	return &roleIsolation{roles: roles, creds: creds, pools: pools, repo: repo, schemas: schemas}, nil
}

// tenantPoolOpener opens a tenant's pool at cfg.TenantDSN logged in as the
// tenant's role. It tries each candidate credential in turn, moving on only
// when the failure is an authentication failure, which is what makes
// rotation with PreviousSecrets work, behind PgBouncer as well as directly.
func tenantPoolOpener(cfg tenant.RoleIsolationConfig, creds *tenant.CredentialSource, logger *zap.Logger) (tenant.PoolOpener, error) {
	base, err := pgx.ParseConfig(cfg.TenantDSN)
	if err != nil {
		return nil, fmt.Errorf("multitenant: parse TenantDSN: %w", err)
	}
	return func(ctx context.Context, tenantID uuid.UUID) (*sql.DB, error) {
		candidates, err := creds.Candidates(tenantID)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for i, c := range candidates {
			connCfg := base.Copy()
			connCfg.User, connCfg.Password = c.User, c.Password
			connCfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
			db := stdlib.OpenDB(*connCfg)
			db.SetMaxOpenConns(cfg.PerTenantMaxConns)
			db.SetMaxIdleConns(1)
			db.SetConnMaxIdleTime(cfg.IdleTimeout)
			err := db.PingContext(ctx)
			if err == nil {
				if i > 0 {
					logger.Warn("A tenant role authenticated with a previous secret; run RotateTenantCredentials",
						zap.String("role", c.User), zap.Int("previous_secret", i))
				}
				return db, nil
			}
			db.Close()
			lastErr = err
			if !tenant.IsAuthFailure(err) {
				break
			}
		}
		return nil, fmt.Errorf("multitenant: tenant role %s could not log in at TenantDSN; check that EnsureTenantRoles has run, "+
			"that Secret matches, and, behind PgBouncer with an auth_file, that the file is current: %w", candidates[0].User, lastErr)
	}, nil
}

// EnsureTenantRoles brings every provisioned tenant's role to the state its
// registry record calls for, creating missing roles. Run it once before
// switching a deployment to role isolation; it is also the repair tool.
// Tenants created but not yet provisioned, and cancelled tenants, are skipped.
func (mt *MultiTenant) EnsureTenantRoles(ctx context.Context) error {
	ri := mt.roleIsolation
	if ri == nil {
		return ErrRoleIsolationDisabled
	}
	return database.ForEachProvisionedTenant(ctx, ri.repo, ri.schemas, func(t *tenant.Tenant) error {
		if err := ri.roles.Ensure(ctx, t); err != nil {
			return fmt.Errorf("tenant %s: %w", t.ID, err)
		}
		return nil
	})
}

// RotateTenantCredentials re-keys every provisioned tenant's role to the
// current Secret. Deploy the new Secret with the old one in PreviousSecrets
// first, so connections keep authenticating throughout; with a PgBouncer
// auth_file, regenerate it and reload PgBouncer immediately afterwards.
func (mt *MultiTenant) RotateTenantCredentials(ctx context.Context) error {
	return mt.EnsureTenantRoles(ctx)
}

// PgBouncerAuthFile writes PgBouncer auth_file entries for every active,
// provisioned tenant. The output holds working credentials; treat it as a
// secret. See database.WriteAuthFile.
func (mt *MultiTenant) PgBouncerAuthFile(ctx context.Context, w io.Writer) error {
	ri := mt.roleIsolation
	if ri == nil {
		return ErrRoleIsolationDisabled
	}
	return database.WriteAuthFile(ctx, w, ri.repo, ri.schemas, ri.creds)
}

// TenantPoolStats reports the tenant connection pools. The second result is
// false when role isolation is not enabled.
func (mt *MultiTenant) TenantPoolStats() (tenant.PoolStats, bool) {
	if mt.roleIsolation == nil {
		return tenant.PoolStats{}, false
	}
	return mt.roleIsolation.pools.Stats(), true
}
```

- [ ] **Step 4: Wire it into `New`**

In `multitenant.go`, add a field to `MultiTenant` after `Limits`:

```go
	roleIsolation *roleIsolation
```

In `New`, immediately after the membership `switch` block, add:

```go
	switch config.Database.Isolation {
	case tenant.IsolationSearchPath:
	case tenant.IsolationRole:
		if err := config.Database.RoleIsolation.Validate(config.Database.SchemaPrefix); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("multitenant: unknown Database.Isolation %q", config.Database.Isolation)
	}
```

Replace the three statements from `migrationMgr := database.NewMigrationManager(...)` through `manager := tenant.NewManager(...)` with:

```go
	migrationMgr := database.NewMigrationManager(db, logger, config.Database.MigrationsDir, schemaManager, repository)

	// Role isolation wraps the migration manager, so every run re-grants the
	// tenant roles, and the manager, so tenant connections log in as those
	// roles. The isolation hook is registered first, before any application
	// hook, so an application's own provisioning hook can already use tenant
	// connections.
	var ri *roleIsolation
	if config.Database.Isolation == tenant.IsolationRole {
		ri, err = setupRoleIsolation(context.Background(), config.Database.RoleIsolation.WithDefaults(), db, schemaManager, repository, logger)
		if err != nil {
			db.Close()
			return nil, err
		}
		migrationMgr = database.NewGrantingMigrationManager(migrationMgr, ri.roles, repository)
	}

	manager := tenant.NewManager(config.Config, db, repository, schemaManager, migrationMgr, logger)
	if ri != nil {
		manager.RegisterHook(database.NewRoleHook(ri.roles, schemaManager, ri.pools.Evict, logger))
		manager = tenant.NewRoleIsolatedManager(manager, ri.pools, schemaManager.GetSchemaName, logger)
	}
```

and in the returned `&MultiTenant{...}` literal add `roleIsolation: ri,`.

- [ ] **Step 5: Run the unit tests**

Run: `go test . -run 'TestNew_RejectsInvalidRoleIsolation|TestNew_RejectsUnknownIsolationMode|TestRoleIsolationMethodsWithoutRoleMode' -v && go test -short ./...`
Expected: PASS.

- [ ] **Step 6: Write the lifecycle integration tests**

Append to `role_isolation_integration_test.go`, and add `"time"` to its imports.

Behind PgBouncer, a failed *server* login — a suspended role, or one that was dropped — makes PgBouncer refuse that role for `server_login_retry` seconds (15 by default; the CI config sets 1). So the tests that bring a role back retry the login for a few seconds through `eventually`, rather than assuming it works at once:

```go
// newRoleModeMT returns a MultiTenant in role mode against the test database,
// with tenant connections going to roleTestTenantDSN.
func newRoleModeMT(t *testing.T, secret []byte, previous ...[]byte) *MultiTenant {
	t.Helper()
	cfg := testConfig(getTestDatabaseURL())
	cfg.Database.Isolation = tenant.IsolationRole
	cfg.Database.RoleIsolation = tenant.RoleIsolationConfig{
		TenantDSN:         roleTestTenantDSN(),
		Secret:            secret,
		PreviousSecrets:   previous,
		MaxConns:          10,
		PerTenantMaxConns: 3,
		MaxWarmTenants:    20,
		IdleTimeout:       time.Minute,
	}
	mt, err := New(cfg)
	if err != nil {
		t.Fatalf("New in role mode: %v", err)
	}
	t.Cleanup(func() { mt.Close() })
	return mt
}

// refreshPgBouncerAuth regenerates PgBouncer's auth_file and reloads it, in
// the CI job that runs PgBouncer with one. Everywhere else it does nothing.
func refreshPgBouncerAuth(t *testing.T, mt *MultiTenant) {
	t.Helper()
	path, admin := os.Getenv("PGBOUNCER_AUTH_FILE"), os.Getenv("PGBOUNCER_ADMIN_DSN")
	if path == "" {
		return
	}
	var buf bytes.Buffer
	if base := os.Getenv("PGBOUNCER_AUTH_FILE_BASE"); base != "" {
		fixed, err := os.ReadFile(base)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(fixed)
	}
	if err := mt.PgBouncerAuthFile(context.Background(), &buf); err != nil {
		t.Fatalf("PgBouncerAuthFile: %v", err)
	}
	// 0644 rather than 0600 only because PgBouncer runs as another user in the
	// CI container. A production auth_file holds live credentials: 0600.
	if err := os.WriteFile(path+".tmp", buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(admin)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect to PgBouncer's admin console: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), "RELOAD"); err != nil {
		t.Fatalf("RELOAD: %v", err)
	}
}

func provisionForRoleTest(t *testing.T, mt *MultiTenant, subdomain string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: subdomain, Subdomain: subdomain}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("ProvisionTenant in role mode: %v", err)
	}
	refreshPgBouncerAuth(t, mt)
	return id
}

func currentUser(t *testing.T, mt *MultiTenant, id uuid.UUID) (string, error) {
	t.Helper()
	conn, err := mt.Manager.GetTenantConn(context.Background(), id)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	var user string
	err = conn.QueryRowContext(context.Background(), "SELECT current_user").Scan(&user)
	return user, err
}

// eventually retries fn for up to five seconds and returns its last error.
// It covers PgBouncer's server_login_retry backoff after a failed server
// login; against PostgreSQL directly the first attempt succeeds.
func eventually(fn func() error) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := fn()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// First provisioning must work in role mode, even though ProvisionTenant
// fires the status change before the provisioned event.
func TestIntegration_RoleIsolation_ProvisionAndConnect(t *testing.T) {
	db := setupTestDatabase(t)
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-provision")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	user, err := currentUser(t, mt, id)
	if err != nil {
		t.Fatalf("tenant connection: %v", err)
	}
	if user != testRoleName(id) {
		t.Errorf("current_user = %q, want the tenant's role %q", user, testRoleName(id))
	}
	if s, ok := mt.TenantPoolStats(); !ok || s.ColdOpens != 1 {
		t.Errorf("TenantPoolStats = %+v, %v; want one cold open", s, ok)
	}
}

// A table created by another role, outside the admin's default privileges,
// is granted by the next migration run.
func TestIntegration_RoleIsolation_MigrationRunGrantsOtherOwnersTables(t *testing.T) {
	db := setupTestDatabase(t)
	ctx := context.Background()
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-migrations")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	schema := `"` + testRoleName(id) + `"`
	admin, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"DROP ROLE IF EXISTS role_test_migrator",
		"CREATE ROLE role_test_migrator NOLOGIN",
		"GRANT USAGE, CREATE ON SCHEMA " + schema + " TO role_test_migrator",
		"SET ROLE role_test_migrator",
		"CREATE TABLE " + schema + ".gadgets (id int)",
		"RESET ROLE",
	} {
		if _, err := admin.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	admin.Close()
	defer db.Exec("DROP TABLE IF EXISTS " + schema + ".gadgets; DROP OWNED BY role_test_migrator; DROP ROLE IF EXISTS role_test_migrator")

	query := func() error {
		conn, err := mt.Manager.GetTenantConn(ctx, id)
		if err != nil {
			return err
		}
		defer conn.Close()
		var n int
		return conn.QueryRowContext(ctx, "SELECT count(*) FROM gadgets").Scan(&n)
	}
	wantSQLState(t, "before a migration run", query(), "42501")

	if err := mt.Migrations.ApplyMigration(ctx, id, &tenant.Migration{Version: "901", Name: "noop", SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	if err := query(); err != nil {
		t.Errorf("after a migration run: %v", err)
	}
}

func TestIntegration_RoleIsolation_SuspendLocksOut(t *testing.T) {
	db := setupTestDatabase(t)
	ctx := context.Background()
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-suspend")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})
	cred := tenant.RoleCredentials{User: testRoleName(id), Password: tenant.DerivePassword(testRoleSecret, id)}

	session, err := connectAsRole(t, roleTestTenantDSN(), cred)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(ctx)

	if err := mt.Manager.SuspendTenant(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Exec(ctx, "SELECT 1"); err == nil {
		t.Error("an open session kept working after its tenant was suspended")
	}
	if c, err := connectAsRole(t, roleTestTenantDSN(), cred); err == nil {
		c.Close(ctx)
		t.Error("a suspended tenant's role could still log in")
	}

	if err := mt.Manager.ActivateTenant(ctx, id); err != nil {
		t.Fatal(err)
	}
	refreshPgBouncerAuth(t, mt)
	if err := eventually(func() error {
		c, err := connectAsRole(t, roleTestTenantDSN(), cred)
		if err == nil {
			c.Close(ctx)
		}
		return err
	}); err != nil {
		t.Fatalf("login after reactivation: %v", err)
	}
}

func TestIntegration_RoleIsolation_DeleteDropsRole(t *testing.T) {
	db := setupTestDatabase(t)
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-delete")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	if err := mt.Manager.DeleteTenant(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", testRoleName(id)).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("the tenant's role survived DeleteTenant")
	}
}

func TestIntegration_RoleIsolation_Rotation(t *testing.T) {
	db := setupTestDatabase(t)
	oldSecret := bytes.Repeat([]byte("o"), tenant.MinRoleSecretLen)
	newSecret := bytes.Repeat([]byte("n"), tenant.MinRoleSecretLen)
	mtOld := newRoleModeMT(t, oldSecret)
	id := provisionForRoleTest(t, mtOld, "role-rotate")
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	// Step 1-2: the new key first, the old as a fallback. Behind PgBouncer the
	// first attempt fails with 08P01, which must trigger the fallback.
	mtBoth := newRoleModeMT(t, newSecret, oldSecret)
	if _, err := currentUser(t, mtBoth, id); err != nil {
		t.Fatalf("before rotation, with the old secret as fallback: %v", err)
	}

	// Step 3-4: re-key, then refresh PgBouncer's auth file.
	if err := mtBoth.RotateTenantCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshPgBouncerAuth(t, mtBoth)

	// Step 5: only the new key.
	if _, err := currentUser(t, newRoleModeMT(t, newSecret), id); err != nil {
		t.Errorf("after rotation, with only the new secret: %v", err)
	}
	if _, err := currentUser(t, newRoleModeMT(t, oldSecret), id); err == nil {
		t.Error("after rotation, the old secret alone still logged in")
	}
}

func TestIntegration_RoleIsolation_EnsureRepairsAndSkips(t *testing.T) {
	db := setupTestDatabase(t)
	ctx := context.Background()
	mt := newRoleModeMT(t, testRoleSecret)
	repaired := provisionForRoleTest(t, mt, "role-repair")
	unprovisioned := uuid.New()
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: unprovisioned, Name: "role-skip", Subdomain: "role-skip"}); err != nil {
		t.Fatal(err)
	}
	defer cleanupTestData(db, []uuid.UUID{repaired, unprovisioned})
	defer dropTestRoles(db, []uuid.UUID{repaired, unprovisioned})

	dropTestRoles(db, []uuid.UUID{repaired})
	if _, err := currentUser(t, newRoleModeMT(t, testRoleSecret), repaired); err == nil {
		t.Fatal("a tenant whose role was dropped could still connect")
	}

	if err := mt.EnsureTenantRoles(ctx); err != nil {
		t.Fatalf("EnsureTenantRoles: %v", err)
	}
	refreshPgBouncerAuth(t, mt)
	fresh := newRoleModeMT(t, testRoleSecret)
	if err := eventually(func() error { _, err := currentUser(t, fresh, repaired); return err }); err != nil {
		t.Errorf("after repair: %v", err)
	}
	var exists bool
	if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", testRoleName(unprovisioned)).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("EnsureTenantRoles created a role for an unprovisioned tenant")
	}
}
```

- [ ] **Step 7: Run the cross-tenant scenario in both modes**

In `cross_tenant_integration_test.go`:

1. Rename `TestIntegration_CrossTenantAccessIsRefused` to `runCrossTenantScenario(t *testing.T, cfg Config, afterProvision func(*MultiTenant), cleanupRoles func(*sql.DB, []uuid.UUID))`, and inside it replace `mt, err := New(testConfig(getTestDatabaseURL()))` with `mt, err := New(cfg)`.
2. Immediately after the provisioning loop, add:
```go
	if afterProvision != nil {
		afterProvision(mt)
	}
```
3. Immediately after `defer cleanupTestData(db, []uuid.UUID{acmeID, globexID, initechID})`, add:
```go
	if cleanupRoles != nil {
		defer cleanupRoles(db, []uuid.UUID{acmeID, globexID, initechID})
	}
```
4. Add `"database/sql"` to the file's imports, and add the two tests:
```go
func TestIntegration_CrossTenantAccessIsRefused(t *testing.T) {
	runCrossTenantScenario(t, testConfig(getTestDatabaseURL()), nil, nil)
}

// The same scenario with every tenant connection logged in as the tenant's
// own role: the refusals and the served rows must be identical.
func TestIntegration_RoleIsolation_CrossTenantAccessIsRefused(t *testing.T) {
	cfg := testConfig(getTestDatabaseURL())
	cfg.Database.Isolation = tenant.IsolationRole
	cfg.Database.RoleIsolation = tenant.RoleIsolationConfig{TenantDSN: roleTestTenantDSN(), Secret: testRoleSecret}
	runCrossTenantScenario(t, cfg, func(mt *MultiTenant) { refreshPgBouncerAuth(t, mt) }, dropTestRoles)
}
```

- [ ] **Step 8: Run the integration suite**

Run: `go test . -run 'RoleIsolation|CrossTenant' -v -count=1`
Expected: every role-isolation test and both cross-tenant tests PASS.

- [ ] **Step 9: Mutation-check that provisioning really creates the role**

The event-order logic is covered by Task 7's unit tests. This check shows the integration test notices a role that was never created. In `database/role_hook.go`, temporarily make `OnTenantProvisioned` return `nil`, and delete the `h.roles.Ensure` call from `OnTenantStatusChanged`.
Run: `go test . -run TestIntegration_RoleIsolation_ProvisionAndConnect -count=1`
Expected: FAIL — the tenant connection cannot log in. Revert both changes and confirm it passes.

- [ ] **Step 10: Verify and commit**

Run: `gofmt -l . && go vet ./... && go test ./... -count=1 && (cd middleware/gin && go test ./... -count=1) && (cd examples && go build ./...)`
Expected: no gofmt output; all PASS.

```bash
git add role_isolation.go role_isolation_test.go multitenant.go role_isolation_integration_test.go cross_tenant_integration_test.go
git commit -m "feat: wire role isolation into multitenant.New

In role mode New checks the database prerequisites, wraps the migration
manager and the manager, and registers the isolation hook before any
application hook. EnsureTenantRoles, RotateTenantCredentials,
PgBouncerAuthFile and TenantPoolStats go on MultiTenant.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Role isolation through PgBouncer in CI

**Files:**
- Create: `scripts/ci/pgbouncer-role-setup.sh`
- Modify: `.github/workflows/go.yml`
- Modify: `role_isolation_integration_test.go` (append the lockout guard)

**Interfaces:**
- Consumes: `newRoleModeMT`, `provisionForRoleTest`, `connectAsRole`, `refreshPgBouncerAuth`, `roleTestTenantDSN` (Task 9).
- Produces: CI jobs `role-pgbouncer (auth_query)` and `role-pgbouncer (auth_file)`; env vars `ROLE_TEST_TENANT_DSN`, `PGBOUNCER_MODE`, `PGBOUNCER_AUTH_FILE`, `PGBOUNCER_AUTH_FILE_BASE`, `PGBOUNCER_ADMIN_DSN`.

- [ ] **Step 1: Add the lockout guard test**

Append to `role_isolation_integration_test.go`:

```go
// TestIntegration_RoleIsolation_PgBouncerLockoutGuard runs only in the CI
// jobs with PgBouncer. A role whose first login since PgBouncer started is a
// failure must still log in with the right password afterwards. PgBouncer
// 1.25.2 breaks this when its auth_file holds SCRAM verifiers, so this test
// fails if the auth_file is ever switched to verifiers.
func TestIntegration_RoleIsolation_PgBouncerLockoutGuard(t *testing.T) {
	if os.Getenv("PGBOUNCER_MODE") == "" {
		t.Skip("runs only through PgBouncer; set PGBOUNCER_MODE")
	}
	db := setupTestDatabase(t)
	mt := newRoleModeMT(t, testRoleSecret)
	id := provisionForRoleTest(t, mt, "role-lockout-guard") // a role PgBouncer has never seen
	defer cleanupTestData(db, []uuid.UUID{id})
	defer dropTestRoles(db, []uuid.UUID{id})

	wrong := tenant.RoleCredentials{User: testRoleName(id), Password: "not-the-password"}
	if c, err := connectAsRole(t, roleTestTenantDSN(), wrong); err == nil {
		c.Close(context.Background())
		t.Fatal("PgBouncer accepted a wrong password")
	}

	right := tenant.RoleCredentials{User: testRoleName(id), Password: tenant.DerivePassword(testRoleSecret, id)}
	c, err := connectAsRole(t, roleTestTenantDSN(), right)
	if err != nil {
		t.Fatalf("after one failed first login, the correct password was refused: %v", err)
	}
	c.Close(context.Background())
}
```

- [ ] **Step 2: Create `scripts/ci/pgbouncer-role-setup.sh`**

```bash
#!/usr/bin/env bash
# Starts PgBouncer on 127.0.0.1:6433 for the role isolation integration
# tests, in one of the two authentication modes the library supports:
#   auth_query  PgBouncer looks up each role's SCRAM verifier in PostgreSQL.
#   auth_file   the tests render the auth_file and reload PgBouncer.
# CI only: it expects PostgreSQL on 127.0.0.1:5432 as postgres/postgres.
set -euo pipefail

mode="${1:?usage: pgbouncer-role-setup.sh auth_query|auth_file}"
dir="${RUNNER_TEMP:-/tmp}/pgb"
mkdir -p "$dir"
export PGPASSWORD=postgres

command -v psql >/dev/null || { sudo apt-get update -q && sudo apt-get install -yq postgresql-client; }

# Fixed entries every mode needs: the admin console user.
printf '"postgres" "postgres"\n' > "$dir/userlist.base"

auth_lines=""
if [ "$mode" = auth_query ]; then
  psql -h 127.0.0.1 -U postgres -d test_multitenant -v ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE pgbouncer_auth LOGIN PASSWORD 'pgbouncer_auth';
CREATE FUNCTION public.pgbouncer_get_auth(p_usename text)
RETURNS TABLE (usename name, passwd text)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog AS
$$ SELECT usename, passwd FROM pg_catalog.pg_shadow WHERE usename = p_usename $$;
REVOKE ALL ON FUNCTION public.pgbouncer_get_auth(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.pgbouncer_get_auth(text) TO pgbouncer_auth;
SQL
  # PgBouncer logs in to PostgreSQL as auth_user itself, and cannot do that
  # from a verifier, so this entry must be plaintext.
  printf '"pgbouncer_auth" "pgbouncer_auth"\n' >> "$dir/userlist.base"
  auth_lines=$'auth_user = pgbouncer_auth\nauth_query = SELECT usename, passwd FROM public.pgbouncer_get_auth($1)'
elif [ "$mode" != auth_file ]; then
  echo "unknown mode: $mode" >&2
  exit 2
fi

cp "$dir/userlist.base" "$dir/userlist.txt"
cat > "$dir/pgbouncer.ini" <<EOF
[databases]
test_multitenant = host=127.0.0.1 port=5432 dbname=test_multitenant

[pgbouncer]
listen_addr = 127.0.0.1
listen_port = 6433
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/userlist.txt
$auth_lines
admin_users = postgres
pool_mode = transaction
max_client_conn = 2000
default_pool_size = 5
max_db_connections = 40
; A failed server login - a suspended or dropped role - makes PgBouncer refuse
; that role for this many seconds. The default of 15 would outlast the tests'
; retries after reactivating or repairing a role.
server_login_retry = 1
ignore_startup_parameters = extra_float_digits
EOF
chmod 644 "$dir"/*

# PgBouncer needs a descriptor per client and server connection; container
# defaults of 1,024 are too low for per-tenant pools.
docker run -d --name pgbouncer-role --network host --ulimit nofile=65536:65536 \
  -v "$dir:/etc/pgbouncer" edoburu/pgbouncer:latest >/dev/null

for _ in $(seq 1 30); do
  (echo > /dev/tcp/127.0.0.1/6433) >/dev/null 2>&1 && exit 0
  sleep 1
done
docker logs pgbouncer-role
echo "pgbouncer did not open port 6433" >&2
exit 1
```

Make it executable: `chmod +x scripts/ci/pgbouncer-role-setup.sh`.

- [ ] **Step 3: Keep the existing PgBouncer job off the role tests**

The existing `pgbouncer` job configures PgBouncer from environment variables for the `postgres` user only, so tenant roles cannot log in through it. In `.github/workflows/go.yml`, change its last step's command from `go test -count=1 -timeout 240s .` to:

```yaml
        run: go test -count=1 -timeout 240s -skip 'RoleIsolation' .
```

- [ ] **Step 4: Add the role isolation jobs**

Append to `.github/workflows/go.yml`, under `jobs:`:

```yaml
  role-pgbouncer:
    name: Role isolation through PgBouncer (${{ matrix.mode }})
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        mode: [ auth_query, auth_file ]
    services:
      postgres:
        image: postgres:16-alpine
        env:
          POSTGRES_USER: postgres
          POSTGRES_PASSWORD: postgres
          POSTGRES_DB: test_multitenant
        ports:
          - 5432:5432
        options: >-
          --health-cmd "pg_isready -U postgres"
          --health-interval 5s
          --health-timeout 5s
          --health-retries 10
    env:
      # The admin connection goes straight to PostgreSQL; tenant roles log in
      # through PgBouncer.
      TEST_DATABASE_URL: postgres://postgres:postgres@localhost:5432/test_multitenant?sslmode=disable
      ROLE_TEST_TENANT_DSN: postgres://postgres@127.0.0.1:6433/test_multitenant?sslmode=disable
      PGBOUNCER_MODE: ${{ matrix.mode }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Start PgBouncer (${{ matrix.mode }})
        run: scripts/ci/pgbouncer-role-setup.sh ${{ matrix.mode }}
      - name: Point the tests at the auth file
        if: matrix.mode == 'auth_file'
        run: |
          echo "PGBOUNCER_AUTH_FILE=$RUNNER_TEMP/pgb/userlist.txt" >> "$GITHUB_ENV"
          echo "PGBOUNCER_AUTH_FILE_BASE=$RUNNER_TEMP/pgb/userlist.base" >> "$GITHUB_ENV"
          echo "PGBOUNCER_ADMIN_DSN=postgres://postgres:postgres@127.0.0.1:6433/pgbouncer?sslmode=disable" >> "$GITHUB_ENV"
      - name: Role isolation tests through PgBouncer
        run: go test -count=1 -timeout 300s -run 'RoleIsolation' .
      - name: PgBouncer log
        if: failure()
        run: docker logs pgbouncer-role
```

- [ ] **Step 5: Run the script locally against Docker**

Run, from the repo root with Docker running:

```bash
docker run -d --name ci-pg -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=test_multitenant -p 5432:5432 postgres:16-alpine
sleep 5
RUNNER_TEMP="$PWD/.superpowers/ci-local" scripts/ci/pgbouncer-role-setup.sh auth_query
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/test_multitenant?sslmode=disable' \
ROLE_TEST_TENANT_DSN='postgres://postgres@127.0.0.1:6433/test_multitenant?sslmode=disable' \
PGBOUNCER_MODE=auth_query go test -count=1 -run 'RoleIsolation' .
docker rm -f pgbouncer-role ci-pg
```

Expected: PASS, including `TestIntegration_RoleIsolation_PgBouncerLockoutGuard` and the rotation test's `08P01` fallback. On Docker Desktop (macOS), `--network host` does not reach the host's port 5432; if the script times out there, run this step on Linux or rely on CI, and note it in the report.

`.superpowers/` is git-ignored, so the local run leaves nothing to commit.

- [ ] **Step 6: Mutation-check the lockout guard (auth_file mode)**

This check shows the guard catches the regression it exists for. With the `auth_file` job's environment, temporarily change `WriteAuthFile` in `database/pgbouncer.go` to emit verifiers:

```go
		verifier, err := tenant.NewSCRAMVerifier(cred.Password)
		if err != nil {
			return err
		}
		lines = append(lines, quoteAuthFile(cred.User)+" "+quoteAuthFile(verifier))
```

Run the auth_file variant as in Step 5 (setup script with `auth_file`, plus the three `PGBOUNCER_*` variables pointing at `$RUNNER_TEMP/pgb` and `127.0.0.1:6433`).
Expected: `TestIntegration_RoleIsolation_PgBouncerLockoutGuard` FAILS with "the correct password was refused". Revert and confirm it passes.

- [ ] **Step 7: Commit**

```bash
git add scripts/ci/pgbouncer-role-setup.sh .github/workflows/go.yml role_isolation_integration_test.go
git commit -m "ci: run role isolation through PgBouncer in both authentication modes

The auth_file run includes a guard test that fails if the auth_file ever
carries SCRAM verifiers, which PgBouncer 1.25.2 locks roles out with.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 8: Push the branch and confirm both new jobs pass in CI**

Run: `git push -u origin feat/role-isolation` then `gh run watch` on the resulting run.
Expected: all jobs green, including `Role isolation through PgBouncer (auth_query)` and `(auth_file)`. If either fails, read its "PgBouncer log" step before changing anything.

---

### Task 11: Documentation and spec corrections

**Files:**
- Modify: `README.md` (new section after "Connection poolers"; Access Control cross-reference)
- Modify: `docs/superpowers/specs/2026-09-22-role-isolation-design.md` (two corrections)

**Interfaces:**
- Consumes: the names every earlier task produced.
- Produces: user-facing documentation.

- [ ] **Step 1: Add the README section**

In `README.md`, immediately before `## 📋 Tenant Management`, insert:

````markdown
### Role isolation

By default every tenant shares one database login and is scoped with
`SET LOCAL search_path`. That stops honest mistakes, but not a query that
names another tenant's schema — from a buggy handler or a SQL injection —
because the shared login can read every schema.

Role isolation gives every tenant its own PostgreSQL login role, with
privileges on its own schema only. A query that reaches another tenant's data
is then refused by PostgreSQL itself, with `permission denied`. It is opt-in:

```go
cfg.Database.Isolation = tenant.IsolationRole
cfg.Database.RoleIsolation = tenant.RoleIsolationConfig{
    TenantDSN: "postgres://pgbouncer.internal:6432/app", // user and password replaced per tenant
    Secret:    secret,                                  // at least 32 bytes, distinct per environment
}
```

Application code does not change: `GetTenantConn`, `WithTenantTx` and the
middleware hand out connections logged in as the tenant's role.

**Requirements.** PostgreSQL 15 or later — earlier versions let every role
create tables in `public`. The admin role in `Database.DSN` needs `CREATEROLE`
and membership in `pg_signal_backend`, which it uses to end a suspended
tenant's sessions. `New` checks all three at startup. Provisioning, rotation,
repair and migrations must all run as that same admin role, or as a role it
is a member of: PostgreSQL 16 lets a `CREATEROLE` role manage only the roles
it created, and a role can grant only on tables it owns.

**Enabling it on an existing deployment.** Deploy with role isolation
configured, then run `mt.EnsureTenantRoles(ctx)` once to create a role for
every provisioned tenant. It is idempotent and doubles as the repair tool.

**What each tenant role can do.** `SELECT`, `INSERT`, `UPDATE` and `DELETE`
on its own tables, and use its own sequences. It cannot read another tenant's
schema or `public.tenants`, run DDL, or `SET ROLE` to anyone. System catalogs
stay readable by every role, so a tenant can list other schema and role
names — which contain tenant ids — but never their data. Extensions that grant
functions to `PUBLIC` extend every tenant role too; audit what is installed.

**Suspension.** `ValidateTenant` still refuses a suspended tenant's requests
immediately, in every instance. Behind it, the tenant's role is set to
`NOLOGIN` and its sessions are ended, so the database refuses it too. Other
application instances drop their now-dead pool entries after `IdleTimeout`.

**Connection limits.** Each tenant gets a small pool, created on first use:

| Field | Default | Meaning |
|---|---|---|
| `MaxConns` | 50 | connections in use at once, all tenants |
| `PerTenantMaxConns` | 5 | connections in use at once, one tenant |
| `MaxWarmTenants` | 1,000 | tenant pools kept open |
| `IdleTimeout` | 5 min | an idle pool closes after this |

A request that cannot get a connection before its context ends is answered
`503 TENANT_DB_BUSY` with `Retry-After: 1`. The defaults assume a pooler.
**Pointed straight at PostgreSQL**, every warm pool holds a real backend, so
`MaxConns + MaxWarmTenants` must fit inside `max_connections` with the admin
pool — the defaults would not. `mt.TenantPoolStats()` reports hits, cold
opens, evictions and slot waits; a high share of cold opens means your active
tenants do not fit.

**Throughput.** A connection logged in as one tenant cannot serve another,
so when traffic spreads across many tenants at once, many transactions pay
for a fresh login. Measured on a laptop with 20 server connections, one
tenant ran about 48,000 transactions per second and 5,000 uniformly random
tenants about 550 through PgBouncer's `auth_query`, or 120 with an
`auth_file`. Real traffic concentrates on fewer tenants and real hardware is
faster, but this is the trade: role isolation spends peak throughput under
wide tenant spread on a boundary the database enforces.

#### Behind PgBouncer

Each tenant logs in to PgBouncer as its own role, so PgBouncer must know
thousands of credentials. Two ways work; prefer `auth_query`.

**`auth_query` — self-hosted PostgreSQL with a superuser.** PgBouncer fetches
each role's verifier on login, so new tenants work immediately. As a
superuser, create the lookup function:

```sql
CREATE ROLE pgbouncer_auth LOGIN PASSWORD '...';
CREATE FUNCTION public.pgbouncer_get_auth(p_usename text)
RETURNS TABLE (usename name, passwd text)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog AS
$$ SELECT usename, passwd FROM pg_catalog.pg_shadow WHERE usename = p_usename $$;
REVOKE ALL ON FUNCTION public.pgbouncer_get_auth(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.pgbouncer_get_auth(text) TO pgbouncer_auth;
```

```ini
auth_type  = scram-sha-256
auth_file  = /etc/pgbouncer/userlist.txt   ; holds "pgbouncer_auth" "<plaintext password>"
auth_user  = pgbouncer_auth
auth_query = SELECT usename, passwd FROM public.pgbouncer_get_auth($1)
```

The `auth_user` entry must be a plaintext password: PgBouncer logs in as that
user itself and cannot do so from a verifier.

**`auth_file` — managed PostgreSQL (RDS, Cloud SQL and others).** These grant
no superuser, so nothing can read verifiers. Render the file instead:

```go
f, _ := os.OpenFile(path+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
f.WriteString(`"admin" "..."` + "\n") // your fixed entries
err := mt.PgBouncerAuthFile(ctx, f)
f.Close()
os.Rename(path+".tmp", path)
// then run RELOAD on PgBouncer's admin console
```

- The file holds **plaintext derived passwords**, so it is a secret: `0600`,
  on a volume only PgBouncer and the renderer can read. Verifiers are not an
  option — PgBouncer 1.25.2 locks a role out after a failed first login when
  given verifiers in an auth file.
- **A new tenant cannot connect until the file is regenerated and PgBouncer
  reloaded.** Run the renderer as a small sidecar on an interval or after
  provisioning. The reload itself is fast — 2.6 ms for 5,000 entries.
- Plaintext entries make PgBouncer do SCRAM key derivation for every login,
  on its single thread; expect about 4.5× lower throughput under wide tenant
  spread than with `auth_query`, or run several PgBouncer processes with
  `so_reuseport`.

**Either mode:**

- Raise PgBouncer's file-descriptor limit to cover
  `MaxWarmTenants × application instances` plus its server connections.
  Container defaults are often 1,024, and running out shows up only as
  connection timeouts at the client, with `accept() failed: No file
  descriptors available` in PgBouncer's log.
- After a failed server login — a suspended tenant's role, say — PgBouncer
  refuses that role for `server_login_retry` seconds, 15 by default. A tenant
  you reactivate can therefore see errors for that long. Lower the setting if
  that matters.

#### Rotating the secret

1. Deploy with `Secret` set to the new key and `PreviousSecrets: [][]byte{old}`.
   A pool that fails to authenticate with the new key retries with the old.
2. Run `mt.RotateTenantCredentials(ctx)`.
3. With an `auth_file`, regenerate it and reload PgBouncer immediately.
4. Remove the old key from `PreviousSecrets`.

With `auth_query` or direct connections nothing is interrupted. With an
`auth_file`, between steps 2 and 3 a tenant that needs a new PgBouncer server
connection cannot get one, so keep that gap short and rotate in a quiet
period.

`RoleIsolationConfig.Credentials` replaces derived passwords with your own —
from a secrets manager, say. Returning an empty password leaves
authentication to `pg_hba.conf`, for client certificates or cloud IAM; such
tenants cannot go through an `auth_file`.
````

- [ ] **Step 2: Cross-reference it from Access Control**

In `README.md`'s `### Access Control` section, append after the paragraph ending "…and add `RequireMembership()` to the chain; see the Gin section above.":

```markdown
Membership decides who may enter a tenant. What a tenant's connection can
reach once inside is a separate question, answered by the isolation mode;
see *Role isolation* for the mode in which PostgreSQL itself enforces it.
```

- [ ] **Step 3: Correct the spec**

In `docs/superpowers/specs/2026-09-22-role-isolation-design.md`:

1. In the `RoleIsolationConfig` code block, on the `Credentials` field line,
   change the hook's results from `(user, password string, err error)` to
   `(password string, err error)`. Then, after the paragraph that follows the
   code block (the one beginning "The defaults assume `TenantDSN` points at a
   connection pooler"), add:

   > The `Credentials` hook returns a password only. The role name is always the
   > tenant's schema name, because grants, lockout and the auth file all address
   > the role by it; a hook-chosen name would have to be consulted by every one
   > of them.

2. In *Migrations*, replace the sentence ending "…after every `ApplyMigration`, `ApplyPending`, `ApplyToAllTenants` and `ApplyPendingToAllTenants`, whoever created the tables." with:

   > The granting-migrations decorator reapplies the two backfill grants for each
   > tenant after every `ApplyMigration`, `ApplyPending`, `ApplyToAllTenants` and
   > `ApplyPendingToAllTenants`. A role can grant only on tables it owns or whose
   > owner it is a member of, so this covers tables created by another user only
   > when the admin role is a member of that user's role; migrations run by an
   > unrelated user remain unsupported, and the README says so.

- [ ] **Step 4: Check the README renders and commit**

Run: `python3 -c "import sys; s=open('README.md').read().splitlines(); print(sum(l.startswith('\`\`\`') for l in s) % 2 == 0, sum(l.startswith('\`\`\`\`') for l in s))"`
Expected: `True 0` — fences balanced, no stray four-backtick lines.

```bash
git add README.md docs/superpowers/specs/2026-09-22-role-isolation-design.md
git commit -m "docs: document role isolation and correct two points in its spec

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

## Final verification

- [ ] `gofmt -l .` prints nothing.
- [ ] Root module: `go build ./... && go vet ./... && go test ./... -count=1` — all PASS, integration included.
- [ ] `go test -short -race ./...` — PASS (the CI unit job).
- [ ] `middleware/gin`: `go test ./... -count=1` — PASS. `examples`: `go build ./...` — PASS.
- [ ] `go mod tidy -diff` in all three modules prints nothing; `git diff master --stat -- '*go.mod' '*go.sum'` is empty.
- [ ] With `Database.Isolation` unset, `TestIntegration_CrossTenantAccessIsRefused` and the whole pre-existing suite pass unchanged.
- [ ] CI: every job green on the pushed branch, including both `Role isolation through PgBouncer` variants.

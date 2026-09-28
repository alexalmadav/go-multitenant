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

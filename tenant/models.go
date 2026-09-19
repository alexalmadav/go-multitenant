package tenant

import (
	"time"

	"github.com/google/uuid"
)

// Tenant represents a tenant in the multi-tenant system
type Tenant struct {
	ID         uuid.UUID      `json:"id"`
	Name       string         `json:"name"`
	Subdomain  string         `json:"subdomain"`
	Status     string         `json:"status"`
	SchemaName string         `json:"schema_name"`
	Metadata   TenantMetadata `json:"metadata"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// Context represents the current tenant context for a request
type Context struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	Subdomain  string    `json:"subdomain"`
	SchemaName string    `json:"schema_name"`
	Status     string    `json:"status"`
}

// Stats represents usage statistics for a tenant
type Stats struct {
	TenantID          uuid.UUID `json:"tenant_id"`
	SchemaExists      bool      `json:"schema_exists"`
	AppliedMigrations int       `json:"applied_migrations"`
}

// Migration represents a tenant migration
type Migration struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Version     string    `json:"version"`
	Name        string    `json:"name"`
	SQL         string    `json:"sql"`
	RollbackSQL *string   `json:"rollback_sql,omitempty"`
	AppliedAt   time.Time `json:"applied_at"`
	Checksum    *string   `json:"checksum,omitempty"`
}

// Config represents configuration for the multi-tenant system
type Config struct {
	Database DatabaseConfig `json:"database"`
	Resolver ResolverConfig `json:"resolver"`
	Logger   LoggerConfig   `json:"logger"`
}

// DatabaseConfig contains database-specific configuration
type DatabaseConfig struct {
	DSN             string        `json:"dsn"`
	MaxOpenConns    int           `json:"max_open_conns"`
	MaxIdleConns    int           `json:"max_idle_conns"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time"`
	SchemaPrefix    string        `json:"schema_prefix"`
	MigrationsDir   string        `json:"migrations_dir"`
}

// ResolverConfig contains tenant resolution configuration
type ResolverConfig struct {
	Strategy          string   `json:"strategy"` // "subdomain", "path", "header"
	Domain            string   `json:"domain"`
	HeaderName        string   `json:"header_name"`
	PathPrefix        string   `json:"path_prefix"`
	ReservedSubdomain []string `json:"reserved_subdomains"`

	// ValidateSubdomain decides whether a subdomain is acceptable, both when
	// resolving requests and when creating or updating tenants. Nil means
	// DefaultSubdomainValidator(ReservedSubdomain).
	ValidateSubdomain func(subdomain string) error `json:"-"`
}

// LoggerConfig contains logging configuration
type LoggerConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"` // "json", "console"
}

// ValidationError represents a validation error
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error implements the error interface
func (e ValidationError) Error() string {
	return e.Message
}

// TenantError represents a tenant-specific error
type TenantError struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Code     string    `json:"code"`
	Message  string    `json:"message"`
}

// Error implements the error interface
func (e TenantError) Error() string {
	return e.Message
}

// Constants for tenant status
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusPending   = "pending"
	StatusCancelled = "cancelled"
)

// Constants for resolver strategies
const (
	ResolverSubdomain = "subdomain"
	ResolverPath      = "path"
	ResolverHeader    = "header"
)

// DefaultConfig returns the core defaults: a connection pool, the
// subdomain resolver and JSON logging. Limits are not part of the core; see
// package limits.
func DefaultConfig() Config {
	return Config{
		Database: DatabaseConfig{
			MaxOpenConns:    100,
			MaxIdleConns:    50,
			ConnMaxLifetime: 15 * time.Minute,
			ConnMaxIdleTime: 5 * time.Minute,
			SchemaPrefix:    "tenant_",
			MigrationsDir:   "", // Applications should set this
		},
		Resolver: ResolverConfig{
			Strategy:          ResolverSubdomain,
			ReservedSubdomain: []string{"www", "api", "admin", "mail", "ftp", "blog", "support", "help"},
		},
		Logger: LoggerConfig{
			Level:  "info",
			Format: "json",
		},
	}
}

// ValidateStatus validates a tenant status
func ValidateStatus(status string) bool {
	switch status {
	case StatusActive, StatusSuspended, StatusPending, StatusCancelled:
		return true
	default:
		return false
	}
}

package tenant

import (
	"time"

	"github.com/google/uuid"
)

// Tenant represents a tenant in the multi-tenant system
type Tenant struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Subdomain  string    `json:"subdomain"`
	PlanType   string    `json:"plan_type"`
	Status     string    `json:"status"`
	SchemaName string    `json:"schema_name"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Context represents the current tenant context for a request
type Context struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	Subdomain  string    `json:"subdomain"`
	SchemaName string    `json:"schema_name"`
	PlanType   string    `json:"plan_type"`
	Status     string    `json:"status"`
}

// Limits represents plan-based limits for a tenant
type Limits struct {
	MaxUsers     int `json:"max_users"`
	MaxProjects  int `json:"max_projects"`
	MaxStorageGB int `json:"max_storage_gb"`
}

// Stats represents usage statistics for a tenant
type Stats struct {
	TenantID       uuid.UUID `json:"tenant_id"`
	UserCount      int       `json:"user_count"`
	ProjectCount   int       `json:"project_count"`
	StorageUsedGB  float64   `json:"storage_used_gb"`
	LastActivity   time.Time `json:"last_activity"`
	SchemaExists   bool      `json:"schema_exists"`
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
	Limits   LimitsConfig   `json:"limits"`
	Logger   LoggerConfig   `json:"logger"`
}

// DatabaseConfig contains database-specific configuration
type DatabaseConfig struct {
	Driver              string        `json:"driver"`
	DSN                 string        `json:"dsn"`
	MaxOpenConns        int           `json:"max_open_conns"`
	MaxIdleConns        int           `json:"max_idle_conns"`
	ConnMaxLifetime     time.Duration `json:"conn_max_lifetime"`
	ConnMaxIdleTime     time.Duration `json:"conn_max_idle_time"`
	SchemaPrefix        string        `json:"schema_prefix"`
	MigrationsTable     string        `json:"migrations_table"`
}

// ResolverConfig contains tenant resolution configuration
type ResolverConfig struct {
	Strategy         string   `json:"strategy"` // "subdomain", "path", "header"
	Domain           string   `json:"domain"`
	HeaderName       string   `json:"header_name"`
	PathPrefix       string   `json:"path_prefix"`
	ReservedSubdomain []string `json:"reserved_subdomains"`
}

// LimitsConfig contains limit enforcement configuration
type LimitsConfig struct {
	EnforceLimits bool               `json:"enforce_limits"`
	PlanLimits    map[string]*Limits `json:"plan_limits"`
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

// Constants for plan types
const (
	PlanBasic      = "basic"
	PlanPro        = "pro"
	PlanEnterprise = "enterprise"
)

// Constants for resolver strategies
const (
	ResolverSubdomain = "subdomain"
	ResolverPath      = "path"
	ResolverHeader    = "header"
)

// DefaultConfig returns a default configuration
func DefaultConfig() Config {
	return Config{
		Database: DatabaseConfig{
			Driver:              "postgres",
			MaxOpenConns:        100,
			MaxIdleConns:        50,
			ConnMaxLifetime:     15 * time.Minute,
			ConnMaxIdleTime:     5 * time.Minute,
			SchemaPrefix:        "tenant_",
			MigrationsTable:     "tenant_migrations",
		},
		Resolver: ResolverConfig{
			Strategy:          ResolverSubdomain,
			ReservedSubdomain: []string{"www", "api", "admin", "mail", "ftp", "blog", "support", "help"},
		},
		Limits: LimitsConfig{
			EnforceLimits: true,
			PlanLimits: map[string]*Limits{
				PlanBasic: {
					MaxUsers:     5,
					MaxProjects:  10,
					MaxStorageGB: 1,
				},
				PlanPro: {
					MaxUsers:     25,
					MaxProjects:  100,
					MaxStorageGB: 10,
				},
				PlanEnterprise: {
					MaxUsers:     -1, // unlimited
					MaxProjects:  -1, // unlimited
					MaxStorageGB: 100,
				},
			},
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

// ValidatePlanType validates a plan type
func ValidatePlanType(planType string) bool {
	switch planType {
	case PlanBasic, PlanPro, PlanEnterprise:
		return true
	default:
		return false
	}
}



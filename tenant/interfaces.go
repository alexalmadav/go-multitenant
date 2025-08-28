package tenant

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/google/uuid"
)

// Manager is the main interface for tenant management
type Manager interface {
	// Tenant CRUD operations
	CreateTenant(ctx context.Context, tenant *Tenant) error
	GetTenant(ctx context.Context, id uuid.UUID) (*Tenant, error)
	GetTenantBySubdomain(ctx context.Context, subdomain string) (*Tenant, error)
	UpdateTenant(ctx context.Context, tenant *Tenant) error
	DeleteTenant(ctx context.Context, id uuid.UUID) error
	ListTenants(ctx context.Context, page, perPage int) ([]*Tenant, int, error)

	// Tenant operations
	ProvisionTenant(ctx context.Context, id uuid.UUID) error
	SuspendTenant(ctx context.Context, id uuid.UUID) error
	ActivateTenant(ctx context.Context, id uuid.UUID) error

	// Access and validation
	ValidateAccess(ctx context.Context, userID, tenantID uuid.UUID) error
	CheckLimits(ctx context.Context, tenantID uuid.UUID) (*Limits, error)
	GetStats(ctx context.Context, tenantID uuid.UUID) (*Stats, error)

	// Database operations
	GetTenantDB(ctx context.Context, tenantID uuid.UUID) (*sql.DB, error)
	WithTenantContext(ctx context.Context, tenantID uuid.UUID) context.Context

	// Close resources
	Close() error
}

// Resolver handles tenant resolution from HTTP requests
type Resolver interface {
	ResolveTenant(ctx context.Context, req *http.Request) (uuid.UUID, error)
	ExtractFromSubdomain(host string) (string, error)
	ExtractFromPath(path string) (string, error)
	ExtractFromHeader(req *http.Request) (string, error)
	ValidateSubdomain(subdomain string) error
}

// SchemaManager handles database schema operations
type SchemaManager interface {
	CreateTenantSchema(ctx context.Context, tenantID uuid.UUID, name string) error
	DropTenantSchema(ctx context.Context, tenantID uuid.UUID) error
	SchemaExists(ctx context.Context, tenantID uuid.UUID) (bool, error)
	GetSchemaName(tenantID uuid.UUID) string
	SetSearchPath(db *sql.DB, tenantID uuid.UUID) error
	ListTenantSchemas(ctx context.Context) ([]string, error)
}

// MigrationManager handles tenant migrations
type MigrationManager interface {
	ApplyMigration(ctx context.Context, tenantID uuid.UUID, migration *Migration) error
	RollbackMigration(ctx context.Context, tenantID uuid.UUID, version string) error
	ApplyToAllTenants(ctx context.Context, migration *Migration) error
	GetAppliedMigrations(ctx context.Context, tenantID uuid.UUID) ([]*Migration, error)
	IsMigrationApplied(ctx context.Context, tenantID uuid.UUID, version string) (bool, error)
}

// LimitChecker handles plan limit validation
type LimitChecker interface {
	CheckUserLimit(ctx context.Context, tenantID uuid.UUID) error
	CheckProjectLimit(ctx context.Context, tenantID uuid.UUID) error
	CheckStorageLimit(ctx context.Context, tenantID uuid.UUID) error
	CheckAllLimits(ctx context.Context, tenantID uuid.UUID) error
	GetLimitsForPlan(planType string) *Limits
}

// Storage handles tenant-aware file storage
type Storage interface {
	Store(ctx context.Context, tenantID uuid.UUID, path string, data []byte) error
	Retrieve(ctx context.Context, tenantID uuid.UUID, path string) ([]byte, error)
	Delete(ctx context.Context, tenantID uuid.UUID, path string) error
	List(ctx context.Context, tenantID uuid.UUID, prefix string) ([]string, error)
	GetURL(ctx context.Context, tenantID uuid.UUID, path string) (string, error)
}

// Repository handles tenant data persistence
type Repository interface {
	Create(ctx context.Context, tenant *Tenant) error
	GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error)
	GetBySubdomain(ctx context.Context, subdomain string) (*Tenant, error)
	Update(ctx context.Context, tenant *Tenant) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, page, perPage int) ([]*Tenant, int, error)
	GetStats(ctx context.Context, tenantID uuid.UUID) (*Stats, error)
}

// Middleware represents HTTP middleware for tenant handling
type Middleware interface {
	ResolveTenant() MiddlewareFunc
	ValidateTenant() MiddlewareFunc
	EnforceLimits() MiddlewareFunc
	RequireAdmin() MiddlewareFunc
	LogAccess() MiddlewareFunc
}

// MiddlewareFunc represents a middleware function
type MiddlewareFunc interface{}

// ContextKey represents keys for context values
type ContextKey string

const (
	// ContextKeyTenant is the context key for tenant information
	ContextKeyTenant ContextKey = "tenant"
	// ContextKeyTenantID is the context key for tenant ID
	ContextKeyTenantID ContextKey = "tenant_id"
	// ContextKeyTenantDB is the context key for tenant database connection
	ContextKeyTenantDB ContextKey = "tenant_db"
)

// GetTenantFromContext extracts tenant context from a context
func GetTenantFromContext(ctx context.Context) (*Context, bool) {
	tenant, ok := ctx.Value(ContextKeyTenant).(*Context)
	return tenant, ok
}

// GetTenantIDFromContext extracts tenant ID from a context
func GetTenantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	tenantID, ok := ctx.Value(ContextKeyTenantID).(uuid.UUID)
	return tenantID, ok
}

// GetTenantDBFromContext extracts tenant database connection from a context
func GetTenantDBFromContext(ctx context.Context) (*sql.DB, bool) {
	db, ok := ctx.Value(ContextKeyTenantDB).(*sql.DB)
	return db, ok
}



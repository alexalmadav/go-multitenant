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
	//
	// Deprecated: GetTenantDB is unsafe with connection pools. Use GetTenantConn or WithTenantTx instead.
	// The search_path set on one connection may not apply to subsequent queries from the pool.
	GetTenantDB(ctx context.Context, tenantID uuid.UUID) (*sql.DB, error)

	// GetTenantConn returns a dedicated database connection with search_path set to the tenant's schema.
	// IMPORTANT: The caller MUST close the connection when done to return it to the pool.
	// Example:
	//   conn, err := manager.GetTenantConn(ctx, tenantID)
	//   if err != nil { return err }
	//   defer conn.Close()
	//   // use conn for queries...
	GetTenantConn(ctx context.Context, tenantID uuid.UUID) (*sql.Conn, error)

	// WithTenantTx executes a function within a transaction with the tenant's search_path set.
	// This is the safest way to execute tenant-scoped queries.
	// The transaction is automatically committed if fn returns nil, or rolled back on error.
	// Example:
	//   err := manager.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
	//       _, err := tx.ExecContext(ctx, "INSERT INTO projects (name) VALUES ($1)", name)
	//       return err
	//   })
	WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(tx *sql.Tx) error) error

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

// Note: LimitChecker interface is now defined in limit_checker.go with flexible limits support

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
	// ContextKeyTenantDB is the context key for tenant database connection (deprecated)
	ContextKeyTenantDB ContextKey = "tenant_db"
	// ContextKeyTenantConn is the context key for dedicated tenant database connection
	ContextKeyTenantConn ContextKey = "tenant_conn"
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

// GetTenantDBFromContext extracts tenant database connection from a context.
//
// Deprecated: Use GetTenantConnFromContext instead for safe tenant-scoped queries.
func GetTenantDBFromContext(ctx context.Context) (*sql.DB, bool) {
	db, ok := ctx.Value(ContextKeyTenantDB).(*sql.DB)
	return db, ok
}

// GetTenantConnFromContext extracts the dedicated tenant database connection from context.
// This connection has the tenant's search_path already set and is safe for tenant-scoped queries.
func GetTenantConnFromContext(ctx context.Context) (*sql.Conn, bool) {
	conn, ok := ctx.Value(ContextKeyTenantConn).(*sql.Conn)
	return conn, ok
}

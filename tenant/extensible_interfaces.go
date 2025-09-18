package tenant

import (
	"context"

	"github.com/google/uuid"
)

// ExtensibleRepository extends the base Repository interface with metadata support
type ExtensibleRepository interface {
	// Embed the base repository interface
	Repository

	// Extended CRUD operations with metadata support
	CreateExtended(ctx context.Context, tenant *ExtensibleTenant) error
	GetExtendedByID(ctx context.Context, id uuid.UUID) (*ExtensibleTenant, error)
	GetExtendedBySubdomain(ctx context.Context, subdomain string) (*ExtensibleTenant, error)
	UpdateExtended(ctx context.Context, tenant *ExtensibleTenant) error
	ListExtended(ctx context.Context, page, perPage int) ([]*ExtensibleTenant, int, error)

	// Metadata-specific operations
	UpdateMetadata(ctx context.Context, tenantID uuid.UUID, metadata TenantMetadata) error
	GetMetadata(ctx context.Context, tenantID uuid.UUID) (TenantMetadata, error)
	UpdateMetadataField(ctx context.Context, tenantID uuid.UUID, key string, value interface{}) error
	RemoveMetadataField(ctx context.Context, tenantID uuid.UUID, key string) error

	// Query by metadata
	FindByMetadata(ctx context.Context, key string, value interface{}) ([]*ExtensibleTenant, error)
	FindByMetadataKeys(ctx context.Context, keys []string) ([]*ExtensibleTenant, error)
}

// ExtensibleManager extends the base Manager interface with metadata support
type ExtensibleManager interface {
	// Embed the base manager interface
	Manager

	// Extended tenant operations with metadata support
	CreateExtendedTenant(ctx context.Context, tenant *ExtensibleTenant) error
	GetExtendedTenant(ctx context.Context, id uuid.UUID) (*ExtensibleTenant, error)
	GetExtendedTenantBySubdomain(ctx context.Context, subdomain string) (*ExtensibleTenant, error)
	UpdateExtendedTenant(ctx context.Context, tenant *ExtensibleTenant) error
	ListExtendedTenants(ctx context.Context, page, perPage int) ([]*ExtensibleTenant, int, error)

	// Metadata operations
	UpdateTenantMetadata(ctx context.Context, tenantID uuid.UUID, metadata TenantMetadata) error
	GetTenantMetadata(ctx context.Context, tenantID uuid.UUID) (TenantMetadata, error)
	SetTenantMetadataField(ctx context.Context, tenantID uuid.UUID, key string, value interface{}) error
	RemoveTenantMetadataField(ctx context.Context, tenantID uuid.UUID, key string) error

	// Integration-specific methods
	FindTenantByStripeCustomer(ctx context.Context, customerID string) (*ExtensibleTenant, error)
	SetStripeCustomerID(ctx context.Context, tenantID uuid.UUID, customerID string) error
	SetStripeSubscriptionID(ctx context.Context, tenantID uuid.UUID, subscriptionID string) error
}

// TenantExtension is a generic interface for tenant extensions
type TenantExtension interface {
	// GetExtensionName returns the unique name of this extension
	GetExtensionName() string

	// ValidateMetadata validates metadata fields for this extension
	ValidateMetadata(metadata TenantMetadata) error

	// GetRequiredFields returns the required metadata fields for this extension
	GetRequiredFields() []string

	// GetOptionalFields returns the optional metadata fields for this extension
	GetOptionalFields() []string

	// InitializeDefaults sets default values for this extension
	InitializeDefaults(metadata TenantMetadata) error

	// OnTenantCreated is called after a tenant is created
	OnTenantCreated(ctx context.Context, tenant *ExtensibleTenant) error

	// OnTenantUpdated is called after a tenant is updated
	OnTenantUpdated(ctx context.Context, tenant *ExtensibleTenant) error

	// OnTenantDeleted is called before a tenant is deleted
	OnTenantDeleted(ctx context.Context, tenant *ExtensibleTenant) error
}

// ExtensionRegistry manages tenant extensions
type ExtensionRegistry interface {
	// RegisterExtension registers a new tenant extension
	RegisterExtension(extension TenantExtension) error

	// GetExtension gets an extension by name
	GetExtension(name string) (TenantExtension, bool)

	// GetAllExtensions returns all registered extensions
	GetAllExtensions() []TenantExtension

	// ValidateExtensions validates all registered extensions for a tenant
	ValidateExtensions(tenant *ExtensibleTenant) error

	// InitializeExtensions initializes defaults for all extensions
	InitializeExtensions(tenant *ExtensibleTenant) error

	// TriggerHooks triggers lifecycle hooks for all extensions
	TriggerCreatedHooks(ctx context.Context, tenant *ExtensibleTenant) error
	TriggerUpdatedHooks(ctx context.Context, tenant *ExtensibleTenant) error
	TriggerDeletedHooks(ctx context.Context, tenant *ExtensibleTenant) error
}

// MetadataField represents a typed metadata field definition
type MetadataField struct {
	Key          string      `json:"key"`
	Type         string      `json:"type"` // "string", "int", "bool", "float", "json"
	Required     bool        `json:"required"`
	DefaultValue interface{} `json:"default_value,omitempty"`
	Validation   string      `json:"validation,omitempty"` // regex or validation rule
	Description  string      `json:"description,omitempty"`
}

// ExtensionSchema defines the schema for a tenant extension
type ExtensionSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Fields      []MetadataField `json:"fields"`
	Version     string          `json:"version"`
}

// SchemaRegistry manages extension schemas
type SchemaRegistry interface {
	// RegisterSchema registers a new extension schema
	RegisterSchema(schema *ExtensionSchema) error

	// GetSchema gets a schema by name
	GetSchema(name string) (*ExtensionSchema, bool)

	// ValidateAgainstSchema validates metadata against a schema
	ValidateAgainstSchema(schemaName string, metadata TenantMetadata) error

	// GetAllSchemas returns all registered schemas
	GetAllSchemas() []*ExtensionSchema
}

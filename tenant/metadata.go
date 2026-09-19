// Package tenant's metadata.go holds per-tenant metadata: a free-form,
// JSONB-backed key-value bag stored on Tenant.Metadata and persisted in
// public.tenants.metadata. Applications use it to attach arbitrary
// integration data (Stripe customer IDs, branding preferences, feature
// flags, ...) to a tenant without a schema migration for every new field.
package tenant

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// TenantMetadata holds extensible key-value metadata for tenants
type TenantMetadata map[string]interface{}

// Value implements the driver.Valuer interface for database storage.
// A nil map serializes as an empty JSON object so the column is never NULL.
//
// This returns a string rather than the []byte json.Marshal produces:
// the database connection here uses pgx's simple query protocol, which
// encodes an untyped []byte parameter as a bytea literal (hex-escaped),
// not as JSON text, so a jsonb column would reject it. A string parameter
// is sent as a plain text literal that Postgres casts to jsonb correctly.
func (tm TenantMetadata) Value() (driver.Value, error) {
	if tm == nil {
		return "{}", nil
	}
	b, err := json.Marshal(tm)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements the sql.Scanner interface for database reading
func (tm *TenantMetadata) Scan(value interface{}) error {
	if value == nil {
		*tm = make(TenantMetadata)
		return nil
	}

	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return errors.New("cannot scan non-string/[]byte into TenantMetadata")
	}

	if err := json.Unmarshal(bytes, tm); err != nil {
		return err
	}
	if *tm == nil {
		*tm = make(TenantMetadata)
	}
	return nil
}

// GetString safely gets a string value from metadata
func (tm TenantMetadata) GetString(key string) (string, bool) {
	if val, exists := tm[key]; exists {
		if str, ok := val.(string); ok {
			return str, true
		}
	}
	return "", false
}

// SetString sets a string value in metadata
func (tm TenantMetadata) SetString(key, value string) {
	tm[key] = value
}

// GetInt safely gets an integer value from metadata
func (tm TenantMetadata) GetInt(key string) (int, bool) {
	if val, exists := tm[key]; exists {
		switch v := val.(type) {
		case int:
			return v, true
		case float64: // JSON numbers are float64
			return int(v), true
		}
	}
	return 0, false
}

// SetInt sets an integer value in metadata
func (tm TenantMetadata) SetInt(key string, value int) {
	tm[key] = value
}

// GetBool safely gets a boolean value from metadata
func (tm TenantMetadata) GetBool(key string) (bool, bool) {
	if val, exists := tm[key]; exists {
		if b, ok := val.(bool); ok {
			return b, true
		}
	}
	return false, false
}

// SetBool sets a boolean value in metadata
func (tm TenantMetadata) SetBool(key string, value bool) {
	tm[key] = value
}

// Has checks if a key exists in metadata
func (tm TenantMetadata) Has(key string) bool {
	_, exists := tm[key]
	return exists
}

// Remove removes a key from metadata
func (tm TenantMetadata) Remove(key string) {
	delete(tm, key)
}

// Common metadata keys (conventions)
const (
	MetadataStripeCustomerID     = "stripe_customer_id"
	MetadataStripeSubscriptionID = "stripe_subscription_id"
	MetadataCustomDomain         = "custom_domain"
	MetadataLogoURL              = "logo_url"
	MetadataTheme                = "theme"
	MetadataTimezone             = "timezone"
	MetadataLanguage             = "language"
	MetadataContactEmail         = "contact_email"
	MetadataWebhookURL           = "webhook_url"
	MetadataAPIKey               = "api_key"
)

// Extension helper functions for common integrations

// StripeExtension provides helpers for Stripe integration
type StripeExtension struct {
	metadata TenantMetadata
}

// NewStripeExtension creates a new Stripe extension helper
func NewStripeExtension(metadata TenantMetadata) *StripeExtension {
	return &StripeExtension{metadata: metadata}
}

// GetCustomerID gets the Stripe customer ID
func (se *StripeExtension) GetCustomerID() (string, bool) {
	return se.metadata.GetString(MetadataStripeCustomerID)
}

// SetCustomerID sets the Stripe customer ID
func (se *StripeExtension) SetCustomerID(customerID string) {
	se.metadata.SetString(MetadataStripeCustomerID, customerID)
}

// GetSubscriptionID gets the Stripe subscription ID
func (se *StripeExtension) GetSubscriptionID() (string, bool) {
	return se.metadata.GetString(MetadataStripeSubscriptionID)
}

// SetSubscriptionID sets the Stripe subscription ID
func (se *StripeExtension) SetSubscriptionID(subscriptionID string) {
	se.metadata.SetString(MetadataStripeSubscriptionID, subscriptionID)
}

// HasStripeIntegration checks if tenant has Stripe integration
func (se *StripeExtension) HasStripeIntegration() bool {
	return se.metadata.Has(MetadataStripeCustomerID)
}

// BrandingExtension provides helpers for branding customization
type BrandingExtension struct {
	metadata TenantMetadata
}

// NewBrandingExtension creates a new branding extension helper
func NewBrandingExtension(metadata TenantMetadata) *BrandingExtension {
	return &BrandingExtension{metadata: metadata}
}

// GetLogoURL gets the tenant's logo URL
func (be *BrandingExtension) GetLogoURL() (string, bool) {
	return be.metadata.GetString(MetadataLogoURL)
}

// SetLogoURL sets the tenant's logo URL
func (be *BrandingExtension) SetLogoURL(url string) {
	be.metadata.SetString(MetadataLogoURL, url)
}

// GetTheme gets the tenant's theme
func (be *BrandingExtension) GetTheme() (string, bool) {
	return be.metadata.GetString(MetadataTheme)
}

// SetTheme sets the tenant's theme
func (be *BrandingExtension) SetTheme(theme string) {
	be.metadata.SetString(MetadataTheme, theme)
}

// GetCustomDomain gets the tenant's custom domain
func (be *BrandingExtension) GetCustomDomain() (string, bool) {
	return be.metadata.GetString(MetadataCustomDomain)
}

// SetCustomDomain sets the tenant's custom domain
func (be *BrandingExtension) SetCustomDomain(domain string) {
	be.metadata.SetString(MetadataCustomDomain, domain)
}

// PlanKey is the metadata key that holds a tenant's plan name. The core
// stores it and never interprets it; the limits package reads it.
const PlanKey = "plan"

// Plan returns the tenant's plan name from metadata, or "" if unset.
func (t *Tenant) Plan() string {
	if t == nil || t.Metadata == nil {
		return ""
	}
	v, _ := t.Metadata.GetString(PlanKey)
	return v
}

// SetPlan stores the plan name in metadata. An empty plan removes the key.
// A nil receiver is a no-op, matching Plan()'s nil-safety.
func (t *Tenant) SetPlan(plan string) {
	if t == nil {
		return
	}
	if t.Metadata == nil {
		t.Metadata = TenantMetadata{}
	}
	if plan == "" {
		t.Metadata.Remove(PlanKey)
		return
	}
	t.Metadata.SetString(PlanKey, plan)
}

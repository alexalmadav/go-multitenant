package tenant

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LimitType represents the type of a limit value
type LimitType string

const (
	LimitTypeInt      LimitType = "int"
	LimitTypeFloat    LimitType = "float"
	LimitTypeString   LimitType = "string"
	LimitTypeBool     LimitType = "bool"
	LimitTypeDuration LimitType = "duration"
)

// LimitValue represents a flexible limit value that can be any type
type LimitValue struct {
	Type  LimitType   `json:"type"`
	Value interface{} `json:"value"`
}

// Int returns the limit value as an int
func (lv *LimitValue) Int() (int, error) {
	if lv.Type != LimitTypeInt {
		return 0, fmt.Errorf("limit is not an int, got %s", lv.Type)
	}

	switch v := lv.Value.(type) {
	case int:
		return v, nil
	case float64: // JSON unmarshaling often gives float64
		return int(v), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
}

// Float returns the limit value as a float64
func (lv *LimitValue) Float() (float64, error) {
	if lv.Type != LimitTypeFloat {
		return 0, fmt.Errorf("limit is not a float, got %s", lv.Type)
	}

	switch v := lv.Value.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", v)
	}
}

// String returns the limit value as a string
func (lv *LimitValue) String() (string, error) {
	if lv.Type != LimitTypeString {
		return "", fmt.Errorf("limit is not a string, got %s", lv.Type)
	}

	if s, ok := lv.Value.(string); ok {
		return s, nil
	}
	return "", fmt.Errorf("cannot convert %T to string", lv.Value)
}

// Bool returns the limit value as a bool
func (lv *LimitValue) Bool() (bool, error) {
	if lv.Type != LimitTypeBool {
		return false, fmt.Errorf("limit is not a bool, got %s", lv.Type)
	}

	if b, ok := lv.Value.(bool); ok {
		return b, nil
	}
	return false, fmt.Errorf("cannot convert %T to bool", lv.Value)
}

// Duration returns the limit value as a time.Duration
func (lv *LimitValue) Duration() (time.Duration, error) {
	if lv.Type != LimitTypeDuration {
		return 0, fmt.Errorf("limit is not a duration, got %s", lv.Type)
	}

	switch v := lv.Value.(type) {
	case string:
		return time.ParseDuration(v)
	case int64:
		return time.Duration(v), nil
	case float64:
		return time.Duration(int64(v)), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to duration", v)
	}
}

// IsUnlimited checks if the limit represents unlimited (-1 for ints, special values for others)
func (lv *LimitValue) IsUnlimited() bool {
	switch lv.Type {
	case LimitTypeInt:
		if val, err := lv.Int(); err == nil && val == -1 {
			return true
		}
	case LimitTypeFloat:
		if val, err := lv.Float(); err == nil && val < 0 {
			return true
		}
	case LimitTypeString:
		if val, err := lv.String(); err == nil && (val == "unlimited" || val == "") {
			return true
		}
	}
	return false
}

// FlexibleLimits represents a dynamic set of limits
type FlexibleLimits map[string]*LimitValue

// Get retrieves a limit by name
func (fl FlexibleLimits) Get(name string) (*LimitValue, bool) {
	limit, exists := fl[name]
	return limit, exists
}

// Set sets a limit by name
func (fl FlexibleLimits) Set(name string, limitType LimitType, value interface{}) {
	fl[name] = &LimitValue{
		Type:  limitType,
		Value: value,
	}
}

// GetInt retrieves an integer limit by name
func (fl FlexibleLimits) GetInt(name string) (int, error) {
	limit, exists := fl[name]
	if !exists {
		return 0, fmt.Errorf("limit '%s' not found", name)
	}
	return limit.Int()
}

// GetFloat retrieves a float limit by name
func (fl FlexibleLimits) GetFloat(name string) (float64, error) {
	limit, exists := fl[name]
	if !exists {
		return 0, fmt.Errorf("limit '%s' not found", name)
	}
	return limit.Float()
}

// GetString retrieves a string limit by name
func (fl FlexibleLimits) GetString(name string) (string, error) {
	limit, exists := fl[name]
	if !exists {
		return "", fmt.Errorf("limit '%s' not found", name)
	}
	return limit.String()
}

// GetBool retrieves a boolean limit by name
func (fl FlexibleLimits) GetBool(name string) (bool, error) {
	limit, exists := fl[name]
	if !exists {
		return false, fmt.Errorf("limit '%s' not found", name)
	}
	return limit.Bool()
}

// GetDuration retrieves a duration limit by name
func (fl FlexibleLimits) GetDuration(name string) (time.Duration, error) {
	limit, exists := fl[name]
	if !exists {
		return 0, fmt.Errorf("limit '%s' not found", name)
	}
	return limit.Duration()
}

// IsUnlimited checks if a specific limit is unlimited
func (fl FlexibleLimits) IsUnlimited(name string) bool {
	limit, exists := fl[name]
	if !exists {
		return false
	}
	return limit.IsUnlimited()
}

// Delete removes a limit by name
func (fl FlexibleLimits) Delete(name string) {
	delete(fl, name)
}

// Has checks if a limit exists
func (fl FlexibleLimits) Has(name string) bool {
	_, exists := fl[name]
	return exists
}

// Keys returns all limit names
func (fl FlexibleLimits) Keys() []string {
	keys := make([]string, 0, len(fl))
	for name := range fl {
		keys = append(keys, name)
	}
	return keys
}

// Len returns the number of limits
func (fl FlexibleLimits) Len() int {
	return len(fl)
}

// LimitDefinition defines metadata for a limit type
type LimitDefinition struct {
	Name         string      `json:"name"`
	DisplayName  string      `json:"display_name"`
	Description  string      `json:"description"`
	Type         LimitType   `json:"type"`
	DefaultValue *LimitValue `json:"default_value"`
	MinValue     *LimitValue `json:"min_value"`
	MaxValue     *LimitValue `json:"max_value"`
	Required     bool        `json:"required"`
	Category     string      `json:"category"` // e.g., "usage", "features", "api", etc.
	Tags         []string    `json:"tags"`
}

// LimitSchema defines available limit types for a system
type LimitSchema struct {
	Definitions map[string]*LimitDefinition `json:"definitions"`
}

// NewLimitSchema creates a new limit schema
func NewLimitSchema() *LimitSchema {
	return &LimitSchema{
		Definitions: make(map[string]*LimitDefinition),
	}
}

// AddDefinition adds a limit definition to the schema
func (ls *LimitSchema) AddDefinition(def *LimitDefinition) {
	ls.Definitions[def.Name] = def
}

// GetDefinition gets a limit definition by name
func (ls *LimitSchema) GetDefinition(name string) (*LimitDefinition, bool) {
	def, exists := ls.Definitions[name]
	return def, exists
}

// GetAllDefinitions returns all limit definitions
func (ls *LimitSchema) GetAllDefinitions() map[string]*LimitDefinition {
	return ls.Definitions
}

// ValidateLimits validates a set of limits against the schema
func (ls *LimitSchema) ValidateLimits(limits FlexibleLimits) error {
	// Check required limits
	for name, def := range ls.Definitions {
		if def.Required {
			if _, exists := limits[name]; !exists {
				return fmt.Errorf("required limit '%s' is missing", name)
			}
		}
	}

	// Validate limit types and values
	for name, limit := range limits {
		def, exists := ls.Definitions[name]
		if !exists {
			return fmt.Errorf("unknown limit '%s'", name)
		}

		if limit.Type != def.Type {
			return fmt.Errorf("limit '%s' has incorrect type: expected %s, got %s",
				name, def.Type, limit.Type)
		}

		// Type-specific validation could be added here
	}

	return nil
}

// CreateDefaultLimits creates a FlexibleLimits with default values from schema
func (ls *LimitSchema) CreateDefaultLimits() FlexibleLimits {
	limits := make(FlexibleLimits)

	for name, def := range ls.Definitions {
		if def.DefaultValue != nil {
			limits[name] = &LimitValue{
				Type:  def.Type,
				Value: def.DefaultValue,
			}
		}
	}

	return limits
}

// FlexibleLimitChecker extends the LimitChecker interface for dynamic limits
type FlexibleLimitChecker interface {
	LimitChecker // Embed existing interface for backward compatibility

	// Dynamic limit checking
	CheckLimit(ctx context.Context, tenantID uuid.UUID, limitName string, currentValue interface{}) error
	CheckLimitByDefinition(ctx context.Context, tenantID uuid.UUID, def *LimitDefinition, currentValue interface{}) error

	// Schema management
	GetLimitSchema() *LimitSchema
	SetLimitSchema(schema *LimitSchema)

	// Flexible limits for plans
	GetFlexibleLimitsForPlan(planType string) FlexibleLimits
	SetFlexibleLimitsForPlan(planType string, limits FlexibleLimits)
}

// UsageTracker helps track current usage against limits
type UsageTracker interface {
	GetCurrentUsage(ctx context.Context, tenantID uuid.UUID, limitName string) (interface{}, error)
	IncrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error
	DecrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error
	ResetUsage(ctx context.Context, tenantID uuid.UUID, limitName string) error
}

// Standard limit names for backward compatibility
const (
	LimitNameMaxUsers     = "max_users"
	LimitNameMaxProjects  = "max_projects"
	LimitNameMaxStorageGB = "max_storage_gb"
)

// DefaultLimitSchema returns a comprehensive schema with common limits
func DefaultLimitSchema() *LimitSchema {
	schema := NewLimitSchema()

	// Usage limits
	schema.AddDefinition(&LimitDefinition{
		Name:         "max_users",
		DisplayName:  "Maximum Users",
		Description:  "Maximum number of users allowed in the tenant",
		Type:         LimitTypeInt,
		DefaultValue: &LimitValue{Type: LimitTypeInt, Value: 5},
		Required:     true,
		Category:     "usage",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "max_projects",
		DisplayName:  "Maximum Projects",
		Description:  "Maximum number of projects allowed in the tenant",
		Type:         LimitTypeInt,
		DefaultValue: &LimitValue{Type: LimitTypeInt, Value: 10},
		Required:     true,
		Category:     "usage",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "max_storage_gb",
		DisplayName:  "Maximum Storage (GB)",
		Description:  "Maximum storage allowed in gigabytes",
		Type:         LimitTypeInt,
		DefaultValue: &LimitValue{Type: LimitTypeInt, Value: 1},
		Required:     true,
		Category:     "usage",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "max_file_size_mb",
		DisplayName:  "Maximum File Size (MB)",
		Description:  "Maximum size for uploaded files in megabytes",
		Type:         LimitTypeInt,
		DefaultValue: &LimitValue{Type: LimitTypeInt, Value: 10},
		Required:     false,
		Category:     "usage",
	})

	// API limits
	schema.AddDefinition(&LimitDefinition{
		Name:         "api_calls_per_month",
		DisplayName:  "API Calls per Month",
		Description:  "Maximum number of API calls allowed per month",
		Type:         LimitTypeInt,
		DefaultValue: &LimitValue{Type: LimitTypeInt, Value: 10000},
		Required:     false,
		Category:     "api",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "api_rate_per_minute",
		DisplayName:  "API Rate per Minute",
		Description:  "Maximum number of API calls allowed per minute",
		Type:         LimitTypeInt,
		DefaultValue: &LimitValue{Type: LimitTypeInt, Value: 100},
		Required:     false,
		Category:     "api",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "webhook_endpoints",
		DisplayName:  "Maximum Webhook Endpoints",
		Description:  "Maximum number of webhook endpoints that can be configured",
		Type:         LimitTypeInt,
		DefaultValue: &LimitValue{Type: LimitTypeInt, Value: 5},
		Required:     false,
		Category:     "api",
	})

	// Feature flags
	schema.AddDefinition(&LimitDefinition{
		Name:         "advanced_features",
		DisplayName:  "Advanced Features",
		Description:  "Whether advanced features are enabled",
		Type:         LimitTypeBool,
		DefaultValue: &LimitValue{Type: LimitTypeBool, Value: false},
		Required:     false,
		Category:     "features",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "custom_integrations",
		DisplayName:  "Custom Integrations",
		Description:  "Whether custom integrations are allowed",
		Type:         LimitTypeBool,
		DefaultValue: &LimitValue{Type: LimitTypeBool, Value: false},
		Required:     false,
		Category:     "features",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "priority_support",
		DisplayName:  "Priority Support",
		Description:  "Whether priority support is included",
		Type:         LimitTypeBool,
		DefaultValue: &LimitValue{Type: LimitTypeBool, Value: false},
		Required:     false,
		Category:     "support",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "dedicated_support",
		DisplayName:  "Dedicated Support",
		Description:  "Whether dedicated support representative is assigned",
		Type:         LimitTypeBool,
		DefaultValue: &LimitValue{Type: LimitTypeBool, Value: false},
		Required:     false,
		Category:     "support",
	})

	// Time-based limits
	schema.AddDefinition(&LimitDefinition{
		Name:         "backup_retention_days",
		DisplayName:  "Backup Retention (Days)",
		Description:  "Number of days to retain backups",
		Type:         LimitTypeInt,
		DefaultValue: &LimitValue{Type: LimitTypeInt, Value: 7},
		Required:     false,
		Category:     "data",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "session_timeout",
		DisplayName:  "Session Timeout",
		Description:  "Maximum session duration before timeout",
		Type:         LimitTypeDuration,
		DefaultValue: &LimitValue{Type: LimitTypeDuration, Value: "24h"},
		Required:     false,
		Category:     "security",
	})

	// String-based limits
	schema.AddDefinition(&LimitDefinition{
		Name:         "custom_domain",
		DisplayName:  "Custom Domain",
		Description:  "Custom domain allowed for this tenant",
		Type:         LimitTypeString,
		DefaultValue: &LimitValue{Type: LimitTypeString, Value: ""},
		Required:     false,
		Category:     "branding",
	})

	schema.AddDefinition(&LimitDefinition{
		Name:         "export_formats",
		DisplayName:  "Export Formats",
		Description:  "Comma-separated list of allowed export formats",
		Type:         LimitTypeString,
		DefaultValue: &LimitValue{Type: LimitTypeString, Value: "csv,json"},
		Required:     false,
		Category:     "features",
	})

	return schema
}

// Helper functions for creating common limit types

// IntLimit creates an integer limit
func IntLimit(value int) *LimitValue {
	return &LimitValue{Type: LimitTypeInt, Value: value}
}

// FloatLimit creates a float limit
func FloatLimit(value float64) *LimitValue {
	return &LimitValue{Type: LimitTypeFloat, Value: value}
}

// StringLimit creates a string limit
func StringLimit(value string) *LimitValue {
	return &LimitValue{Type: LimitTypeString, Value: value}
}

// BoolLimit creates a boolean limit
func BoolLimit(value bool) *LimitValue {
	return &LimitValue{Type: LimitTypeBool, Value: value}
}

// DurationLimit creates a duration limit
func DurationLimit(value time.Duration) *LimitValue {
	return &LimitValue{Type: LimitTypeDuration, Value: value.String()}
}

// UnlimitedInt creates an unlimited integer limit (-1)
func UnlimitedInt() *LimitValue {
	return &LimitValue{Type: LimitTypeInt, Value: -1}
}

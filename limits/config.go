// Package limits provides optional plan-limit definitions and enforcement for
// go-multitenant. It builds on package tenant; the core knows nothing about
// plans or limits, so applications that do not enforce limits never import
// this package.
package limits

import "github.com/alexalmadav/go-multitenant/tenant"

// Example plan names. The core does not know these; they are only defaults
// for ExampleConfig and the examples.
const (
	PlanBasic      = "basic"
	PlanPro        = "pro"
	PlanEnterprise = "enterprise"
)

// Config configures limit enforcement.
type Config struct {
	// EnforceLimits switches checking on. False makes every check pass.
	EnforceLimits bool `json:"enforce_limits"`
	// PlanLimits maps a plan name to its limits.
	PlanLimits map[string]FlexibleLimits `json:"plan_limits"`
	// LimitSchema validates limit names and types. Nil means DefaultLimitSchema().
	LimitSchema *LimitSchema `json:"limit_schema,omitempty"`
	// UsageTables maps a limit name to a table in the tenant schema whose row
	// count is that limit's current usage, e.g. {"max_projects": "projects"}.
	// Limits not listed are not checked unless a custom UsageTracker supplies
	// a value.
	UsageTables map[string]string `json:"usage_tables"`
	// PlanOf returns the plan name for a tenant. Nil means t.Plan(), i.e.
	// metadata["plan"].
	PlanOf func(t *tenant.Tenant) string `json:"-"`
}

// ExampleConfig returns three example plans (basic, pro, enterprise) with
// usage counted from "projects" and "tenant_users" tables. Copy and adapt.
func ExampleConfig() Config {
	basic := make(FlexibleLimits)
	basic.Set("max_users", LimitTypeInt, 5)
	basic.Set("max_projects", LimitTypeInt, 10)
	basic.Set("max_storage_gb", LimitTypeInt, 1)
	basic.Set("api_calls_per_month", LimitTypeInt, 10000)
	basic.Set("advanced_features", LimitTypeBool, false)

	pro := make(FlexibleLimits)
	pro.Set("max_users", LimitTypeInt, 25)
	pro.Set("max_projects", LimitTypeInt, 100)
	pro.Set("max_storage_gb", LimitTypeInt, 10)
	pro.Set("api_calls_per_month", LimitTypeInt, 100000)
	pro.Set("advanced_features", LimitTypeBool, true)
	pro.Set("priority_support", LimitTypeBool, true)

	enterprise := make(FlexibleLimits)
	enterprise.Set("max_users", LimitTypeInt, -1)
	enterprise.Set("max_projects", LimitTypeInt, -1)
	enterprise.Set("max_storage_gb", LimitTypeInt, 100)
	enterprise.Set("api_calls_per_month", LimitTypeInt, -1)
	enterprise.Set("advanced_features", LimitTypeBool, true)
	enterprise.Set("priority_support", LimitTypeBool, true)
	enterprise.Set("custom_integrations", LimitTypeBool, true)
	enterprise.Set("dedicated_support", LimitTypeBool, true)

	return Config{
		EnforceLimits: true,
		LimitSchema:   DefaultLimitSchema(),
		PlanLimits:    map[string]FlexibleLimits{PlanBasic: basic, PlanPro: pro, PlanEnterprise: enterprise},
		UsageTables:   map[string]string{"max_projects": "projects", "max_users": "tenant_users"},
	}
}

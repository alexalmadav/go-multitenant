package limits

import (
	"context"
	"fmt"
	"sync"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Enforcer is the narrow interface the HTTP middleware needs.
type Enforcer interface {
	// CheckTenant checks every limit of the tenant's plan and returns a
	// snapshot of those limits. A limit violation is a *tenant.TenantError
	// with Code LIMIT_EXCEEDED or FEATURE_NOT_ALLOWED. A plan with no
	// configured limits (including the empty plan) is refused, not
	// allowed: a *tenant.TenantError with Code PLAN_NOT_CONFIGURED.
	CheckTenant(ctx context.Context, tenantID uuid.UUID) (FlexibleLimits, error)
}

// Checker provides dynamic limit checking capabilities.
//
// With enforcement on (Config.EnforceLimits), every method that resolves a
// tenant's plan agrees on an unconfigured plan: CheckLimit, CheckTenant, and
// CheckAllLimits all return a *tenant.TenantError with Code
// PLAN_NOT_CONFIGURED rather than allowing the request. Set the tenant's
// plan with Tenant.SetPlan, or supply a fallback via Config.PlanOf.
type Checker interface {
	Enforcer

	// Dynamic limit checking
	CheckLimit(ctx context.Context, tenantID uuid.UUID, limitName string, currentValue interface{}) error
	CheckLimitByDefinition(ctx context.Context, tenantID uuid.UUID, def *LimitDefinition, currentValue interface{}) error
	CheckAllLimits(ctx context.Context, tenantID uuid.UUID) error

	// Usage returns the current count for every limit in Config.UsageTables.
	Usage(ctx context.Context, tenantID uuid.UUID) (map[string]int, error)

	// Schema management
	GetLimitSchema() *LimitSchema
	SetLimitSchema(schema *LimitSchema)

	// Plan limit management
	GetLimitsForPlan(planType string) FlexibleLimits
	SetLimitsForPlan(planType string, limits FlexibleLimits)

	// Limit management
	AddLimit(planType, limitName string, limitType LimitType, value interface{}) error
	RemoveLimit(planType, limitName string) error
	UpdateLimit(planType, limitName string, value interface{}) error

	// Validation
	ValidateLimits(planType string, limits FlexibleLimits) error

	// Usage integration
	SetUsageTracker(tracker UsageTracker)
	GetUsageTracker() UsageTracker
}

// checker implements the Checker interface
type checker struct {
	mu           sync.RWMutex
	config       Config
	repository   tenant.Repository
	logger       *zap.Logger
	schema       *LimitSchema
	planLimits   map[string]FlexibleLimits
	usageTracker UsageTracker
}

// NewChecker creates a new limit checker
func NewChecker(config Config, repository tenant.Repository, logger *zap.Logger) Checker {
	c := &checker{
		config:     config,
		repository: repository,
		logger:     logger.Named("limits"),
		schema:     config.LimitSchema,
		planLimits: config.PlanLimits,
	}

	// Use default schema if none provided
	if c.schema == nil {
		c.schema = DefaultLimitSchema()
	}

	// Initialize plan limits if empty
	if c.planLimits == nil {
		c.planLimits = make(map[string]FlexibleLimits)
	}

	return c
}

// planOf resolves the tenant's plan name.
func (lc *checker) planOf(t *tenant.Tenant) string {
	if lc.config.PlanOf != nil {
		return lc.config.PlanOf(t)
	}
	return t.Plan()
}

// CheckLimit validates a specific limit for a tenant
func (lc *checker) CheckLimit(ctx context.Context, tenantID uuid.UUID, limitName string, currentValue interface{}) error {
	if !lc.config.EnforceLimits {
		return nil
	}

	t, err := lc.repository.GetByID(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	plan := lc.planOf(t)
	planLimits := lc.GetLimitsForPlan(plan)
	if planLimits == nil {
		return &tenant.TenantError{
			TenantID: t.ID,
			Code:     "PLAN_NOT_CONFIGURED",
			Message:  fmt.Sprintf("no limits configured for plan %q", plan),
		}
	}

	return lc.checkOne(ctx, t, plan, planLimits, limitName, currentValue)
}

// checkOne validates one limit of an already-loaded tenant against the limits
// of its plan. A nil currentValue is read from the usage tracker, if any.
func (lc *checker) checkOne(ctx context.Context, t *tenant.Tenant, plan string, planLimits FlexibleLimits, limitName string, currentValue interface{}) error {
	limit, exists := planLimits.Get(limitName)
	if !exists {
		// If limit doesn't exist in plan, it's not restricted
		lc.logger.Debug("Limit not defined for plan",
			zap.String("limit", limitName),
			zap.String("plan", plan))
		return nil
	}

	// Check if unlimited
	if limit.IsUnlimited() {
		return nil
	}

	// Get current usage if not provided
	if tracker := lc.GetUsageTracker(); currentValue == nil && tracker != nil {
		value, err := tracker.GetCurrentUsage(ctx, t.ID, limitName)
		if err != nil {
			lc.logger.Warn("Failed to get current usage, skipping limit check",
				zap.String("tenant_id", t.ID.String()),
				zap.String("limit", limitName),
				zap.Error(err))
			return nil
		}
		currentValue = value
	}

	// Perform validation
	return lc.validateLimit(t.ID, limitName, limit, currentValue)
}

// CheckLimitByDefinition checks a limit using its definition
func (lc *checker) CheckLimitByDefinition(ctx context.Context, tenantID uuid.UUID, def *LimitDefinition, currentValue interface{}) error {
	return lc.CheckLimit(ctx, tenantID, def.Name, currentValue)
}

// CheckTenant loads the tenant once, checks every limit of its plan, and
// returns a snapshot of those limits. When enforcement is disabled it returns
// an empty snapshot without consulting the repository; use
// GetLimitsForPlan(t.Plan()) for the configured limits. With enforcement on, a
// plan that has no configured limits is an error.
func (lc *checker) CheckTenant(ctx context.Context, tenantID uuid.UUID) (FlexibleLimits, error) {
	if !lc.config.EnforceLimits {
		return FlexibleLimits{}, nil
	}
	t, err := lc.repository.GetByID(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}
	plan := lc.planOf(t)
	planLimits := lc.GetLimitsForPlan(plan)
	if planLimits == nil {
		return nil, &tenant.TenantError{
			TenantID: t.ID,
			Code:     "PLAN_NOT_CONFIGURED",
			Message:  fmt.Sprintf("no limits configured for plan %q", plan),
		}
	}
	for name := range planLimits {
		if err := lc.checkOne(ctx, t, plan, planLimits, name, nil); err != nil {
			return nil, fmt.Errorf("limit check failed for %s: %w", name, err)
		}
	}
	return planLimits, nil
}

// CheckAllLimits validates all limits for a tenant. With enforcement off it
// is a no-op.
func (lc *checker) CheckAllLimits(ctx context.Context, tenantID uuid.UUID) error {
	_, err := lc.CheckTenant(ctx, tenantID)
	return err
}

// Usage returns the current count for every limit in Config.UsageTables.
func (lc *checker) Usage(ctx context.Context, tenantID uuid.UUID) (map[string]int, error) {
	out := make(map[string]int, len(lc.config.UsageTables))
	tracker := lc.GetUsageTracker()
	if tracker == nil {
		return out, nil
	}
	for name := range lc.config.UsageTables {
		v, err := tracker.GetCurrentUsage(ctx, tenantID, name)
		if err != nil {
			return nil, fmt.Errorf("usage for %s: %w", name, err)
		}
		switch n := v.(type) {
		case int:
			out[name] = n
		case int64:
			out[name] = int(n)
		case float64:
			out[name] = int(n)
		}
	}
	return out, nil
}

// validateLimit performs type-specific validation
func (lc *checker) validateLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
	if currentValue == nil {
		// No current value to compare, skip validation
		return nil
	}

	switch limit.Type {
	case LimitTypeInt:
		return lc.validateIntLimit(tenantID, limitName, limit, currentValue)
	case LimitTypeFloat:
		return lc.validateFloatLimit(tenantID, limitName, limit, currentValue)
	case LimitTypeString:
		return lc.validateStringLimit(tenantID, limitName, limit, currentValue)
	case LimitTypeBool:
		return lc.validateBoolLimit(tenantID, limitName, limit, currentValue)
	case LimitTypeDuration:
		return lc.validateDurationLimit(tenantID, limitName, limit, currentValue)
	default:
		lc.logger.Warn("Unknown limit type, skipping validation",
			zap.String("tenant_id", tenantID.String()),
			zap.String("limit", limitName),
			zap.String("type", string(limit.Type)))
		return nil
	}
}

func (lc *checker) validateIntLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
	limitVal, err := limit.Int()
	if err != nil {
		return fmt.Errorf("invalid limit value for %s: %w", limitName, err)
	}

	var current int
	switch v := currentValue.(type) {
	case int:
		current = v
	case int64:
		current = int(v)
	case float64:
		current = int(v)
	default:
		return fmt.Errorf("cannot compare %T with int limit for %s", currentValue, limitName)
	}

	if current > limitVal {
		return &tenant.TenantError{
			TenantID: tenantID,
			Code:     "LIMIT_EXCEEDED",
			Message:  fmt.Sprintf("Limit exceeded for %s: current=%d, limit=%d", limitName, current, limitVal),
		}
	}

	return nil
}

func (lc *checker) validateFloatLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
	limitVal, err := limit.Float()
	if err != nil {
		return fmt.Errorf("invalid limit value for %s: %w", limitName, err)
	}

	var current float64
	switch v := currentValue.(type) {
	case float64:
		current = v
	case float32:
		current = float64(v)
	case int:
		current = float64(v)
	case int64:
		current = float64(v)
	default:
		return fmt.Errorf("cannot compare %T with float limit for %s", currentValue, limitName)
	}

	if current > limitVal {
		return &tenant.TenantError{
			TenantID: tenantID,
			Code:     "LIMIT_EXCEEDED",
			Message:  fmt.Sprintf("Limit exceeded for %s: current=%.2f, limit=%.2f", limitName, current, limitVal),
		}
	}

	return nil
}

func (lc *checker) validateStringLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
	limitVal, err := limit.String()
	if err != nil {
		return fmt.Errorf("invalid limit value for %s: %w", limitName, err)
	}

	current, ok := currentValue.(string)
	if !ok {
		return fmt.Errorf("cannot compare %T with string limit for %s", currentValue, limitName)
	}

	// String validation can be customized based on the limit name
	// For now, implement basic length comparison
	if len(current) > len(limitVal) && limitVal != "unlimited" && limitVal != "" {
		return &tenant.TenantError{
			TenantID: tenantID,
			Code:     "LIMIT_EXCEEDED",
			Message:  fmt.Sprintf("String limit exceeded for %s: current length=%d, limit length=%d", limitName, len(current), len(limitVal)),
		}
	}

	return nil
}

func (lc *checker) validateBoolLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
	limitVal, err := limit.Bool()
	if err != nil {
		return fmt.Errorf("invalid limit value for %s: %w", limitName, err)
	}

	current, ok := currentValue.(bool)
	if !ok {
		return fmt.Errorf("cannot compare %T with bool limit for %s", currentValue, limitName)
	}

	// For boolean limits, if limit is false and current usage is true, it's exceeded
	if !limitVal && current {
		return &tenant.TenantError{
			TenantID: tenantID,
			Code:     "FEATURE_NOT_ALLOWED",
			Message:  fmt.Sprintf("Feature not allowed: %s is disabled for this plan", limitName),
		}
	}

	return nil
}

func (lc *checker) validateDurationLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
	_, err := limit.Duration()
	if err != nil {
		return fmt.Errorf("invalid limit value for %s: %w", limitName, err)
	}

	// Current value could be a duration or time that needs comparison
	// Implementation depends on specific use case
	lc.logger.Debug("Duration limit validation not fully implemented",
		zap.String("limit", limitName))

	return nil
}

// Schema management

func (lc *checker) GetLimitSchema() *LimitSchema {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.schema
}

func (lc *checker) SetLimitSchema(schema *LimitSchema) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.schema = schema
}

// Plan limit management

// GetLimitsForPlan returns a snapshot of the plan's limits. Mutating the
// returned map does not affect the checker; use AddLimit/UpdateLimit/RemoveLimit.
func (lc *checker) GetLimitsForPlan(planType string) FlexibleLimits {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	limits, ok := lc.planLimits[planType]
	if !ok {
		return nil
	}
	snapshot := make(FlexibleLimits, len(limits))
	for name, lv := range limits {
		copied := *lv
		snapshot[name] = &copied
	}
	return snapshot
}

func (lc *checker) SetLimitsForPlan(planType string, limits FlexibleLimits) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.planLimits[planType] = limits
}

// Limit management

func (lc *checker) AddLimit(planType, limitName string, limitType LimitType, value interface{}) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	// Validate limit definition exists in schema
	if _, exists := lc.schema.GetDefinition(limitName); !exists {
		// Add to schema if not exists
		def := &LimitDefinition{
			Name:        limitName,
			DisplayName: limitName,
			Description: fmt.Sprintf("Custom limit: %s", limitName),
			Type:        limitType,
			Required:    false,
			Category:    "custom",
		}
		lc.schema.AddDefinition(def)
	}

	// Add to plan limits
	if lc.planLimits[planType] == nil {
		lc.planLimits[planType] = make(FlexibleLimits)
	}

	lc.planLimits[planType][limitName] = &LimitValue{
		Type:  limitType,
		Value: value,
	}

	lc.logger.Info("Added limit to plan",
		zap.String("plan", planType),
		zap.String("limit", limitName),
		zap.String("type", string(limitType)),
		zap.Any("value", value))

	return nil
}

func (lc *checker) RemoveLimit(planType, limitName string) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if planLimits, exists := lc.planLimits[planType]; exists {
		delete(planLimits, limitName)
		lc.logger.Info("Removed limit from plan",
			zap.String("plan", planType),
			zap.String("limit", limitName))
	}
	return nil
}

func (lc *checker) UpdateLimit(planType, limitName string, value interface{}) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	planLimits := lc.planLimits[planType]
	if planLimits == nil {
		return fmt.Errorf("plan %s not found", planType)
	}

	limit, exists := planLimits[limitName]
	if !exists {
		return fmt.Errorf("limit %s not found in plan %s", limitName, planType)
	}

	// Replace rather than mutate so snapshots handed out earlier stay stable.
	planLimits[limitName] = &LimitValue{Type: limit.Type, Value: value}

	lc.logger.Info("Updated limit value",
		zap.String("plan", planType),
		zap.String("limit", limitName),
		zap.Any("value", value))

	return nil
}

// Validation

func (lc *checker) ValidateLimits(planType string, limits FlexibleLimits) error {
	return lc.GetLimitSchema().ValidateLimits(limits)
}

// Usage tracker integration

func (lc *checker) SetUsageTracker(tracker UsageTracker) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.usageTracker = tracker
}

func (lc *checker) GetUsageTracker() UsageTracker {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.usageTracker
}

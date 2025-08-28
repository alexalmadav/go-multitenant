package tenant

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// LimitChecker provides dynamic limit checking capabilities
type LimitChecker interface {
	// Dynamic limit checking
	CheckLimit(ctx context.Context, tenantID uuid.UUID, limitName string, currentValue interface{}) error
	CheckLimitByDefinition(ctx context.Context, tenantID uuid.UUID, def *LimitDefinition, currentValue interface{}) error
	CheckAllLimits(ctx context.Context, tenantID uuid.UUID) error

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

// limitChecker implements the LimitChecker interface
type limitChecker struct {
	config       LimitsConfig
	repository   Repository
	logger       *zap.Logger
	schema       *LimitSchema
	planLimits   map[string]FlexibleLimits
	usageTracker UsageTracker
}

// NewLimitChecker creates a new limit checker
func NewLimitChecker(config LimitsConfig, repository Repository, logger *zap.Logger) LimitChecker {
	checker := &limitChecker{
		config:     config,
		repository: repository,
		logger:     logger.Named("limits"),
		schema:     config.LimitSchema,
		planLimits: config.PlanLimits,
	}

	// Use default schema if none provided
	if checker.schema == nil {
		checker.schema = DefaultLimitSchema()
	}

	// Initialize plan limits if empty
	if checker.planLimits == nil {
		checker.planLimits = make(map[string]FlexibleLimits)
	}

	return checker
}

// CheckLimit validates a specific limit for a tenant
func (lc *limitChecker) CheckLimit(ctx context.Context, tenantID uuid.UUID, limitName string, currentValue interface{}) error {
	if !lc.config.EnforceLimits {
		return nil
	}

	// Get tenant to determine plan
	tenant, err := lc.repository.GetByID(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	// Get plan limits
	planLimits := lc.GetLimitsForPlan(tenant.PlanType)
	if planLimits == nil {
		lc.logger.Warn("No limits found for plan", zap.String("plan", tenant.PlanType))
		return nil
	}

	// Get the specific limit
	limit, exists := planLimits.Get(limitName)
	if !exists {
		// If limit doesn't exist in plan, it's not restricted
		lc.logger.Debug("Limit not defined for plan",
			zap.String("limit", limitName),
			zap.String("plan", tenant.PlanType))
		return nil
	}

	// Check if unlimited
	if limit.IsUnlimited() {
		return nil
	}

	// Get current usage if not provided
	if currentValue == nil && lc.usageTracker != nil {
		currentValue, err = lc.usageTracker.GetCurrentUsage(ctx, tenantID, limitName)
		if err != nil {
			lc.logger.Warn("Failed to get current usage, skipping limit check",
				zap.String("tenant_id", tenantID.String()),
				zap.String("limit", limitName),
				zap.Error(err))
			return nil
		}
	}

	// Perform validation
	return lc.validateLimit(tenantID, limitName, limit, currentValue)
}

// CheckLimitByDefinition checks a limit using its definition
func (lc *limitChecker) CheckLimitByDefinition(ctx context.Context, tenantID uuid.UUID, def *LimitDefinition, currentValue interface{}) error {
	return lc.CheckLimit(ctx, tenantID, def.Name, currentValue)
}

// CheckAllLimits validates all limits for a tenant
func (lc *limitChecker) CheckAllLimits(ctx context.Context, tenantID uuid.UUID) error {
	if !lc.config.EnforceLimits {
		return nil
	}

	// Get tenant
	tenant, err := lc.repository.GetByID(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	// Get plan limits
	planLimits := lc.GetLimitsForPlan(tenant.PlanType)
	if planLimits == nil {
		return fmt.Errorf("no limits found for plan: %s", tenant.PlanType)
	}

	// Check each limit in the plan
	for limitName := range planLimits {
		if err := lc.CheckLimit(ctx, tenantID, limitName, nil); err != nil {
			return fmt.Errorf("limit check failed for %s: %w", limitName, err)
		}
	}

	return nil
}

// validateLimit performs type-specific validation
func (lc *limitChecker) validateLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
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

func (lc *limitChecker) validateIntLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
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
		return &TenantError{
			TenantID: tenantID,
			Code:     "LIMIT_EXCEEDED",
			Message:  fmt.Sprintf("Limit exceeded for %s: current=%d, limit=%d", limitName, current, limitVal),
		}
	}

	return nil
}

func (lc *limitChecker) validateFloatLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
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
		return &TenantError{
			TenantID: tenantID,
			Code:     "LIMIT_EXCEEDED",
			Message:  fmt.Sprintf("Limit exceeded for %s: current=%.2f, limit=%.2f", limitName, current, limitVal),
		}
	}

	return nil
}

func (lc *limitChecker) validateStringLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
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
		return &TenantError{
			TenantID: tenantID,
			Code:     "LIMIT_EXCEEDED",
			Message:  fmt.Sprintf("String limit exceeded for %s: current length=%d, limit length=%d", limitName, len(current), len(limitVal)),
		}
	}

	return nil
}

func (lc *limitChecker) validateBoolLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
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
		return &TenantError{
			TenantID: tenantID,
			Code:     "FEATURE_NOT_ALLOWED",
			Message:  fmt.Sprintf("Feature not allowed: %s is disabled for this plan", limitName),
		}
	}

	return nil
}

func (lc *limitChecker) validateDurationLimit(tenantID uuid.UUID, limitName string, limit *LimitValue, currentValue interface{}) error {
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

func (lc *limitChecker) GetLimitSchema() *LimitSchema {
	return lc.schema
}

func (lc *limitChecker) SetLimitSchema(schema *LimitSchema) {
	lc.schema = schema
}

// Plan limit management

func (lc *limitChecker) GetLimitsForPlan(planType string) FlexibleLimits {
	return lc.planLimits[planType]
}

func (lc *limitChecker) SetLimitsForPlan(planType string, limits FlexibleLimits) {
	lc.planLimits[planType] = limits
}

// Limit management

func (lc *limitChecker) AddLimit(planType, limitName string, limitType LimitType, value interface{}) error {
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

func (lc *limitChecker) RemoveLimit(planType, limitName string) error {
	if planLimits, exists := lc.planLimits[planType]; exists {
		delete(planLimits, limitName)
		lc.logger.Info("Removed limit from plan",
			zap.String("plan", planType),
			zap.String("limit", limitName))
	}
	return nil
}

func (lc *limitChecker) UpdateLimit(planType, limitName string, value interface{}) error {
	planLimits := lc.planLimits[planType]
	if planLimits == nil {
		return fmt.Errorf("plan %s not found", planType)
	}

	limit, exists := planLimits[limitName]
	if !exists {
		return fmt.Errorf("limit %s not found in plan %s", limitName, planType)
	}

	limit.Value = value

	lc.logger.Info("Updated limit value",
		zap.String("plan", planType),
		zap.String("limit", limitName),
		zap.Any("value", value))

	return nil
}

// Validation

func (lc *limitChecker) ValidateLimits(planType string, limits FlexibleLimits) error {
	return lc.schema.ValidateLimits(limits)
}

// Usage tracker integration

func (lc *limitChecker) SetUsageTracker(tracker UsageTracker) {
	lc.usageTracker = tracker
}

func (lc *limitChecker) GetUsageTracker() UsageTracker {
	return lc.usageTracker
}

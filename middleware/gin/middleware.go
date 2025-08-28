package gin

import (
	"net/http"
	"strings"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Middleware provides Gin-specific middleware for multi-tenant applications
type Middleware struct {
	manager  tenant.Manager
	resolver tenant.Resolver
	logger   *zap.Logger
	config   Config
}

// Config contains configuration for the Gin middleware
type Config struct {
	// SkipPaths are paths that should skip tenant resolution
	SkipPaths []string
	// RequireAuthentication determines if authentication is required
	RequireAuthentication bool
	// ErrorHandler is called when an error occurs
	ErrorHandler func(*gin.Context, error)
}

// NewMiddleware creates a new Gin middleware
func NewMiddleware(manager tenant.Manager, resolver tenant.Resolver, logger *zap.Logger, config Config) *Middleware {
	if config.ErrorHandler == nil {
		config.ErrorHandler = defaultErrorHandler
	}

	return &Middleware{
		manager:  manager,
		resolver: resolver,
		logger:   logger.Named("gin_middleware"),
		config:   config,
	}
}

// ResolveTenant is middleware that resolves tenant from the request
func (m *Middleware) ResolveTenant() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip if path should be skipped
		if m.shouldSkipPath(c.Request.URL.Path) {
			c.Next()
			return
		}

		// Resolve tenant from request
		tenantID, err := m.resolver.ResolveTenant(c.Request.Context(), c.Request)
		if err != nil {
			m.logger.Debug("Failed to resolve tenant",
				zap.String("path", c.Request.URL.Path),
				zap.String("host", c.Request.Host),
				zap.Error(err))

			m.config.ErrorHandler(c, &tenant.TenantError{
				Code:    "TENANT_NOT_FOUND",
				Message: "Unable to resolve tenant from request",
			})
			return
		}

		// Get tenant details
		t, err := m.manager.GetTenant(c.Request.Context(), tenantID)
		if err != nil {
			m.logger.Error("Failed to get tenant details",
				zap.String("tenant_id", tenantID.String()),
				zap.Error(err))

			m.config.ErrorHandler(c, &tenant.TenantError{
				TenantID: tenantID,
				Code:     "TENANT_NOT_FOUND",
				Message:  "Tenant not found",
			})
			return
		}

		// Create tenant context
		tenantCtx := &tenant.Context{
			TenantID:   t.ID,
			Subdomain:  t.Subdomain,
			SchemaName: t.SchemaName,
			PlanType:   t.PlanType,
			Status:     t.Status,
		}

		// Set tenant information in Gin context
		c.Set("tenant", tenantCtx)
		c.Set("tenant_id", t.ID.String())
		c.Set("tenant_object", t)

		// Add tenant context to request context
		ctx := m.manager.WithTenantContext(c.Request.Context(), tenantID)
		c.Request = c.Request.WithContext(ctx)

		m.logger.Debug("Resolved tenant",
			zap.String("tenant_id", tenantID.String()),
			zap.String("subdomain", t.Subdomain),
			zap.String("path", c.Request.URL.Path))

		c.Next()
	}
}

// ValidateTenant is middleware that validates tenant status and access
func (m *Middleware) ValidateTenant() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantCtx, exists := GetTenantFromContext(c)
		if !exists {
			m.config.ErrorHandler(c, &tenant.TenantError{
				Code:    "TENANT_CONTEXT_MISSING",
				Message: "Tenant context not found - ensure ResolveTenant middleware is applied first",
			})
			return
		}

		// Check tenant status
		switch tenantCtx.Status {
		case tenant.StatusActive:
			// All good, continue
		case tenant.StatusSuspended:
			m.config.ErrorHandler(c, &tenant.TenantError{
				TenantID: tenantCtx.TenantID,
				Code:     "TENANT_SUSPENDED",
				Message:  "Account suspended. Please contact support.",
			})
			return
		case tenant.StatusPending:
			m.config.ErrorHandler(c, &tenant.TenantError{
				TenantID: tenantCtx.TenantID,
				Code:     "TENANT_PENDING",
				Message:  "Account pending verification. Please check your email.",
			})
			return
		case tenant.StatusCancelled:
			m.config.ErrorHandler(c, &tenant.TenantError{
				TenantID: tenantCtx.TenantID,
				Code:     "TENANT_CANCELLED",
				Message:  "Account cancelled.",
			})
			return
		default:
			m.config.ErrorHandler(c, &tenant.TenantError{
				TenantID: tenantCtx.TenantID,
				Code:     "TENANT_INVALID_STATUS",
				Message:  "Account status invalid.",
			})
			return
		}

		// Validate user access if authentication is required
		if m.config.RequireAuthentication {
			userIDStr := c.GetString("user_id")
			if userIDStr == "" {
				m.config.ErrorHandler(c, &tenant.TenantError{
					TenantID: tenantCtx.TenantID,
					Code:     "USER_NOT_AUTHENTICATED",
					Message:  "User authentication required",
				})
				return
			}

			userID, err := uuid.Parse(userIDStr)
			if err != nil {
				m.config.ErrorHandler(c, &tenant.TenantError{
					TenantID: tenantCtx.TenantID,
					Code:     "INVALID_USER_ID",
					Message:  "Invalid user ID format",
				})
				return
			}

			if err := m.manager.ValidateAccess(c.Request.Context(), userID, tenantCtx.TenantID); err != nil {
				m.logger.Warn("User access validation failed",
					zap.String("user_id", userID.String()),
					zap.String("tenant_id", tenantCtx.TenantID.String()),
					zap.Error(err))

				m.config.ErrorHandler(c, &tenant.TenantError{
					TenantID: tenantCtx.TenantID,
					Code:     "ACCESS_DENIED",
					Message:  "Access denied to this tenant",
				})
				return
			}
		}

		c.Next()
	}
}

// EnforceLimits is middleware that enforces plan limits
func (m *Middleware) EnforceLimits() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantCtx, exists := GetTenantFromContext(c)
		if !exists {
			m.config.ErrorHandler(c, &tenant.TenantError{
				Code:    "TENANT_CONTEXT_MISSING",
				Message: "Tenant context not found",
			})
			return
		}

		// Check plan limits
		limits, err := m.manager.CheckLimits(c.Request.Context(), tenantCtx.TenantID)
		if err != nil {
			m.logger.Error("Plan limits check failed",
				zap.String("tenant_id", tenantCtx.TenantID.String()),
				zap.Error(err))

			// Determine error type and response
			if strings.Contains(err.Error(), "limit exceeded") {
				m.config.ErrorHandler(c, &tenant.TenantError{
					TenantID: tenantCtx.TenantID,
					Code:     "PLAN_LIMIT_EXCEEDED",
					Message:  err.Error(),
				})
			} else {
				m.config.ErrorHandler(c, &tenant.TenantError{
					TenantID: tenantCtx.TenantID,
					Code:     "LIMIT_CHECK_FAILED",
					Message:  "Unable to verify plan limits",
				})
			}
			return
		}

		// Set limits in context for use in handlers
		c.Set("plan_limits", limits)
		c.Next()
	}
}

// RequireAdmin is middleware that requires tenant admin privileges
func (m *Middleware) RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Check if user is authenticated
		userRole := c.GetString("user_role")
		isAdmin := c.GetBool("is_tenant_admin")

		if userRole != "admin" && !isAdmin {
			tenantCtx, _ := GetTenantFromContext(c)
			var tenantID uuid.UUID
			if tenantCtx != nil {
				tenantID = tenantCtx.TenantID
			}

			m.config.ErrorHandler(c, &tenant.TenantError{
				TenantID: tenantID,
				Code:     "ADMIN_REQUIRED",
				Message:  "Admin access required",
			})
			return
		}

		c.Next()
	}
}

// LogAccess is middleware that logs tenant access for monitoring
func (m *Middleware) LogAccess() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantCtx, exists := GetTenantFromContext(c)
		if !exists {
			c.Next()
			return
		}

		userID := c.GetString("user_id")
		method := c.Request.Method
		path := c.Request.URL.Path

		// Log tenant access
		m.logger.Info("Tenant access",
			zap.String("method", method),
			zap.String("path", path),
			zap.String("user_id", userID),
			zap.String("tenant_id", tenantCtx.TenantID.String()),
			zap.String("subdomain", tenantCtx.Subdomain),
			zap.String("client_ip", c.ClientIP()),
			zap.String("user_agent", c.Request.UserAgent()))

		c.Next()
	}
}

// SetTenantDB is middleware that sets up tenant-specific database connection
func (m *Middleware) SetTenantDB() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantCtx, exists := GetTenantFromContext(c)
		if !exists {
			c.Next()
			return
		}

		// Get tenant-specific database connection
		db, err := m.manager.GetTenantDB(c.Request.Context(), tenantCtx.TenantID)
		if err != nil {
			m.logger.Error("Failed to get tenant database",
				zap.String("tenant_id", tenantCtx.TenantID.String()),
				zap.Error(err))

			m.config.ErrorHandler(c, &tenant.TenantError{
				TenantID: tenantCtx.TenantID,
				Code:     "DATABASE_ERROR",
				Message:  "Failed to access tenant database",
			})
			return
		}

		// Set database connection in context
		c.Set("tenant_db", db)
		c.Next()
	}
}

// Helper functions

// GetTenantFromContext extracts tenant context from Gin context
func GetTenantFromContext(c *gin.Context) (*tenant.Context, bool) {
	tenantCtx, exists := c.Get("tenant")
	if !exists {
		return nil, false
	}

	t, ok := tenantCtx.(*tenant.Context)
	return t, ok
}

// GetTenantFromGinContext extracts full tenant object from Gin context
func GetTenantFromGinContext(c *gin.Context) (*tenant.Tenant, bool) {
	tenantObj, exists := c.Get("tenant_object")
	if !exists {
		return nil, false
	}

	t, ok := tenantObj.(*tenant.Tenant)
	return t, ok
}

// GetTenantLimitsFromContext extracts tenant limits from Gin context
func GetTenantLimitsFromContext(c *gin.Context) (*tenant.Limits, bool) {
	limits, exists := c.Get("plan_limits")
	if !exists {
		return nil, false
	}

	l, ok := limits.(*tenant.Limits)
	return l, ok
}

// shouldSkipPath checks if a path should skip tenant resolution
func (m *Middleware) shouldSkipPath(path string) bool {
	for _, skipPath := range m.config.SkipPaths {
		if strings.HasPrefix(path, skipPath) {
			return true
		}
	}
	return false
}

// defaultErrorHandler is the default error handler for tenant errors
func defaultErrorHandler(c *gin.Context, err error) {
	var statusCode int
	var response gin.H

	switch e := err.(type) {
	case *tenant.TenantError:
		switch e.Code {
		case "TENANT_NOT_FOUND":
			statusCode = http.StatusNotFound
		case "TENANT_SUSPENDED", "TENANT_CANCELLED", "ACCESS_DENIED":
			statusCode = http.StatusForbidden
		case "TENANT_PENDING":
			statusCode = http.StatusForbidden
		case "PLAN_LIMIT_EXCEEDED":
			statusCode = http.StatusPaymentRequired
		case "ADMIN_REQUIRED":
			statusCode = http.StatusForbidden
		case "USER_NOT_AUTHENTICATED":
			statusCode = http.StatusUnauthorized
		default:
			statusCode = http.StatusInternalServerError
		}

		response = gin.H{
			"error": gin.H{
				"code":    e.Code,
				"message": e.Message,
			},
		}

		if e.TenantID != uuid.Nil {
			response["tenant_id"] = e.TenantID.String()
		}

	case *tenant.ValidationError:
		statusCode = http.StatusBadRequest
		response = gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": e.Message,
				"field":   e.Field,
			},
		}

	default:
		statusCode = http.StatusInternalServerError
		response = gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "An internal error occurred",
			},
		}
	}

	c.JSON(statusCode, response)
	c.Abort()
}

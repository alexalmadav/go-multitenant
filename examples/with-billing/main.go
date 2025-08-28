package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/alexalmadav/go-multitenant"
	ginmiddleware "github.com/alexalmadav/go-multitenant/middleware/gin"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func main() {
	// Create configuration with custom limits for different plans
	config := multitenant.DefaultConfig()

	// Configure database
	config.Database.DSN = "postgres://username:password@localhost:5432/multitenant_billing_db?sslmode=disable"

	// Configure resolver
	config.Resolver.Strategy = multitenant.ResolverSubdomain
	config.Resolver.Domain = "saas.example.com"

	// Configure custom plan limits
	config.Limits.PlanLimits = map[string]*multitenant.Limits{
		multitenant.PlanBasic: {
			MaxUsers:     2,
			MaxProjects:  3,
			MaxStorageGB: 1,
		},
		multitenant.PlanPro: {
			MaxUsers:     10,
			MaxProjects:  25,
			MaxStorageGB: 5,
		},
		multitenant.PlanEnterprise: {
			MaxUsers:     -1, // unlimited
			MaxProjects:  -1, // unlimited
			MaxStorageGB: 50,
		},
	}

	// Initialize multi-tenant system
	mt, err := multitenant.New(config)
	if err != nil {
		log.Fatal("Failed to initialize multi-tenant system:", err)
	}
	defer mt.Close()

	// Create example tenants with different plans
	if err := createBillingExampleTenants(mt); err != nil {
		log.Fatal("Failed to create example tenants:", err)
	}

	// Setup Gin router
	r := gin.Default()

	// Public routes
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})

	// Configure middleware with stricter settings
	ginConfig := ginmiddleware.Config{
		SkipPaths:             []string{"/health", "/api/public/", "/billing/"},
		RequireAuthentication: true,
		ErrorHandler:          customErrorHandler,
	}

	mw := ginmiddleware.NewMiddleware(mt.Manager, mt.Resolver, mt.GetLogger(), ginConfig)

	// Multi-tenant API routes with authentication simulation
	api := r.Group("/api")
	{
		api.Use(simulateAuthentication()) // Simulate user authentication
		api.Use(mw.ResolveTenant())
		api.Use(mw.ValidateTenant())
		api.Use(mw.EnforceLimits())
		api.Use(mw.SetTenantDB())
		api.Use(mw.LogAccess())

		// Protected routes
		api.GET("/dashboard", getDashboard)
		api.GET("/limits", getCurrentLimits)
		api.POST("/projects", createProjectWithLimits(mt))
		api.GET("/usage", getUsage(mt))
	}

	// Admin routes with admin requirement
	admin := r.Group("/admin")
	{
		admin.Use(simulateAdminAuth())
		admin.Use(mw.ResolveTenant())
		admin.Use(mw.RequireAdmin())

		admin.GET("/analytics", getAdminAnalytics)
		admin.PUT("/plan", upgradePlan(mt))
		admin.POST("/suspend", suspendTenant(mt))
		admin.POST("/activate", activateTenant(mt))
	}

	// Billing routes (bypass tenant resolution for billing management)
	billing := r.Group("/billing")
	{
		billing.GET("/plans", getAvailablePlans)
		billing.POST("/upgrade", requestPlanUpgrade(mt))
		billing.GET("/usage/:tenant_id", getBillingUsage(mt))
	}

	fmt.Println("Starting advanced multi-tenant server on :8080")
	fmt.Println("Try accessing:")
	fmt.Println("  - http://startup.saas.example.com:8080/api/dashboard (Basic plan)")
	fmt.Println("  - http://enterprise.saas.example.com:8080/api/dashboard (Enterprise plan)")
	fmt.Println("  - http://localhost:8080/billing/plans")

	log.Fatal(http.ListenAndServe(":8080", r))
}

func createBillingExampleTenants(mt *multitenant.MultiTenant) error {
	ctx := context.Background()

	tenants := []*multitenant.Tenant{
		{
			ID:        uuid.New(),
			Name:      "Startup Inc",
			Subdomain: "startup",
			PlanType:  multitenant.PlanBasic,
			Status:    multitenant.StatusActive,
		},
		{
			ID:        uuid.New(),
			Name:      "Enterprise Corp",
			Subdomain: "enterprise",
			PlanType:  multitenant.PlanEnterprise,
			Status:    multitenant.StatusActive,
		},
		{
			ID:        uuid.New(),
			Name:      "Suspended Company",
			Subdomain: "suspended",
			PlanType:  multitenant.PlanPro,
			Status:    multitenant.StatusSuspended,
		},
	}

	for _, tenant := range tenants {
		existing, err := mt.Manager.GetTenantBySubdomain(ctx, tenant.Subdomain)
		if err == nil && existing != nil {
			continue
		}

		if err := mt.Manager.CreateTenant(ctx, tenant); err != nil {
			return err
		}

		if err := mt.Manager.ProvisionTenant(ctx, tenant.ID); err != nil {
			return err
		}

		fmt.Printf("Created tenant: %s (%s plan)\n", tenant.Name, tenant.PlanType)
	}

	return nil
}

// Middleware functions

func simulateAuthentication() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Simulate authenticated user
		c.Set("user_id", "user-123")
		c.Set("user_role", "user")
		c.Set("is_tenant_admin", false)
		c.Next()
	}
}

func simulateAdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Simulate admin user
		c.Set("user_id", "admin-456")
		c.Set("user_role", "admin")
		c.Set("is_tenant_admin", true)
		c.Next()
	}
}

// Handler functions

func getDashboard(c *gin.Context) {
	tenant, _ := ginmiddleware.GetTenantFromContext(c)
	limits, _ := ginmiddleware.GetTenantLimitsFromContext(c)

	c.JSON(http.StatusOK, gin.H{
		"welcome":  fmt.Sprintf("Welcome to %s dashboard!", tenant.Subdomain),
		"plan":     tenant.PlanType,
		"limits":   limits,
		"features": getFeaturesByPlan(tenant.PlanType),
	})
}

func getCurrentLimits(c *gin.Context) {
	limits, exists := ginmiddleware.GetTenantLimitsFromContext(c)
	if !exists {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Limits not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"limits": limits,
		"usage": gin.H{
			"users":    2, // Mock current usage
			"projects": 1,
			"storage":  "0.5GB",
		},
	})
}

func createProjectWithLimits(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Name string `json:"name" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		tenantID, _ := multitenant.GetTenantIDFromContext(c.Request.Context())

		// Simulate checking project limits
		stats, err := mt.Manager.GetStats(c.Request.Context(), tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get stats"})
			return
		}

		limits, _ := ginmiddleware.GetTenantLimitsFromContext(c)
		if limits.MaxProjects > 0 && stats.ProjectCount >= limits.MaxProjects {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error":        "Project limit reached",
				"current_plan": "Consider upgrading your plan",
				"upgrade_url":  "/billing/upgrade",
			})
			return
		}

		project := gin.H{
			"id":         uuid.New(),
			"name":       req.Name,
			"created_at": time.Now(),
			"tenant_id":  tenantID,
		}

		c.JSON(http.StatusCreated, gin.H{
			"project": project,
			"message": "Project created successfully",
		})
	}
}

func getUsage(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID, _ := multitenant.GetTenantIDFromContext(c.Request.Context())

		stats, err := mt.Manager.GetStats(c.Request.Context(), tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		limits, _ := ginmiddleware.GetTenantLimitsFromContext(c)

		usage := gin.H{
			"current": gin.H{
				"users":    stats.UserCount,
				"projects": stats.ProjectCount,
				"storage":  fmt.Sprintf("%.1fGB", stats.StorageUsedGB),
			},
			"limits": limits,
			"percentage": gin.H{
				"users":    calculatePercentage(stats.UserCount, limits.MaxUsers),
				"projects": calculatePercentage(stats.ProjectCount, limits.MaxProjects),
				"storage":  calculatePercentage(int(stats.StorageUsedGB), limits.MaxStorageGB),
			},
		}

		c.JSON(http.StatusOK, usage)
	}
}

func getAdminAnalytics(c *gin.Context) {
	tenant, _ := ginmiddleware.GetTenantFromGinContext(c)

	analytics := gin.H{
		"tenant": tenant,
		"metrics": gin.H{
			"active_users":    25,
			"monthly_revenue": fmt.Sprintf("$%d", getPlanPrice(tenant.PlanType)),
			"storage_usage":   "15.2GB",
			"api_calls":       142350,
			"last_login":      time.Now().Add(-2 * time.Hour),
		},
		"alerts": []gin.H{
			{"type": "info", "message": "Usage within normal limits"},
		},
	}

	c.JSON(http.StatusOK, analytics)
}

func upgradePlan(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			NewPlan string `json:"new_plan" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		tenantID, _ := multitenant.GetTenantIDFromContext(c.Request.Context())
		tenant, err := mt.Manager.GetTenant(c.Request.Context(), tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Validate plan upgrade
		if !isValidUpgrade(tenant.PlanType, req.NewPlan) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":          "Invalid plan upgrade",
				"current_plan":   tenant.PlanType,
				"requested_plan": req.NewPlan,
			})
			return
		}

		// Update tenant plan
		tenant.PlanType = req.NewPlan
		if err := mt.Manager.UpdateTenant(c.Request.Context(), tenant); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":        "Plan upgraded successfully",
			"old_plan":       tenant.PlanType,
			"new_plan":       req.NewPlan,
			"effective_date": time.Now(),
		})
	}
}

func suspendTenant(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID, _ := multitenant.GetTenantIDFromContext(c.Request.Context())

		if err := mt.Manager.SuspendTenant(c.Request.Context(), tenantID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "Tenant suspended successfully",
		})
	}
}

func activateTenant(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID, _ := multitenant.GetTenantIDFromContext(c.Request.Context())

		if err := mt.Manager.ActivateTenant(c.Request.Context(), tenantID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "Tenant activated successfully",
		})
	}
}

// Billing-specific handlers

func getAvailablePlans(c *gin.Context) {
	plans := gin.H{
		"plans": []gin.H{
			{
				"name":     multitenant.PlanBasic,
				"price":    29,
				"features": getFeaturesByPlan(multitenant.PlanBasic),
				"limits": gin.H{
					"users":    2,
					"projects": 3,
					"storage":  "1GB",
				},
			},
			{
				"name":     multitenant.PlanPro,
				"price":    99,
				"features": getFeaturesByPlan(multitenant.PlanPro),
				"limits": gin.H{
					"users":    10,
					"projects": 25,
					"storage":  "5GB",
				},
			},
			{
				"name":     multitenant.PlanEnterprise,
				"price":    299,
				"features": getFeaturesByPlan(multitenant.PlanEnterprise),
				"limits": gin.H{
					"users":    "unlimited",
					"projects": "unlimited",
					"storage":  "50GB",
				},
			},
		},
	}

	c.JSON(http.StatusOK, plans)
}

func requestPlanUpgrade(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			TenantID string `json:"tenant_id" binding:"required"`
			NewPlan  string `json:"new_plan" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		tenantID, err := uuid.Parse(req.TenantID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tenant ID"})
			return
		}

		// In a real application, this would handle payment processing
		// For now, we'll just simulate the upgrade request
		c.JSON(http.StatusAccepted, gin.H{
			"message":    "Plan upgrade requested",
			"tenant_id":  req.TenantID,
			"new_plan":   req.NewPlan,
			"status":     "pending_payment",
			"next_steps": "Complete payment to activate new plan",
		})
	}
}

func getBillingUsage(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantIDStr := c.Param("tenant_id")
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tenant ID"})
			return
		}

		tenant, err := mt.Manager.GetTenant(c.Request.Context(), tenantID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Tenant not found"})
			return
		}

		stats, err := mt.Manager.GetStats(c.Request.Context(), tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		billing := gin.H{
			"tenant": gin.H{
				"id":   tenant.ID,
				"name": tenant.Name,
				"plan": tenant.PlanType,
			},
			"usage": stats,
			"charges": gin.H{
				"base_plan": getPlanPrice(tenant.PlanType),
				"overages":  0, // Calculate based on usage
				"total":     getPlanPrice(tenant.PlanType),
			},
			"billing_period": gin.H{
				"start": time.Now().AddDate(0, -1, 0).Format("2006-01-02"),
				"end":   time.Now().Format("2006-01-02"),
			},
		}

		c.JSON(http.StatusOK, billing)
	}
}

// Helper functions

func customErrorHandler(c *gin.Context, err error) {
	var statusCode int
	var response gin.H

	switch e := err.(type) {
	case *multitenant.TenantError:
		switch e.Code {
		case "PLAN_LIMIT_EXCEEDED":
			statusCode = http.StatusPaymentRequired
			response = gin.H{
				"error": gin.H{
					"code":             e.Code,
					"message":          e.Message,
					"upgrade_required": true,
					"upgrade_url":      "/billing/upgrade",
					"contact_sales":    "/contact/sales",
				},
			}
		default:
			// Use default error handler for other cases
			ginmiddleware.Config{}.ErrorHandler(c, err)
			return
		}
	default:
		statusCode = http.StatusInternalServerError
		response = gin.H{"error": "Internal server error"}
	}

	c.JSON(statusCode, response)
	c.Abort()
}

func getFeaturesByPlan(planType string) []string {
	switch planType {
	case multitenant.PlanBasic:
		return []string{"Basic dashboard", "Email support", "2 users", "3 projects"}
	case multitenant.PlanPro:
		return []string{"Advanced dashboard", "Priority support", "10 users", "25 projects", "API access"}
	case multitenant.PlanEnterprise:
		return []string{"Full dashboard", "24/7 support", "Unlimited users", "Unlimited projects", "Full API", "Custom integrations"}
	default:
		return []string{}
	}
}

func getPlanPrice(planType string) int {
	switch planType {
	case multitenant.PlanBasic:
		return 29
	case multitenant.PlanPro:
		return 99
	case multitenant.PlanEnterprise:
		return 299
	default:
		return 0
	}
}

func calculatePercentage(current, limit int) string {
	if limit <= 0 {
		return "unlimited"
	}
	percentage := float64(current) / float64(limit) * 100
	return fmt.Sprintf("%.1f%%", percentage)
}

func isValidUpgrade(currentPlan, newPlan string) bool {
	planOrder := map[string]int{
		multitenant.PlanBasic:      1,
		multitenant.PlanPro:        2,
		multitenant.PlanEnterprise: 3,
	}

	current, exists1 := planOrder[currentPlan]
	new, exists2 := planOrder[newPlan]

	return exists1 && exists2 && new > current
}

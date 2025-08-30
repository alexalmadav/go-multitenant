package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/alexalmadav/go-multitenant"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func main() {
	// Create a custom configuration with user-defined limits
	config := createCustomConfig()

	// Initialize the multi-tenant system
	mt, err := multitenant.New(config)
	if err != nil {
		log.Fatal("Failed to initialize multi-tenant system:", err)
	}
	defer mt.Close()

	// Demo: Add custom limits to existing plans
	demonstrateCustomLimits(mt)

	// Create example tenants
	if err := createExampleTenants(mt); err != nil {
		log.Fatal("Failed to create example tenants:", err)
	}

	// Setup Gin router
	r := gin.Default()

	// Public routes
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})

	// Tenant management routes
	setupTenantRoutes(r, mt)

	// API routes with limit enforcement
	setupAPIRoutes(r, mt)

	// Admin routes for limit management
	setupAdminRoutes(r, mt)

	fmt.Println("Starting flexible limits demo server on :8080")
	fmt.Println("Try these endpoints:")
	fmt.Println("  - http://localhost:8080/admin/schema - View limit schema")
	fmt.Println("  - http://localhost:8080/admin/plans - View all plan limits")
	fmt.Println("  - http://startup.localhost:8080/api/limits - Check current limits")
	fmt.Println("  - http://startup.localhost:8080/api/check/advanced_features - Check feature availability")

	log.Fatal(http.ListenAndServe(":8080", r))
}

func createCustomConfig() multitenant.Config {
	// Start with default config
	config := multitenant.DefaultConfig()

	// Customize database
	config.Database.DSN = "postgres://username:password@localhost:5432/flexible_limits_db?sslmode=disable"

	// Add custom limits to the schema
	schema := config.Limits.LimitSchema

	// Add industry-specific limits
	schema.AddDefinition(&tenant.LimitDefinition{
		Name:         "video_processing_minutes",
		DisplayName:  "Video Processing Minutes",
		Description:  "Monthly allowance for video processing",
		Type:         tenant.LimitTypeInt,
		DefaultValue: 60,
		Required:     false,
		Category:     "media",
	})

	schema.AddDefinition(&tenant.LimitDefinition{
		Name:         "ai_model_calls",
		DisplayName:  "AI Model API Calls",
		Description:  "Monthly AI model API call allowance",
		Type:         tenant.LimitTypeInt,
		DefaultValue: 1000,
		Required:     false,
		Category:     "ai",
	})

	schema.AddDefinition(&tenant.LimitDefinition{
		Name:         "custom_branding",
		DisplayName:  "Custom Branding",
		Description:  "Allow custom branding and white-labeling",
		Type:         tenant.LimitTypeBool,
		DefaultValue: false,
		Required:     false,
		Category:     "branding",
	})

	schema.AddDefinition(&tenant.LimitDefinition{
		Name:         "data_retention_years",
		DisplayName:  "Data Retention (Years)",
		Description:  "How long data is retained in years",
		Type:         tenant.LimitTypeFloat,
		DefaultValue: 1.0,
		Required:     false,
		Category:     "compliance",
	})

	schema.AddDefinition(&tenant.LimitDefinition{
		Name:         "concurrent_connections",
		DisplayName:  "Concurrent Connections",
		Description:  "Maximum concurrent WebSocket connections",
		Type:         tenant.LimitTypeInt,
		DefaultValue: 10,
		Required:     false,
		Category:     "performance",
	})

	// Create custom plans with the new limits
	startupLimits := make(tenant.FlexibleLimits)
	startupLimits.Set("max_users", tenant.LimitTypeInt, 3)
	startupLimits.Set("max_projects", tenant.LimitTypeInt, 5)
	startupLimits.Set("max_storage_gb", tenant.LimitTypeInt, 1)
	startupLimits.Set("api_calls_per_month", tenant.LimitTypeInt, 5000)
	startupLimits.Set("video_processing_minutes", tenant.LimitTypeInt, 30)
	startupLimits.Set("ai_model_calls", tenant.LimitTypeInt, 500)
	startupLimits.Set("advanced_features", tenant.LimitTypeBool, false)
	startupLimits.Set("custom_branding", tenant.LimitTypeBool, false)
	startupLimits.Set("data_retention_years", tenant.LimitTypeFloat, 1.0)
	startupLimits.Set("concurrent_connections", tenant.LimitTypeInt, 5)

	businessLimits := make(tenant.FlexibleLimits)
	businessLimits.Set("max_users", tenant.LimitTypeInt, 15)
	businessLimits.Set("max_projects", tenant.LimitTypeInt, 50)
	businessLimits.Set("max_storage_gb", tenant.LimitTypeInt, 25)
	businessLimits.Set("api_calls_per_month", tenant.LimitTypeInt, 50000)
	businessLimits.Set("video_processing_minutes", tenant.LimitTypeInt, 300)
	businessLimits.Set("ai_model_calls", tenant.LimitTypeInt, 5000)
	businessLimits.Set("advanced_features", tenant.LimitTypeBool, true)
	businessLimits.Set("custom_branding", tenant.LimitTypeBool, true)
	businessLimits.Set("priority_support", tenant.LimitTypeBool, true)
	businessLimits.Set("data_retention_years", tenant.LimitTypeFloat, 3.0)
	businessLimits.Set("concurrent_connections", tenant.LimitTypeInt, 25)

	scaleLimits := make(tenant.FlexibleLimits)
	scaleLimits.Set("max_users", tenant.LimitTypeInt, -1)    // unlimited
	scaleLimits.Set("max_projects", tenant.LimitTypeInt, -1) // unlimited
	scaleLimits.Set("max_storage_gb", tenant.LimitTypeInt, 500)
	scaleLimits.Set("api_calls_per_month", tenant.LimitTypeInt, -1) // unlimited
	scaleLimits.Set("video_processing_minutes", tenant.LimitTypeInt, 2000)
	scaleLimits.Set("ai_model_calls", tenant.LimitTypeInt, 50000)
	scaleLimits.Set("advanced_features", tenant.LimitTypeBool, true)
	scaleLimits.Set("custom_branding", tenant.LimitTypeBool, true)
	scaleLimits.Set("priority_support", tenant.LimitTypeBool, true)
	scaleLimits.Set("dedicated_support", tenant.LimitTypeBool, true)
	scaleLimits.Set("custom_integrations", tenant.LimitTypeBool, true)
	scaleLimits.Set("data_retention_years", tenant.LimitTypeFloat, 7.0)
	scaleLimits.Set("concurrent_connections", tenant.LimitTypeInt, 100)

	// Update plan limits
	config.Limits.PlanLimits = map[string]tenant.FlexibleLimits{
		"startup":  startupLimits,
		"business": businessLimits,
		"scale":    scaleLimits,
	}

	return config
}

func demonstrateCustomLimits(mt *multitenant.MultiTenant) {
	// TODO: This functionality requires exposing the limit checker from the manager
	// The Manager interface would need to include a GetLimitChecker() method

	fmt.Println("Custom limits functionality demonstration (not yet implemented)")
	fmt.Println("This would allow adding runtime limits like:")
	fmt.Println("- Custom API endpoint limits for specific plans")
	fmt.Println("- Feature toggles (beta features, etc.)")
	fmt.Println("- Dynamic limit adjustments")

	// Example of what this would look like once properly exposed:
	/*
		limitChecker := mt.Manager.GetLimitChecker() // This method would need to be added

		// Add a runtime custom limit
		err := limitChecker.AddLimit("startup", "custom_api_endpoints", tenant.LimitTypeInt, 3)
		if err != nil {
			log.Printf("Failed to add custom limit: %v", err)
		}

		// Add a feature toggle
		err = limitChecker.AddLimit("business", "beta_features", tenant.LimitTypeBool, true)
		if err != nil {
			log.Printf("Failed to add beta features limit: %v", err)
		}
	*/
}

func createExampleTenants(mt *multitenant.MultiTenant) error {
	ctx := context.Background()

	tenants := []*multitenant.Tenant{
		{
			ID:        uuid.New(),
			Name:      "Startup Corp",
			Subdomain: "startup",
			PlanType:  "startup",
			Status:    multitenant.StatusActive,
		},
		{
			ID:        uuid.New(),
			Name:      "Business Solutions Inc",
			Subdomain: "business",
			PlanType:  "business",
			Status:    multitenant.StatusActive,
		},
		{
			ID:        uuid.New(),
			Name:      "Scale Enterprises",
			Subdomain: "scale",
			PlanType:  "scale",
			Status:    multitenant.StatusActive,
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

func setupTenantRoutes(r *gin.Engine, mt *multitenant.MultiTenant) {
	tenantGroup := r.Group("/tenants")
	{
		tenantGroup.GET("/", listAllTenants(mt))
		tenantGroup.POST("/", createTenant(mt))
		tenantGroup.GET("/:id/limits", getTenantLimits(mt))
		tenantGroup.PUT("/:id/plan", changeTenantPlan(mt))
	}
}

func setupAPIRoutes(r *gin.Engine, mt *multitenant.MultiTenant) {
	// These would normally use the multi-tenant middleware
	// For demo purposes, we'll simulate tenant context
	api := r.Group("/api")
	{
		api.Use(simulateTenantContext())
		api.GET("/limits", getCurrentLimits(mt))
		api.GET("/check/:limit", checkSpecificLimit(mt))
		api.POST("/consume/:limit", consumeLimit(mt))
		api.GET("/features", getAvailableFeatures(mt))
	}
}

func setupAdminRoutes(r *gin.Engine, mt *multitenant.MultiTenant) {
	admin := r.Group("/admin")
	{
		admin.GET("/schema", getLimitSchema(mt))
		admin.POST("/schema/limits", addLimitDefinition(mt))
		admin.GET("/plans", getAllPlanLimits(mt))
		admin.PUT("/plans/:plan/limits/:limit", updatePlanLimit(mt))
		admin.POST("/plans/:plan/limits", addPlanLimit(mt))
		admin.DELETE("/plans/:plan/limits/:limit", removePlanLimit(mt))
	}
}

// Middleware to simulate tenant context
func simulateTenantContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract subdomain from request (simplified)
		host := c.Request.Host
		if host == "startup.localhost:8080" {
			c.Set("tenant_subdomain", "startup")
		} else if host == "business.localhost:8080" {
			c.Set("tenant_subdomain", "business")
		} else if host == "scale.localhost:8080" {
			c.Set("tenant_subdomain", "scale")
		} else {
			c.Set("tenant_subdomain", "startup") // default
		}
		c.Next()
	}
}

// Handler implementations

func listAllTenants(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenants, total, err := mt.Manager.ListTenants(c.Request.Context(), 1, 10)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"tenants": tenants,
			"total":   total,
		})
	}
}

func createTenant(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Name      string `json:"name" binding:"required"`
			Subdomain string `json:"subdomain" binding:"required"`
			PlanType  string `json:"plan_type" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		tenant := &multitenant.Tenant{
			ID:        uuid.New(),
			Name:      req.Name,
			Subdomain: req.Subdomain,
			PlanType:  req.PlanType,
			Status:    multitenant.StatusPending,
		}

		if err := mt.Manager.CreateTenant(c.Request.Context(), tenant); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"tenant":  tenant,
			"message": "Tenant created successfully",
		})
	}
}

func getTenantLimits(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantIDStr := c.Param("id")
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

		// This would need to be exposed from the manager
		// For demo, we'll simulate getting limits
		c.JSON(http.StatusOK, gin.H{
			"tenant_id": tenantID,
			"plan_type": tenant.PlanType,
			"limits":    "Would show flexible limits here",
		})
	}
}

func changeTenantPlan(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantIDStr := c.Param("id")
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tenant ID"})
			return
		}

		var req struct {
			PlanType string `json:"plan_type" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		tenant, err := mt.Manager.GetTenant(c.Request.Context(), tenantID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Tenant not found"})
			return
		}

		oldPlan := tenant.PlanType
		tenant.PlanType = req.PlanType

		if err := mt.Manager.UpdateTenant(c.Request.Context(), tenant); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":  "Plan updated successfully",
			"old_plan": oldPlan,
			"new_plan": req.PlanType,
		})
	}
}

func getCurrentLimits(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		subdomain, _ := c.Get("tenant_subdomain")

		// Get tenant by subdomain
		tenant, err := mt.Manager.GetTenantBySubdomain(c.Request.Context(), subdomain.(string))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Tenant not found"})
			return
		}

		// This would need manager to expose limit checker
		// For demo, return simulated limits
		c.JSON(http.StatusOK, gin.H{
			"tenant":    tenant.Name,
			"plan_type": tenant.PlanType,
			"limits": gin.H{
				"max_users":                getSimulatedLimit(tenant.PlanType, "max_users"),
				"max_projects":             getSimulatedLimit(tenant.PlanType, "max_projects"),
				"api_calls_per_month":      getSimulatedLimit(tenant.PlanType, "api_calls_per_month"),
				"video_processing_minutes": getSimulatedLimit(tenant.PlanType, "video_processing_minutes"),
				"ai_model_calls":           getSimulatedLimit(tenant.PlanType, "ai_model_calls"),
				"advanced_features":        getSimulatedFeature(tenant.PlanType, "advanced_features"),
				"custom_branding":          getSimulatedFeature(tenant.PlanType, "custom_branding"),
				"concurrent_connections":   getSimulatedLimit(tenant.PlanType, "concurrent_connections"),
			},
		})
	}
}

func checkSpecificLimit(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		limitName := c.Param("limit")
		subdomain, _ := c.Get("tenant_subdomain")

		tenant, err := mt.Manager.GetTenantBySubdomain(c.Request.Context(), subdomain.(string))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Tenant not found"})
			return
		}

		// Simulate limit checking
		allowed := true
		limitValue := getSimulatedLimit(tenant.PlanType, limitName)
		currentUsage := 0 // Would get from usage tracker

		if limitValue > 0 && currentUsage >= limitValue {
			allowed = false
		}

		c.JSON(http.StatusOK, gin.H{
			"limit":         limitName,
			"allowed":       allowed,
			"current_value": limitValue,
			"usage":         currentUsage,
			"plan":          tenant.PlanType,
		})
	}
}

func consumeLimit(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		limitName := c.Param("limit")
		subdomain, _ := c.Get("tenant_subdomain")

		var req struct {
			Amount int `json:"amount" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		tenant, err := mt.Manager.GetTenantBySubdomain(c.Request.Context(), subdomain.(string))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Tenant not found"})
			return
		}

		// Simulate limit consumption
		// In real implementation, this would use the limit checker and usage tracker
		c.JSON(http.StatusOK, gin.H{
			"message":   fmt.Sprintf("Consumed %d units of %s", req.Amount, limitName),
			"tenant":    tenant.Name,
			"limit":     limitName,
			"consumed":  req.Amount,
			"remaining": getSimulatedLimit(tenant.PlanType, limitName) - req.Amount,
		})
	}
}

func getAvailableFeatures(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		subdomain, _ := c.Get("tenant_subdomain")

		tenant, err := mt.Manager.GetTenantBySubdomain(c.Request.Context(), subdomain.(string))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Tenant not found"})
			return
		}

		features := getFeaturesByPlan(tenant.PlanType)

		c.JSON(http.StatusOK, gin.H{
			"tenant":   tenant.Name,
			"plan":     tenant.PlanType,
			"features": features,
		})
	}
}

func getLimitSchema(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Would get this from the limit checker
		c.JSON(http.StatusOK, gin.H{
			"message": "Limit schema would be returned here",
			"note":    "This would show all available limit definitions with their types and descriptions",
		})
	}
}

func addLimitDefinition(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req tenant.LimitDefinition

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Would add to schema via limit checker
		c.JSON(http.StatusCreated, gin.H{
			"message":    "Limit definition added",
			"definition": req,
		})
	}
}

func getAllPlanLimits(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Would get all plan limits from limit checker
		plans := map[string]interface{}{
			"startup": gin.H{
				"max_users":                3,
				"max_projects":             5,
				"video_processing_minutes": 30,
				"advanced_features":        false,
			},
			"business": gin.H{
				"max_users":                15,
				"max_projects":             50,
				"video_processing_minutes": 300,
				"advanced_features":        true,
				"custom_branding":          true,
			},
			"scale": gin.H{
				"max_users":                -1,
				"max_projects":             -1,
				"video_processing_minutes": 2000,
				"advanced_features":        true,
				"custom_branding":          true,
				"dedicated_support":        true,
			},
		}

		c.JSON(http.StatusOK, gin.H{
			"plans": plans,
		})
	}
}

func updatePlanLimit(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		planType := c.Param("plan")
		limitName := c.Param("limit")

		var req struct {
			Value interface{} `json:"value" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Would update via limit checker
		c.JSON(http.StatusOK, gin.H{
			"message": fmt.Sprintf("Updated %s limit for %s plan", limitName, planType),
			"plan":    planType,
			"limit":   limitName,
			"value":   req.Value,
		})
	}
}

func addPlanLimit(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		planType := c.Param("plan")

		var req struct {
			Name  string      `json:"name" binding:"required"`
			Type  string      `json:"type" binding:"required"`
			Value interface{} `json:"value" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Would add via limit checker
		c.JSON(http.StatusCreated, gin.H{
			"message": fmt.Sprintf("Added %s limit to %s plan", req.Name, planType),
			"plan":    planType,
			"limit":   req.Name,
			"type":    req.Type,
			"value":   req.Value,
		})
	}
}

func removePlanLimit(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		planType := c.Param("plan")
		limitName := c.Param("limit")

		// Would remove via limit checker
		c.JSON(http.StatusOK, gin.H{
			"message": fmt.Sprintf("Removed %s limit from %s plan", limitName, planType),
			"plan":    planType,
			"limit":   limitName,
		})
	}
}

// Helper functions

func getSimulatedLimit(planType, limitName string) int {
	limits := map[string]map[string]int{
		"startup": {
			"max_users":                3,
			"max_projects":             5,
			"max_storage_gb":           1,
			"api_calls_per_month":      5000,
			"video_processing_minutes": 30,
			"ai_model_calls":           500,
			"concurrent_connections":   5,
		},
		"business": {
			"max_users":                15,
			"max_projects":             50,
			"max_storage_gb":           25,
			"api_calls_per_month":      50000,
			"video_processing_minutes": 300,
			"ai_model_calls":           5000,
			"concurrent_connections":   25,
		},
		"scale": {
			"max_users":                -1,
			"max_projects":             -1,
			"max_storage_gb":           500,
			"api_calls_per_month":      -1,
			"video_processing_minutes": 2000,
			"ai_model_calls":           50000,
			"concurrent_connections":   100,
		},
	}

	if planLimits, exists := limits[planType]; exists {
		if limit, exists := planLimits[limitName]; exists {
			return limit
		}
	}
	return 0
}

func getSimulatedFeature(planType, featureName string) bool {
	features := map[string]map[string]bool{
		"startup": {
			"advanced_features": false,
			"custom_branding":   false,
			"priority_support":  false,
		},
		"business": {
			"advanced_features": true,
			"custom_branding":   true,
			"priority_support":  true,
		},
		"scale": {
			"advanced_features":   true,
			"custom_branding":     true,
			"priority_support":    true,
			"dedicated_support":   true,
			"custom_integrations": true,
		},
	}

	if planFeatures, exists := features[planType]; exists {
		if feature, exists := planFeatures[featureName]; exists {
			return feature
		}
	}
	return false
}

func getFeaturesByPlan(planType string) []string {
	features := map[string][]string{
		"startup": {
			"Basic dashboard",
			"Email support",
			"Standard API access",
			"Basic video processing",
		},
		"business": {
			"Advanced dashboard",
			"Priority support",
			"Full API access",
			"Advanced video processing",
			"AI model integration",
			"Custom branding",
			"Webhook support",
		},
		"scale": {
			"Enterprise dashboard",
			"Dedicated support",
			"Unlimited API access",
			"Premium video processing",
			"Advanced AI models",
			"Full custom branding",
			"Custom integrations",
			"SLA guarantees",
			"Advanced analytics",
		},
	}

	if planFeatures, exists := features[planType]; exists {
		return planFeatures
	}
	return []string{}
}

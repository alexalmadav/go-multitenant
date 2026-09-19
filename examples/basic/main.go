package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/alexalmadav/go-multitenant"
	ginmiddleware "github.com/alexalmadav/go-multitenant/middleware/gin"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func main() {
	// Create configuration
	config := multitenant.DefaultConfig()

	// Configure database connection
	config.Database.DSN = "postgres://username:password@localhost:5432/multitenant_db?sslmode=disable"
	config.Database.MigrationsDir = "./migrations/tenant_migrations"

	// Configure tenant resolver
	config.Resolver.Strategy = multitenant.ResolverSubdomain
	config.Resolver.Domain = "example.com"

	// Configure logging
	config.Logger.Level = "info"
	config.Logger.Format = "console"

	// Initialize the multi-tenant system
	mt, err := multitenant.New(config)
	if err != nil {
		log.Fatal("Failed to initialize multi-tenant system:", err)
	}
	defer mt.Close()

	// Create some example tenants
	if err := createExampleTenants(mt); err != nil {
		log.Fatal("Failed to create example tenants:", err)
	}

	// Setup Gin router with multi-tenant middleware
	r := gin.Default()

	// Wrap the framework-neutral middleware with the Gin adapter.
	ginMw := ginmiddleware.NewMiddleware(mt.Manager, mt.Resolver, mt.GetLogger(), ginmiddleware.Config{
		SkipPaths: []string{"/health"},
	})

	// Global middleware for health checks
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})

	// Multi-tenant API routes
	api := r.Group("/api")
	{
		// Apply multi-tenant middleware
		api.Use(ginMw.ResolveTenant())
		api.Use(ginMw.ValidateTenant())
		api.Use(ginMw.EnforceLimits())
		api.Use(ginMw.SetTenantDB())
		api.Use(ginMw.LogAccess())

		// Tenant-specific routes
		api.GET("/info", getTenantInfo)
		api.GET("/projects", getProjects)
		api.POST("/projects", createProject)
		api.GET("/stats", getTenantStats)
	}

	// Admin routes (no tenant context needed)
	admin := r.Group("/admin")
	{
		admin.GET("/tenants", listTenants(mt))
		admin.POST("/tenants", createTenant(mt))
		admin.POST("/tenants/:id/provision", provisionTenant(mt))
	}

	fmt.Println("Starting server on :8080")
	fmt.Println("Try accessing:")
	fmt.Println("  - http://acme.localhost:8080/api/info")
	fmt.Println("  - http://globex.localhost:8080/api/info")
	fmt.Println("  - http://localhost:8080/admin/tenants")

	log.Fatal(http.ListenAndServe(":8080", r))
}

// createExampleTenants creates some example tenants for demonstration
func createExampleTenants(mt *multitenant.MultiTenant) error {
	ctx := context.Background()

	tenants := []*multitenant.Tenant{
		{
			ID:        uuid.New(),
			Name:      "Acme Corporation",
			Subdomain: "acme",
			Status:    multitenant.StatusActive,
		},
		{
			ID:        uuid.New(),
			Name:      "Globex Industries",
			Subdomain: "globex",
			Status:    multitenant.StatusActive,
		},
	}
	tenants[0].SetPlan(multitenant.PlanPro)
	tenants[1].SetPlan(multitenant.PlanBasic)

	for _, tenant := range tenants {
		// Check if tenant already exists
		existing, err := mt.Manager.GetTenantBySubdomain(ctx, tenant.Subdomain)
		if err == nil && existing != nil {
			fmt.Printf("Tenant %s already exists\n", tenant.Subdomain)
			continue
		}

		// Create tenant
		if err := mt.Manager.CreateTenant(ctx, tenant); err != nil {
			return fmt.Errorf("failed to create tenant %s: %w", tenant.Name, err)
		}

		// Provision tenant schema
		if err := mt.Manager.ProvisionTenant(ctx, tenant.ID); err != nil {
			return fmt.Errorf("failed to provision tenant %s: %w", tenant.Name, err)
		}

		fmt.Printf("Created and provisioned tenant: %s (%s)\n", tenant.Name, tenant.Subdomain)
	}

	return nil
}

// Tenant-specific handlers

func getTenantInfo(c *gin.Context) {
	// Use the full tenant record (not the lightweight request context) since
	// plan lives in Tenant.Metadata, not in the summary tenant.Context.
	tenant, exists := ginmiddleware.GetTenantFromGinContext(c)
	if !exists {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Tenant context not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"tenant_id": tenant.ID,
		"subdomain": tenant.Subdomain,
		"plan":      tenant.Plan(),
		"status":    tenant.Status,
		"message":   fmt.Sprintf("Hello from %s!", tenant.Subdomain),
	})
}

func getProjects(c *gin.Context) {
	// In a real application, you'd query the tenant-specific database
	// The middleware has already set the correct schema context

	c.JSON(http.StatusOK, gin.H{
		"projects": []gin.H{
			{"id": 1, "name": "Project Alpha", "status": "active"},
			{"id": 2, "name": "Project Beta", "status": "completed"},
		},
		"message": "Projects from tenant database",
	})
}

func createProject(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// In a real application, you'd insert into the tenant-specific database
	project := gin.H{
		"id":          uuid.New(),
		"name":        req.Name,
		"description": req.Description,
		"status":      "active",
		"created_at":  time.Now(),
	}

	c.JSON(http.StatusCreated, gin.H{
		"project": project,
		"message": "Project created in tenant database",
	})
}

func getTenantStats(c *gin.Context) {
	tenantID, exists := multitenant.GetTenantIDFromContext(c.Request.Context())
	if !exists {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Tenant ID not found"})
		return
	}

	// This would be a real call to get tenant statistics
	stats := gin.H{
		"tenant_id":     tenantID,
		"project_count": 5,
		"user_count":    12,
		"storage_used":  "2.5GB",
		"last_activity": time.Now().Format(time.RFC3339),
	}

	c.JSON(http.StatusOK, gin.H{
		"stats": stats,
	})
}

// Admin handlers

func listTenants(mt *multitenant.MultiTenant) gin.HandlerFunc {
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
			Plan      string `json:"plan"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.Plan == "" {
			req.Plan = multitenant.PlanBasic
		}

		tenant := &multitenant.Tenant{
			ID:        uuid.New(),
			Name:      req.Name,
			Subdomain: req.Subdomain,
			Status:    multitenant.StatusPending,
		}
		tenant.SetPlan(req.Plan)

		if err := mt.Manager.CreateTenant(c.Request.Context(), tenant); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"tenant":  tenant,
			"message": "Tenant created successfully. Use /admin/tenants/:id/provision to provision the schema.",
		})
	}
}

func provisionTenant(mt *multitenant.MultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantIDStr := c.Param("id")
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tenant ID"})
			return
		}

		if err := mt.Manager.ProvisionTenant(c.Request.Context(), tenantID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "Tenant provisioned successfully",
		})
	}
}

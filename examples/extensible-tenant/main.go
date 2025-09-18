package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"

	"github.com/alexalmadav/go-multitenant"
	"github.com/alexalmadav/go-multitenant/database/postgres"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func main() {
	// Create configuration
	config := multitenant.DefaultConfig()

	// Configure database connection
	config.Database.DSN = "postgres://username:password@localhost:5432/multitenant_extensible_db?sslmode=disable"
	config.Database.MigrationsDir = "./migrations"

	// Configure tenant resolver
	config.Resolver.Strategy = multitenant.ResolverSubdomain
	config.Resolver.Domain = "example.com"

	// Initialize the multi-tenant system with extensible repository
	mt, err := setupExtensibleMultiTenant(config)
	if err != nil {
		log.Fatal("Failed to initialize extensible multi-tenant system:", err)
	}
	defer mt.Close()

	// Create example tenants with Stripe integration
	if err := createExtensibleTenants(mt); err != nil {
		log.Fatal("Failed to create extensible tenants:", err)
	}

	// Setup Gin router
	r := gin.Default()

	// Health check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})

	// Admin routes for managing tenant metadata
	admin := r.Group("/admin")
	{
		admin.GET("/tenants", listExtensibleTenants(mt))
		admin.POST("/tenants", createExtensibleTenant(mt))
		admin.PUT("/tenants/:id/stripe", setStripeIntegration(mt))
		admin.PUT("/tenants/:id/branding", setBranding(mt))
		admin.GET("/tenants/:id/metadata", getTenantMetadata(mt))
		admin.PUT("/tenants/:id/metadata/:key", setTenantMetadataField(mt))
		admin.DELETE("/tenants/:id/metadata/:key", removeTenantMetadataField(mt))
		admin.GET("/tenants/by-stripe/:customer_id", getTenantByStripeCustomer(mt))
	}

	// Multi-tenant API routes
	api := r.Group("/api")
	{
		api.Use(mt.GinMiddleware.ResolveTenant())
		api.Use(mt.GinMiddleware.ValidateTenant())
		api.Use(mt.GinMiddleware.EnforceLimits())

		api.GET("/tenant-info", getExtendedTenantInfo(mt))
		api.GET("/branding", getTenantBranding(mt))
		api.GET("/integrations", getTenantIntegrations(mt))
	}

	// Stripe webhook simulation
	webhooks := r.Group("/webhooks")
	{
		webhooks.POST("/stripe", handleStripeWebhook(mt))
	}

	fmt.Println("Starting extensible multi-tenant server on :8080")
	fmt.Println("Try accessing:")
	fmt.Println("  - http://localhost:8080/admin/tenants")
	fmt.Println("  - http://acme.localhost:8080/api/tenant-info")
	fmt.Println("  - http://localhost:8080/admin/tenants/by-stripe/cus_example123")

	log.Fatal(http.ListenAndServe(":8080", r))
}

// ExtensibleMultiTenant wraps the base MultiTenant with extensible repository
type ExtensibleMultiTenant struct {
	*multitenant.MultiTenant
	ExtensibleRepo tenant.ExtensibleRepository
}

func setupExtensibleMultiTenant(config multitenant.Config) (*ExtensibleMultiTenant, error) {
	// Setup database connection manually to use extensible repository
	db, err := setupDatabase(config.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to setup database: %w", err)
	}

	// Setup logger
	logger, err := setupLogger(config.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to setup logger: %w", err)
	}

	// Create extensible repository
	extRepo := postgres.NewExtensibleRepository(db, logger)

	// Create master tables with metadata support
	if err := extRepo.CreateMasterTablesExtended(context.Background()); err != nil {
		logger.Warn("Failed to create extensible master tables - they may already exist", zap.Error(err))
	}

	// Create base MultiTenant instance
	mt, err := multitenant.New(config)
	if err != nil {
		return nil, err
	}

	return &ExtensibleMultiTenant{
		MultiTenant:    mt,
		ExtensibleRepo: extRepo,
	}, nil
}

func createExtensibleTenants(mt *ExtensibleMultiTenant) error {
	ctx := context.Background()

	tenants := []*tenant.ExtensibleTenant{
		{
			ID:         uuid.New(),
			Name:       "Acme Corporation",
			Subdomain:  "acme",
			PlanType:   multitenant.PlanPro,
			Status:     multitenant.StatusActive,
			SchemaName: "tenant_acme",
			Metadata:   make(tenant.TenantMetadata),
		},
		{
			ID:         uuid.New(),
			Name:       "Globex Industries",
			Subdomain:  "globex",
			PlanType:   multitenant.PlanEnterprise,
			Status:     multitenant.StatusActive,
			SchemaName: "tenant_globex",
			Metadata:   make(tenant.TenantMetadata),
		},
	}

	// Setup Acme with Stripe and branding
	acme := tenants[0]
	stripeExt := tenant.NewStripeExtension(acme.Metadata)
	stripeExt.SetCustomerID("cus_acme123")
	stripeExt.SetSubscriptionID("sub_acme456")

	brandingExt := tenant.NewBrandingExtension(acme.Metadata)
	brandingExt.SetLogoURL("https://acme.com/logo.png")
	brandingExt.SetTheme("blue")
	brandingExt.SetCustomDomain("app.acme.com")

	acme.Metadata.SetString(tenant.MetadataContactEmail, "admin@acme.com")
	acme.Metadata.SetString(tenant.MetadataTimezone, "America/New_York")

	// Setup Globex with different configuration
	globex := tenants[1]
	stripeExtGlobex := tenant.NewStripeExtension(globex.Metadata)
	stripeExtGlobex.SetCustomerID("cus_globex789")

	brandingExtGlobex := tenant.NewBrandingExtension(globex.Metadata)
	brandingExtGlobex.SetLogoURL("https://globex.com/logo.svg")
	brandingExtGlobex.SetTheme("green")

	globex.Metadata.SetString(tenant.MetadataContactEmail, "support@globex.com")
	globex.Metadata.SetString(tenant.MetadataTimezone, "Europe/London")
	globex.Metadata.SetString(tenant.MetadataLanguage, "en-GB")
	globex.Metadata.SetBool("enterprise_features", true)

	for _, tenant := range tenants {
		// Check if tenant already exists
		existing, err := mt.ExtensibleRepo.GetExtendedBySubdomain(ctx, tenant.Subdomain)
		if err == nil && existing != nil {
			fmt.Printf("Extensible tenant %s already exists\n", tenant.Subdomain)
			continue
		}

		// Create tenant
		if err := mt.ExtensibleRepo.CreateExtended(ctx, tenant); err != nil {
			return fmt.Errorf("failed to create extensible tenant %s: %w", tenant.Name, err)
		}

		// Provision tenant schema using base manager
		if err := mt.Manager.ProvisionTenant(ctx, tenant.ID); err != nil {
			return fmt.Errorf("failed to provision tenant %s: %w", tenant.Name, err)
		}

		fmt.Printf("Created extensible tenant: %s (%s) with %d metadata fields\n",
			tenant.Name, tenant.Subdomain, len(tenant.Metadata))
	}

	return nil
}

// Handlers

func listExtensibleTenants(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenants, total, err := mt.ExtensibleRepo.ListExtended(c.Request.Context(), 1, 10)
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

func createExtensibleTenant(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Name      string                 `json:"name" binding:"required"`
			Subdomain string                 `json:"subdomain" binding:"required"`
			PlanType  string                 `json:"plan_type"`
			Metadata  map[string]interface{} `json:"metadata"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.PlanType == "" {
			req.PlanType = multitenant.PlanBasic
		}

		metadata := make(tenant.TenantMetadata)
		if req.Metadata != nil {
			for k, v := range req.Metadata {
				metadata[k] = v
			}
		}

		extTenant := &tenant.ExtensibleTenant{
			ID:         uuid.New(),
			Name:       req.Name,
			Subdomain:  req.Subdomain,
			PlanType:   req.PlanType,
			Status:     multitenant.StatusPending,
			SchemaName: fmt.Sprintf("tenant_%s", req.Subdomain),
			Metadata:   metadata,
		}

		if err := mt.ExtensibleRepo.CreateExtended(c.Request.Context(), extTenant); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"tenant":  extTenant,
			"message": "Extensible tenant created successfully",
		})
	}
}

func setStripeIntegration(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantIDStr := c.Param("id")
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tenant ID"})
			return
		}

		var req struct {
			CustomerID     string `json:"customer_id" binding:"required"`
			SubscriptionID string `json:"subscription_id,omitempty"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Set Stripe customer ID
		if err := mt.ExtensibleRepo.UpdateMetadataField(c.Request.Context(), tenantID,
			tenant.MetadataStripeCustomerID, req.CustomerID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Set subscription ID if provided
		if req.SubscriptionID != "" {
			if err := mt.ExtensibleRepo.UpdateMetadataField(c.Request.Context(), tenantID,
				tenant.MetadataStripeSubscriptionID, req.SubscriptionID); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"message":         "Stripe integration updated",
			"customer_id":     req.CustomerID,
			"subscription_id": req.SubscriptionID,
		})
	}
}

func setBranding(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantIDStr := c.Param("id")
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tenant ID"})
			return
		}

		var req struct {
			LogoURL      string `json:"logo_url,omitempty"`
			Theme        string `json:"theme,omitempty"`
			CustomDomain string `json:"custom_domain,omitempty"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Update branding fields
		if req.LogoURL != "" {
			if err := mt.ExtensibleRepo.UpdateMetadataField(c.Request.Context(), tenantID,
				tenant.MetadataLogoURL, req.LogoURL); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}

		if req.Theme != "" {
			if err := mt.ExtensibleRepo.UpdateMetadataField(c.Request.Context(), tenantID,
				tenant.MetadataTheme, req.Theme); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}

		if req.CustomDomain != "" {
			if err := mt.ExtensibleRepo.UpdateMetadataField(c.Request.Context(), tenantID,
				tenant.MetadataCustomDomain, req.CustomDomain); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "Branding updated successfully",
		})
	}
}

func getTenantMetadata(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantIDStr := c.Param("id")
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tenant ID"})
			return
		}

		metadata, err := mt.ExtensibleRepo.GetMetadata(c.Request.Context(), tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"tenant_id": tenantID,
			"metadata":  metadata,
		})
	}
}

func setTenantMetadataField(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantIDStr := c.Param("id")
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tenant ID"})
			return
		}

		key := c.Param("key")
		var req struct {
			Value interface{} `json:"value" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if err := mt.ExtensibleRepo.UpdateMetadataField(c.Request.Context(), tenantID, key, req.Value); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "Metadata field updated",
			"key":     key,
			"value":   req.Value,
		})
	}
}

func removeTenantMetadataField(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantIDStr := c.Param("id")
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tenant ID"})
			return
		}

		key := c.Param("key")
		if err := mt.ExtensibleRepo.RemoveMetadataField(c.Request.Context(), tenantID, key); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "Metadata field removed",
			"key":     key,
		})
	}
}

func getTenantByStripeCustomer(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		customerID := c.Param("customer_id")

		tenants, err := mt.ExtensibleRepo.FindByMetadata(c.Request.Context(),
			tenant.MetadataStripeCustomerID, customerID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if len(tenants) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "Tenant not found for Stripe customer"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"tenant":      tenants[0],
			"customer_id": customerID,
		})
	}
}

func getExtendedTenantInfo(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantCtx, exists := multitenant.GetTenantFromContext(c.Request.Context())
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Tenant context not found"})
			return
		}

		// Get extended tenant info
		extTenant, err := mt.ExtensibleRepo.GetExtendedByID(c.Request.Context(), tenantCtx.TenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"tenant_id":      extTenant.ID,
			"name":           extTenant.Name,
			"subdomain":      extTenant.Subdomain,
			"plan_type":      extTenant.PlanType,
			"status":         extTenant.Status,
			"metadata":       extTenant.Metadata,
			"metadata_count": len(extTenant.Metadata),
		})
	}
}

func getTenantBranding(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantCtx, exists := multitenant.GetTenantFromContext(c.Request.Context())
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Tenant context not found"})
			return
		}

		metadata, err := mt.ExtensibleRepo.GetMetadata(c.Request.Context(), tenantCtx.TenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		branding := tenant.NewBrandingExtension(metadata)
		logoURL, _ := branding.GetLogoURL()
		theme, _ := branding.GetTheme()
		customDomain, _ := branding.GetCustomDomain()

		c.JSON(http.StatusOK, gin.H{
			"logo_url":      logoURL,
			"theme":         theme,
			"custom_domain": customDomain,
		})
	}
}

func getTenantIntegrations(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantCtx, exists := multitenant.GetTenantFromContext(c.Request.Context())
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Tenant context not found"})
			return
		}

		metadata, err := mt.ExtensibleRepo.GetMetadata(c.Request.Context(), tenantCtx.TenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		stripe := tenant.NewStripeExtension(metadata)
		customerID, hasCustomer := stripe.GetCustomerID()
		subscriptionID, hasSubscription := stripe.GetSubscriptionID()

		c.JSON(http.StatusOK, gin.H{
			"stripe": gin.H{
				"enabled":          stripe.HasStripeIntegration(),
				"customer_id":      customerID,
				"subscription_id":  subscriptionID,
				"has_customer":     hasCustomer,
				"has_subscription": hasSubscription,
			},
		})
	}
}

func handleStripeWebhook(mt *ExtensibleMultiTenant) gin.HandlerFunc {
	return func(c *gin.Context) {
		var webhook struct {
			Type string `json:"type"`
			Data struct {
				Object struct {
					Customer string `json:"customer"`
					ID       string `json:"id"`
				} `json:"object"`
			} `json:"data"`
		}

		if err := c.ShouldBindJSON(&webhook); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Simulate handling different webhook types
		switch webhook.Type {
		case "customer.subscription.created":
			tenants, err := mt.ExtensibleRepo.FindByMetadata(c.Request.Context(),
				tenant.MetadataStripeCustomerID, webhook.Data.Object.Customer)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			if len(tenants) > 0 {
				tenantObj := tenants[0]
				stripeExt := tenant.NewStripeExtension(tenantObj.Metadata)
				stripeExt.SetSubscriptionID(webhook.Data.Object.ID)

				if err := mt.ExtensibleRepo.UpdateExtended(c.Request.Context(), tenantObj); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}

				c.JSON(http.StatusOK, gin.H{
					"message":         "Subscription created",
					"tenant_id":       tenantObj.ID,
					"subscription_id": webhook.Data.Object.ID,
				})
				return
			}

		case "customer.subscription.deleted":
			tenants, err := mt.ExtensibleRepo.FindByMetadata(c.Request.Context(),
				tenant.MetadataStripeCustomerID, webhook.Data.Object.Customer)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			if len(tenants) > 0 {
				tenantObj := tenants[0]
				if err := mt.ExtensibleRepo.RemoveMetadataField(c.Request.Context(),
					tenantObj.ID, tenant.MetadataStripeSubscriptionID); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}

				c.JSON(http.StatusOK, gin.H{
					"message":   "Subscription deleted",
					"tenant_id": tenantObj.ID,
				})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "Webhook received"})
	}
}

// Helper functions

func setupDatabase(config tenant.DatabaseConfig) (*sql.DB, error) {
	db, err := sql.Open(config.Driver, config.DSN)
	if err != nil {
		return nil, err
	}

	// Set connection pool settings
	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)
	db.SetConnMaxIdleTime(config.ConnMaxIdleTime)

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

func setupLogger(config tenant.LoggerConfig) (*zap.Logger, error) {
	var logger *zap.Logger
	var err error

	if config.Format == "console" {
		logger, err = zap.NewDevelopment()
	} else {
		logger, err = zap.NewProduction()
	}

	if err != nil {
		return nil, err
	}

	// Set log level
	switch config.Level {
	case "debug":
		// Development config sets debug by default
	case "info":
		// Default for both configs
	case "warn":
		logger = logger.WithOptions(zap.IncreaseLevel(zap.WarnLevel))
	case "error":
		logger = logger.WithOptions(zap.IncreaseLevel(zap.ErrorLevel))
	}

	return logger, nil
}

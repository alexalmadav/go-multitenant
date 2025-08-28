// Package multitenant provides a comprehensive multi-tenant solution for Go applications
// using a schema-per-tenant PostgreSQL architecture.
package multitenant

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/alexalmadav/go-multitenant/database"
	"github.com/alexalmadav/go-multitenant/database/postgres"
	ginmiddleware "github.com/alexalmadav/go-multitenant/middleware/gin"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
	_ "github.com/lib/pq"
)

// MultiTenant is the main struct that provides all multi-tenant functionality
type MultiTenant struct {
	Manager       tenant.Manager
	Resolver      tenant.Resolver
	GinMiddleware *ginmiddleware.Middleware
	db            *sql.DB
	logger        *zap.Logger
}

// New creates a new MultiTenant instance with the provided configuration
func New(config tenant.Config) (*MultiTenant, error) {
	// Setup logger
	logger, err := setupLogger(config.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to setup logger: %w", err)
	}

	// Setup database connection
	db, err := setupDatabase(config.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to setup database: %w", err)
	}

	// Create repository
	repository := postgres.NewRepository(db, logger)

	// Create master tables
	if err := repository.CreateMasterTables(context.Background()); err != nil {
		logger.Warn("Failed to create master tables - they may already exist", zap.Error(err))
	}

	// Create schema manager
	schemaManager := database.NewSchemaManager(db, logger, config.Database.SchemaPrefix)

	// Create migration manager (placeholder for now)
	migrationMgr := newMigrationManager(db, logger)

	// Create limit checker
	limitChecker := newLimitChecker(config.Limits, repository, logger)

	// Create tenant manager
	manager := tenant.NewManager(config, db, repository, schemaManager, migrationMgr, limitChecker, logger)

	// Create resolver
	resolver := tenant.NewResolver(config.Resolver, repository, logger)

	// Create Gin middleware
	ginConfig := ginmiddleware.Config{
		SkipPaths: []string{"/health", "/metrics", "/api/public/"},
		RequireAuthentication: true,
	}
	ginMw := ginmiddleware.NewMiddleware(manager, resolver, logger, ginConfig)

	return &MultiTenant{
		Manager:       manager,
		Resolver:      resolver,
		GinMiddleware: ginMw,
		db:            db,
		logger:        logger,
	}, nil
}

// Close closes all resources
func (mt *MultiTenant) Close() error {
	if err := mt.Manager.Close(); err != nil {
		mt.logger.Error("Failed to close manager", zap.Error(err))
	}

	if err := mt.db.Close(); err != nil {
		mt.logger.Error("Failed to close database", zap.Error(err))
		return err
	}

	return nil
}

// GetDatabase returns the database connection
func (mt *MultiTenant) GetDatabase() *sql.DB {
	return mt.db
}

// GetLogger returns the logger instance
func (mt *MultiTenant) GetLogger() *zap.Logger {
	return mt.logger
}

// setupLogger creates a logger based on configuration
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
		// Zap production config already sets info level
		if config.Format == "console" {
			// Development config sets debug by default
		}
	case "info":
		// Default for both configs
	case "warn":
		logger = logger.WithOptions(zap.IncreaseLevel(zap.WarnLevel))
	case "error":
		logger = logger.WithOptions(zap.IncreaseLevel(zap.ErrorLevel))
	}

	return logger, nil
}

// setupDatabase creates a database connection based on configuration
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

// Helper functions for creating components

// migrationManager is a placeholder implementation of tenant.MigrationManager
type migrationManager struct {
	db     *sql.DB
	logger *zap.Logger
}

func newMigrationManager(db *sql.DB, logger *zap.Logger) tenant.MigrationManager {
	return &migrationManager{
		db:     db,
		logger: logger.Named("migration"),
	}
}

func (m *migrationManager) ApplyMigration(ctx context.Context, tenantID uuid.UUID, migration *tenant.Migration) error {
	// TODO: Implement migration application
	return fmt.Errorf("not implemented")
}

func (m *migrationManager) RollbackMigration(ctx context.Context, tenantID uuid.UUID, version string) error {
	// TODO: Implement migration rollback
	return fmt.Errorf("not implemented")
}

func (m *migrationManager) ApplyToAllTenants(ctx context.Context, migration *tenant.Migration) error {
	// TODO: Implement bulk migration
	return fmt.Errorf("not implemented")
}

func (m *migrationManager) GetAppliedMigrations(ctx context.Context, tenantID uuid.UUID) ([]*tenant.Migration, error) {
	// TODO: Implement migration listing
	return nil, fmt.Errorf("not implemented")
}

func (m *migrationManager) IsMigrationApplied(ctx context.Context, tenantID uuid.UUID, version string) (bool, error) {
	// TODO: Implement migration check
	return false, fmt.Errorf("not implemented")
}

// limitChecker implements tenant.LimitChecker
type limitChecker struct {
	config     tenant.LimitsConfig
	repository tenant.Repository
	logger     *zap.Logger
}

func newLimitChecker(config tenant.LimitsConfig, repository tenant.Repository, logger *zap.Logger) tenant.LimitChecker {
	return &limitChecker{
		config:     config,
		repository: repository,
		logger:     logger.Named("limits"),
	}
}

func (lc *limitChecker) CheckUserLimit(ctx context.Context, tenantID uuid.UUID) error {
	if !lc.config.EnforceLimits {
		return nil
	}
	// TODO: Implement user limit checking
	return nil
}

func (lc *limitChecker) CheckProjectLimit(ctx context.Context, tenantID uuid.UUID) error {
	if !lc.config.EnforceLimits {
		return nil
	}
	// TODO: Implement project limit checking
	return nil
}

func (lc *limitChecker) CheckStorageLimit(ctx context.Context, tenantID uuid.UUID) error {
	if !lc.config.EnforceLimits {
		return nil
	}
	// TODO: Implement storage limit checking
	return nil
}

func (lc *limitChecker) CheckAllLimits(ctx context.Context, tenantID uuid.UUID) error {
	if !lc.config.EnforceLimits {
		return nil
	}

	if err := lc.CheckUserLimit(ctx, tenantID); err != nil {
		return err
	}

	if err := lc.CheckProjectLimit(ctx, tenantID); err != nil {
		return err
	}

	if err := lc.CheckStorageLimit(ctx, tenantID); err != nil {
		return err
	}

	return nil
}

func (lc *limitChecker) GetLimitsForPlan(planType string) *tenant.Limits {
	if limits, exists := lc.config.PlanLimits[planType]; exists {
		return limits
	}
	
	// Return basic plan limits as default
	if limits, exists := lc.config.PlanLimits[tenant.PlanBasic]; exists {
		return limits
	}
	
	// Fallback to hardcoded basic limits
	return &tenant.Limits{
		MaxUsers:     5,
		MaxProjects:  10,
		MaxStorageGB: 1,
	}
}

// Re-export key types and functions for convenience
type (
	Tenant     = tenant.Tenant
	Context    = tenant.Context
	Config     = tenant.Config
	Manager    = tenant.Manager
	Resolver   = tenant.Resolver
	Limits     = tenant.Limits
	Stats      = tenant.Stats
	Migration  = tenant.Migration
)

// Re-export key constants
const (
	StatusActive    = tenant.StatusActive
	StatusSuspended = tenant.StatusSuspended
	StatusPending   = tenant.StatusPending
	StatusCancelled = tenant.StatusCancelled

	PlanBasic      = tenant.PlanBasic
	PlanPro        = tenant.PlanPro
	PlanEnterprise = tenant.PlanEnterprise

	ResolverSubdomain = tenant.ResolverSubdomain
	ResolverPath      = tenant.ResolverPath
	ResolverHeader    = tenant.ResolverHeader
)

// Re-export helper functions
var (
	DefaultConfig         = tenant.DefaultConfig
	GetTenantFromContext  = tenant.GetTenantFromContext
	GetTenantIDFromContext = tenant.GetTenantIDFromContext
)

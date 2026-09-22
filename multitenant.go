// Package multitenant provides a comprehensive multi-tenant solution for Go applications
// using a schema-per-tenant PostgreSQL architecture.
package multitenant

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/alexalmadav/go-multitenant/database"
	"github.com/alexalmadav/go-multitenant/database/postgres"
	"github.com/alexalmadav/go-multitenant/limits"
	"github.com/alexalmadav/go-multitenant/middleware/httpmw"
	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

// Config configures New. Embed tenant.Config for the core and set Limits to
// enable plan-limit enforcement; nil Limits means no limits are checked.
type Config struct {
	tenant.Config
	Limits *limits.Config
	// Membership authorises an authenticated subject for the resolved tenant.
	// When set, HTTPMiddleware.Standard enforces it. Nil leaves the check out
	// of Standard; HTTPMiddleware.RequireMembership then denies every
	// request, which is deliberate — see package httpmw.
	Membership tenant.Membership
	// SkipPaths are path prefixes whose requests bypass tenant handling. A
	// nil slice keeps the default, []string{"/health", "/metrics",
	// "/api/public/"}; a non-nil empty slice skips nothing. These prefixes
	// bypass the membership check as well as tenant resolution, so a prefix
	// listed here is an authorization decision, not only a routing one.
	SkipPaths []string
	// SkipHosts are hosts whose requests bypass tenant handling, matched
	// against the request host without its port and ignoring case. Use it for
	// an origin that serves no tenant, such as a single sign-on host.
	//
	// Warning: the request host is client-controlled, so only use SkipHosts
	// where the front door pins it; see httpmw.Config.SkipHosts for the full
	// caveat.
	SkipHosts []string
}

// DefaultConfig returns the core defaults and no limits.
func DefaultConfig() Config {
	return Config{Config: tenant.DefaultConfig()}
}

// MultiTenant is the main struct that provides all multi-tenant functionality
type MultiTenant struct {
	Manager        tenant.Manager
	Resolver       tenant.Resolver
	Migrations     tenant.MigrationManager
	HTTPMiddleware *httpmw.Middleware
	// Limits is the limit checker built from Config.Limits, or nil when no
	// limits were configured. Use it to check limits, read usage, and adjust
	// plans at runtime.
	Limits limits.Checker
	db     *sql.DB
	logger *zap.Logger
}

// New creates a new MultiTenant instance with the provided configuration
func New(config Config) (*MultiTenant, error) {
	// Setup logger
	logger, err := setupLogger(config.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to setup logger: %w", err)
	}

	// Validate the migrations directory early so a typo is visible at startup.
	if dir := config.Database.MigrationsDir; dir == "" {
		logger.Warn("MigrationsDir is not set; newly provisioned tenants will have an empty schema")
	} else if info, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("migrations directory %q: %w", dir, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("migrations directory %q is not a directory", dir)
	}

	// Setup database connection
	db, err := setupDatabase(config.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to setup database: %w", err)
	}

	// Create repository
	repository := postgres.NewRepository(db, logger)

	// Create master tables. This also runs the v0.6 -> v0.7 metadata-column
	// upgrade (ALTER TABLE ... ADD COLUMN IF NOT EXISTS metadata); failing
	// here silently would leave a *MultiTenant whose every tenant read fails,
	// so treat it as fatal rather than logging and continuing.
	if err := repository.CreateMasterTables(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create master tables: %w", err)
	}

	// Create schema manager
	schemaManager := database.NewSchemaManager(db, logger, config.Database.SchemaPrefix)

	// Create migration manager
	// Note: Applications should specify their own migrations directory path
	migrationMgr := database.NewMigrationManager(db, logger, config.Database.MigrationsDir, schemaManager, repository)

	// Create tenant manager
	manager := tenant.NewManager(config.Config, db, repository, schemaManager, migrationMgr, logger)

	// Create resolver
	resolver := tenant.NewResolver(config.Resolver, repository, logger)

	// Limits are optional. When configured, the checker gets a usage tracker
	// that counts rows in the tables named by config.Limits.UsageTables;
	// applications can replace it with Limits.SetUsageTracker.
	var checker limits.Checker
	var mwOpts []httpmw.Option
	if config.Limits != nil {
		checker = limits.NewChecker(*config.Limits, repository, logger)
		tracker, err := postgres.NewUsageTracker(db, schemaManager, config.Limits.UsageTables, logger)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to configure usage tracker: %w", err)
		}
		checker.SetUsageTracker(tracker)
		mwOpts = append(mwOpts, httpmw.WithLimits(checker))
	}
	if config.Membership != nil {
		mwOpts = append(mwOpts, httpmw.WithMembership(config.Membership))
	}

	// Framework-neutral middleware. Gin users wrap it with the adapter in
	// github.com/alexalmadav/go-multitenant/middleware/gin. Only a nil
	// SkipPaths takes the default; an explicitly empty slice skips nothing.
	skipPaths := config.SkipPaths
	if skipPaths == nil {
		skipPaths = []string{"/health", "/metrics", "/api/public/"}
	}
	httpMw := httpmw.New(manager, resolver, logger, httpmw.Config{
		SkipPaths: skipPaths,
		SkipHosts: config.SkipHosts,
	}, mwOpts...)

	return &MultiTenant{
		Manager:        manager,
		Resolver:       resolver,
		Migrations:     migrationMgr,
		HTTPMiddleware: httpMw,
		Limits:         checker,
		db:             db,
		logger:         logger,
	}, nil
}

// Close closes all resources
func (mt *MultiTenant) Close() error {
	if mt.Manager != nil {
		if err := mt.Manager.Close(); err != nil {
			mt.logger.Error("Failed to close manager", zap.Error(err))
		}
	}

	if mt.db != nil {
		if err := mt.db.Close(); err != nil {
			mt.logger.Error("Failed to close database", zap.Error(err))
			return err
		}
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

// setupDatabase creates a database connection based on configuration.
// It uses pgx with simple protocol to avoid unnamed prepared statement conflicts
// when running behind PgBouncer in transaction-pooling mode.
func setupDatabase(config tenant.DatabaseConfig) (*sql.DB, error) {
	connConfig, err := pgx.ParseConfig(config.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database DSN: %w", err)
	}
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	db := stdlib.OpenDB(*connConfig)

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

// Re-export key types and functions for convenience
type (
	Tenant         = tenant.Tenant
	Context        = tenant.Context
	Manager        = tenant.Manager
	Resolver       = tenant.Resolver
	Stats          = tenant.Stats
	Migration      = tenant.Migration
	TenantMetadata = tenant.TenantMetadata

	TenantError     = tenant.TenantError
	ValidationError = tenant.ValidationError

	Hook      = tenant.Hook
	BaseHook  = tenant.BaseHook
	HookError = tenant.HookError
)

// Re-export key constants
const (
	StatusActive    = tenant.StatusActive
	StatusSuspended = tenant.StatusSuspended
	StatusPending   = tenant.StatusPending
	StatusCancelled = tenant.StatusCancelled

	ResolverSubdomain = tenant.ResolverSubdomain
	ResolverPath      = tenant.ResolverPath
	ResolverHeader    = tenant.ResolverHeader
)

// Re-export helper functions
var (
	GetTenantFromContext   = tenant.GetTenantFromContext
	GetTenantIDFromContext = tenant.GetTenantIDFromContext
	NewStripeExtension     = tenant.NewStripeExtension
	NewBrandingExtension   = tenant.NewBrandingExtension
)

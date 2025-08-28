package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// resolver implements the Resolver interface
type resolver struct {
	config     ResolverConfig
	repository Repository
	logger     *zap.Logger
}

// NewResolver creates a new tenant resolver
func NewResolver(config ResolverConfig, repository Repository, logger *zap.Logger) Resolver {
	return &resolver{
		config:     config,
		repository: repository,
		logger:     logger.Named("resolver"),
	}
}

// ResolveTenant resolves tenant from HTTP request based on configured strategy
func (r *resolver) ResolveTenant(ctx context.Context, req *http.Request) (uuid.UUID, error) {
	var subdomain string
	var err error

	switch r.config.Strategy {
	case ResolverSubdomain:
		subdomain, err = r.ExtractFromSubdomain(req.Host)
	case ResolverPath:
		subdomain, err = r.ExtractFromPath(req.URL.Path)
	case ResolverHeader:
		subdomain, err = r.ExtractFromHeader(req)
	default:
		return uuid.UUID{}, fmt.Errorf("unknown resolver strategy: %s", r.config.Strategy)
	}

	if err != nil {
		return uuid.UUID{}, err
	}

	// Get tenant by subdomain
	tenant, err := r.repository.GetBySubdomain(ctx, subdomain)
	if err != nil {
		r.logger.Debug("Failed to find tenant by subdomain",
			zap.String("subdomain", subdomain),
			zap.Error(err))
		return uuid.UUID{}, fmt.Errorf("tenant not found for subdomain: %s", subdomain)
	}

	r.logger.Debug("Resolved tenant",
		zap.String("subdomain", subdomain),
		zap.String("tenant_id", tenant.ID.String()),
		zap.String("strategy", r.config.Strategy))

	return tenant.ID, nil
}

// ExtractFromSubdomain extracts tenant subdomain from host
func (r *resolver) ExtractFromSubdomain(host string) (string, error) {
	if host == "" {
		return "", errors.New("empty host")
	}

	// Remove port if present
	if colonIndex := strings.Index(host, ":"); colonIndex != -1 {
		host = host[:colonIndex]
	}

	// Extract subdomain from host (e.g., "tenant.domain.com" -> "tenant")
	parts := strings.Split(host, ".")

	// Need at least subdomain.domain.tld
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid host format: %s", host)
	}

	subdomain := parts[0]

	// Check for reserved subdomains
	for _, reserved := range r.config.ReservedSubdomain {
		if strings.EqualFold(subdomain, reserved) {
			return "", fmt.Errorf("reserved subdomain: %s", subdomain)
		}
	}

	// Validate subdomain format
	if err := r.ValidateSubdomain(subdomain); err != nil {
		return "", fmt.Errorf("invalid subdomain: %w", err)
	}

	return subdomain, nil
}

// ExtractFromPath extracts tenant subdomain from URL path
func (r *resolver) ExtractFromPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}

	prefix := r.config.PathPrefix
	if prefix == "" {
		prefix = "/tenant/"
	}

	if !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("path does not start with tenant prefix: %s", prefix)
	}

	// Extract tenant from path (e.g., "/tenant/acme/api/projects" -> "acme")
	pathWithoutPrefix := strings.TrimPrefix(path, prefix)
	parts := strings.Split(pathWithoutPrefix, "/")

	if len(parts) == 0 || parts[0] == "" {
		return "", errors.New("no tenant found in path")
	}

	subdomain := parts[0]

	// Validate subdomain format
	if err := r.ValidateSubdomain(subdomain); err != nil {
		return "", fmt.Errorf("invalid subdomain: %w", err)
	}

	return subdomain, nil
}

// ExtractFromHeader extracts tenant subdomain from HTTP header
func (r *resolver) ExtractFromHeader(req *http.Request) (string, error) {
	headerName := r.config.HeaderName
	if headerName == "" {
		headerName = "X-Tenant"
	}

	subdomain := req.Header.Get(headerName)
	if subdomain == "" {
		return "", fmt.Errorf("no tenant header found: %s", headerName)
	}

	// Validate subdomain format
	if err := r.ValidateSubdomain(subdomain); err != nil {
		return "", fmt.Errorf("invalid subdomain: %w", err)
	}

	return subdomain, nil
}

// ValidateSubdomain validates a subdomain format
func (r *resolver) ValidateSubdomain(subdomain string) error {
	if len(subdomain) < 3 || len(subdomain) > 50 {
		return errors.New("subdomain must be between 3 and 50 characters")
	}

	// Check for valid characters (alphanumeric and hyphens only)
	validSubdomain := regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)
	if !validSubdomain.MatchString(subdomain) {
		return errors.New("subdomain must contain only lowercase letters, numbers, and hyphens, and cannot start or end with a hyphen")
	}

	// Check for reserved subdomains
	for _, reserved := range r.config.ReservedSubdomain {
		if strings.EqualFold(subdomain, reserved) {
			return fmt.Errorf("subdomain '%s' is reserved", subdomain)
		}
	}

	return nil
}

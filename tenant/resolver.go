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

// subdomainPattern matches a valid tenant subdomain: lowercase alphanumerics
// and hyphens, not starting or ending with a hyphen.
var subdomainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// resolver implements the Resolver interface
type resolver struct {
	config     ResolverConfig
	repository Repository
	logger     *zap.Logger
}

// DefaultSubdomainValidator returns the built-in policy: 3-50 characters,
// lowercase letters, digits and hyphens, not starting or ending with a
// hyphen, and not in reserved.
func DefaultSubdomainValidator(reserved []string) func(string) error {
	return func(subdomain string) error {
		if len(subdomain) < 3 || len(subdomain) > 50 {
			return errors.New("subdomain must be between 3 and 50 characters")
		}
		if !subdomainPattern.MatchString(subdomain) {
			return errors.New("subdomain must contain only lowercase letters, numbers, and hyphens, and cannot start or end with a hyphen")
		}
		for _, r := range reserved {
			if strings.EqualFold(subdomain, r) {
				return fmt.Errorf("subdomain '%s' is reserved", subdomain)
			}
		}
		return nil
	}
}

// NewResolver creates a new tenant resolver
func NewResolver(config ResolverConfig, repository Repository, logger *zap.Logger) Resolver {
	if config.ValidateSubdomain == nil {
		config.ValidateSubdomain = DefaultSubdomainValidator(config.ReservedSubdomain)
	}
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
	host = strings.ToLower(host)

	var subdomain string
	if domain := strings.ToLower(strings.TrimPrefix(r.config.Domain, ".")); domain != "" {
		// With a configured domain the host must be exactly <subdomain>.<domain>.
		suffix := "." + domain
		if !strings.HasSuffix(host, suffix) {
			return "", fmt.Errorf("host %s is not under configured domain %s", host, domain)
		}
		subdomain = strings.TrimSuffix(host, suffix)
		if subdomain == "" {
			return "", fmt.Errorf("no subdomain in host: %s", host)
		}
		if strings.Contains(subdomain, ".") {
			return "", fmt.Errorf("nested subdomains are not supported: %s", host)
		}
	} else {
		// Without a configured domain, fall back to the first label of a
		// host with at least three labels (subdomain.domain.tld).
		parts := strings.Split(host, ".")
		if len(parts) < 3 {
			return "", fmt.Errorf("invalid host format: %s", host)
		}
		subdomain = parts[0]
	}

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
	return r.config.ValidateSubdomain(subdomain)
}

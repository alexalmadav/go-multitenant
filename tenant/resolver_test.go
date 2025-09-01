package tenant

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap/zaptest"
)

func TestNewResolver(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := ResolverConfig{
		Strategy: ResolverSubdomain,
		Domain:   "example.com",
	}

	mockRepo := &mockRepository{
		tenants: make(map[uuid.UUID]*Tenant),
	}

	resolver := NewResolver(config, mockRepo, logger)
	if resolver == nil {
		t.Error("NewResolver() should not return nil")
	}
}

func TestResolver_ResolveTenant(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tenantID := uuid.New()

	// Create mock repository with test tenant
	mockRepo := &mockRepository{
		tenants: map[uuid.UUID]*Tenant{
			tenantID: {
				ID:        tenantID,
				Name:      "Test Tenant",
				Subdomain: "test-tenant",
				Status:    StatusActive,
			},
		},
	}

	tests := []struct {
		name    string
		config  ResolverConfig
		req     *http.Request
		wantID  uuid.UUID
		wantErr bool
	}{
		{
			name: "subdomain strategy success",
			config: ResolverConfig{
				Strategy: ResolverSubdomain,
				Domain:   "example.com",
			},
			req: &http.Request{
				Host: "test-tenant.example.com",
			},
			wantID:  tenantID,
			wantErr: false,
		},
		{
			name: "path strategy success",
			config: ResolverConfig{
				Strategy:   ResolverPath,
				PathPrefix: "/tenant/",
			},
			req: func() *http.Request {
				req, _ := http.NewRequest("GET", "/tenant/test-tenant/api/projects", nil)
				return req
			}(),
			wantID:  tenantID,
			wantErr: false,
		},
		{
			name: "header strategy success",
			config: ResolverConfig{
				Strategy:   ResolverHeader,
				HeaderName: "X-Tenant",
			},
			req: func() *http.Request {
				req, _ := http.NewRequest("GET", "/api/projects", nil)
				req.Header.Set("X-Tenant", "test-tenant")
				return req
			}(),
			wantID:  tenantID,
			wantErr: false,
		},
		{
			name: "unknown strategy",
			config: ResolverConfig{
				Strategy: "unknown",
			},
			req:     &http.Request{},
			wantErr: true,
		},
		{
			name: "tenant not found",
			config: ResolverConfig{
				Strategy: ResolverSubdomain,
				Domain:   "example.com",
			},
			req: &http.Request{
				Host: "nonexistent.example.com",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := NewResolver(tt.config, mockRepo, logger)
			gotID, err := resolver.ResolveTenant(context.Background(), tt.req)

			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveTenant() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && gotID != tt.wantID {
				t.Errorf("ResolveTenant() = %v, want %v", gotID, tt.wantID)
			}
		})
	}
}

func TestResolver_ExtractFromSubdomain(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := ResolverConfig{
		Strategy:          ResolverSubdomain,
		Domain:            "example.com",
		ReservedSubdomain: []string{"www", "api", "admin"},
	}
	resolver := NewResolver(config, &mockRepository{}, logger)

	tests := []struct {
		name    string
		host    string
		want    string
		wantErr bool
	}{
		{
			name:    "valid subdomain",
			host:    "test-tenant.example.com",
			want:    "test-tenant",
			wantErr: false,
		},
		{
			name:    "valid subdomain with port",
			host:    "test-tenant.example.com:8080",
			want:    "test-tenant",
			wantErr: false,
		},
		{
			name:    "empty host",
			host:    "",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid format - only domain",
			host:    "example.com",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid format - no TLD",
			host:    "subdomain.domain",
			want:    "",
			wantErr: true,
		},
		{
			name:    "reserved subdomain",
			host:    "www.example.com",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid subdomain format - starts with hyphen",
			host:    "-invalid.example.com",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid subdomain format - ends with hyphen",
			host:    "invalid-.example.com",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid subdomain format - too short",
			host:    "ab.example.com",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolver.ExtractFromSubdomain(tt.host)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractFromSubdomain() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractFromSubdomain() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolver_ExtractFromPath(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := ResolverConfig{
		Strategy:   ResolverPath,
		PathPrefix: "/tenant/",
	}
	resolver := NewResolver(config, &mockRepository{}, logger)

	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{
			name:    "valid path",
			path:    "/tenant/test-tenant/api/projects",
			want:    "test-tenant",
			wantErr: false,
		},
		{
			name:    "valid path - tenant only",
			path:    "/tenant/test-tenant",
			want:    "test-tenant",
			wantErr: false,
		},
		{
			name:    "valid path - tenant with trailing slash",
			path:    "/tenant/test-tenant/",
			want:    "test-tenant",
			wantErr: false,
		},
		{
			name:    "empty path",
			path:    "",
			want:    "",
			wantErr: true,
		},
		{
			name:    "path without prefix",
			path:    "/api/projects",
			want:    "",
			wantErr: true,
		},
		{
			name:    "path with prefix but no tenant",
			path:    "/tenant/",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid subdomain in path",
			path:    "/tenant/ab/api",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolver.ExtractFromPath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractFromPath() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractFromPath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolver_ExtractFromPath_CustomPrefix(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := ResolverConfig{
		Strategy:   ResolverPath,
		PathPrefix: "/api/v1/tenant/",
	}
	resolver := NewResolver(config, &mockRepository{}, logger)

	got, err := resolver.ExtractFromPath("/api/v1/tenant/test-tenant/projects")
	if err != nil {
		t.Errorf("ExtractFromPath() error = %v, want nil", err)
		return
	}
	if got != "test-tenant" {
		t.Errorf("ExtractFromPath() = %v, want %v", got, "test-tenant")
	}
}

func TestResolver_ExtractFromPath_DefaultPrefix(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := ResolverConfig{
		Strategy: ResolverPath,
		// No PathPrefix specified - should use default
	}
	resolver := NewResolver(config, &mockRepository{}, logger)

	got, err := resolver.ExtractFromPath("/tenant/test-tenant/projects")
	if err != nil {
		t.Errorf("ExtractFromPath() error = %v, want nil", err)
		return
	}
	if got != "test-tenant" {
		t.Errorf("ExtractFromPath() = %v, want %v", got, "test-tenant")
	}
}

func TestResolver_ExtractFromHeader(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := ResolverConfig{
		Strategy:   ResolverHeader,
		HeaderName: "X-Tenant",
	}
	resolver := NewResolver(config, &mockRepository{}, logger)

	tests := []struct {
		name    string
		req     *http.Request
		want    string
		wantErr bool
	}{
		{
			name: "valid header",
			req: func() *http.Request {
				req, _ := http.NewRequest("GET", "/api/projects", nil)
				req.Header.Set("X-Tenant", "test-tenant")
				return req
			}(),
			want:    "test-tenant",
			wantErr: false,
		},
		{
			name: "missing header",
			req: func() *http.Request {
				req, _ := http.NewRequest("GET", "/api/projects", nil)
				return req
			}(),
			want:    "",
			wantErr: true,
		},
		{
			name: "empty header value",
			req: func() *http.Request {
				req, _ := http.NewRequest("GET", "/api/projects", nil)
				req.Header.Set("X-Tenant", "")
				return req
			}(),
			want:    "",
			wantErr: true,
		},
		{
			name: "invalid subdomain in header",
			req: func() *http.Request {
				req, _ := http.NewRequest("GET", "/api/projects", nil)
				req.Header.Set("X-Tenant", "ab")
				return req
			}(),
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolver.ExtractFromHeader(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractFromHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractFromHeader() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolver_ExtractFromHeader_DefaultHeaderName(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := ResolverConfig{
		Strategy: ResolverHeader,
		// No HeaderName specified - should use default "X-Tenant"
	}
	resolver := NewResolver(config, &mockRepository{}, logger)

	req, _ := http.NewRequest("GET", "/api/projects", nil)
	req.Header.Set("X-Tenant", "test-tenant")

	got, err := resolver.ExtractFromHeader(req)
	if err != nil {
		t.Errorf("ExtractFromHeader() error = %v, want nil", err)
		return
	}
	if got != "test-tenant" {
		t.Errorf("ExtractFromHeader() = %v, want %v", got, "test-tenant")
	}
}

func TestResolver_ValidateSubdomain(t *testing.T) {
	logger := zaptest.NewLogger(t)
	config := ResolverConfig{
		Strategy:          ResolverSubdomain,
		ReservedSubdomain: []string{"www", "api", "admin"},
	}
	resolver := NewResolver(config, &mockRepository{}, logger)

	tests := []struct {
		name      string
		subdomain string
		wantErr   bool
	}{
		{
			name:      "valid subdomain",
			subdomain: "test-tenant",
			wantErr:   false,
		},
		{
			name:      "valid subdomain with numbers",
			subdomain: "tenant123",
			wantErr:   false,
		},
		{
			name:      "minimum length",
			subdomain: "abc",
			wantErr:   false,
		},
		{
			name:      "too short",
			subdomain: "ab",
			wantErr:   true,
		},
		{
			name:      "too long",
			subdomain: "a-very-long-subdomain-name-that-exceeds-fifty-chars",
			wantErr:   true,
		},
		{
			name:      "starts with hyphen",
			subdomain: "-invalid",
			wantErr:   true,
		},
		{
			name:      "ends with hyphen",
			subdomain: "invalid-",
			wantErr:   true,
		},
		{
			name:      "contains uppercase",
			subdomain: "Invalid",
			wantErr:   true,
		},
		{
			name:      "contains special characters",
			subdomain: "test_tenant",
			wantErr:   true,
		},
		{
			name:      "reserved subdomain",
			subdomain: "www",
			wantErr:   true,
		},
		{
			name:      "reserved subdomain case insensitive",
			subdomain: "API",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := resolver.ValidateSubdomain(tt.subdomain)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSubdomain() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// mockRepository is a simple mock for testing resolver
type mockRepository struct {
	tenants map[uuid.UUID]*Tenant
}

func (m *mockRepository) Create(ctx context.Context, tenant *Tenant) error {
	m.tenants[tenant.ID] = tenant
	return nil
}

func (m *mockRepository) GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	tenant, exists := m.tenants[id]
	if !exists {
		return nil, ErrTenantNotFound
	}
	return tenant, nil
}

func (m *mockRepository) GetBySubdomain(ctx context.Context, subdomain string) (*Tenant, error) {
	for _, tenant := range m.tenants {
		if tenant.Subdomain == subdomain {
			return tenant, nil
		}
	}
	return nil, ErrTenantNotFound
}

func (m *mockRepository) Update(ctx context.Context, tenant *Tenant) error {
	m.tenants[tenant.ID] = tenant
	return nil
}

func (m *mockRepository) Delete(ctx context.Context, id uuid.UUID) error {
	delete(m.tenants, id)
	return nil
}

func (m *mockRepository) List(ctx context.Context, page, perPage int) ([]*Tenant, int, error) {
	var tenants []*Tenant
	for _, tenant := range m.tenants {
		tenants = append(tenants, tenant)
	}
	return tenants, len(tenants), nil
}

func (m *mockRepository) GetStats(ctx context.Context, tenantID uuid.UUID) (*Stats, error) {
	return &Stats{TenantID: tenantID}, nil
}

// ErrTenantNotFound is a mock error for tenant not found
var ErrTenantNotFound = &TenantError{
	Code:    "TENANT_NOT_FOUND",
	Message: "tenant not found",
}

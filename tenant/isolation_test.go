package tenant

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validRoleConfig() RoleIsolationConfig {
	return RoleIsolationConfig{
		TenantDSN: "postgres://localhost:6432/app",
		Secret:    bytes.Repeat([]byte("s"), MinRoleSecretLen),
	}
}

func TestRoleIsolationDefaults(t *testing.T) {
	got := RoleIsolationConfig{}.WithDefaults()
	if got.MaxConns != DefaultRoleMaxConns || got.PerTenantMaxConns != DefaultRolePerTenantMaxConns ||
		got.MaxWarmTenants != DefaultRoleMaxWarmTenants || got.IdleTimeout != DefaultRoleIdleTimeout {
		t.Errorf("WithDefaults() = %+v, want every limit at its default", got)
	}

	set := RoleIsolationConfig{MaxConns: 7, PerTenantMaxConns: 2, MaxWarmTenants: 9, IdleTimeout: time.Second}.WithDefaults()
	if set.MaxConns != 7 || set.PerTenantMaxConns != 2 || set.MaxWarmTenants != 9 || set.IdleTimeout != time.Second {
		t.Errorf("WithDefaults() changed explicit limits: %+v", set)
	}
}

func TestRoleIsolationValidate(t *testing.T) {
	short := []byte("too short")
	for name, tc := range map[string]struct {
		mutate  func(*RoleIsolationConfig)
		prefix  string
		wantErr string
	}{
		"valid":        {mutate: func(*RoleIsolationConfig) {}, prefix: "tenant_"},
		"no TenantDSN": {mutate: func(c *RoleIsolationConfig) { c.TenantDSN = "" }, prefix: "tenant_", wantErr: "TenantDSN"},
		"short Secret": {mutate: func(c *RoleIsolationConfig) { c.Secret = short }, prefix: "tenant_", wantErr: "Secret"},
		"short Secret with a hook": {mutate: func(c *RoleIsolationConfig) {
			c.Secret = nil
			c.Credentials = func(uuid.UUID) (string, error) { return "pw", nil }
		}, prefix: "tenant_"},
		"short PreviousSecrets entry": {mutate: func(c *RoleIsolationConfig) { c.PreviousSecrets = [][]byte{short} }, prefix: "tenant_", wantErr: "PreviousSecrets[0]"},
		"prefix too long":             {mutate: func(*RoleIsolationConfig) {}, prefix: strings.Repeat("p", 28), wantErr: "63"},
		"prefix with a quote":         {mutate: func(*RoleIsolationConfig) {}, prefix: `te"nant_`, wantErr: "double quote"},
		"prefix with NUL":             {mutate: func(*RoleIsolationConfig) {}, prefix: "ten\x00ant_", wantErr: "NUL"},
		"prefix starting with pg_":    {mutate: func(*RoleIsolationConfig) {}, prefix: "pg_tenant_", wantErr: "pg_"},
		"prefix containing pg_":       {mutate: func(*RoleIsolationConfig) {}, prefix: "app_pg_"},
		"prefix at the limit":         {mutate: func(*RoleIsolationConfig) {}, prefix: strings.Repeat("p", 27)},
		"negative limit":              {mutate: func(c *RoleIsolationConfig) { c.MaxConns = -1 }, prefix: "tenant_", wantErr: "negative"},
		"per-tenant above global":     {mutate: func(c *RoleIsolationConfig) { c.MaxConns = 3; c.PerTenantMaxConns = 4 }, prefix: "tenant_", wantErr: "PerTenantMaxConns"},
	} {
		t.Run(name, func(t *testing.T) {
			c := validRoleConfig()
			tc.mutate(&c)
			err := c.Validate(tc.prefix)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRoleIsolation) {
				t.Fatalf("Validate() = %v, want ErrInvalidRoleIsolation", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// The zero value must be today's behaviour, so every existing Config keeps it.
func TestIsolationDefaultsToSearchPath(t *testing.T) {
	if got := DefaultConfig().Database.Isolation; got != IsolationSearchPath {
		t.Errorf("DefaultConfig().Database.Isolation = %q, want IsolationSearchPath", got)
	}
}

package multitenant

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
)

// Role isolation config errors are caught before New touches the database.
func TestNew_RejectsInvalidRoleIsolationBeforeConnecting(t *testing.T) {
	for name, set := range map[string]func(*Config){
		"no TenantDSN": func(c *Config) {
			c.Database.Isolation = tenant.IsolationRole
			c.Database.RoleIsolation = tenant.RoleIsolationConfig{Secret: bytes.Repeat([]byte("s"), 32)}
		},
		"short secret": func(c *Config) {
			c.Database.Isolation = tenant.IsolationRole
			c.Database.RoleIsolation = tenant.RoleIsolationConfig{TenantDSN: "postgres://x", Secret: []byte("short")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := decisionConfig()
			cfg.InsecureSkipMembership = true
			set(&cfg)
			_, err := New(cfg)
			if !errors.Is(err, tenant.ErrInvalidRoleIsolation) {
				t.Fatalf("New() = %v, want ErrInvalidRoleIsolation", err)
			}
			if strings.Contains(err.Error(), "failed to setup database") {
				t.Errorf("New reached the database before rejecting the config: %v", err)
			}
		})
	}
}

func TestNew_RejectsUnknownIsolationMode(t *testing.T) {
	cfg := decisionConfig()
	cfg.InsecureSkipMembership = true
	cfg.Database.Isolation = "rolez"
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "rolez") {
		t.Errorf("New() = %v, want an error naming the unknown mode", err)
	}
}

func TestRoleIsolationMethodsWithoutRoleMode(t *testing.T) {
	mt := &MultiTenant{}
	ctx := context.Background()
	for name, err := range map[string]error{
		"EnsureTenantRoles":       mt.EnsureTenantRoles(ctx),
		"RotateTenantCredentials": mt.RotateTenantCredentials(ctx),
		"PgBouncerAuthFile":       mt.PgBouncerAuthFile(ctx, &bytes.Buffer{}),
	} {
		if !errors.Is(err, ErrRoleIsolationDisabled) {
			t.Errorf("%s = %v, want ErrRoleIsolationDisabled", name, err)
		}
	}
	if _, ok := mt.TenantPoolStats(); ok {
		t.Error("TenantPoolStats reported pools without role mode")
	}
}

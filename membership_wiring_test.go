package multitenant

import (
	"context"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

// DefaultConfig must leave the membership decision unmade: no Membership, and
// no opt-out. Either default would make the decision on the application's
// behalf - a Membership default would enforce a policy nobody chose, and an
// opt-out default would restore the silently unprotected chain New exists to
// refuse.
func TestDefaultConfigLeavesTheMembershipDecisionUnmade(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Membership != nil {
		t.Errorf("DefaultConfig().Membership = %v, want nil", cfg.Membership)
	}
	if cfg.InsecureSkipMembership {
		t.Error("DefaultConfig().InsecureSkipMembership = true, want false")
	}
}

// SkipPaths and SkipHosts must default to nil. New reads a nil SkipPaths as
// "keep the built-in defaults", so a non-nil default here would erase that
// distinction and make an explicitly empty slice indistinguishable from an
// unset one. A non-nil SkipHosts default would disable every check on an
// origin nobody asked to exempt.
func TestDefaultConfigHasNoSkipLists(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.SkipPaths != nil {
		t.Errorf("DefaultConfig().SkipPaths = %v, want nil", cfg.SkipPaths)
	}
	if cfg.SkipHosts != nil {
		t.Errorf("DefaultConfig().SkipHosts = %v, want nil", cfg.SkipHosts)
	}
}

// unreachableDSN is never dialled by the decision tests: New checks the
// membership decision before it touches the database, so a Config that fails
// the decision fails before this DSN matters, and one that passes it fails
// here instead - which is how these tests tell the two apart without a
// database.
const unreachableDSN = "invalid-dsn"

func decisionConfig() Config {
	cfg := DefaultConfig()
	cfg.Database.DSN = unreachableDSN
	return cfg
}

func TestNew_RequiresAMembershipDecision(t *testing.T) {
	_, err := New(decisionConfig())
	if err == nil {
		t.Fatal("New accepted a Config with neither Membership nor InsecureSkipMembership")
	}
	if !strings.Contains(err.Error(), "Config.Membership is nil") {
		t.Errorf("error should name the missing decision, got: %v", err)
	}
}

func TestNew_RejectsMembershipAndSkipTogether(t *testing.T) {
	cfg := decisionConfig()
	cfg.Membership = tenant.ClaimMembership("org_id")
	cfg.InsecureSkipMembership = true

	_, err := New(cfg)
	if err == nil {
		t.Fatal("New accepted a Config that both sets a Membership and skips it")
	}
	if !strings.Contains(err.Error(), "both set") {
		t.Errorf("error should say the two settings conflict, got: %v", err)
	}
}

// Either choice satisfies the decision, so New proceeds to the database and
// fails there instead. The error must not be the membership one.
func TestNew_EitherChoiceSatisfiesTheDecision(t *testing.T) {
	for name, set := range map[string]func(*Config){
		"a Membership": func(c *Config) {
			c.Membership = tenant.MembershipFunc(func(context.Context, string, uuid.UUID) error { return nil })
		},
		"InsecureSkipMembership": func(c *Config) { c.InsecureSkipMembership = true },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := decisionConfig()
			set(&cfg)

			_, err := New(cfg)
			if err == nil {
				t.Fatal("New succeeded against an unreachable database")
			}
			if strings.Contains(err.Error(), "Membership") {
				t.Errorf("New refused the membership decision it was given: %v", err)
			}
		})
	}
}

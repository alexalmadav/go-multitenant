package multitenant

import (
	"context"
	"errors"
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

// unreachableDSN is never dialled by the decision tests. New checks the
// membership decision before it touches the database, so a Config that fails
// the decision returns a decision error, and one that passes it goes on to
// fail parsing this DSN with "failed to setup database". The tests assert
// both sides positively, so neither can pass for the other's reason.
const unreachableDSN = "invalid-dsn"

func decisionConfig() Config {
	cfg := DefaultConfig()
	cfg.Database.DSN = unreachableDSN
	return cfg
}

// ptrMembership is a Membership implemented on a pointer, so a nil
// *ptrMembership is a non-nil interface holding a nil pointer.
type ptrMembership struct{}

func (*ptrMembership) Allow(context.Context, string, uuid.UUID) error { return nil }

func TestNew_RequiresAMembershipDecision(t *testing.T) {
	_, err := New(decisionConfig())
	if !errors.Is(err, ErrNoMembershipDecision) {
		t.Fatalf("New() error = %v, want ErrNoMembershipDecision", err)
	}
}

func TestNew_RejectsMembershipAndSkipTogether(t *testing.T) {
	cfg := decisionConfig()
	cfg.Membership = tenant.ClaimMembership("org_id")
	cfg.InsecureSkipMembership = true

	_, err := New(cfg)
	if !errors.Is(err, ErrConflictingMembershipDecision) {
		t.Fatalf("New() error = %v, want ErrConflictingMembershipDecision", err)
	}
}

// A Membership that is a nil pointer or nil function compares unequal to nil,
// so a plain nil check would accept it and every request would then panic
// inside Allow. New must treat it as no decision, and say what it found, even
// when the opt-out is also set.
func TestNew_TreatsATypedNilMembershipAsNoDecision(t *testing.T) {
	for name, m := range map[string]tenant.Membership{
		"nil pointer":        (*ptrMembership)(nil),
		"nil MembershipFunc": tenant.MembershipFunc(nil),
	} {
		for _, skip := range []bool{false, true} {
			label := name
			if skip {
				label += " with the opt-out set"
			}
			t.Run(label, func(t *testing.T) {
				cfg := decisionConfig()
				cfg.Membership = m
				cfg.InsecureSkipMembership = skip

				_, err := New(cfg)
				if !errors.Is(err, ErrNoMembershipDecision) {
					t.Fatalf("New() error = %v, want ErrNoMembershipDecision", err)
				}
				if !strings.Contains(err.Error(), "holds a nil") {
					t.Errorf("error should say the Membership is nil, got: %v", err)
				}
			})
		}
	}
}

// Either choice satisfies the decision, so New goes on to the database and
// fails there. The test asserts both halves: no decision error, and the
// database error that only a passed decision can reach.
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
			if errors.Is(err, ErrNoMembershipDecision) || errors.Is(err, ErrConflictingMembershipDecision) {
				t.Fatalf("New refused the membership decision it was given: %v", err)
			}
			if !strings.Contains(err.Error(), "failed to setup database") {
				t.Errorf("New should have reached the database, got: %v", err)
			}
		})
	}
}

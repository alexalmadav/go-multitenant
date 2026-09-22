package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// claimCtx builds a context holding a principal with one claim and a resolved
// tenant, which is what ClaimMembership reads.
func claimCtx(claim string, value any, t *Tenant) context.Context {
	ctx := WithPrincipal(context.Background(), Principal{
		Subject: "u-1",
		Claims:  map[string]any{claim: value},
	})
	if t != nil {
		ctx = WithTenantObject(ctx, t)
	}
	return ctx
}

func TestMembershipFuncAdaptsAFunction(t *testing.T) {
	want := errors.New("nope")
	var m Membership = MembershipFunc(func(context.Context, string, uuid.UUID) error {
		return want
	})
	if got := m.Allow(context.Background(), "u-1", uuid.New()); !errors.Is(got, want) {
		t.Errorf("Allow = %v, want %v", got, want)
	}
}

func TestClaimMembershipMatchesTenantID(t *testing.T) {
	id := uuid.New()
	ctx := claimCtx("org_id", id.String(), nil)
	if err := ClaimMembership("org_id").Allow(ctx, "u-1", id); err != nil {
		t.Errorf("Allow = %v, want nil", err)
	}
}

func TestClaimMembershipMatchesSubdomain(t *testing.T) {
	id := uuid.New()
	ctx := claimCtx("org_slug", "acme", &Tenant{ID: id, Subdomain: "acme"})
	if err := ClaimMembership("org_slug").Allow(ctx, "u-1", id); err != nil {
		t.Errorf("Allow = %v, want nil", err)
	}
}

// A user who belongs to several tenants has a list-valued claim. JSON decoding
// produces []any, so both shapes must work.
func TestClaimMembershipMatchesWithinAList(t *testing.T) {
	id := uuid.New()
	for name, value := range map[string]any{
		"[]any":    []any{"globex", "acme"},
		"[]string": []string{"globex", "acme"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := claimCtx("orgs", value, &Tenant{ID: id, Subdomain: "acme"})
			if err := ClaimMembership("orgs").Allow(ctx, "u-1", id); err != nil {
				t.Errorf("Allow = %v, want nil", err)
			}
		})
	}
}

func TestClaimMembershipDeniesAnotherTenant(t *testing.T) {
	ctx := claimCtx("org_slug", "globex", &Tenant{ID: uuid.New(), Subdomain: "acme"})
	err := ClaimMembership("org_slug").Allow(ctx, "u-1", uuid.New())
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("Allow = %v, want ErrNotMember", err)
	}
}

func TestClaimMembershipDeniesMissingClaim(t *testing.T) {
	ctx := claimCtx("something_else", "acme", &Tenant{Subdomain: "acme"})
	err := ClaimMembership("org_slug").Allow(ctx, "u-1", uuid.New())
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("Allow = %v, want ErrNotMember", err)
	}
}

func TestClaimMembershipDeniesWithoutAPrincipal(t *testing.T) {
	err := ClaimMembership("org_id").Allow(context.Background(), "u-1", uuid.New())
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("Allow = %v, want ErrNotMember", err)
	}
}

// A claim of an unexpected type denies rather than panicking.
func TestClaimMembershipDeniesNonStringClaim(t *testing.T) {
	ctx := claimCtx("org_id", 42, nil)
	err := ClaimMembership("org_id").Allow(ctx, "u-1", uuid.New())
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("Allow = %v, want ErrNotMember", err)
	}
}

// ClaimMembership can only answer for the principal in the context, so a
// question about a different subject is denied rather than answered wrongly.
func TestClaimMembershipDeniesASubjectMismatch(t *testing.T) {
	id := uuid.New()
	ctx := claimCtx("org_id", id.String(), nil)
	err := ClaimMembership("org_id").Allow(ctx, "someone-else", id)
	if !errors.Is(err, ErrNotMember) {
		t.Errorf("Allow = %v, want ErrNotMember", err)
	}
}

package tenant

import (
	"context"
	"testing"
)

func TestPrincipalRoundTrip(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{
		Subject: "auth0|abc",
		Claims:  map[string]any{"org_id": "acme"},
	})

	p, ok := PrincipalFromContext(ctx)
	if !ok {
		t.Fatal("PrincipalFromContext returned ok=false")
	}
	if p.Subject != "auth0|abc" {
		t.Errorf("Subject = %q, want %q", p.Subject, "auth0|abc")
	}
	v, ok := p.Claim("org_id")
	if !ok || v != "acme" {
		t.Errorf("Claim(org_id) = %v, %v; want acme, true", v, ok)
	}
}

func TestPrincipalFromContextMissing(t *testing.T) {
	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Error("PrincipalFromContext returned ok=true for an empty context")
	}
}

func TestClaimOnNilClaims(t *testing.T) {
	var p Principal
	if _, ok := p.Claim("anything"); ok {
		t.Error("Claim returned ok=true for a principal with no claims")
	}
}

// WithPrincipal must keep the string user-id API working, because LogAccess
// and the Gin adapter read it.
func TestWithPrincipalSetsUserID(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{Subject: "u-1"})
	if id, ok := UserIDFromContext(ctx); !ok || id != "u-1" {
		t.Errorf("UserIDFromContext = %q, %v; want u-1, true", id, ok)
	}
}

// WithUserID keeps working and is readable as a principal.
func TestWithUserIDIsAPrincipal(t *testing.T) {
	ctx := WithUserID(context.Background(), "u-2")
	if id, ok := UserIDFromContext(ctx); !ok || id != "u-2" {
		t.Errorf("UserIDFromContext = %q, %v; want u-2, true", id, ok)
	}
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.Subject != "u-2" {
		t.Errorf("PrincipalFromContext = %+v, %v; want Subject u-2, true", p, ok)
	}
	if p.Claims != nil {
		t.Errorf("Claims = %v, want nil", p.Claims)
	}
}

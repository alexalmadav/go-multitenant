package tenant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Membership answers whether an authenticated subject may act inside a
// tenant. It is the only authorization decision this library makes, and the
// application always supplies the answer: nothing here authenticates anyone
// or records who belongs where.
//
// Apply it with httpmw.RequireMembership, which reads the subject from the
// Principal the application's auth middleware put in the context.
type Membership interface {
	// Allow returns nil when subject may act inside tenantID. Any non-nil
	// error denies the request; wrap ErrNotMember to say why.
	Allow(ctx context.Context, subject string, tenantID uuid.UUID) error
}

// MembershipFunc adapts an ordinary function to Membership. Most
// applications answer from their own table with their own subject type:
//
//	tenant.MembershipFunc(func(ctx context.Context, subject string, id uuid.UUID) error {
//	    var ok bool
//	    err := db.QueryRowContext(ctx,
//	        `SELECT EXISTS (SELECT 1 FROM memberships WHERE user_id = $1 AND tenant_id = $2)`,
//	        subject, id).Scan(&ok)
//	    if err != nil {
//	        return err
//	    }
//	    if !ok {
//	        return tenant.ErrNotMember
//	    }
//	    return nil
//	})
type MembershipFunc func(ctx context.Context, subject string, tenantID uuid.UUID) error

// Allow implements Membership.
func (f MembershipFunc) Allow(ctx context.Context, subject string, tenantID uuid.UUID) error {
	return f(ctx, subject, tenantID)
}

// ErrNotMember reports that the subject does not belong to the tenant.
// Middleware turns it, and any other error, into ACCESS_DENIED; the cause is
// logged rather than returned to the client.
var ErrNotMember = errors.New("subject is not a member of this tenant")

// ClaimMembership allows a request when the named claim on the Principal
// names the resolved tenant, either by id or by subdomain. The claim may hold
// one string or a list of them, so a subject that belongs to several tenants
// works.
//
// It issues no queries: the answer is already in the token. Use it with
// providers that put organisation membership in claims, such as Auth0
// "org_id", Clerk "org_slug" and WorkOS "organization_id", or with your own
// JWT. Matching by subdomain needs the full tenant record, which
// httpmw.ResolveTenant puts in the context.
func ClaimMembership(claim string) Membership {
	return MembershipFunc(func(ctx context.Context, _ string, tenantID uuid.UUID) error {
		p, ok := PrincipalFromContext(ctx)
		if !ok {
			return fmt.Errorf("%w: no principal in context", ErrNotMember)
		}
		raw, ok := p.Claim(claim)
		if !ok {
			return fmt.Errorf("%w: principal has no %q claim", ErrNotMember, claim)
		}

		want := []string{tenantID.String()}
		if t, ok := TenantObjectFromContext(ctx); ok && t.Subdomain != "" {
			want = append(want, t.Subdomain)
		}
		for _, got := range claimStrings(raw) {
			for _, w := range want {
				if strings.EqualFold(got, w) {
					return nil
				}
			}
		}
		return fmt.Errorf("%w: %q claim does not name it", ErrNotMember, claim)
	})
}

// claimStrings normalises a claim value to a list of strings. It accepts a
// string, a []string, and the []any that encoding/json produces. Anything
// else yields no candidates, which denies the request.
func claimStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

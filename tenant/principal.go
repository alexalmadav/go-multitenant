package tenant

import "context"

// Principal is the authenticated caller, as the application's auth
// middleware sees it. Subject is the stable identifier the identity provider
// issues: "auth0|abc", a UUID in string form, a service-account name.
// Claims carries whatever else that provider supplied and may be nil.
//
// This library never authenticates anyone. It only reads a Principal the
// application put in the context.
type Principal struct {
	Subject string
	Claims  map[string]any
}

// Claim returns the named claim. The second result is false when the
// principal carries no such claim.
func (p Principal) Claim(name string) (any, bool) {
	if p.Claims == nil {
		return nil, false
	}
	v, ok := p.Claims[name]
	return v, ok
}

// WithPrincipal returns a context carrying the authenticated caller. It also
// stores the subject under ContextKeyUserID, so UserIDFromContext, LogAccess
// and the Gin adapter's user_id bridge keep working.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	ctx = context.WithValue(ctx, ContextKeyPrincipal, p)
	return context.WithValue(ctx, ContextKeyUserID, p.Subject)
}

// PrincipalFromContext returns the principal set by WithPrincipal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ContextKeyPrincipal).(Principal)
	return p, ok
}

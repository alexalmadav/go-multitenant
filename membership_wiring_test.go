package multitenant

import "testing"

// Membership must default to nil. A non-nil default would turn enforcement on
// for every application that never asked for it, and Standard would then
// reject callers that carry no principal.
//
// That a configured value reaches the middleware is covered by httpmw's
// TestStandardEnforcesMembershipWhenConfigured; asserting it through New
// would need a live database.
func TestDefaultConfigHasNoMembership(t *testing.T) {
	if got := DefaultConfig().Membership; got != nil {
		t.Errorf("DefaultConfig().Membership = %v, want nil", got)
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

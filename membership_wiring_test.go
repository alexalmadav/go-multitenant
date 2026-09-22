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

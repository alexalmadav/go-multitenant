package tenant

import (
	"context"
	"errors"
	"fmt"
)

// Hook receives tenant lifecycle events. ValidateMetadata runs before a create
// or update is written and blocks it on error. Every other method runs after
// the write has committed, synchronously, outside any transaction, so it may
// call external services. Embed BaseHook to implement only what you need.
//
// A hook that calls Manager methods will trigger hooks itself; keep that in
// mind to avoid loops.
type Hook interface {
	Name() string
	ValidateMetadata(ctx context.Context, t *Tenant) error
	OnTenantCreated(ctx context.Context, t *Tenant) error
	OnTenantProvisioned(ctx context.Context, t *Tenant) error
	OnTenantUpdated(ctx context.Context, before, after *Tenant) error
	OnTenantStatusChanged(ctx context.Context, t *Tenant, previousStatus string) error
	OnTenantDeleted(ctx context.Context, t *Tenant) error
}

// BaseHook implements every Hook method as a no-op.
type BaseHook struct{}

func (BaseHook) Name() string                                                 { return "hook" }
func (BaseHook) ValidateMetadata(context.Context, *Tenant) error              { return nil }
func (BaseHook) OnTenantCreated(context.Context, *Tenant) error               { return nil }
func (BaseHook) OnTenantProvisioned(context.Context, *Tenant) error           { return nil }
func (BaseHook) OnTenantUpdated(context.Context, *Tenant, *Tenant) error      { return nil }
func (BaseHook) OnTenantStatusChanged(context.Context, *Tenant, string) error { return nil }
func (BaseHook) OnTenantDeleted(context.Context, *Tenant) error               { return nil }

// HookError reports every hook that failed for one event. The tenant write it
// followed has already been committed.
type HookError struct {
	Event  string
	Errors []error
}

func (e *HookError) Error() string {
	return fmt.Sprintf("%d hook(s) failed on %s: %v", len(e.Errors), e.Event, errors.Join(e.Errors...))
}

// Unwrap exposes the individual hook errors to errors.Is / errors.As.
func (e *HookError) Unwrap() []error { return e.Errors }

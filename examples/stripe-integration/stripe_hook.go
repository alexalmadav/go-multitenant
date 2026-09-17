package main

import (
	"context"
	"fmt"

	"github.com/alexalmadav/go-multitenant/tenant"
)

// StripeClient is the slice of the Stripe API this example needs.
type StripeClient interface {
	CreateCustomer(ctx context.Context, name, subdomain string) (string, error)
	DeleteCustomer(ctx context.Context, customerID string) error
}

// StripeHook creates a Stripe customer when a tenant is created, stores the
// customer id in tenant metadata, and deletes the customer when the tenant
// is deleted.
type StripeHook struct {
	tenant.BaseHook
	stripe  StripeClient
	manager tenant.Manager
}

func NewStripeHook(stripe StripeClient, manager tenant.Manager) *StripeHook {
	return &StripeHook{stripe: stripe, manager: manager}
}

func (h *StripeHook) Name() string { return "stripe" }

func (h *StripeHook) OnTenantCreated(ctx context.Context, t *tenant.Tenant) error {
	customerID, err := h.stripe.CreateCustomer(ctx, t.Name, t.Subdomain)
	if err != nil {
		return fmt.Errorf("create stripe customer: %w", err)
	}
	if t.Metadata == nil {
		t.Metadata = tenant.TenantMetadata{}
	}
	tenant.NewStripeExtension(t.Metadata).SetCustomerID(customerID)
	// UpdateTenant fires OnTenantUpdated hooks; this hook ignores that event.
	if err := h.manager.UpdateTenant(ctx, t); err != nil {
		return fmt.Errorf("store stripe customer id: %w", err)
	}
	return nil
}

func (h *StripeHook) OnTenantDeleted(ctx context.Context, t *tenant.Tenant) error {
	id, ok := tenant.NewStripeExtension(t.Metadata).GetCustomerID()
	if !ok {
		return nil
	}
	if err := h.stripe.DeleteCustomer(ctx, id); err != nil {
		return fmt.Errorf("delete stripe customer %s: %w", id, err)
	}
	return nil
}

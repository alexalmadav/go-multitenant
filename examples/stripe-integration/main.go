// Command stripe-integration shows a lifecycle hook that keeps a Stripe
// customer in sync with each tenant. It uses a logging stand-in for the
// Stripe API so it runs without credentials.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/alexalmadav/go-multitenant"
	"github.com/alexalmadav/go-multitenant/tenant"
)

// logStripe prints what a real client would do.
type logStripe struct{}

func (logStripe) CreateCustomer(ctx context.Context, name, subdomain string) (string, error) {
	id := "cus_" + subdomain
	fmt.Printf("stripe: create customer %s for %q\n", id, name)
	return id, nil
}

func (logStripe) DeleteCustomer(ctx context.Context, id string) error {
	fmt.Printf("stripe: delete customer %s\n", id)
	return nil
}

func main() {
	config := multitenant.DefaultConfig()
	config.Database.DSN = os.Getenv("DATABASE_URL")
	config.Database.MigrationsDir = "./migrations" // your tenant schema lives here

	mt, err := multitenant.New(config)
	if err != nil {
		log.Fatal(err)
	}
	defer mt.Close()

	mt.Manager.RegisterHook(NewStripeHook(logStripe{}, mt.Manager))

	ctx := context.Background()
	t := &tenant.Tenant{Name: "Acme Corp", Subdomain: "acme"}
	if err := mt.Manager.CreateTenant(ctx, t); err != nil {
		log.Fatalf("create: %v", err) // a *tenant.HookError here means the tenant exists but Stripe failed
	}
	got, _ := mt.Manager.GetTenant(ctx, t.ID)
	id, _ := tenant.NewStripeExtension(got.Metadata).GetCustomerID()
	fmt.Println("tenant", got.ID, "has stripe customer", id)

	if err := mt.Manager.DeleteTenant(ctx, t.ID); err != nil {
		log.Fatalf("delete: %v", err)
	}
}

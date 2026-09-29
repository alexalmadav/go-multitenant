package database

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

type fakeMigrations struct {
	tenant.MigrationManager
	err error
}

func (f *fakeMigrations) ApplyMigration(context.Context, uuid.UUID, *tenant.Migration) error {
	return f.err
}
func (f *fakeMigrations) ApplyPending(context.Context, uuid.UUID) error              { return f.err }
func (f *fakeMigrations) ApplyToAllTenants(context.Context, *tenant.Migration) error { return f.err }
func (f *fakeMigrations) ApplyPendingToAllTenants(context.Context) error             { return f.err }

type fakeRepo struct {
	tenant.Repository
	tenants []*tenant.Tenant
}

func (f fakeRepo) List(_ context.Context, page, perPage int) ([]*tenant.Tenant, int, error) {
	start := (page - 1) * perPage
	if start >= len(f.tenants) {
		return nil, len(f.tenants), nil
	}
	end := min(start+perPage, len(f.tenants))
	return f.tenants[start:end], len(f.tenants), nil
}

type grantLog struct {
	fakeRoles
	granted []uuid.UUID
}

func (g *grantLog) Grant(_ context.Context, id uuid.UUID) error {
	g.granted = append(g.granted, id)
	return nil
}

func TestGrantingMigrationsGrantAfterEachRun(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	roles := &grantLog{}
	m := NewGrantingMigrationManager(&fakeMigrations{}, roles, fakeRepo{}, fakeSchemas{exists: true})

	if err := m.ApplyMigration(ctx, id, &tenant.Migration{}); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyPending(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(roles.granted, []uuid.UUID{id, id}) {
		t.Errorf("granted = %v, want the tenant after each run", roles.granted)
	}
}

func TestGrantingMigrationsGrantAfterAFailedRun(t *testing.T) {
	roles := &grantLog{}
	boom := errors.New("migration failed")
	m := NewGrantingMigrationManager(&fakeMigrations{err: boom}, roles, fakeRepo{}, fakeSchemas{exists: true})
	id := uuid.New()
	// A failed migration may have created tables before it failed.
	if err := m.ApplyPending(context.Background(), id); !errors.Is(err, boom) {
		t.Fatalf("ApplyPending = %v, want the migration error", err)
	}
	if err := m.ApplyMigration(context.Background(), id, &tenant.Migration{}); !errors.Is(err, boom) {
		t.Fatalf("ApplyMigration = %v, want the migration error", err)
	}
	if !slices.Equal(roles.granted, []uuid.UUID{id, id}) {
		t.Errorf("granted = %v, want a grant after each failed run", roles.granted)
	}
}

func TestGrantingMigrationsSkipUnprovisionedTenants(t *testing.T) {
	roles := &grantLog{}
	ts := []*tenant.Tenant{{ID: uuid.New()}}
	m := NewGrantingMigrationManager(&fakeMigrations{}, roles, fakeRepo{tenants: ts}, fakeSchemas{exists: false})
	if err := m.ApplyPendingToAllTenants(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(roles.granted) != 0 {
		t.Errorf("granted for a tenant with no schema: %v", roles.granted)
	}
}

// Across all tenants, grants run for every tenant even when the migration
// failed for some, and pagination reaches past the first page.
func TestGrantingMigrationsGrantEveryTenantAcrossPages(t *testing.T) {
	var ts []*tenant.Tenant
	for i := 0; i < 150; i++ {
		ts = append(ts, &tenant.Tenant{ID: uuid.New()})
	}
	roles := &grantLog{}
	boom := errors.New("one tenant failed")
	m := NewGrantingMigrationManager(&fakeMigrations{err: boom}, roles, fakeRepo{tenants: ts}, fakeSchemas{exists: true})

	if err := m.ApplyPendingToAllTenants(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("ApplyPendingToAllTenants = %v, want the migration error joined in", err)
	}
	if len(roles.granted) != len(ts) {
		t.Errorf("granted %d tenants, want %d", len(roles.granted), len(ts))
	}
}

package database

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

type provisionedSchemas struct {
	tenant.SchemaManager
	provisioned map[uuid.UUID]bool
}

func (p provisionedSchemas) SchemaExists(_ context.Context, id uuid.UUID) (bool, error) {
	return p.provisioned[id], nil
}

func roleName(id uuid.UUID) string { return "tenant_" + strings.ReplaceAll(id.String(), "-", "_") }

func TestWriteAuthFile(t *testing.T) {
	active, suspended, unprovisioned := uuid.New(), uuid.New(), uuid.New()
	repo := fakeRepo{tenants: []*tenant.Tenant{
		{ID: active, Status: tenant.StatusActive},
		{ID: suspended, Status: tenant.StatusSuspended},
		{ID: unprovisioned, Status: tenant.StatusActive},
	}}
	schemas := provisionedSchemas{provisioned: map[uuid.UUID]bool{active: true, suspended: true}}
	secret := []byte("0123456789abcdef0123456789abcdef")
	creds := tenant.NewCredentialSource(tenant.RoleIsolationConfig{Secret: secret}, roleName)

	var buf bytes.Buffer
	if err := WriteAuthFile(context.Background(), &buf, repo, schemas, creds); err != nil {
		t.Fatal(err)
	}
	want := `"` + roleName(active) + `" "` + tenant.DerivePassword(secret, active) + `"` + "\n"
	if buf.String() != want {
		t.Errorf("auth file =\n%s\nwant only the active, provisioned tenant, in plaintext:\n%s", buf.String(), want)
	}
}

func TestWriteAuthFileRejectsPasswordlessRoles(t *testing.T) {
	id := uuid.New()
	repo := fakeRepo{tenants: []*tenant.Tenant{{ID: id, Status: tenant.StatusActive}}}
	schemas := provisionedSchemas{provisioned: map[uuid.UUID]bool{id: true}}
	creds := tenant.NewCredentialSource(tenant.RoleIsolationConfig{
		Credentials: func(uuid.UUID) (string, error) { return "", nil },
	}, roleName)

	err := WriteAuthFile(context.Background(), &bytes.Buffer{}, repo, schemas, creds)
	if err == nil || !strings.Contains(err.Error(), roleName(id)) {
		t.Errorf("WriteAuthFile = %v, want an error naming the passwordless role", err)
	}
}

func TestAuthFileQuoting(t *testing.T) {
	if got := quoteAuthFile(`a"b`); got != `"a""b"` {
		t.Errorf("quoteAuthFile = %s, want embedded quotes doubled", got)
	}
}

func hookCreds(pw func(uuid.UUID) (string, error)) *tenant.CredentialSource {
	return tenant.NewCredentialSource(tenant.RoleIsolationConfig{Credentials: pw}, roleName)
}

func TestWriteAuthFileSortsByRoleAndSkipsInactiveTenants(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	sort.Slice(ids, func(i, j int) bool { return roleName(ids[i]) < roleName(ids[j]) })
	first, second, cancelled, pending := ids[0], ids[1], ids[2], uuid.New()
	// Inserted out of order.
	repo := fakeRepo{tenants: []*tenant.Tenant{
		{ID: second, Status: tenant.StatusActive},
		{ID: cancelled, Status: tenant.StatusCancelled},
		{ID: pending, Status: tenant.StatusPending},
		{ID: first, Status: tenant.StatusActive},
	}}
	schemas := provisionedSchemas{provisioned: map[uuid.UUID]bool{first: true, second: true, cancelled: true, pending: true}}
	secret := []byte("0123456789abcdef0123456789abcdef")
	creds := tenant.NewCredentialSource(tenant.RoleIsolationConfig{Secret: secret}, roleName)

	var buf bytes.Buffer
	if err := WriteAuthFile(context.Background(), &buf, repo, schemas, creds); err != nil {
		t.Fatal(err)
	}
	want := quoteAuthFile(roleName(first)) + " " + quoteAuthFile(tenant.DerivePassword(secret, first)) + "\n" +
		quoteAuthFile(roleName(second)) + " " + quoteAuthFile(tenant.DerivePassword(secret, second)) + "\n"
	if buf.String() != want {
		t.Errorf("auth file =\n%s\nwant sorted active tenants only:\n%s", buf.String(), want)
	}
}

func TestWriteAuthFileQuotesEmbeddedQuotes(t *testing.T) {
	id := uuid.New()
	repo := fakeRepo{tenants: []*tenant.Tenant{{ID: id, Status: tenant.StatusActive}}}
	schemas := provisionedSchemas{provisioned: map[uuid.UUID]bool{id: true}}
	var buf bytes.Buffer
	err := WriteAuthFile(context.Background(), &buf, repo, schemas, hookCreds(func(uuid.UUID) (string, error) { return `pa"ss`, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"pa""ss"`) {
		t.Errorf("auth file = %q, want the password quoted as \"pa\"\"ss\"", buf.String())
	}
}

func TestWriteAuthFileRejectsControlCharacters(t *testing.T) {
	for _, pw := range []string{"hunter\n2", "hunter\r2", "hunter\x002"} {
		id := uuid.New()
		repo := fakeRepo{tenants: []*tenant.Tenant{{ID: id, Status: tenant.StatusActive}}}
		schemas := provisionedSchemas{provisioned: map[uuid.UUID]bool{id: true}}
		var buf bytes.Buffer
		err := WriteAuthFile(context.Background(), &buf, repo, schemas, hookCreds(func(uuid.UUID) (string, error) { return pw, nil }))
		if err == nil || !strings.Contains(err.Error(), roleName(id)) {
			t.Errorf("password %q: WriteAuthFile = %v, want an error naming the role", pw, err)
			continue
		}
		if strings.Contains(err.Error(), "hunter") {
			t.Errorf("error leaks the password: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("password %q: wrote %q, want nothing", pw, buf.String())
		}
	}
}

func TestWriteAuthFilePropagatesCredentialErrors(t *testing.T) {
	id := uuid.New()
	repo := fakeRepo{tenants: []*tenant.Tenant{{ID: id, Status: tenant.StatusActive}}}
	schemas := provisionedSchemas{provisioned: map[uuid.UUID]bool{id: true}}
	boom := errors.New("boom")
	var buf bytes.Buffer
	err := WriteAuthFile(context.Background(), &buf, repo, schemas, hookCreds(func(uuid.UUID) (string, error) { return "", boom }))
	if !errors.Is(err, boom) {
		t.Errorf("WriteAuthFile = %v, want it to wrap %v", err, boom)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %q, want nothing", buf.String())
	}
}

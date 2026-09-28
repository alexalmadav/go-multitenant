package database

import (
	"bytes"
	"context"
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

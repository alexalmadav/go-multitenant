package database

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/alexalmadav/go-multitenant/tenant"
)

// WriteAuthFile writes PgBouncer auth_file entries for every active,
// provisioned tenant: one "<role>" "<password>" line each, sorted by role.
// Suspended and cancelled tenants are left out, so PgBouncer refuses them too.
//
// The entries are plaintext passwords, not SCRAM verifiers: PgBouncer 1.25.2
// locks a role out after a failed first login when given verifiers. The
// output is therefore a secret, as sensitive as the role secret itself. It
// contains only tenant entries; a deployment adds its own fixed entries, such
// as the admin user.
//
// Nothing is written to w unless every entry could be rendered. The caller
// owns the destination: write it to a file with mode 0600, and replace the
// live file atomically (write a temporary file in the same directory, then
// rename it) so PgBouncer never reads a partial file.
func WriteAuthFile(ctx context.Context, w io.Writer, repo tenant.Repository, schemas tenant.SchemaManager, creds *tenant.CredentialSource) error {
	var lines, passwordless []string
	err := ForEachProvisionedTenant(ctx, repo, schemas, func(t *tenant.Tenant) error {
		if t.Status != tenant.StatusActive {
			return nil
		}
		cred, err := creds.Current(t.ID)
		if err != nil {
			return err
		}
		if cred.Password == "" {
			passwordless = append(passwordless, cred.User)
			return nil
		}
		lines = append(lines, quoteAuthFile(cred.User)+" "+quoteAuthFile(cred.Password))
		return nil
	})
	if err != nil {
		return err
	}
	if len(passwordless) > 0 {
		return fmt.Errorf("database: %d tenant role(s) have no password and cannot authenticate through an auth_file: %s",
			len(passwordless), strings.Join(passwordless, ", "))
	}
	sort.Strings(lines)
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return err
		}
	}
	return nil
}

// quoteAuthFile quotes a value the way PgBouncer's auth_file expects, with
// embedded double quotes doubled.
func quoteAuthFile(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

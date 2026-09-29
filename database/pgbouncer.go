package database

import (
	"bytes"
	"context"
	"errors"
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
// auth_file has no escape for a line break, so a role or password containing
// NUL, CR or LF is rejected, naming the role only. On error, w must be
// discarded; write to a temp file with mode 0600 and rename. The whole file is
// rendered first and written to w with a single Write call.
func WriteAuthFile(ctx context.Context, w io.Writer, repo tenant.Repository, schemas tenant.SchemaManager, creds *tenant.CredentialSource) error {
	type entry struct{ user, line string }
	var entries []entry
	var problems []error
	err := ForEachProvisionedTenant(ctx, repo, schemas, func(t *tenant.Tenant) error {
		if t.Status != tenant.StatusActive {
			return nil
		}
		cred, err := creds.Current(t.ID)
		if err != nil {
			return err
		}
		switch {
		case cred.Password == "":
			problems = append(problems, fmt.Errorf("database: tenant role %q has no password and cannot authenticate through an auth_file", cred.User))
		case strings.ContainsAny(cred.User, authFileForbidden) || strings.ContainsAny(cred.Password, authFileForbidden):
			problems = append(problems, fmt.Errorf("database: tenant role %q has a name or password containing a NUL or line break, which an auth_file cannot represent", cred.User))
		default:
			entries = append(entries, entry{cred.User, quoteAuthFile(cred.User) + " " + quoteAuthFile(cred.Password)})
		}
		return nil
	})
	if err != nil {
		return errors.Join(append(problems, err)...)
	}
	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].user < entries[j].user })
	var buf bytes.Buffer
	for _, e := range entries {
		buf.WriteString(e.line)
		buf.WriteByte('\n')
	}
	_, err = w.Write(buf.Bytes())
	return err
}

// authFileForbidden lists the characters an auth_file value cannot carry.
const authFileForbidden = "\x00\r\n"

// quoteAuthFile quotes a value the way PgBouncer's auth_file expects, with
// embedded double quotes doubled.
func quoteAuthFile(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

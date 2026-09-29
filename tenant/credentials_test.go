package tenant

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDerivePasswordKnownAnswer(t *testing.T) {
	secret := bytes.Repeat([]byte("k"), 32)
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	if got, want := DerivePassword(secret, id), "3y1jGN_R3_fvFo_TDofAlY4FsI9FpYBVbWSpd-70YeI"; got != want {
		t.Errorf("DerivePassword() = %q, want %q", got, want)
	}
}

func TestDerivePasswordVaries(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	s1, s2 := bytes.Repeat([]byte("1"), 32), bytes.Repeat([]byte("2"), 32)
	if DerivePassword(s1, a) != DerivePassword(s1, a) {
		t.Error("DerivePassword is not stable for the same inputs")
	}
	if DerivePassword(s1, a) == DerivePassword(s1, b) {
		t.Error("two tenants derived the same password")
	}
	if DerivePassword(s1, a) == DerivePassword(s2, a) {
		t.Error("two secrets derived the same password")
	}
}

func TestSCRAMVerifierKnownAnswer(t *testing.T) {
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i)
	}
	got, err := SCRAMVerifier("pencil", salt)
	if err != nil {
		t.Fatal(err)
	}
	want := "SCRAM-SHA-256$4096:AAECAwQFBgcICQoLDA0ODw==$zHCdol2044/ZyWzPLi7oxApCkamKw9Z+E4U/QApd/5Y=:dd5peBOitVnLNFu7VmwP+HiDaaw4OUCv396eVCWhYiE="
	if got != want {
		t.Errorf("SCRAMVerifier() =\n  %s\nwant\n  %s", got, want)
	}
}

func TestNewSCRAMVerifierUsesARandomSalt(t *testing.T) {
	a, err := NewSCRAMVerifier("pw")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSCRAMVerifier("pw")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two verifiers for one password were identical; the salt is not random")
	}
	if !strings.HasPrefix(a, "SCRAM-SHA-256$4096:") {
		t.Errorf("verifier %q is not SCRAM-SHA-256 with 4096 iterations", a)
	}
}

func roleNameForTest(id uuid.UUID) string {
	return "tenant_" + strings.ReplaceAll(id.String(), "-", "_")
}

func TestCredentialSourceDerived(t *testing.T) {
	id := uuid.New()
	cur, old := bytes.Repeat([]byte("c"), 32), bytes.Repeat([]byte("o"), 32)
	s := NewCredentialSource(RoleIsolationConfig{Secret: cur, PreviousSecrets: [][]byte{old}}, roleNameForTest)

	c, err := s.Current(id)
	if err != nil {
		t.Fatal(err)
	}
	if c.User != roleNameForTest(id) || c.Password != DerivePassword(cur, id) {
		t.Errorf("Current() = %+v, want the schema name and the current secret's password", c)
	}

	cands, err := s.Candidates(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 || cands[0] != c || cands[1].Password != DerivePassword(old, id) || cands[1].User != c.User {
		t.Errorf("Candidates() = %+v, want current then previous", cands)
	}
}

func TestCredentialSourceHook(t *testing.T) {
	id := uuid.New()
	s := NewCredentialSource(RoleIsolationConfig{
		PreviousSecrets: [][]byte{bytes.Repeat([]byte("o"), 32)},
		Credentials:     func(got uuid.UUID) (string, error) { return "from-hook-" + got.String()[:4], nil },
	}, roleNameForTest)

	cands, err := s.Candidates(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Password != "from-hook-"+id.String()[:4] {
		t.Errorf("Candidates() = %+v, want only the hook's credentials; PreviousSecrets are ignored with a hook", cands)
	}

	failing := NewCredentialSource(RoleIsolationConfig{
		Credentials: func(uuid.UUID) (string, error) { return "", errors.New("vault down") },
	}, roleNameForTest)
	if _, err := failing.Current(id); err == nil || !strings.Contains(err.Error(), "vault down") {
		t.Errorf("Current() = %v, want the hook's error", err)
	}
}

func TestIsAuthFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"postgres invalid password":  {&pgconn.PgError{Code: "28P01", Message: "password authentication failed for user x"}, true},
		"postgres role cannot login": {&pgconn.PgError{Code: "28000", Message: `role "x" is not permitted to log in`}, true},
		"pgbouncer sasl failure":     {&pgconn.PgError{Code: "08P01", Message: "SASL authentication failed"}, true},
		"wrapped pgbouncer failure":  {fmt.Errorf("connect: %w", &pgconn.PgError{Code: "08P01", Message: "SASL authentication failed"}), true},
		"other protocol violation":   {&pgconn.PgError{Code: "08P01", Message: "server login failed: wrong password type"}, false},
		"undefined table":            {&pgconn.PgError{Code: "42P01", Message: "relation does not exist"}, false},
		"not a postgres error":       {errors.New("dial tcp: connection refused"), false},
		"nil":                        {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IsAuthFailure(tc.err); got != tc.want {
				t.Errorf("IsAuthFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

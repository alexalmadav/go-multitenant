package tenant

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	passwordLabel   = "go-multitenant/role-password/v1:"
	scramIterations = 4096
	scramSaltLen    = 16
)

// DerivePassword returns the database password for a tenant's role, derived
// from secret. The result is base64url, so ASCII, which makes the SASLprep
// normalisation SCRAM specifies the identity.
func DerivePassword(secret []byte, tenantID uuid.UUID) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(passwordLabel + tenantID.String()))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SCRAMVerifier returns the SCRAM-SHA-256 verifier PostgreSQL stores for
// password with the given salt, in the form ALTER ROLE ... PASSWORD accepts.
func SCRAMVerifier(password string, salt []byte) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, scramIterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("tenant: derive SCRAM key: %w", err)
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	enc := base64.StdEncoding
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", scramIterations,
		enc.EncodeToString(salt), enc.EncodeToString(storedKey[:]), enc.EncodeToString(serverKey)), nil
}

// NewSCRAMVerifier is SCRAMVerifier with a random 16-byte salt.
func NewSCRAMVerifier(password string) (string, error) {
	salt := make([]byte, scramSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("tenant: SCRAM salt: %w", err)
	}
	return SCRAMVerifier(password, salt)
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

// RoleCredentials is one way to log in as a tenant's role.
type RoleCredentials struct {
	User     string
	Password string
}

// CredentialSource resolves tenant role credentials under a
// RoleIsolationConfig: derived from the secrets, or from its Credentials hook.
type CredentialSource struct {
	cfg      RoleIsolationConfig
	roleName func(uuid.UUID) string
}

// NewCredentialSource returns a CredentialSource for cfg. roleName maps a
// tenant to its role, which is its schema name.
func NewCredentialSource(cfg RoleIsolationConfig, roleName func(uuid.UUID) string) *CredentialSource {
	return &CredentialSource{cfg: cfg, roleName: roleName}
}

// RoleName returns the database role for a tenant.
func (s *CredentialSource) RoleName(tenantID uuid.UUID) string { return s.roleName(tenantID) }

// Current returns the credentials the tenant's role should have now.
func (s *CredentialSource) Current(tenantID uuid.UUID) (RoleCredentials, error) {
	user := s.roleName(tenantID)
	if s.cfg.Credentials != nil {
		password, err := s.cfg.Credentials(tenantID)
		if err != nil {
			return RoleCredentials{}, fmt.Errorf("tenant: credentials for %s: %w", user, err)
		}
		return RoleCredentials{User: user, Password: password}, nil
	}
	return RoleCredentials{User: user, Password: DerivePassword(s.cfg.Secret, tenantID)}, nil
}

// Candidates returns every set of credentials worth trying when connecting:
// the current one first, then one per previous secret. With a Credentials
// hook it returns only the current one.
func (s *CredentialSource) Candidates(tenantID uuid.UUID) ([]RoleCredentials, error) {
	current, err := s.Current(tenantID)
	if err != nil {
		return nil, err
	}
	out := []RoleCredentials{current}
	if s.cfg.Credentials != nil {
		return out, nil
	}
	for _, prev := range s.cfg.PreviousSecrets {
		out = append(out, RoleCredentials{User: current.User, Password: DerivePassword(prev, tenantID)})
	}
	return out, nil
}

// IsAuthFailure reports whether err is a failed login worth retrying with a
// previous secret: a SQLSTATE in class 28, which PostgreSQL sends, or 08P01
// with "authentication failed" in its message, which PgBouncer sends. 08P01
// alone is a generic protocol violation and is not one.
func IsAuthFailure(err error) bool {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return false
	}
	if strings.HasPrefix(pe.Code, "28") {
		return true
	}
	return pe.Code == "08P01" && strings.Contains(strings.ToLower(pe.Message), "authentication failed")
}

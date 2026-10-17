// Package auth hashes passwords (argon2id), issues session tokens and defines the role hierarchy (RBAC).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// Roles in increasing order of privilege; each role includes the permissions of the ones before it.
const (
	Viewer       = "viewer"
	Operator     = "operator"
	Configurator = "configurator"
	Publisher    = "publisher"
	Admin        = "admin"
)

// Roles lists the roles from least to most privileged.
var Roles = []string{Viewer, Operator, Configurator, Publisher, Admin}

func rank(role string) int { return slices.Index(Roles, role) }

// ValidRoles reports whether every role is known and at least one is given.
func ValidRoles(roles []string) error {
	if len(roles) == 0 {
		return errors.New("at least one role")
	}
	for _, r := range roles {
		if rank(r) < 0 {
			return fmt.Errorf("unknown role %q (want one of %s)", r, strings.Join(Roles, ", "))
		}
	}
	return nil
}

// Allows reports whether any of the roles grants the required role.
func Allows(roles []string, need string) bool {
	n := rank(need)
	for _, r := range roles {
		if rank(r) >= n && n >= 0 {
			return true
		}
	}
	return false
}

// Argon2id parameters (OWASP: m=19 MiB, t=2, p=1 minimum; these are above it).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

var b64 = base64.RawStdEncoding

// HashPassword returns a PHC-format argon2id hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// ErrBadHash is returned for hashes not produced by HashPassword.
var ErrBadHash = errors.New("unsupported password hash")

// VerifyPassword checks a password against a hash in constant time.
func VerifyPassword(hash, pw string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var v int
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, ErrBadHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return false, ErrBadHash
	}
	salt, err1 := b64.DecodeString(parts[4])
	key, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(key) == 0 {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// CheckPasswordPolicy enforces a minimum length of 12 and three character classes.
func CheckPasswordPolicy(pw string) error {
	if len([]rune(pw)) < 12 {
		return errors.New("password must have at least 12 characters")
	}
	var lower, upper, digit, other bool
	for _, r := range pw {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			n++
		}
	}
	if n < 3 {
		return errors.New("password needs three of: lower case, upper case, digits, symbols")
	}
	return nil
}

// NewToken returns a random session token (base64url, 256 bits) and its storage hash.
func NewToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	return tok, TokenHash(tok), nil
}

// TokenHash is the SHA-256 of a token; only the hash is stored.
func TokenHash(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

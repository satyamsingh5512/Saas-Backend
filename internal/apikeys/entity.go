// Package apikeys implements machine-to-machine credentials: long-lived, scoped
// keys that authenticate a tenant without a user session.
//
// Only a SHA-256 hash of each key is stored. The plaintext is returned exactly
// once at creation and is unrecoverable afterward, which is why the UI must tell
// the operator to copy it immediately. This is the same model GitHub and Stripe
// use, and it means a database compromise does not hand over working credentials.
package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Key mirrors the `api_keys` table.
type Key struct {
	ID         uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID   uuid.UUID      `gorm:"column:tenant_id" json:"tenant_id"`
	UserID     *uuid.UUID     `gorm:"column:user_id" json:"user_id,omitempty"`
	Name       string         `json:"name"`
	KeyPrefix  string         `gorm:"column:key_prefix" json:"key_prefix"`
	KeyHash    string         `gorm:"column:key_hash" json:"-"`
	Scopes     pq.StringArray `gorm:"column:scopes;type:text[]" json:"scopes"`
	LastUsedAt *time.Time     `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time     `gorm:"column:expires_at" json:"expires_at,omitempty"`
	RevokedAt  *time.Time     `gorm:"column:revoked_at" json:"revoked_at,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

func (Key) TableName() string { return "api_keys" }

// Active reports whether the key may currently authenticate a request.
func (k *Key) Active() bool {
	if k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return false
	}
	return true
}

// CreateResult carries a newly created key plus its one-time plaintext secret.
type CreateResult struct {
	Key *Key `json:"key"`
	// Secret is shown once and never stored in recoverable form.
	Secret string `json:"secret"`
}

const (
	// keyPrefixLiteral namespaces the credential so a leaked string is
	// recognizable as an API key by secret scanners (GitHub's push protection and
	// similar tools match on known prefixes), which is what gets a leak revoked
	// quickly rather than silently exploited.
	keyPrefixLiteral = "sk_live_"

	// secretBytes is the random entropy behind each key: 256 bits, matching the
	// invitation tokens, since a key grants ongoing tenant access.
	secretBytes = 32

	// storedPrefixLength must not exceed the key_prefix column's VARCHAR(12).
	storedPrefixLength = 12
)

// GenerateKey returns a new plaintext key, the display prefix to persist, and the
// hash to persist.
func GenerateKey() (plaintext, prefix, hash string, err error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("apikeys: generate key: %w", err)
	}

	plaintext = keyPrefixLiteral + base64.RawURLEncoding.EncodeToString(buf)
	prefix = plaintext[:storedPrefixLength]
	return plaintext, prefix, HashKey(plaintext), nil
}

// HashKey returns the hex-encoded SHA-256 of a plaintext key.
//
// SHA-256 rather than bcrypt is deliberate: the input is 256 bits of uniform
// randomness (not a guessable human secret), and this hash is computed on every
// authenticated machine request, where a purposely slow KDF would become the
// request's dominant cost.
func HashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// LooksLikeAPIKey reports whether a credential string carries the API key prefix,
// letting the authentication middleware decide whether to treat a bearer token as
// an API key or a JWT without attempting to parse it as both.
func LooksLikeAPIKey(credential string) bool {
	return strings.HasPrefix(credential, keyPrefixLiteral)
}

// SecureEqual compares two hashes in constant time.
//
// The lookup is by hash equality in SQL, so this is belt-and-braces rather than
// the primary defense, but it costs nothing and keeps any future in-Go comparison
// free of a timing side channel.
func SecureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

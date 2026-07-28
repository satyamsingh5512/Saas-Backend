package apikeys

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateKeyProducesDistinctPrefixedKeys(t *testing.T) {
	first, prefix, hash, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey returned an error: %v", err)
	}

	if !strings.HasPrefix(first, keyPrefixLiteral) {
		t.Errorf("expected key to carry the %q prefix, got %q", keyPrefixLiteral, first)
	}
	if len(prefix) != storedPrefixLength {
		t.Errorf("expected a %d character stored prefix, got %d (%q)", storedPrefixLength, len(prefix), prefix)
	}
	if !strings.HasPrefix(first, prefix) {
		t.Errorf("stored prefix %q is not a prefix of the key", prefix)
	}
	if hash != HashKey(first) {
		t.Error("returned hash does not match HashKey of the plaintext")
	}
	// The plaintext must not be recoverable from what gets persisted.
	if strings.Contains(hash, first) {
		t.Error("hash contains the plaintext key")
	}

	second, _, _, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey returned an error: %v", err)
	}
	if first == second {
		t.Error("two generated keys collided, which indicates the entropy source is broken")
	}
}

func TestHashKeyIsStableAndDistinct(t *testing.T) {
	if HashKey("sk_live_abc") != HashKey("sk_live_abc") {
		t.Error("HashKey is not deterministic")
	}
	if HashKey("sk_live_abc") == HashKey("sk_live_abd") {
		t.Error("HashKey collided for different inputs")
	}
}

func TestLooksLikeAPIKey(t *testing.T) {
	if !LooksLikeAPIKey("sk_live_something") {
		t.Error("expected a prefixed credential to be recognized as an API key")
	}
	// A JWT must not be mistaken for an API key, or it would be looked up as one
	// and rejected instead of being parsed as a token.
	if LooksLikeAPIKey("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abc.def") {
		t.Error("expected a JWT not to be recognized as an API key")
	}
	if LooksLikeAPIKey("") {
		t.Error("expected an empty credential not to be recognized as an API key")
	}
}

func TestSecureEqual(t *testing.T) {
	if !SecureEqual("abc", "abc") {
		t.Error("expected identical strings to compare equal")
	}
	if SecureEqual("abc", "abd") {
		t.Error("expected differing strings to compare unequal")
	}
	if SecureEqual("abc", "abcd") {
		t.Error("expected different-length strings to compare unequal")
	}
}

func TestKeyActive(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	if (&Key{}).Active() != true {
		t.Error("a key with no expiry and no revocation should be active")
	}
	if (&Key{ExpiresAt: &future}).Active() != true {
		t.Error("a key expiring in the future should be active")
	}
	if (&Key{ExpiresAt: &past}).Active() != false {
		t.Error("an expired key must not be active")
	}
	if (&Key{RevokedAt: &past}).Active() != false {
		t.Error("a revoked key must not be active")
	}
	// Revocation must win even when the expiry is still in the future.
	if (&Key{ExpiresAt: &future, RevokedAt: &past}).Active() != false {
		t.Error("a revoked key must not be active regardless of its expiry")
	}
}

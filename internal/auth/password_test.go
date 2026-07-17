package auth

import "testing"

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("supersecret123")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if hash == "" {
		t.Fatal("HashPassword returned empty hash")
	}
	if hash == "supersecret123" {
		t.Fatal("hash must not equal the plaintext password")
	}
}

func TestCheckPassword(t *testing.T) {
	hash, err := HashPassword("supersecret123")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	if !CheckPassword(hash, "supersecret123") {
		t.Error("expected correct password to match")
	}

	if CheckPassword(hash, "wrongpassword") {
		t.Error("expected incorrect password to not match")
	}
}

func TestCheckPassword_InvalidHash(t *testing.T) {
	if CheckPassword("not-a-valid-bcrypt-hash", "anything") {
		t.Error("expected invalid hash to fail comparison")
	}
}

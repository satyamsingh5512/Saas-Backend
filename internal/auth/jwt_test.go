package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestGenerateAndParseToken(t *testing.T) {
	secret := "test-secret"
	userID := uuid.New()
	tenantID := uuid.New()
	role := "admin"

	token, err := GenerateToken(secret, "24", userID, tenantID, role)
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}
	if token == "" {
		t.Fatal("GenerateToken returned empty token")
	}

	claims, err := ParseToken(secret, token)
	if err != nil {
		t.Fatalf("ParseToken returned error: %v", err)
	}

	if claims.UserID != userID {
		t.Errorf("expected UserID %v, got %v", userID, claims.UserID)
	}
	if claims.TenantID != tenantID {
		t.Errorf("expected TenantID %v, got %v", tenantID, claims.TenantID)
	}
	if claims.Role != role {
		t.Errorf("expected Role %v, got %v", role, claims.Role)
	}
}

func TestParseToken_WrongSecret(t *testing.T) {
	token, err := GenerateToken("secret-a", "24", uuid.New(), uuid.New(), "admin")
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}

	if _, err := ParseToken("secret-b", token); err == nil {
		t.Error("expected error when parsing token with wrong secret")
	}
}

func TestParseToken_Expired(t *testing.T) {
	secret := "test-secret"
	userID := uuid.New()
	tenantID := uuid.New()

	claims := Claims{
		UserID:   userID,
		TenantID: tenantID,
		Role:     "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to sign test token: %v", err)
	}

	if _, err := ParseToken(secret, signed); err == nil {
		t.Error("expected error when parsing expired token")
	}
}

func TestParseToken_Malformed(t *testing.T) {
	if _, err := ParseToken("test-secret", "not-a-jwt"); err == nil {
		t.Error("expected error when parsing malformed token")
	}
}

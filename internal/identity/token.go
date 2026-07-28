package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims are the custom JWT claims embedded in access tokens. TenantID is
// the sole source of truth for tenant scoping post-authentication (Phase 2.2
// design rule); Role is retained for quick display purposes but permission
// checks (internal/authz) always re-verify against the database/cache,
// never trust the token's Role claim alone, since role/permission changes
// must take effect before the token's natural expiry.
type Claims struct {
	UserID   uuid.UUID `json:"user_id"`
	TenantID uuid.UUID `json:"tenant_id"`
	Role     string    `json:"role"`
	jwt.RegisteredClaims
}

// GenerateAccessToken creates a signed, short-lived JWT for the given
// user/tenant. Kept as a small pure function (no DB/IO) for easy unit testing.
func GenerateAccessToken(secret string, expiryMinutes int, userID, tenantID uuid.UUID, role string) (string, error) {
	if expiryMinutes <= 0 {
		expiryMinutes = 15
	}

	claims := Claims{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(expiryMinutes) * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// GenerateAccessTokenLegacyHours preserves the previous JWT_EXPIRY_HOURS-based
// signature for callers/tests that haven't migrated to the short-lived
// access + long-lived refresh token model yet.
func GenerateAccessTokenLegacyHours(secret string, expiryHours string, userID, tenantID uuid.UUID, role string) (string, error) {
	hours, err := strconv.Atoi(expiryHours)
	if err != nil || hours <= 0 {
		hours = 24
	}
	return GenerateAccessToken(secret, hours*60, userID, tenantID, role)
}

// ParseAccessToken validates and parses a JWT, returning its claims.
func ParseAccessToken(secret, tokenString string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// GenerateOpaqueToken creates a cryptographically random, URL-safe token
// (used for refresh tokens, invitations, email verification, and password
// reset). Only the SHA-256 hash of this value is ever persisted; the raw
// value is returned to the caller exactly once.
func GenerateOpaqueToken() (string, error) {
	buf := make([]byte, 32) // 256 bits of entropy
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashOpaqueToken returns the hex-encoded SHA-256 hash of an opaque token,
// suitable for equality-comparison lookups without storing the token itself.
func HashOpaqueToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

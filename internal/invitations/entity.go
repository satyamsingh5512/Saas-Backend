// Package invitations implements emailing a prospective member an opaque invite
// token and redeeming it into a user account with a preassigned role.
//
// Only a SHA-256 hash of the token is ever persisted, the same pattern used for
// refresh and verification tokens. A database dump therefore does not yield usable
// invites: the plaintext exists only in the response to the inviter (for delivery)
// and in the invitee's email.
package invitations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Invitation lifecycle states permitted by the schema's CHECK constraint.
const (
	StatusPending  = "pending"
	StatusAccepted = "accepted"
	StatusRevoked  = "revoked"
	StatusExpired  = "expired"
)

// Invitation mirrors the `invitations` table.
type Invitation struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID   uuid.UUID  `gorm:"column:tenant_id" json:"tenant_id"`
	Email      string     `json:"email"`
	RoleID     uuid.UUID  `gorm:"column:role_id" json:"role_id"`
	TokenHash  string     `gorm:"column:token_hash" json:"-"`
	Status     string     `json:"status"`
	InvitedBy  *uuid.UUID `gorm:"column:invited_by" json:"invited_by,omitempty"`
	ExpiresAt  time.Time  `gorm:"column:expires_at" json:"expires_at"`
	AcceptedAt *time.Time `gorm:"column:accepted_at" json:"accepted_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (Invitation) TableName() string { return "invitations" }

// Detail is an invitation joined with its role name for list rendering.
type Detail struct {
	Invitation
	RoleName string `json:"role_name"`
	RoleSlug string `json:"role_slug"`
}

// Preview is the deliberately minimal, unauthenticated view of an invitation
// returned to someone holding a token.
//
// It exposes the organization name, the invited email, and the role -- enough to
// let the invitee confirm they are joining the right place. It withholds the
// inviter's identity and any organization membership detail, since anyone who
// obtains a leaked token can read this without authenticating.
type Preview struct {
	Email            string    `json:"email"`
	OrganizationName string    `json:"organization_name"`
	OrganizationSlug string    `json:"organization_slug"`
	RoleName         string    `json:"role_name"`
	ExpiresAt        time.Time `json:"expires_at"`
	RequiresPassword bool      `json:"requires_password"`
}

// AcceptResult reports which tenant and user an accepted invite resolved to, so
// the caller can immediately issue that user a session.
type AcceptResult struct {
	TenantID uuid.UUID
	UserID   uuid.UUID
	Email    string
}

// tokenBytes is the entropy of a generated invite token. 32 bytes (256 bits) puts
// brute-force guessing far out of reach, which matters because a valid token
// alone is sufficient to join an organization.
const tokenBytes = 32

// GenerateToken returns a new URL-safe invite token and its storage hash.
func GenerateToken() (plaintext, hash string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("invitations: generate token: %w", err)
	}
	plaintext = base64.RawURLEncoding.EncodeToString(buf)
	return plaintext, HashToken(plaintext), nil
}

// HashToken returns the hex-encoded SHA-256 of an invite token.
//
// A plain hash (not bcrypt) is correct here: the input is 256 bits of uniform
// random data, so it is not subject to dictionary or brute-force attack the way a
// user-chosen password is, and the lookup must be a fast indexed equality match.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

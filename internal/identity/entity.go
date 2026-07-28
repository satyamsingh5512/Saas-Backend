package identity

import (
	"time"

	"github.com/google/uuid"
)

// User mirrors the `users` table (migrations/000002). Tenant-scoped and
// RLS-protected; all repository access MUST go through pkg/txscope.
type User struct {
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID        uuid.UUID  `gorm:"column:tenant_id" json:"tenant_id"`
	Email           string     `json:"email"`
	PasswordHash    *string    `gorm:"column:password_hash" json:"-"`
	FullName        string     `gorm:"column:full_name" json:"full_name"`
	AvatarURL       *string    `gorm:"column:avatar_url" json:"avatar_url,omitempty"`
	Status          string     `json:"status"`
	EmailVerifiedAt *time.Time `gorm:"column:email_verified_at" json:"email_verified_at,omitempty"`
	LastLoginAt     *time.Time `gorm:"column:last_login_at" json:"last_login_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"-"`
}

func (User) TableName() string { return "users" }

const (
	StatusActive   = "active"
	StatusInvited  = "invited"
	StatusDisabled = "disabled"
)

// RefreshToken mirrors the `refresh_tokens` table (migrations/000005).
// Only the hash is ever persisted; the plaintext token is returned to the
// client once and never stored.
type RefreshToken struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID   uuid.UUID  `gorm:"column:tenant_id" json:"-"`
	UserID     uuid.UUID  `gorm:"column:user_id" json:"-"`
	TokenHash  string     `gorm:"column:token_hash" json:"-"`
	FamilyID   uuid.UUID  `gorm:"column:family_id" json:"-"`
	ReplacedBy *uuid.UUID `gorm:"column:replaced_by" json:"-"`
	UserAgent  string     `gorm:"column:user_agent" json:"-"`
	IPAddress  *string    `gorm:"column:ip_address" json:"-"`
	ExpiresAt  time.Time  `gorm:"column:expires_at" json:"-"`
	RevokedAt  *time.Time `gorm:"column:revoked_at" json:"-"`
	CreatedAt  time.Time  `json:"-"`
}

func (RefreshToken) TableName() string { return "refresh_tokens" }

// OAuthAccount mirrors the `oauth_accounts` table (migrations/000005).
type OAuthAccount struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID       uuid.UUID  `gorm:"column:tenant_id" json:"-"`
	UserID         uuid.UUID  `gorm:"column:user_id" json:"-"`
	Provider       string     `json:"provider"`
	ProviderUserID string     `gorm:"column:provider_user_id" json:"-"`
	AccessToken    *string    `gorm:"column:access_token" json:"-"`
	RefreshToken   *string    `gorm:"column:refresh_token" json:"-"`
	TokenExpiresAt *time.Time `gorm:"column:token_expires_at" json:"-"`
	CreatedAt      time.Time  `json:"-"`
	UpdatedAt      time.Time  `json:"-"`
}

func (OAuthAccount) TableName() string { return "oauth_accounts" }

const (
	ProviderGoogle = "google"
	ProviderGitHub = "github"
)

// VerificationToken mirrors the `verification_tokens` table (migrations/000005),
// used for both email verification and password reset flows.
type VerificationToken struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"-"`
	TenantID  uuid.UUID  `gorm:"column:tenant_id" json:"-"`
	UserID    uuid.UUID  `gorm:"column:user_id" json:"-"`
	Purpose   string     `json:"-"`
	TokenHash string     `gorm:"column:token_hash" json:"-"`
	ExpiresAt time.Time  `gorm:"column:expires_at" json:"-"`
	UsedAt    *time.Time `gorm:"column:used_at" json:"-"`
	CreatedAt time.Time  `json:"-"`
}

func (VerificationToken) TableName() string { return "verification_tokens" }

const (
	PurposeEmailVerification = "email_verification"
	PurposePasswordReset     = "password_reset"
)

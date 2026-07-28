// Package preferences implements per-user settings and self-service profile
// management, backed by the `user_preferences` table from migrations/000007.
//
// Preferences live in their own table rather than as columns on `users` because
// `users` is read on every authenticated request; keeping frequently-updated
// display settings out of that row avoids generating MVCC churn on a hot table.
package preferences

import (
	"time"

	"github.com/google/uuid"
)

// Theme values permitted by the schema's CHECK constraint.
const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

// Preferences mirrors the `user_preferences` table, one row per user.
type Preferences struct {
	UserID             uuid.UUID `gorm:"column:user_id;primaryKey" json:"user_id"`
	TenantID           uuid.UUID `gorm:"column:tenant_id" json:"tenant_id"`
	Timezone           string    `json:"timezone"`
	Locale             string    `json:"locale"`
	Theme              string    `json:"theme"`
	EmailNotifications bool      `gorm:"column:email_notifications" json:"email_notifications"`
	UpdatedAt          time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Preferences) TableName() string { return "user_preferences" }

// Defaults returns the preference row a user implicitly has before they save any
// settings, matching the column defaults in the schema.
//
// Returning defaults for a missing row keeps GET idempotent and side-effect free:
// the row is created on first write, not on first read.
func Defaults(tenantID, userID uuid.UUID) *Preferences {
	return &Preferences{
		UserID:             userID,
		TenantID:           tenantID,
		Timezone:           "UTC",
		Locale:             "en-US",
		Theme:              ThemeSystem,
		EmailNotifications: true,
	}
}

// Profile is the self-service view of the caller's own account, combining user
// fields with their preferences and role.
type Profile struct {
	UserID          uuid.UUID    `json:"user_id"`
	TenantID        uuid.UUID    `json:"tenant_id"`
	Email           string       `json:"email"`
	FullName        string       `json:"full_name"`
	AvatarURL       *string      `json:"avatar_url,omitempty"`
	Status          string       `json:"status"`
	EmailVerifiedAt *time.Time   `json:"email_verified_at,omitempty"`
	LastLoginAt     *time.Time   `json:"last_login_at,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	Roles           []string     `json:"roles"`
	Permissions     []string     `json:"permissions"`
	Preferences     *Preferences `json:"preferences"`
	Organization    Organization `json:"organization"`
}

// Organization is the tenant summary embedded in a profile response.
type Organization struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Slug     string    `json:"slug"`
	PlanCode string    `json:"plan_code"`
}

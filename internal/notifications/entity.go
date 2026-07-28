// Package notifications implements per-user in-app notifications, backed by the
// `notifications` table from migrations/000007.
//
// Notifications are written by domain flows and event consumers, never directly by
// clients: a client-writable notification endpoint would let one user fabricate
// alerts for another. The HTTP surface here is therefore read-and-acknowledge only.
package notifications

import (
	"time"

	"github.com/google/uuid"
)

// Well-known notification types, used by clients to pick an icon and rendering.
const (
	TypeInvitationAccepted = "invitation_accepted"
	TypeRoleChanged        = "role_changed"
	TypeTeamInvite         = "team_invite"
	TypeProjectAssigned    = "project_assigned"
	TypeQuotaWarning       = "quota_warning"
	TypeSubscription       = "subscription_changed"
)

// Notification mirrors the `notifications` table.
type Notification struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID  uuid.UUID      `gorm:"column:tenant_id" json:"tenant_id"`
	UserID    uuid.UUID      `gorm:"column:user_id" json:"user_id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	Metadata  map[string]any `gorm:"serializer:json" json:"metadata"`
	ReadAt    *time.Time     `gorm:"column:read_at" json:"read_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func (Notification) TableName() string { return "notifications" }

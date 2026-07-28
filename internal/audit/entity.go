// Package audit implements the two complementary history streams defined in
// migrations/000007: a compliance-facing append-only audit log, and a
// product-facing activity feed.
//
// They are deliberately separate tables rather than one "events" table with a
// flag. Audit entries answer "who changed permissions/billing/credentials, from
// which IP" for a security reviewer and must never be edited or deleted;
// activity entries answer "what happened in this project lately" for end users
// and are safe to prune. Conflating them would force the stricter retention and
// immutability rules of the former onto the high-volume, disposable latter.
package audit

import (
	"time"

	"github.com/google/uuid"
)

// AuditLog mirrors the `audit_logs` table. Append-only: this package exposes no
// update or delete operation, and the database role is expected to lack
// UPDATE/DELETE grants on the table as defense in depth (see migrations/000007).
type AuditLog struct {
	ID         uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID   uuid.UUID      `gorm:"column:tenant_id" json:"tenant_id"`
	ActorID    *uuid.UUID     `gorm:"column:actor_id" json:"actor_id,omitempty"`
	Action     string         `json:"action"`
	TargetType *string        `gorm:"column:target_type" json:"target_type,omitempty"`
	TargetID   *uuid.UUID     `gorm:"column:target_id" json:"target_id,omitempty"`
	IPAddress  *string        `gorm:"column:ip_address;type:inet" json:"ip_address,omitempty"`
	UserAgent  string         `gorm:"column:user_agent" json:"user_agent"`
	Metadata   map[string]any `gorm:"serializer:json" json:"metadata"`
	CreatedAt  time.Time      `json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_logs" }

// ActivityEvent mirrors the `activity_events` table: a per-resource timeline
// built from the same domain actions, with a polymorphic target so one feed can
// span projects, teams, and the organization itself.
type ActivityEvent struct {
	ID         uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID   uuid.UUID      `gorm:"column:tenant_id" json:"tenant_id"`
	ActorID    *uuid.UUID     `gorm:"column:actor_id" json:"actor_id,omitempty"`
	TargetType string         `gorm:"column:target_type" json:"target_type"`
	TargetID   uuid.UUID      `gorm:"column:target_id" json:"target_id"`
	Verb       string         `json:"verb"`
	Metadata   map[string]any `gorm:"serializer:json" json:"metadata"`
	CreatedAt  time.Time      `json:"created_at"`
}

func (ActivityEvent) TableName() string { return "activity_events" }

// Audit action codes. Namespaced `resource.action` and treated as an append-only
// vocabulary: existing codes are never renamed, because stored rows and external
// compliance tooling reference them as strings.
const (
	ActionUserInvited        = "user.invited"
	ActionUserRemoved        = "user.removed"
	ActionUserRoleChanged    = "user.role_changed"
	ActionUserProfileUpdate  = "user.profile_updated"
	ActionRoleCreated        = "role.created"
	ActionRoleUpdated        = "role.updated"
	ActionRoleDeleted        = "role.deleted"
	ActionTeamCreated        = "team.created"
	ActionTeamUpdated        = "team.updated"
	ActionTeamDeleted        = "team.deleted"
	ActionTeamMemberAdded    = "team.member_added"
	ActionTeamMemberRemoved  = "team.member_removed"
	ActionProjectCreated     = "project.created"
	ActionProjectUpdated     = "project.updated"
	ActionProjectArchived    = "project.archived"
	ActionProjectDeleted     = "project.deleted"
	ActionAPIKeyCreated      = "apikey.created"
	ActionAPIKeyRevoked      = "apikey.revoked"
	ActionInviteRevoked      = "invitation.revoked"
	ActionInviteAccepted     = "invitation.accepted"
	ActionSubscriptionChange = "billing.subscription_changed"
	ActionPasswordChanged    = "auth.password_changed"
)

// Activity target types for the polymorphic feed.
const (
	TargetOrganization = "organization"
	TargetTeam         = "team"
	TargetProject      = "project"
	TargetUser         = "user"
)

// Activity verbs, kept short and past-tense for readable feed rendering.
const (
	VerbCreated  = "created"
	VerbUpdated  = "updated"
	VerbDeleted  = "deleted"
	VerbArchived = "archived"
	VerbJoined   = "joined"
	VerbLeft     = "left"
)

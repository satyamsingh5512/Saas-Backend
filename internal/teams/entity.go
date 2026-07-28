// Package teams implements team management and team membership, backed by the
// `teams` and `team_members` tables from migrations/000004.
//
// Teams are an organizational grouping inside a tenant, distinct from roles:
// a role decides what a user may do, a team decides which slice of work they are
// grouped around. Team membership therefore grants no permissions on its own.
package teams

import (
	"time"

	"github.com/google/uuid"
)

// Team mirrors the `teams` table. Soft-deleted via DeletedAt so that the
// (tenant_id, slug) uniqueness index -- which is partial on deleted_at IS NULL --
// frees a slug for reuse once a team is removed.
type Team struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID    uuid.UUID  `gorm:"column:tenant_id" json:"tenant_id"`
	Name        string     `json:"name"`
	Slug        string     `json:"slug"`
	Description string     `json:"description"`
	CreatedBy   *uuid.UUID `gorm:"column:created_by" json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"-"`
}

func (Team) TableName() string { return "teams" }

// Member mirrors the `team_members` join table.
type Member struct {
	TeamID   uuid.UUID `gorm:"column:team_id;primaryKey" json:"team_id"`
	UserID   uuid.UUID `gorm:"column:user_id;primaryKey" json:"user_id"`
	TenantID uuid.UUID `gorm:"column:tenant_id" json:"tenant_id"`
	JoinedAt time.Time `gorm:"column:joined_at" json:"joined_at"`
}

func (Member) TableName() string { return "team_members" }

// MemberDetail is a team member joined with the user fields a UI needs to render
// a member list, avoiding an N+1 user lookup per row.
type MemberDetail struct {
	UserID   uuid.UUID `json:"user_id"`
	Email    string    `json:"email"`
	FullName string    `json:"full_name"`
	Status   string    `json:"status"`
	JoinedAt time.Time `json:"joined_at"`
}

// TeamDetail is a team plus its member count, the shape list views need.
type TeamDetail struct {
	Team
	MemberCount int64 `json:"member_count"`
}

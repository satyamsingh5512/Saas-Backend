// Package projects implements project management and project membership, backed
// by the `projects` and `project_members` tables from migrations/000004.
//
// A project may optionally belong to a team. The FK is ON DELETE SET NULL, so
// deleting a team orphans its projects rather than cascading the deletion -- work
// product outliving the group that happened to own it is the safer default.
package projects

import (
	"time"

	"github.com/google/uuid"
)

// Lifecycle states permitted by the schema's CHECK constraint on projects.status.
const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

// Project mirrors the `projects` table.
type Project struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID    uuid.UUID  `gorm:"column:tenant_id" json:"tenant_id"`
	TeamID      *uuid.UUID `gorm:"column:team_id" json:"team_id,omitempty"`
	Name        string     `json:"name"`
	Slug        string     `json:"slug"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	CreatedBy   *uuid.UUID `gorm:"column:created_by" json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"-"`
}

func (Project) TableName() string { return "projects" }

// Member mirrors the `project_members` join table.
type Member struct {
	ProjectID uuid.UUID `gorm:"column:project_id;primaryKey" json:"project_id"`
	UserID    uuid.UUID `gorm:"column:user_id;primaryKey" json:"user_id"`
	TenantID  uuid.UUID `gorm:"column:tenant_id" json:"tenant_id"`
	JoinedAt  time.Time `gorm:"column:joined_at" json:"joined_at"`
}

func (Member) TableName() string { return "project_members" }

// MemberDetail is a project member joined with displayable user fields.
type MemberDetail struct {
	UserID   uuid.UUID `json:"user_id"`
	Email    string    `json:"email"`
	FullName string    `json:"full_name"`
	Status   string    `json:"status"`
	JoinedAt time.Time `json:"joined_at"`
}

// ProjectDetail is a project enriched with its member count and owning team name
// for list rendering without follow-up queries.
type ProjectDetail struct {
	Project
	TeamName    *string `json:"team_name,omitempty"`
	MemberCount int64   `json:"member_count"`
}

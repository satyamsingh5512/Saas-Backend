package tenancy

import (
	"time"

	"github.com/google/uuid"
)

// Tenant mirrors the `tenants` table (migrations/000002). Deliberately not a
// GORM model with tenant_id/RLS -- this is the root entity multi-tenancy is
// built on and has no tenant scope of its own.
type Tenant struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	Name      string         `json:"name"`
	Slug      string         `json:"slug"`
	PlanCode  string         `gorm:"column:plan_code" json:"plan_code"`
	Status    string         `json:"status"`
	Settings  map[string]any `gorm:"serializer:json" json:"settings"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt *time.Time     `json:"-"`
}

// TableName pins the GORM table name explicitly rather than relying on
// pluralization inference, which is more robust across GORM versions.
func (Tenant) TableName() string { return "tenants" }

const (
	StatusActive          = "active"
	StatusSuspended       = "suspended"
	StatusPendingDeletion = "pending_deletion"
)

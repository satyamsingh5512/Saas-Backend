package authz

import (
	"time"

	"github.com/google/uuid"
)

// Role mirrors the `roles` table (migrations/000003). Tenant-scoped,
// RLS-protected. System roles (Owner/Admin/Manager/Member/Guest) are
// auto-provisioned per tenant by the seed_default_roles trigger
// (migrations/000008) and cannot be deleted (IsSystem), but their
// permission sets remain editable -- this is what makes RBAC "dynamic"
// rather than a hardcoded enum.
type Role struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID    uuid.UUID `gorm:"column:tenant_id" json:"tenant_id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	IsSystem    bool      `gorm:"column:is_system" json:"is_system"`
	Rank        int16     `json:"rank"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Role) TableName() string { return "roles" }

// Permission mirrors the `permissions` table (migrations/000003): a global,
// platform-defined capability catalog. Not tenant-scoped -- tenants compose
// roles from this fixed set rather than inventing new capability strings.
type Permission struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Code        string    `json:"code"`
	Resource    string    `json:"resource"`
	Action      string    `json:"action"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

func (Permission) TableName() string { return "permissions" }

// RolePermission mirrors the `role_permissions` join table.
type RolePermission struct {
	RoleID       uuid.UUID `gorm:"column:role_id" json:"role_id"`
	PermissionID uuid.UUID `gorm:"column:permission_id" json:"permission_id"`
	TenantID     uuid.UUID `gorm:"column:tenant_id" json:"tenant_id"`
	CreatedAt    time.Time `json:"created_at"`
}

func (RolePermission) TableName() string { return "role_permissions" }

// UserRole mirrors the `user_roles` join table: a user can hold multiple
// roles simultaneously, and their effective permission set is the union of
// every held role's permissions.
type UserRole struct {
	UserID     uuid.UUID  `gorm:"column:user_id" json:"user_id"`
	RoleID     uuid.UUID  `gorm:"column:role_id" json:"role_id"`
	TenantID   uuid.UUID  `gorm:"column:tenant_id" json:"tenant_id"`
	AssignedAt time.Time  `gorm:"column:assigned_at" json:"assigned_at"`
	AssignedBy *uuid.UUID `gorm:"column:assigned_by" json:"assigned_by,omitempty"`
}

func (UserRole) TableName() string { return "user_roles" }

// System role slugs seeded by migrations/000008's trigger. Application
// code should generally prefer permission checks over role-slug checks
// (that's the point of dynamic RBAC), but a few flows -- like "who can
// delete the organization" -- are deliberately tied to the Owner role
// specifically regardless of its (editable) permission set.
const (
	RoleOwner   = "owner"
	RoleAdmin   = "admin"
	RoleManager = "manager"
	RoleMember  = "member"
	RoleGuest   = "guest"
)

// Well-known permission codes, matching the catalog seeded in
// migrations/000003. Defined as constants so call sites get compile-time
// checking instead of typo-prone magic strings, while the underlying
// role->permission mapping remains fully dynamic/DB-driven.
const (
	PermOrgManage     = "org:manage"
	PermOrgView       = "org:view"
	PermMemberInvite  = "member:invite"
	PermMemberRemove  = "member:remove"
	PermMemberView    = "member:view"
	PermRoleManage    = "role:manage"
	PermRoleView      = "role:view"
	PermTeamCreate    = "team:create"
	PermTeamManage    = "team:manage"
	PermTeamView      = "team:view"
	PermProjectCreate = "project:create"
	PermProjectManage = "project:manage"
	PermProjectView   = "project:view"
	PermProjectDelete = "project:delete"
	PermAPIKeyManage  = "apikey:manage"
	PermAPIKeyView    = "apikey:view"
	PermBillingManage = "billing:manage"
	PermBillingView   = "billing:view"
	PermFileUpload    = "file:upload"
	PermFileDelete    = "file:delete"
	PermAuditView     = "audit:view"
)

// GrantableScopes returns every permission code an API key may be scoped to.
//
// Role and organization administration are deliberately excluded. An API key is a
// bearer credential with no second factor and a long life; letting one grant roles
// or delete the organization would make a single leaked key an unrecoverable
// compromise. Those actions stay bound to an interactive user session.
func GrantableScopes() []string {
	return []string{
		PermOrgView,
		PermMemberView,
		PermMemberInvite,
		PermRoleView,
		PermTeamView,
		PermTeamCreate,
		PermTeamManage,
		PermProjectView,
		PermProjectCreate,
		PermProjectManage,
		PermProjectDelete,
		PermAPIKeyView,
		PermBillingView,
		PermFileUpload,
		PermFileDelete,
		PermAuditView,
	}
}

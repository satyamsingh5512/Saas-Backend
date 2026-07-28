package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
)

var ErrRoleNotFound = errors.New("authz: role not found")

// Repository provides tenant-scoped data access for roles, permissions, and
// their assignments.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// FindRoleBySlugTx finds a role by slug within tx's already-set tenant
// scope. Used during registration (Service.AssignSystemRoleTx), where the
// caller already holds an open transaction from identity.Service.Register.
func (r *Repository) FindRoleBySlugTx(tx *gorm.DB, tenantID uuid.UUID, slug string) (*Role, error) {
	var role Role
	err := tx.Where("tenant_id = ? AND slug = ?", tenantID, slug).First(&role).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRoleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authz: find role by slug tx: %w", err)
	}
	return &role, nil
}

// AssignUserRoleTx inserts a user_roles row within an already-open
// transaction (used by the same registration flow).
func (r *Repository) AssignUserRoleTx(tx *gorm.DB, ur *UserRole) error {
	if err := tx.Create(ur).Error; err != nil {
		return fmt.Errorf("authz: assign user role tx: %w", err)
	}
	return nil
}

// ListRoles returns all roles for the current tenant scope, ordered by rank
// (most powerful first).
func (r *Repository) ListRoles(ctx context.Context) ([]Role, error) {
	var roles []Role
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Order("rank ASC").Find(&roles).Error
	})
	if err != nil {
		return nil, fmt.Errorf("authz: list roles: %w", err)
	}
	return roles, nil
}

// FindRoleBySlug finds a role by slug within the current tenant scope.
func (r *Repository) FindRoleBySlug(ctx context.Context, slug string) (*Role, error) {
	var role Role
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("slug = ?", slug).First(&role).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrRoleNotFound
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	return &role, nil
}

// CreateRole inserts a new custom (non-system) role for the current tenant.
func (r *Repository) CreateRole(ctx context.Context, role *Role) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Create(role).Error
	})
}

// UpdateRole persists changes to a role's name/description/permission set.
// Deleting/renaming an IsSystem role's slug is prevented at the service
// layer, not here.
func (r *Repository) UpdateRole(ctx context.Context, role *Role) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Save(role).Error
	})
}

// DeleteRole removes a non-system role. System roles must never be passed
// here; the service layer enforces that invariant.
func (r *Repository) DeleteRole(ctx context.Context, roleID uuid.UUID) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Delete(&Role{}, "id = ?", roleID).Error
	})
}

// SetRolePermissions replaces a role's entire permission set atomically
// (delete-then-insert within one transaction).
func (r *Repository) SetRolePermissions(ctx context.Context, tenantID, roleID uuid.UUID, permissionIDs []uuid.UUID) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Delete(&RolePermission{}, "role_id = ?", roleID).Error; err != nil {
			return err
		}
		if len(permissionIDs) == 0 {
			return nil
		}
		rows := make([]RolePermission, 0, len(permissionIDs))
		for _, pid := range permissionIDs {
			rows = append(rows, RolePermission{RoleID: roleID, PermissionID: pid, TenantID: tenantID})
		}
		return tx.Create(&rows).Error
	})
}

// ListPermissions returns the entire global permission catalog. Not
// tenant-scoped (no RLS on the permissions table), safe to call without a
// tenant in context.
func (r *Repository) ListPermissions(ctx context.Context) ([]Permission, error) {
	var perms []Permission
	if err := r.db.WithContext(ctx).Order("resource, action").Find(&perms).Error; err != nil {
		return nil, fmt.Errorf("authz: list permissions: %w", err)
	}
	return perms, nil
}

// FindPermissionsByCodes looks up permission catalog rows by code, used
// when translating a role-permission-update request's codes into IDs.
func (r *Repository) FindPermissionsByCodes(ctx context.Context, codes []string) ([]Permission, error) {
	var perms []Permission
	if err := r.db.WithContext(ctx).Where("code IN ?", codes).Find(&perms).Error; err != nil {
		return nil, fmt.Errorf("authz: find permissions by codes: %w", err)
	}
	return perms, nil
}

// UserPermissionCodes returns the union of permission codes granted by every
// role the given user holds within the current tenant scope. This is the
// query the permission-check middleware calls (through a Redis cache layer
// added in the Redis integration phase); it is intentionally a single query
// with joins rather than N+1 role lookups.
func (r *Repository) UserPermissionCodes(ctx context.Context, userID uuid.UUID) ([]string, error) {
	var codes []string
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("user_roles ur").
			Joins("JOIN role_permissions rp ON rp.role_id = ur.role_id").
			Joins("JOIN permissions p ON p.id = rp.permission_id").
			Where("ur.user_id = ?", userID).
			Distinct().
			Pluck("p.code", &codes).Error
	})
	if err != nil {
		return nil, fmt.Errorf("authz: user permission codes: %w", err)
	}
	return codes, nil
}

// UserRoles returns every role a user holds within the current tenant scope,
// ordered by rank (most powerful first).
func (r *Repository) UserRoles(ctx context.Context, userID uuid.UUID) ([]Role, error) {
	var roles []Role
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("roles r").
			Joins("JOIN user_roles ur ON ur.role_id = r.id").
			Where("ur.user_id = ?", userID).
			Order("r.rank ASC").
			Find(&roles).Error
	})
	if err != nil {
		return nil, fmt.Errorf("authz: user roles: %w", err)
	}
	return roles, nil
}

// AssignRole grants roleID to userID within the current tenant scope.
func (r *Repository) AssignRole(ctx context.Context, ur *UserRole) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Create(ur).Error
	})
}

// RevokeRole removes a role grant from a user.
func (r *Repository) RevokeRole(ctx context.Context, userID, roleID uuid.UUID) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Delete(&UserRole{}, "user_id = ? AND role_id = ?", userID, roleID).Error
	})
}

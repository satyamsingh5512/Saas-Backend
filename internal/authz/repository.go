package authz

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrRoleNotFound          = errors.New("authz: role not found")
	ErrUnknownPermissionCode = errors.New("authz: unknown permission code")
	ErrStaleRoleRevision     = errors.New("authz: stale role revision")
	ErrUserNotFound          = errors.New("authz: user not found")
	ErrRoleAssignmentMissing = errors.New("authz: role assignment not found")
)

// Repository provides tenant-scoped data access for roles, permissions, and
// their assignments.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// actorAccess is loaded inside the same transaction as an RBAC mutation. That
// keeps rank and permission checks from racing a concurrent role edit.
type actorAccess struct {
	Rank        int16
	IsOwner     bool
	Permissions map[string]struct{}
}

func (r *Repository) withTenantTx(ctx context.Context, fn func(*gorm.DB) error) error {
	return txscope.WithTenantTx(ctx, r.db, fn)
}

func (r *Repository) actorAccessTx(tx *gorm.DB, actorID uuid.UUID) (*actorAccess, error) {
	var actorRow struct{ ID uuid.UUID }
	if err := tx.Table("users").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", actorID).Take(&actorRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	var roles []Role
	if err := tx.Table("roles r").Clauses(clause.Locking{Strength: "UPDATE"}).
		Joins("JOIN user_roles ur ON ur.role_id = r.id").
		Where("ur.user_id = ?", actorID).
		Order("r.rank ASC").Find(&roles).Error; err != nil {
		return nil, err
	}
	if len(roles) == 0 {
		return nil, ErrUserNotFound
	}

	var codes []string
	if err := tx.Table("user_roles ur").
		Joins("JOIN role_permissions rp ON rp.role_id = ur.role_id").
		Joins("JOIN permissions p ON p.id = rp.permission_id").
		Where("ur.user_id = ?", actorID).Distinct().Pluck("p.code", &codes).Error; err != nil {
		return nil, err
	}
	access := &actorAccess{Rank: roles[0].Rank, Permissions: make(map[string]struct{}, len(codes))}
	for _, role := range roles {
		if role.Slug == RoleOwner {
			access.IsOwner = true
		}
	}
	for _, code := range codes {
		access.Permissions[code] = struct{}{}
	}
	return access, nil
}

func (r *Repository) roleForUpdateTx(tx *gorm.DB, roleID uuid.UUID) (*Role, error) {
	var role Role
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", roleID).First(&role).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRoleNotFound
	}
	return &role, err
}

func (r *Repository) rolePermissionCodesTx(tx *gorm.DB, roleID uuid.UUID) ([]string, error) {
	var codes []string
	err := tx.Table("role_permissions rp").
		Joins("JOIN permissions p ON p.id = rp.permission_id").
		Where("rp.role_id = ?", roleID).Pluck("p.code", &codes).Error
	return codes, err
}

func (r *Repository) permissionsByCodesTx(tx *gorm.DB, codes []string) ([]Permission, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	var permissions []Permission
	if err := tx.Where("code IN ?", codes).Find(&permissions).Error; err != nil {
		return nil, err
	}
	if len(permissions) != len(codes) {
		return nil, ErrUnknownPermissionCode
	}
	return permissions, nil
}

func (r *Repository) userExistsTx(tx *gorm.DB, userID uuid.UUID) (bool, error) {
	var row struct{ ID uuid.UUID }
	err := tx.Table("users").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", userID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (r *Repository) highestRoleRankTx(tx *gorm.DB, userID uuid.UUID) (int16, bool, error) {
	var rows []struct{ Rank int16 }
	err := tx.Table("roles r").Select("r.rank").
		Joins("JOIN user_roles ur ON ur.role_id = r.id").
		Where("ur.user_id = ?", userID).Order("r.rank ASC").Limit(1).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return 0, false, err
	}
	return rows[0].Rank, true, nil
}

func (r *Repository) ownerCountTx(tx *gorm.DB) (int64, error) {
	var count int64
	err := tx.Table("user_roles ur").
		Joins("JOIN roles r ON r.id = ur.role_id").
		Joins("JOIN users u ON u.id = ur.user_id AND u.deleted_at IS NULL AND u.status <> 'disabled'").
		Where("r.slug = ?", RoleOwner).Count(&count).Error
	return count, err
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

// RolePermissionCodes returns one role and its authoritative grants from the
// caller's tenant. The role lookup happens before the join query so an absent
// or cross-tenant role is distinguishable from a real role with no grants.
func (r *Repository) RolePermissionCodes(ctx context.Context, roleID uuid.UUID) (*Role, []string, error) {
	var role Role
	codes := make([]string, 0)
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", roleID).First(&role).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoleNotFound
			}
			return err
		}
		return tx.Table("role_permissions rp").
			Joins("JOIN permissions p ON p.id = rp.permission_id").
			Where("rp.role_id = ?", roleID).
			Order("p.code ASC").
			Pluck("p.code", &codes).Error
	})
	if err != nil {
		return nil, nil, fmt.Errorf("authz: get role permissions: %w", err)
	}
	return &role, codes, nil
}

// SetRolePermissions replaces a role's entire permission set atomically.
// It is retained for role creation, where the role has already been inserted
// and permission IDs have already been resolved.
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

// ReplaceRolePermissions validates and replaces an existing role's grants in
// one tenant-scoped transaction. The role row is locked so revision checking,
// delete/insert, and revision advancement cannot race another editor.
func (r *Repository) ReplaceRolePermissions(
	ctx context.Context,
	tenantID, roleID uuid.UUID,
	permissionCodes []string,
	expectedRevision *time.Time,
) (*Role, []string, error) {
	var role Role
	codes := append([]string(nil), permissionCodes...)
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", roleID).First(&role).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoleNotFound
			}
			return err
		}
		if expectedRevision != nil && !role.UpdatedAt.Equal(*expectedRevision) {
			return ErrStaleRoleRevision
		}

		var permissions []Permission
		if len(codes) > 0 {
			if err := tx.Where("code IN ?", codes).Find(&permissions).Error; err != nil {
				return err
			}
			if len(permissions) != len(codes) {
				return ErrUnknownPermissionCode
			}
		}

		if err := tx.Delete(&RolePermission{}, "role_id = ?", roleID).Error; err != nil {
			return err
		}
		if len(permissions) > 0 {
			rows := make([]RolePermission, 0, len(permissions))
			for _, permission := range permissions {
				rows = append(rows, RolePermission{
					RoleID: roleID, PermissionID: permission.ID, TenantID: tenantID,
				})
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}

		// The roles updated_at trigger supplies the new authoritative revision.
		if err := tx.Model(&Role{}).Where("id = ?", roleID).
			UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", roleID).First(&role).Error
	})
	if err != nil {
		return nil, nil, fmt.Errorf("authz: replace role permissions: %w", err)
	}
	sort.Strings(codes)
	return &role, codes, nil
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

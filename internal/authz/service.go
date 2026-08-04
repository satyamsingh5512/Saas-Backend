package authz

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PermissionCache is the minimal surface Service needs from the Redis-backed
// permission cache (added in the Redis integration phase). A nil cache
// falls back to querying Postgres on every check, which is correct but
// slower -- so this module is fully functional before Redis exists.
type PermissionCache interface {
	GetUserPermissions(ctx context.Context, tenantID, userID uuid.UUID) ([]string, bool)
	SetUserPermissions(ctx context.Context, tenantID, userID uuid.UUID, codes []string)
	InvalidateUserPermissions(ctx context.Context, tenantID, userID uuid.UUID)
}

// Service implements the dynamic RBAC engine: role/permission CRUD and the
// permission-check queries used by authorization middleware.
type Service struct {
	repo  *Repository
	cache PermissionCache
}

func NewService(repo *Repository, cache PermissionCache) *Service {
	return &Service{repo: repo, cache: cache}
}

// AssignSystemRoleTx looks up a system role by slug and grants it to a user,
// all within an already-open transaction. Implements identity.RoleAssigner,
// used by the registration flow to grant the first user the Owner role.
func (s *Service) AssignSystemRoleTx(tx *gorm.DB, tenantID, userID uuid.UUID, roleSlug string) error {
	role, err := s.repo.FindRoleBySlugTx(tx, tenantID, roleSlug)
	if err != nil {
		return fmt.Errorf("authz: assign system role: %w", err)
	}
	return s.repo.AssignUserRoleTx(tx, &UserRole{UserID: userID, RoleID: role.ID, TenantID: tenantID})
}

// AssignSystemRole is the non-transactional variant of AssignSystemRoleTx,
// used by flows (e.g. OAuth-provisioned users) that create the user outside
// of the single atomic tenant-creation transaction Register uses. Implements
// identity.RoleAssigner.
func (s *Service) AssignSystemRole(ctx context.Context, tenantID, userID uuid.UUID, roleSlug string) error {
	role, err := s.repo.FindRoleBySlug(ctx, roleSlug)
	if err != nil {
		return fmt.Errorf("authz: assign system role: %w", err)
	}
	return s.repo.AssignRole(ctx, &UserRole{UserID: userID, RoleID: role.ID, TenantID: tenantID})
}

// PrimaryRoleSlug returns the highest-ranked (most powerful) role slug held
// by the user, for display and JWT-claim purposes. Implements
// identity.RoleAssigner.
func (s *Service) PrimaryRoleSlug(ctx context.Context, userID uuid.UUID) (string, error) {
	roles, err := s.repo.UserRoles(ctx, userID)
	if err != nil {
		return "", err
	}
	if len(roles) == 0 {
		return RoleGuest, nil
	}
	return roles[0].Slug, nil // UserRoles is ordered by rank ASC (most powerful first)
}

// UserPermissions returns the full set of permission codes granted to a
// user (union across all their roles), checking the cache first.
func (s *Service) UserPermissions(ctx context.Context, tenantID, userID uuid.UUID) ([]string, error) {
	if s.cache != nil {
		if codes, ok := s.cache.GetUserPermissions(ctx, tenantID, userID); ok {
			return codes, nil
		}
	}

	codes, err := s.repo.UserPermissionCodes(ctx, userID)
	if err != nil {
		return nil, err
	}

	if s.cache != nil {
		s.cache.SetUserPermissions(ctx, tenantID, userID, codes)
	}
	return codes, nil
}

// HasPermission checks whether a user holds the given permission code
// within the current tenant scope.
func (s *Service) HasPermission(ctx context.Context, tenantID, userID uuid.UUID, code string) (bool, error) {
	codes, err := s.UserPermissions(ctx, tenantID, userID)
	if err != nil {
		return false, err
	}
	for _, c := range codes {
		if c == code {
			return true, nil
		}
	}
	return false, nil
}

// InvalidateCache must be called whenever a user's role assignments or a
// role's permission set changes, so stale cached permissions don't outlive
// the change. Called by RoleChanged/PermissionChanged event consumers and
// directly by the mutating service methods below.
func (s *Service) InvalidateCache(ctx context.Context, tenantID, userID uuid.UUID) {
	if s.cache != nil {
		s.cache.InvalidateUserPermissions(ctx, tenantID, userID)
	}
}

// ListRoles returns all roles for the current tenant.
func (s *Service) ListRoles(ctx context.Context) ([]Role, error) {
	return s.repo.ListRoles(ctx)
}

// ListPermissionCatalog returns the full global permission catalog, for
// building role-editing UI.
func (s *Service) ListPermissionCatalog(ctx context.Context) ([]Permission, error) {
	return s.repo.ListPermissions(ctx)
}

// CreateRole creates a custom role only from permissions the actor currently
// possesses. Custom roles are deliberately lowest-ranked and can never carry
// org:manage, which remains exclusive to the seeded Owner role.
func (s *Service) CreateRole(ctx context.Context, tenantID, actorID uuid.UUID, name, description string, permissionCodes []string) (*Role, error) {
	codes := uniquePermissionCodes(permissionCodes)
	role := &Role{
		ID: uuid.New(), TenantID: tenantID, Name: name, Slug: slugify(name),
		Description: description, IsSystem: false, Rank: 100,
	}

	err := s.repo.withTenantTx(ctx, func(tx *gorm.DB) error {
		actor, err := s.repo.actorAccessTx(tx, actorID)
		if err != nil {
			return err
		}
		if !actor.IsOwner && role.Rank <= actor.Rank {
			return apperror.New(apperror.CodeForbidden, "cannot create a role at or above your rank")
		}
		permissions, err := s.repo.permissionsByCodesTx(tx, codes)
		if err != nil {
			return err
		}
		for _, permission := range permissions {
			if permission.Code == PermOrgManage {
				return apperror.New(apperror.CodeForbidden, "org:manage is exclusive to the Owner role")
			}
			if _, ok := actor.Permissions[permission.Code]; !ok {
				return apperror.New(apperror.CodeForbidden, "cannot delegate a permission you do not possess")
			}
		}
		if err := tx.Create(role).Error; err != nil {
			return err
		}
		return setRolePermissionsTx(tx, tenantID, role.ID, permissions)
	})
	if err != nil {
		return nil, roleMutationError("failed to create role", err)
	}
	return role, nil
}

func uniquePermissionCodes(codes []string) []string {
	seen := make(map[string]struct{}, len(codes))
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

func setRolePermissionsTx(tx *gorm.DB, tenantID, roleID uuid.UUID, permissions []Permission) error {
	if err := tx.Delete(&RolePermission{}, "role_id = ?", roleID).Error; err != nil {
		return err
	}
	if len(permissions) == 0 {
		return nil
	}
	rows := make([]RolePermission, 0, len(permissions))
	for _, permission := range permissions {
		rows = append(rows, RolePermission{RoleID: roleID, PermissionID: permission.ID, TenantID: tenantID})
	}
	return tx.Create(&rows).Error
}

func roleMutationError(message string, err error) error {
	if appErr, ok := apperror.As(err); ok {
		return appErr
	}
	switch {
	case errors.Is(err, ErrRoleNotFound):
		return apperror.New(apperror.CodeNotFound, "role not found")
	case errors.Is(err, ErrUserNotFound):
		return apperror.New(apperror.CodeNotFound, "user not found")
	case errors.Is(err, ErrUnknownPermissionCode):
		return apperror.New(apperror.CodeValidation, "one or more permission codes are unknown")
	case errors.Is(err, ErrStaleRoleRevision):
		return apperror.New(apperror.CodeConflict, "role permissions changed since they were loaded")
	case errors.Is(err, ErrRoleAssignmentMissing):
		return apperror.New(apperror.CodeConflict, "role is not assigned to that user")
	default:
		return apperror.Wrap(apperror.CodeInternal, message, err)
	}
}

func authorizeRoleDelegation(actor *actorAccess, role *Role, codes []string) error {
	if role.Slug == RoleOwner && !actor.IsOwner {
		return apperror.New(apperror.CodeForbidden, "only an Owner can manage the Owner role")
	}
	if !actor.IsOwner && role.Rank <= actor.Rank {
		return apperror.New(apperror.CodeForbidden, "cannot manage a role at or above your rank")
	}
	for _, code := range codes {
		if code == PermOrgManage && role.Slug != RoleOwner {
			return apperror.New(apperror.CodeForbidden, "org:manage is exclusive to the Owner role")
		}
		if _, ok := actor.Permissions[code]; !ok {
			return apperror.New(apperror.CodeForbidden, "cannot delegate a permission you do not possess")
		}
	}
	return nil
}

func authorizeRoleControl(actor *actorAccess, role *Role, codes []string) error {
	if role.Slug == RoleOwner && !actor.IsOwner {
		return apperror.New(apperror.CodeForbidden, "only an Owner can manage the Owner role")
	}
	if !actor.IsOwner && role.Rank <= actor.Rank {
		return apperror.New(apperror.CodeForbidden, "cannot manage a role at or above your rank")
	}
	for _, code := range codes {
		if code == PermOrgManage && !actor.IsOwner {
			return apperror.New(apperror.CodeForbidden, "only an Owner can manage org:manage")
		}
		if _, ok := actor.Permissions[code]; !ok {
			return apperror.New(apperror.CodeForbidden, "cannot manage permissions you do not possess")
		}
	}
	return nil
}

// CanDelegateRole is used by invitations before persisting a role selection.
func (s *Service) CanDelegateRole(ctx context.Context, tenantID, actorID, roleID uuid.UUID) error {
	err := s.repo.withTenantTx(ctx, func(tx *gorm.DB) error {
		actor, err := s.repo.actorAccessTx(tx, actorID)
		if err != nil {
			return err
		}
		role, err := s.repo.roleForUpdateTx(tx, roleID)
		if err != nil {
			return err
		}
		codes, err := s.repo.rolePermissionCodesTx(tx, roleID)
		if err != nil {
			return err
		}
		return authorizeRoleDelegation(actor, role, codes)
	})
	if err != nil {
		return roleMutationError("failed to authorize role delegation", err)
	}
	return nil
}

// RolePermissions is the authoritative editable state for one tenant role.
// Revision is derived from roles.updated_at and can be supplied on a later PUT
// to prevent one administrator from overwriting another administrator's edit.
type RolePermissions struct {
	RoleID          uuid.UUID `json:"role_id"`
	PermissionCodes []string  `json:"permission_codes"`
	Revision        string    `json:"revision"`
}

func roleRevision(updatedAt time.Time) string {
	return updatedAt.UTC().Format(time.RFC3339Nano)
}

// GetRolePermissions returns one tenant-scoped role's current grants.
func (s *Service) GetRolePermissions(ctx context.Context, roleID uuid.UUID) (*RolePermissions, error) {
	role, codes, err := s.repo.RolePermissionCodes(ctx, roleID)
	if err != nil {
		if errors.Is(err, ErrRoleNotFound) {
			return nil, apperror.New(apperror.CodeNotFound, "role not found")
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load role permissions", err)
	}
	return &RolePermissions{
		RoleID: role.ID, PermissionCodes: codes, Revision: roleRevision(role.UpdatedAt),
	}, nil
}

// UpdateRolePermissions replaces a role's permission set. System roles CAN
// have their permissions edited (that's the "dynamic" requirement) but
// their slug/IsSystem flag can never change, which is enforced by only
// exposing this narrow update, never a generic "update role" that could
// rename a system role's slug out from under code that matches on it
// (e.g. RoleOwner-gated org-deletion checks).
func (s *Service) UpdateRolePermissions(
	ctx context.Context,
	tenantID, actorID, roleID uuid.UUID,
	permissionCodes []string,
	expectedRevision *time.Time,
) (*RolePermissions, error) {
	codes := uniquePermissionCodes(permissionCodes)
	var role Role
	err := s.repo.withTenantTx(ctx, func(tx *gorm.DB) error {
		actor, err := s.repo.actorAccessTx(tx, actorID)
		if err != nil {
			return err
		}
		lockedRole, err := s.repo.roleForUpdateTx(tx, roleID)
		if err != nil {
			return err
		}
		role = *lockedRole
		if expectedRevision != nil && !role.UpdatedAt.Equal(*expectedRevision) {
			return ErrStaleRoleRevision
		}
		permissions, err := s.repo.permissionsByCodesTx(tx, codes)
		if err != nil {
			return err
		}
		if err := authorizeRoleDelegation(actor, &role, codes); err != nil {
			return err
		}
		if role.Slug == RoleOwner {
			hasOrgManage := false
			for _, code := range codes {
				if code == PermOrgManage {
					hasOrgManage = true
					break
				}
			}
			if !hasOrgManage {
				return apperror.New(apperror.CodeForbidden, "the Owner role must retain org:manage")
			}
		}
		if err := setRolePermissionsTx(tx, tenantID, roleID, permissions); err != nil {
			return err
		}
		if err := tx.Model(&Role{}).Where("id = ?", roleID).
			UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", roleID).First(&role).Error
	})
	if err != nil {
		return nil, roleMutationError("failed to update role permissions", err)
	}
	sort.Strings(codes)
	return &RolePermissions{RoleID: role.ID, PermissionCodes: codes, Revision: roleRevision(role.UpdatedAt)}, nil
}

// DeleteRole removes only a lower-ranked custom role. The role row is locked
// while the actor's current authority is checked.
func (s *Service) DeleteRole(ctx context.Context, actorID, roleID uuid.UUID) error {
	err := s.repo.withTenantTx(ctx, func(tx *gorm.DB) error {
		actor, err := s.repo.actorAccessTx(tx, actorID)
		if err != nil {
			return err
		}
		role, err := s.repo.roleForUpdateTx(tx, roleID)
		if err != nil {
			return err
		}
		if role.IsSystem {
			return apperror.New(apperror.CodeForbidden, "system roles cannot be deleted")
		}
		codes, err := s.repo.rolePermissionCodesTx(tx, roleID)
		if err != nil {
			return err
		}
		if err := authorizeRoleControl(actor, role, codes); err != nil {
			return err
		}
		return tx.Delete(&Role{}, "id = ?", roleID).Error
	})
	if err != nil {
		return roleMutationError("failed to delete role", err)
	}
	return nil
}

// AssignRole grants a role only when the actor may delegate that role's rank
// and complete permission set. Peer/higher-ranked users cannot be modified by
// non-Owners even when the specific role being added is lower-ranked.
func (s *Service) AssignRole(ctx context.Context, tenantID, userID, roleID, assignedBy uuid.UUID) error {
	err := s.repo.withTenantTx(ctx, func(tx *gorm.DB) error {
		actor, err := s.repo.actorAccessTx(tx, assignedBy)
		if err != nil {
			return err
		}
		role, err := s.repo.roleForUpdateTx(tx, roleID)
		if err != nil {
			return err
		}
		codes, err := s.repo.rolePermissionCodesTx(tx, roleID)
		if err != nil {
			return err
		}
		if err := authorizeRoleDelegation(actor, role, codes); err != nil {
			return err
		}
		exists, err := s.repo.userExistsTx(tx, userID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrUserNotFound
		}
		if rank, hasRole, err := s.repo.highestRoleRankTx(tx, userID); err != nil {
			return err
		} else if hasRole && !actor.IsOwner && rank <= actor.Rank {
			return apperror.New(apperror.CodeForbidden, "cannot modify a user at or above your rank")
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&UserRole{
			UserID: userID, RoleID: roleID, TenantID: tenantID, AssignedBy: &assignedBy,
		}).Error
	})
	if err != nil {
		return roleMutationError("failed to assign role", err)
	}
	s.InvalidateCache(ctx, tenantID, userID)
	return nil
}

// RevokeRole enforces the same delegation boundary as assignment and locks the
// Owner role before counting grants, serializing concurrent last-Owner checks.
func (s *Service) RevokeRole(ctx context.Context, tenantID, actorID, userID, roleID uuid.UUID) error {
	err := s.repo.withTenantTx(ctx, func(tx *gorm.DB) error {
		actor, err := s.repo.actorAccessTx(tx, actorID)
		if err != nil {
			return err
		}
		role, err := s.repo.roleForUpdateTx(tx, roleID)
		if err != nil {
			return err
		}
		codes, err := s.repo.rolePermissionCodesTx(tx, roleID)
		if err != nil {
			return err
		}
		if err := authorizeRoleControl(actor, role, codes); err != nil {
			return err
		}
		exists, err := s.repo.userExistsTx(tx, userID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrUserNotFound
		}
		if rank, hasRole, err := s.repo.highestRoleRankTx(tx, userID); err != nil {
			return err
		} else if hasRole && !actor.IsOwner && rank <= actor.Rank {
			return apperror.New(apperror.CodeForbidden, "cannot modify a user at or above your rank")
		}
		result := tx.Delete(&UserRole{}, "user_id = ? AND role_id = ?", userID, roleID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrRoleAssignmentMissing
		}
		if role.Slug == RoleOwner {
			count, err := s.repo.ownerCountTx(tx)
			if err != nil {
				return err
			}
			if count == 0 {
				return apperror.New(apperror.CodeConflict, "the organization must retain at least one Owner")
			}
		}
		return nil
	})
	if err != nil {
		return roleMutationError("failed to revoke role", err)
	}
	s.InvalidateCache(ctx, tenantID, userID)
	return nil
}

// tenantIDFromCtx is a small helper re-exported for handlers that need to
// pull the tenant ID without importing pkg/txscope directly.
func tenantIDFromCtx(ctx context.Context) (uuid.UUID, bool) {
	return txscope.TenantIDFromContext(ctx)
}

func slugify(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		case r == ' ' || r == '_' || r == '-':
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	return string(out)
}

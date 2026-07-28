package authz

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
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

// CreateRole creates a new custom role with the given permission codes.
func (s *Service) CreateRole(ctx context.Context, tenantID uuid.UUID, name, description string, permissionCodes []string) (*Role, error) {
	role := &Role{
		ID:          uuid.New(),
		TenantID:    tenantID,
		Name:        name,
		Slug:        slugify(name),
		Description: description,
		IsSystem:    false,
		Rank:        100,
	}
	if err := s.repo.CreateRole(ctx, role); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to create role", err)
	}

	if len(permissionCodes) > 0 {
		perms, err := s.repo.FindPermissionsByCodes(ctx, permissionCodes)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to resolve permissions", err)
		}
		ids := make([]uuid.UUID, 0, len(perms))
		for _, p := range perms {
			ids = append(ids, p.ID)
		}
		if err := s.repo.SetRolePermissions(ctx, tenantID, role.ID, ids); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to set role permissions", err)
		}
	}
	return role, nil
}

// UpdateRolePermissions replaces a role's permission set. System roles CAN
// have their permissions edited (that's the "dynamic" requirement) but
// their slug/IsSystem flag can never change, which is enforced by only
// exposing this narrow update, never a generic "update role" that could
// rename a system role's slug out from under code that matches on it
// (e.g. RoleOwner-gated org-deletion checks).
func (s *Service) UpdateRolePermissions(ctx context.Context, tenantID, roleID uuid.UUID, permissionCodes []string) error {
	perms, err := s.repo.FindPermissionsByCodes(ctx, permissionCodes)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to resolve permissions", err)
	}
	ids := make([]uuid.UUID, 0, len(perms))
	for _, p := range perms {
		ids = append(ids, p.ID)
	}
	if err := s.repo.SetRolePermissions(ctx, tenantID, roleID, ids); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to update role permissions", err)
	}
	return nil
}

// DeleteRole removes a custom role. Returns an error if the role is a
// system role (Owner/Admin/Manager/Member/Guest), which must never be
// deletable since core authorization flows and the registration flow
// assume they always exist.
func (s *Service) DeleteRole(ctx context.Context, roleID uuid.UUID) error {
	roles, err := s.repo.ListRoles(ctx)
	if err != nil {
		return err
	}
	for _, r := range roles {
		if r.ID == roleID && r.IsSystem {
			return apperror.New(apperror.CodeForbidden, "system roles cannot be deleted")
		}
	}
	return s.repo.DeleteRole(ctx, roleID)
}

// AssignRole grants a role to a user and invalidates their permission
// cache so the change takes effect on their very next request rather than
// waiting for cache TTL expiry.
func (s *Service) AssignRole(ctx context.Context, tenantID, userID, roleID uuid.UUID, assignedBy uuid.UUID) error {
	err := s.repo.AssignRole(ctx, &UserRole{
		UserID: userID, RoleID: roleID, TenantID: tenantID, AssignedBy: &assignedBy,
	})
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to assign role", err)
	}
	s.InvalidateCache(ctx, tenantID, userID)
	return nil
}

// RevokeRole removes a role grant and invalidates the user's permission cache.
func (s *Service) RevokeRole(ctx context.Context, tenantID, userID, roleID uuid.UUID) error {
	if err := s.repo.RevokeRole(ctx, userID, roleID); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to revoke role", err)
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

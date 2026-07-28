package invitations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
)

// ErrNotFound indicates no invitation matched.
var ErrNotFound = errors.New("invitations: invitation not found")

// Repository provides data access for invitations. Most methods are tenant-scoped;
// the token-keyed lookups deliberately are not, and are documented individually.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a pending invitation.
func (r *Repository) Create(ctx context.Context, invite *Invitation) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Create(invite).Error
	})
}

// List returns a page of the tenant's invitations joined with role names,
// optionally filtered by status.
func (r *Repository) List(ctx context.Context, status string, limit, offset int) ([]Detail, int64, error) {
	var (
		details []Detail
		total   int64
	)

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		count := tx.Model(&Invitation{})
		if status != "" {
			count = count.Where("status = ?", status)
		}
		if err := count.Count(&total).Error; err != nil {
			return err
		}

		query := tx.Table("invitations i").
			Select("i.*, r.name AS role_name, r.slug AS role_slug").
			Joins("JOIN roles r ON r.id = i.role_id")
		if status != "" {
			query = query.Where("i.status = ?", status)
		}
		return query.Order("i.created_at DESC").Limit(limit).Offset(offset).Find(&details).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("invitations: list: %w", err)
	}
	return details, total, nil
}

// FindByID returns one invitation within the caller's tenant.
func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*Invitation, error) {
	var invite Invitation
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("id = ?", id).First(&invite).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("invitations: find by id: %w", err)
	}
	return &invite, nil
}

// UpdateStatus transitions an invitation, guarding the transition with a WHERE on
// the expected current status so a concurrent change cannot be overwritten.
func (r *Repository) UpdateStatus(ctx context.Context, id uuid.UUID, from, to string) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Model(&Invitation{}).
			Where("id = ? AND status = ?", id, from).
			Update("status", to)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// FindByTokenHash resolves an invitation from its token hash with no tenant
// context, via the find_invitation_by_hash SECURITY DEFINER function
// (migrations/000012).
//
// This is unavoidable rather than a shortcut: the invitee may have no account, so
// the tenant can only be learned from the token itself. The function constrains
// the elevated privilege to a single indexed equality match, so possessing a valid
// token is the only way to retrieve anything.
func (r *Repository) FindByTokenHash(ctx context.Context, tokenHash string) (*Invitation, error) {
	var invites []Invitation
	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw("SELECT * FROM find_invitation_by_hash(?)", tokenHash).Scan(&invites).Error
	})
	if err != nil {
		return nil, fmt.Errorf("invitations: find by token hash: %w", err)
	}
	if len(invites) == 0 {
		return nil, ErrNotFound
	}
	return &invites[0], nil
}

// PreviewContext holds the organization/role names needed to render an invite
// preview, fetched in the invitation's own tenant scope once it is known.
type PreviewContext struct {
	OrganizationName string
	OrganizationSlug string
	RoleName         string
}

// LoadPreviewContext fetches the tenant and role display names for an invitation.
func (r *Repository) LoadPreviewContext(ctx context.Context, invite *Invitation) (*PreviewContext, error) {
	out := &PreviewContext{}

	err := txscope.WithTenantTxID(ctx, r.db, invite.TenantID, func(tx *gorm.DB) error {
		var role struct{ Name string }
		if err := tx.Table("roles").Select("name").Where("id = ?", invite.RoleID).Scan(&role).Error; err != nil {
			return err
		}
		out.RoleName = role.Name

		// tenants carries no RLS policy, so it is filtered by explicit primary key.
		var tenant struct {
			Name string
			Slug string
		}
		if err := tx.Table("tenants").Select("name, slug").Where("id = ?", invite.TenantID).Scan(&tenant).Error; err != nil {
			return err
		}
		out.OrganizationName = tenant.Name
		out.OrganizationSlug = tenant.Slug
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("invitations: load preview context: %w", err)
	}
	return out, nil
}

// UserExistsInTenant reports whether the invited email already has an account in
// the invitation's tenant, which decides whether accepting needs a password.
func (r *Repository) UserExistsInTenant(ctx context.Context, tenantID uuid.UUID, email string) (bool, error) {
	var count int64
	err := txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		return tx.Table("users").
			Where("email = ? AND deleted_at IS NULL", email).
			Count(&count).Error
	})
	if err != nil {
		return false, fmt.Errorf("invitations: check existing user: %w", err)
	}
	return count > 0, nil
}

// Accept redeems an invitation via the accept_invitation SECURITY DEFINER
// function (migrations/000012), which atomically claims the invite, creates or
// reuses the user, and grants the invited role.
//
// The multi-table write lives in SQL rather than Go because it spans RLS-protected
// tables in a tenant this connection has no scope for until the token is
// validated, and because the function's single UPDATE ... WHERE status = 'pending'
// is what makes two simultaneous redemptions of one token safe.
func (r *Repository) Accept(ctx context.Context, tokenHash, fullName string, passwordHash *string) (uuid.UUID, error) {
	var userID uuid.UUID

	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		// Row().Scan is used rather than GORM's Scan because the function returns a
		// single scalar; GORM's Scan expects a struct/map/slice destination and
		// would leave a bare uuid.UUID untouched.
		return tx.Raw("SELECT accept_invitation(?, ?, ?, ?)",
			tokenHash, uuid.New(), fullName, passwordHash).Row().Scan(&userID)
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("invitations: accept: %w", err)
	}
	return userID, nil
}

// ExpirePending marks pending invitations past their expiry as expired. Intended
// for a scheduled sweep; correctness does not depend on it, since every read path
// also checks expires_at.
func (r *Repository) ExpirePending(ctx context.Context) (int64, error) {
	var affected int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Model(&Invitation{}).
			Where("status = ? AND expires_at <= ?", StatusPending, time.Now()).
			Update("status", StatusExpired)
		affected = result.RowsAffected
		return result.Error
	})
	if err != nil {
		return 0, fmt.Errorf("invitations: expire pending: %w", err)
	}
	return affected, nil
}

// RoleExists verifies a role belongs to the caller's tenant before it is attached
// to an invitation, preventing an invite from referencing another tenant's role.
func (r *Repository) RoleExists(ctx context.Context, roleID uuid.UUID) (bool, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("roles").Where("id = ?", roleID).Count(&count).Error
	})
	if err != nil {
		return false, fmt.Errorf("invitations: verify role: %w", err)
	}
	return count > 0, nil
}

// FindRoleBySlug resolves a role by slug in the caller's tenant, letting clients
// invite by role name instead of having to look up an ID first.
func (r *Repository) FindRoleBySlug(ctx context.Context, slug string) (uuid.UUID, error) {
	// Scanned into a struct rather than a bare uuid.UUID: GORM's Scan requires a
	// struct, map, or slice destination and silently leaves a scalar untouched.
	var row struct {
		ID uuid.UUID
	}
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("roles").Select("id").Where("slug = ?", slug).Limit(1).Scan(&row).Error
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("invitations: find role by slug: %w", err)
	}
	if row.ID == uuid.Nil {
		return uuid.Nil, ErrNotFound
	}
	return row.ID, nil
}

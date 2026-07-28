package teams

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrNotFound is returned when a team does not exist within the caller's tenant.
// A team belonging to another tenant is indistinguishable from a nonexistent one
// here, because RLS filters it out before this code sees it -- which is the
// intended behavior: a cross-tenant probe must not be able to tell the
// difference.
var ErrNotFound = errors.New("teams: team not found")

// Repository provides tenant-scoped data access for teams and their members.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a new team.
func (r *Repository) Create(ctx context.Context, team *Team) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Create(team).Error
	})
}

// List returns a page of live teams with their member counts, newest first.
//
// The member count is computed by a correlated subquery rather than a second
// round of per-team queries, keeping this endpoint at one database round trip
// regardless of page size.
func (r *Repository) List(ctx context.Context, search string, limit, offset int) ([]TeamDetail, int64, error) {
	var (
		details []TeamDetail
		total   int64
	)

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		base := tx.Model(&Team{}).Where("deleted_at IS NULL")
		if search != "" {
			// CITEXT columns compare case-insensitively already; ILIKE keeps the
			// behavior explicit and also covers the plain-text name column.
			pattern := "%" + search + "%"
			base = base.Where("name ILIKE ? OR slug ILIKE ?", pattern, pattern)
		}

		if err := base.Count(&total).Error; err != nil {
			return err
		}

		return base.
			Select("teams.*, (SELECT COUNT(*) FROM team_members tm WHERE tm.team_id = teams.id) AS member_count").
			Order("created_at DESC").
			Limit(limit).
			Offset(offset).
			Find(&details).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("teams: list: %w", err)
	}
	return details, total, nil
}

// FindByID returns a single live team.
func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*Team, error) {
	var team Team
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("id = ? AND deleted_at IS NULL", id).First(&team).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("teams: find by id: %w", err)
	}
	return &team, nil
}

// Update persists name/slug/description changes.
func (r *Repository) Update(ctx context.Context, team *Team) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Model(&Team{}).
			Where("id = ? AND deleted_at IS NULL", team.ID).
			Updates(map[string]any{
				"name":        team.Name,
				"slug":        team.Slug,
				"description": team.Description,
				"updated_at":  time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SoftDelete marks a team deleted, releasing its slug for reuse.
func (r *Repository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Model(&Team{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Update("deleted_at", time.Now())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AddMember adds a user to a team. A repeat add is ignored rather than erroring,
// making the operation idempotent -- retrying a request whose response was lost
// must not fail with a primary-key violation.
func (r *Repository) AddMember(ctx context.Context, member *Member) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "team_id"}, {Name: "user_id"}},
				DoNothing: true,
			}).
			Create(member).Error
	})
}

// RemoveMember removes a user from a team.
func (r *Repository) RemoveMember(ctx context.Context, teamID, userID uuid.UUID) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Where("team_id = ? AND user_id = ?", teamID, userID).Delete(&Member{}).Error
	})
}

// ListMembers returns a team's members joined with their user details.
func (r *Repository) ListMembers(ctx context.Context, teamID uuid.UUID, limit, offset int) ([]MemberDetail, int64, error) {
	var (
		members []MemberDetail
		total   int64
	)

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Model(&Member{}).Where("team_id = ?", teamID).Count(&total).Error; err != nil {
			return err
		}
		return tx.Table("team_members tm").
			Select("tm.user_id, u.email, u.full_name, u.status, tm.joined_at").
			Joins("JOIN users u ON u.id = tm.user_id").
			Where("tm.team_id = ? AND u.deleted_at IS NULL", teamID).
			Order("tm.joined_at ASC").
			Limit(limit).
			Offset(offset).
			Find(&members).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("teams: list members: %w", err)
	}
	return members, total, nil
}

// UserBelongsToTenant verifies a user exists in the caller's tenant before being
// added to one of its teams.
//
// This check is what stops an attacker from grafting a user ID harvested from
// another tenant onto their own team: without it the insert would succeed
// (team_members only constrains tenant_id, which the caller supplies), quietly
// creating a cross-tenant reference.
func (r *Repository) UserBelongsToTenant(ctx context.Context, userID uuid.UUID) (bool, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("users").
			Where("id = ? AND deleted_at IS NULL", userID).
			Count(&count).Error
	})
	if err != nil {
		return false, fmt.Errorf("teams: verify user tenant: %w", err)
	}
	return count > 0, nil
}

// CountLive returns the number of non-deleted teams in the tenant, used for plan
// quota checks.
func (r *Repository) CountLive(ctx context.Context) (int64, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Model(&Team{}).Where("deleted_at IS NULL").Count(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("teams: count live: %w", err)
	}
	return count, nil
}

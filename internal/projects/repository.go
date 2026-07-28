package projects

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

// ErrNotFound is returned when a project is absent from the caller's tenant.
var ErrNotFound = errors.New("projects: project not found")

// Repository provides tenant-scoped data access for projects and members.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// ListFilter narrows a project listing. Zero values are ignored.
type ListFilter struct {
	Search string
	Status string
	TeamID *uuid.UUID
}

// Create inserts a new project.
func (r *Repository) Create(ctx context.Context, project *Project) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Create(project).Error
	})
}

// List returns a filtered page of live projects with member counts and team names.
func (r *Repository) List(ctx context.Context, filter ListFilter, limit, offset int) ([]ProjectDetail, int64, error) {
	var (
		details []ProjectDetail
		total   int64
	)

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		build := func() *gorm.DB {
			q := tx.Model(&Project{}).Where("projects.deleted_at IS NULL")
			if filter.Search != "" {
				pattern := "%" + filter.Search + "%"
				q = q.Where("projects.name ILIKE ? OR projects.slug ILIKE ?", pattern, pattern)
			}
			if filter.Status != "" {
				q = q.Where("projects.status = ?", filter.Status)
			}
			if filter.TeamID != nil {
				q = q.Where("projects.team_id = ?", *filter.TeamID)
			}
			return q
		}

		if err := build().Count(&total).Error; err != nil {
			return err
		}

		// LEFT JOIN (not INNER) because team_id is nullable: a project with no
		// team must still appear in the list.
		return build().
			Select(`projects.*, t.name AS team_name,
				(SELECT COUNT(*) FROM project_members pm WHERE pm.project_id = projects.id) AS member_count`).
			Joins("LEFT JOIN teams t ON t.id = projects.team_id AND t.deleted_at IS NULL").
			Order("projects.created_at DESC").
			Limit(limit).
			Offset(offset).
			Find(&details).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("projects: list: %w", err)
	}
	return details, total, nil
}

// FindByID returns a single live project.
func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*Project, error) {
	var project Project
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("id = ? AND deleted_at IS NULL", id).First(&project).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("projects: find by id: %w", err)
	}
	return &project, nil
}

// Update persists mutable project fields.
func (r *Repository) Update(ctx context.Context, project *Project) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Model(&Project{}).
			Where("id = ? AND deleted_at IS NULL", project.ID).
			Updates(map[string]any{
				"name":        project.Name,
				"slug":        project.Slug,
				"description": project.Description,
				"status":      project.Status,
				"team_id":     project.TeamID,
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

// SoftDelete marks a project deleted, releasing its slug for reuse.
func (r *Repository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Model(&Project{}).
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

// AddMember adds a user to a project idempotently.
func (r *Repository) AddMember(ctx context.Context, member *Member) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}, {Name: "user_id"}},
			DoNothing: true,
		}).Create(member).Error
	})
}

// RemoveMember removes a user from a project.
func (r *Repository) RemoveMember(ctx context.Context, projectID, userID uuid.UUID) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Where("project_id = ? AND user_id = ?", projectID, userID).Delete(&Member{}).Error
	})
}

// ListMembers returns a project's roster joined with user details.
func (r *Repository) ListMembers(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]MemberDetail, int64, error) {
	var (
		members []MemberDetail
		total   int64
	)

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Model(&Member{}).Where("project_id = ?", projectID).Count(&total).Error; err != nil {
			return err
		}
		return tx.Table("project_members pm").
			Select("pm.user_id, u.email, u.full_name, u.status, pm.joined_at").
			Joins("JOIN users u ON u.id = pm.user_id").
			Where("pm.project_id = ? AND u.deleted_at IS NULL", projectID).
			Order("pm.joined_at ASC").
			Limit(limit).
			Offset(offset).
			Find(&members).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("projects: list members: %w", err)
	}
	return members, total, nil
}

// UserBelongsToTenant guards against attaching a user from another tenant, the
// same cross-tenant reference risk described in the teams repository.
func (r *Repository) UserBelongsToTenant(ctx context.Context, userID uuid.UUID) (bool, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("users").Where("id = ? AND deleted_at IS NULL", userID).Count(&count).Error
	})
	if err != nil {
		return false, fmt.Errorf("projects: verify user tenant: %w", err)
	}
	return count > 0, nil
}

// TeamExists verifies a team belongs to the caller's tenant before a project is
// attached to it.
func (r *Repository) TeamExists(ctx context.Context, teamID uuid.UUID) (bool, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("teams").Where("id = ? AND deleted_at IS NULL", teamID).Count(&count).Error
	})
	if err != nil {
		return false, fmt.Errorf("projects: verify team: %w", err)
	}
	return count > 0, nil
}

// CountLive returns the number of non-deleted projects in the tenant, used to
// enforce the subscription plan's max_projects quota.
func (r *Repository) CountLive(ctx context.Context) (int64, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Model(&Project{}).Where("deleted_at IS NULL").Count(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("projects: count live: %w", err)
	}
	return count, nil
}

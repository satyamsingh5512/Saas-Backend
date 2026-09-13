package files

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
)

var ErrNotFound = errors.New("files: file not found")

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, file *File) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Create(file).Error
	})
}

func (r *Repository) List(ctx context.Context, projectID *uuid.UUID, limit, offset int) ([]File, int64, error) {
	var (
		files []File
		total int64
	)
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		query := tx.Model(&File{})
		if projectID != nil {
			query = query.Where("project_id = ?", *projectID)
		}
		if err := query.Count(&total).Error; err != nil {
			return err
		}
		return query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&files).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("files: list: %w", err)
	}
	return files, total, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*File, error) {
	var file File
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).First(&file).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		} else {
			return err
		}
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("files: find by id: %w", err)
	}
	return &file, nil
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Where("id = ?", id).Delete(&File{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("files: delete: %w", err)
	}
	return nil
}

func (r *Repository) ProjectExists(ctx context.Context, projectID uuid.UUID) (bool, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("projects").Where("id = ? AND deleted_at IS NULL", projectID).Count(&count).Error
	})
	if err != nil {
		return false, fmt.Errorf("files: verify project: %w", err)
	}
	return count > 0, nil
}

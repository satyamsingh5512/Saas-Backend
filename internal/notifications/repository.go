package notifications

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
)

// Repository provides tenant-scoped data access for notifications.
//
// Every method takes an explicit userID and filters on it. RLS confines rows to
// the tenant, but a tenant contains many users, so tenant scope alone would let one
// colleague read another's notifications -- the user filter is the actual
// authorization boundary here and is applied in every query without exception.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a notification for a user.
func (r *Repository) Create(ctx context.Context, n *Notification) error {
	return txscope.WithTenantTxID(ctx, r.db, n.TenantID, func(tx *gorm.DB) error {
		return tx.Create(n).Error
	})
}

// CreateBatch inserts several notifications in one statement, used when an event
// fans out to many recipients.
func (r *Repository) CreateBatch(ctx context.Context, tenantID uuid.UUID, rows []Notification) error {
	if len(rows) == 0 {
		return nil
	}
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		return tx.Create(&rows).Error
	})
}

// List returns a page of the given user's notifications, newest first.
func (r *Repository) List(ctx context.Context, userID uuid.UUID, unreadOnly bool, limit, offset int) ([]Notification, int64, error) {
	var (
		items []Notification
		total int64
	)

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		build := func() *gorm.DB {
			q := tx.Model(&Notification{}).Where("user_id = ?", userID)
			if unreadOnly {
				q = q.Where("read_at IS NULL")
			}
			return q
		}

		if err := build().Count(&total).Error; err != nil {
			return err
		}
		return build().Order("created_at DESC").Limit(limit).Offset(offset).Find(&items).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("notifications: list: %w", err)
	}
	return items, total, nil
}

// CountUnread returns the user's unread notification count, matching the partial
// index on (user_id, created_at DESC) WHERE read_at IS NULL.
func (r *Repository) CountUnread(ctx context.Context, userID uuid.UUID) (int64, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Model(&Notification{}).
			Where("user_id = ? AND read_at IS NULL", userID).
			Count(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("notifications: count unread: %w", err)
	}
	return count, nil
}

// MarkRead marks one notification read. The user_id predicate makes this both the
// lookup and the authorization check, so a caller cannot acknowledge someone
// else's notification by guessing its ID.
func (r *Repository) MarkRead(ctx context.Context, userID, notificationID uuid.UUID) (bool, error) {
	var affected int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Model(&Notification{}).
			Where("id = ? AND user_id = ? AND read_at IS NULL", notificationID, userID).
			Update("read_at", time.Now())
		affected = result.RowsAffected
		return result.Error
	})
	if err != nil {
		return false, fmt.Errorf("notifications: mark read: %w", err)
	}
	return affected > 0, nil
}

// MarkAllRead marks every unread notification for the user as read.
func (r *Repository) MarkAllRead(ctx context.Context, userID uuid.UUID) (int64, error) {
	var affected int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Model(&Notification{}).
			Where("user_id = ? AND read_at IS NULL", userID).
			Update("read_at", time.Now())
		affected = result.RowsAffected
		return result.Error
	})
	if err != nil {
		return 0, fmt.Errorf("notifications: mark all read: %w", err)
	}
	return affected, nil
}

// Delete removes one of the user's notifications.
func (r *Repository) Delete(ctx context.Context, userID, notificationID uuid.UUID) (bool, error) {
	var affected int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND user_id = ?", notificationID, userID).Delete(&Notification{})
		affected = result.RowsAffected
		return result.Error
	})
	if err != nil {
		return false, fmt.Errorf("notifications: delete: %w", err)
	}
	return affected > 0, nil
}

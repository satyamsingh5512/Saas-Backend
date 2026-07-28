package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
)

// Repository provides tenant-scoped access to the audit log and activity feed.
// Every method routes through txscope so Postgres RLS is active, including the
// read paths -- an audit log that could be read cross-tenant would be a worse
// leak than the records it exists to protect.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// AuditFilter narrows an audit log query. Zero-valued fields are ignored.
type AuditFilter struct {
	ActorID *uuid.UUID
	Action  string
	From    *time.Time
	To      *time.Time
}

// CreateAuditLog appends one audit entry.
func (r *Repository) CreateAuditLog(ctx context.Context, entry *AuditLog) error {
	return txscope.WithTenantTxID(ctx, r.db, entry.TenantID, func(tx *gorm.DB) error {
		if err := tx.Create(entry).Error; err != nil {
			return fmt.Errorf("audit: create audit log: %w", err)
		}
		return nil
	})
}

// ListAuditLogs returns a page of audit entries, newest first, matching filter.
func (r *Repository) ListAuditLogs(ctx context.Context, filter AuditFilter, limit, offset int) ([]AuditLog, int64, error) {
	var (
		logs  []AuditLog
		total int64
	)

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		query := tx.Model(&AuditLog{})
		if filter.ActorID != nil {
			query = query.Where("actor_id = ?", *filter.ActorID)
		}
		if filter.Action != "" {
			query = query.Where("action = ?", filter.Action)
		}
		if filter.From != nil {
			query = query.Where("created_at >= ?", *filter.From)
		}
		if filter.To != nil {
			query = query.Where("created_at <= ?", *filter.To)
		}

		// Count before applying limit/offset so pagination metadata reflects the
		// whole filtered set, not just the returned page.
		if err := query.Count(&total).Error; err != nil {
			return err
		}
		return query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&logs).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("audit: list audit logs: %w", err)
	}
	return logs, total, nil
}

// CreateActivityEvent appends one activity feed entry.
func (r *Repository) CreateActivityEvent(ctx context.Context, event *ActivityEvent) error {
	return txscope.WithTenantTxID(ctx, r.db, event.TenantID, func(tx *gorm.DB) error {
		if err := tx.Create(event).Error; err != nil {
			return fmt.Errorf("audit: create activity event: %w", err)
		}
		return nil
	})
}

// ListActivity returns a page of activity events, newest first. When targetType
// and targetID are supplied the feed is scoped to that single resource, matching
// the (target_type, target_id, created_at DESC) index; otherwise it returns the
// whole tenant's recent activity.
func (r *Repository) ListActivity(ctx context.Context, targetType string, targetID *uuid.UUID, limit, offset int) ([]ActivityEvent, int64, error) {
	var (
		events []ActivityEvent
		total  int64
	)

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		query := tx.Model(&ActivityEvent{})
		if targetType != "" {
			query = query.Where("target_type = ?", targetType)
		}
		if targetID != nil {
			query = query.Where("target_id = ?", *targetID)
		}

		if err := query.Count(&total).Error; err != nil {
			return err
		}
		return query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&events).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("audit: list activity: %w", err)
	}
	return events, total, nil
}

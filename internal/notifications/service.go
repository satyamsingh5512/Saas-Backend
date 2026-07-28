package notifications

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Service implements notification reads, acknowledgement, and internal delivery.
type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// NotifyInput describes a notification to deliver to one user.
type NotifyInput struct {
	TenantID uuid.UUID
	UserID   uuid.UUID
	Type     string
	Title    string
	Body     string
	Metadata map[string]any
}

// Notify delivers a notification. Called by domain services and event consumers,
// never from a client-facing route.
func (s *Service) Notify(ctx context.Context, in NotifyInput) error {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return apperror.New(apperror.CodeValidation, "notification title is required")
	}
	if in.Metadata == nil {
		in.Metadata = map[string]any{}
	}

	n := &Notification{
		ID:        uuid.New(),
		TenantID:  in.TenantID,
		UserID:    in.UserID,
		Type:      in.Type,
		Title:     title,
		Body:      strings.TrimSpace(in.Body),
		Metadata:  in.Metadata,
		CreatedAt: time.Now(),
	}
	if err := s.repo.Create(ctx, n); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to create notification", err)
	}
	return nil
}

// NotifyMany delivers the same notification to several users in one insert.
func (s *Service) NotifyMany(ctx context.Context, tenantID uuid.UUID, userIDs []uuid.UUID, in NotifyInput) error {
	if len(userIDs) == 0 {
		return nil
	}
	if in.Metadata == nil {
		in.Metadata = map[string]any{}
	}

	now := time.Now()
	rows := make([]Notification, 0, len(userIDs))
	for _, userID := range userIDs {
		rows = append(rows, Notification{
			ID:        uuid.New(),
			TenantID:  tenantID,
			UserID:    userID,
			Type:      in.Type,
			Title:     strings.TrimSpace(in.Title),
			Body:      strings.TrimSpace(in.Body),
			Metadata:  in.Metadata,
			CreatedAt: now,
		})
	}

	if err := s.repo.CreateBatch(ctx, tenantID, rows); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to create notifications", err)
	}
	return nil
}

// List returns the caller's notifications.
func (s *Service) List(ctx context.Context, userID uuid.UUID, unreadOnly bool, page, pageSize int) ([]Notification, int64, error) {
	items, total, err := s.repo.List(ctx, userID, unreadOnly, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list notifications", err)
	}
	return items, total, nil
}

// UnreadCount returns the caller's unread badge count.
func (s *Service) UnreadCount(ctx context.Context, userID uuid.UUID) (int64, error) {
	count, err := s.repo.CountUnread(ctx, userID)
	if err != nil {
		return 0, apperror.Wrap(apperror.CodeInternal, "failed to count notifications", err)
	}
	return count, nil
}

// MarkRead acknowledges one notification.
//
// An already-read notification is reported as success rather than an error: the
// operation is idempotent, and a client retrying a lost response should not see a
// failure for a state it already achieved.
func (s *Service) MarkRead(ctx context.Context, userID, notificationID uuid.UUID) error {
	updated, err := s.repo.MarkRead(ctx, userID, notificationID)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to mark notification read", err)
	}
	if !updated {
		// Either it does not exist, belongs to another user, or was already read.
		// These are not distinguished, so the endpoint cannot be used to probe for
		// other users' notification IDs.
		return nil
	}
	return nil
}

// MarkAllRead acknowledges every unread notification and returns how many changed.
func (s *Service) MarkAllRead(ctx context.Context, userID uuid.UUID) (int64, error) {
	count, err := s.repo.MarkAllRead(ctx, userID)
	if err != nil {
		return 0, apperror.Wrap(apperror.CodeInternal, "failed to mark notifications read", err)
	}
	return count, nil
}

// Delete removes one of the caller's notifications.
func (s *Service) Delete(ctx context.Context, userID, notificationID uuid.UUID) error {
	deleted, err := s.repo.Delete(ctx, userID, notificationID)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to delete notification", err)
	}
	if !deleted {
		return apperror.New(apperror.CodeNotFound, "notification not found")
	}
	return nil
}

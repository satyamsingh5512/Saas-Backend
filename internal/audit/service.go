package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Service implements audit and activity recording plus their read paths.
type Service struct {
	repo   *Repository
	logger *slog.Logger
}

func NewService(repo *Repository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{repo: repo, logger: logger}
}

// Entry describes one audit record to append. Constructed by callers rather than
// assembled from a gin.Context inside this package, so the service layer stays
// transport-agnostic.
type Entry struct {
	TenantID   uuid.UUID
	ActorID    *uuid.UUID
	Action     string
	TargetType string
	TargetID   *uuid.UUID
	IPAddress  string
	UserAgent  string
	Metadata   map[string]any
}

// Record appends an audit entry.
//
// It returns no error by design. Audit writes are secondary to the business
// operation that triggered them: failing a completed role change because its
// audit row could not be inserted would leave the caller believing the change
// did not happen, when it did. Failures are logged at ERROR with the full entry
// so they are alertable and reconstructable, rather than silently dropped.
func (s *Service) Record(ctx context.Context, entry Entry) {
	row := &AuditLog{
		ID:        uuid.New(),
		TenantID:  entry.TenantID,
		ActorID:   entry.ActorID,
		Action:    entry.Action,
		TargetID:  entry.TargetID,
		UserAgent: entry.UserAgent,
		Metadata:  entry.Metadata,
		CreatedAt: time.Now(),
	}
	if row.Metadata == nil {
		row.Metadata = map[string]any{}
	}
	if entry.TargetType != "" {
		row.TargetType = &entry.TargetType
	}
	if entry.IPAddress != "" {
		row.IPAddress = &entry.IPAddress
	}

	if err := s.repo.CreateAuditLog(ctx, row); err != nil {
		s.logger.ErrorContext(ctx, "failed to write audit log",
			slog.Any("error", err),
			slog.String("action", entry.Action),
			slog.String("tenant_id", entry.TenantID.String()),
			slog.Any("actor_id", entry.ActorID),
			slog.Any("target_id", entry.TargetID),
		)
	}
}

// ActivityEntry describes one activity feed record to append.
type ActivityEntry struct {
	TenantID   uuid.UUID
	ActorID    *uuid.UUID
	TargetType string
	TargetID   uuid.UUID
	Verb       string
	Metadata   map[string]any
}

// RecordActivity appends an activity feed entry, with the same
// best-effort-but-logged failure semantics as Record.
func (s *Service) RecordActivity(ctx context.Context, entry ActivityEntry) {
	row := &ActivityEvent{
		ID:         uuid.New(),
		TenantID:   entry.TenantID,
		ActorID:    entry.ActorID,
		TargetType: entry.TargetType,
		TargetID:   entry.TargetID,
		Verb:       entry.Verb,
		Metadata:   entry.Metadata,
		CreatedAt:  time.Now(),
	}
	if row.Metadata == nil {
		row.Metadata = map[string]any{}
	}

	if err := s.repo.CreateActivityEvent(ctx, row); err != nil {
		s.logger.ErrorContext(ctx, "failed to write activity event",
			slog.Any("error", err),
			slog.String("verb", entry.Verb),
			slog.String("target_type", entry.TargetType),
			slog.String("tenant_id", entry.TenantID.String()),
		)
	}
}

// ListAuditLogs returns a filtered, paginated page of audit entries. Gated by
// the audit:view permission at the route layer.
func (s *Service) ListAuditLogs(ctx context.Context, filter AuditFilter, page, pageSize int) ([]AuditLog, int64, error) {
	logs, total, err := s.repo.ListAuditLogs(ctx, filter, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list audit logs", err)
	}
	return logs, total, nil
}

// ListActivity returns a paginated activity feed, optionally scoped to a single
// target resource.
func (s *Service) ListActivity(ctx context.Context, targetType string, targetID *uuid.UUID, page, pageSize int) ([]ActivityEvent, int64, error) {
	events, total, err := s.repo.ListActivity(ctx, targetType, targetID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list activity", err)
	}
	return events, total, nil
}

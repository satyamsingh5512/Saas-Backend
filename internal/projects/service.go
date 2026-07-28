package projects

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/internal/notifications"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
	"github.com/satym-in/tenant-saas-backend/pkg/dberr"
	"github.com/satym-in/tenant-saas-backend/pkg/slug"
)

// Recorder is the slice of internal/audit this module needs.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
	RecordActivity(ctx context.Context, entry audit.ActivityEntry)
}

// QuotaChecker is the slice of internal/billing this module needs to enforce the
// subscription plan's project limit. Declared as a consumer-side interface so
// projects does not import billing, and so a nil value cleanly means "no plan
// enforcement configured".
type QuotaChecker interface {
	CheckProjectQuota(ctx context.Context, tenantID uuid.UUID, currentCount int64) error
}

// Service implements project business logic.
type Service struct {
	repo    *Repository
	audit   Recorder
	quotas  QuotaChecker
	notifer Notifier
}

// Notifier is the slice of internal/notifications used to tell a user they were
// added to a project. Optional: nil disables in-app notifications.
type Notifier interface {
	Notify(ctx context.Context, in notifications.NotifyInput) error
}

func NewService(repo *Repository, recorder Recorder, quotas QuotaChecker, notifier Notifier) *Service {
	return &Service{repo: repo, audit: recorder, quotas: quotas, notifer: notifier}
}

// CreateInput is the validated input for creating a project.
type CreateInput struct {
	Name        string
	Slug        string
	Description string
	TeamID      *uuid.UUID
}

// Create creates a project, enforcing the tenant's plan project quota first.
func (s *Service) Create(ctx context.Context, entry audit.Entry, tenantID, actorID uuid.UUID, in CreateInput) (*Project, error) {
	projectSlug, err := s.resolveSlug(in.Slug, in.Name)
	if err != nil {
		return nil, err
	}

	if in.TeamID != nil {
		exists, err := s.repo.TeamExists(ctx, *in.TeamID)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to verify team", err)
		}
		if !exists {
			return nil, apperror.New(apperror.CodeValidation, "team not found in this organization")
		}
	}

	// Quota is checked before insert rather than after. Two concurrent creates can
	// still both pass this check and exceed the limit by one; that is accepted
	// deliberately, because the alternative (locking the projects table per
	// create) costs far more than the occasional off-by-one overage, which the
	// next create will correct.
	if s.quotas != nil {
		current, err := s.repo.CountLive(ctx)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to count projects", err)
		}
		if err := s.quotas.CheckProjectQuota(ctx, tenantID, current); err != nil {
			return nil, err
		}
	}

	project := &Project{
		ID:          uuid.New(),
		TenantID:    tenantID,
		TeamID:      in.TeamID,
		Name:        strings.TrimSpace(in.Name),
		Slug:        projectSlug,
		Description: strings.TrimSpace(in.Description),
		Status:      StatusActive,
		CreatedBy:   &actorID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.repo.Create(ctx, project); err != nil {
		if dberr.IsUniqueViolation(err) {
			return nil, apperror.New(apperror.CodeConflict, "a project with that slug already exists")
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to create project", err)
	}

	s.recordProjectAction(ctx, entry, audit.ActionProjectCreated, audit.VerbCreated, project)
	return project, nil
}

// List returns a filtered, paginated project list.
func (s *Service) List(ctx context.Context, filter ListFilter, page, pageSize int) ([]ProjectDetail, int64, error) {
	if filter.Status != "" && filter.Status != StatusActive && filter.Status != StatusArchived {
		return nil, 0, apperror.New(apperror.CodeValidation, "status must be active or archived")
	}
	filter.Search = strings.TrimSpace(filter.Search)

	details, total, err := s.repo.List(ctx, filter, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list projects", err)
	}
	return details, total, nil
}

// Get returns a single project.
func (s *Service) Get(ctx context.Context, projectID uuid.UUID) (*Project, error) {
	project, err := s.repo.FindByID(ctx, projectID)
	if err != nil {
		return nil, translateNotFound(err, "failed to load project")
	}
	return project, nil
}

// UpdateInput carries mutable project fields; nil means "leave unchanged".
type UpdateInput struct {
	Name        *string
	Slug        *string
	Description *string
	Status      *string
	TeamID      *uuid.UUID
	// ClearTeam distinguishes "detach from team" from "leave team unchanged",
	// which a nil TeamID alone cannot express.
	ClearTeam bool
}

// Update applies a partial update to a project.
func (s *Service) Update(ctx context.Context, entry audit.Entry, projectID uuid.UUID, in UpdateInput) (*Project, error) {
	project, err := s.repo.FindByID(ctx, projectID)
	if err != nil {
		return nil, translateNotFound(err, "failed to load project")
	}

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, apperror.New(apperror.CodeValidation, "name cannot be empty")
		}
		project.Name = name
	}
	if in.Slug != nil {
		newSlug, err := s.resolveSlug(*in.Slug, project.Name)
		if err != nil {
			return nil, err
		}
		project.Slug = newSlug
	}
	if in.Description != nil {
		project.Description = strings.TrimSpace(*in.Description)
	}
	if in.Status != nil {
		if *in.Status != StatusActive && *in.Status != StatusArchived {
			return nil, apperror.New(apperror.CodeValidation, "status must be active or archived")
		}
		project.Status = *in.Status
	}
	switch {
	case in.ClearTeam:
		project.TeamID = nil
	case in.TeamID != nil:
		exists, err := s.repo.TeamExists(ctx, *in.TeamID)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to verify team", err)
		}
		if !exists {
			return nil, apperror.New(apperror.CodeValidation, "team not found in this organization")
		}
		project.TeamID = in.TeamID
	}

	if err := s.repo.Update(ctx, project); err != nil {
		if dberr.IsUniqueViolation(err) {
			return nil, apperror.New(apperror.CodeConflict, "a project with that slug already exists")
		}
		return nil, translateNotFound(err, "failed to update project")
	}

	action, verb := audit.ActionProjectUpdated, audit.VerbUpdated
	if project.Status == StatusArchived {
		action, verb = audit.ActionProjectArchived, audit.VerbArchived
	}
	s.recordProjectAction(ctx, entry, action, verb, project)
	return project, nil
}

// Delete soft-deletes a project.
func (s *Service) Delete(ctx context.Context, entry audit.Entry, projectID uuid.UUID) error {
	project, err := s.repo.FindByID(ctx, projectID)
	if err != nil {
		return translateNotFound(err, "failed to load project")
	}
	if err := s.repo.SoftDelete(ctx, projectID); err != nil {
		return translateNotFound(err, "failed to delete project")
	}

	s.recordProjectAction(ctx, entry, audit.ActionProjectDeleted, audit.VerbDeleted, project)
	return nil
}

// AddMember adds a tenant user to a project.
func (s *Service) AddMember(ctx context.Context, entry audit.Entry, tenantID, projectID, userID uuid.UUID) error {
	if _, err := s.repo.FindByID(ctx, projectID); err != nil {
		return translateNotFound(err, "failed to load project")
	}

	belongs, err := s.repo.UserBelongsToTenant(ctx, userID)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to verify user", err)
	}
	if !belongs {
		return apperror.New(apperror.CodeNotFound, "user not found in this organization")
	}

	member := &Member{ProjectID: projectID, UserID: userID, TenantID: tenantID, JoinedAt: time.Now()}
	if err := s.repo.AddMember(ctx, member); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to add project member", err)
	}

	s.recordMembership(ctx, entry, tenantID, projectID, userID, audit.VerbJoined)

	// Best effort: the membership is already committed, so a notification failure
	// must not fail the request.
	if s.notifer != nil {
		if project, err := s.repo.FindByID(ctx, projectID); err == nil {
			_ = s.notifer.Notify(ctx, notifications.NotifyInput{
				TenantID: tenantID,
				UserID:   userID,
				Type:     notifications.TypeProjectAssigned,
				Title:    "You were added to " + project.Name,
				Body:     "You now have access to the " + project.Name + " project.",
				Metadata: map[string]any{"project_id": projectID.String(), "project_slug": project.Slug},
			})
		}
	}
	return nil
}

// RemoveMember removes a user from a project.
func (s *Service) RemoveMember(ctx context.Context, entry audit.Entry, tenantID, projectID, userID uuid.UUID) error {
	if _, err := s.repo.FindByID(ctx, projectID); err != nil {
		return translateNotFound(err, "failed to load project")
	}
	if err := s.repo.RemoveMember(ctx, projectID, userID); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to remove project member", err)
	}

	s.recordMembership(ctx, entry, tenantID, projectID, userID, audit.VerbLeft)
	return nil
}

// ListMembers returns a project's roster.
func (s *Service) ListMembers(ctx context.Context, projectID uuid.UUID, page, pageSize int) ([]MemberDetail, int64, error) {
	if _, err := s.repo.FindByID(ctx, projectID); err != nil {
		return nil, 0, translateNotFound(err, "failed to load project")
	}
	members, total, err := s.repo.ListMembers(ctx, projectID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list project members", err)
	}
	return members, total, nil
}

// --- internal helpers ---

func (s *Service) resolveSlug(explicit, name string) (string, error) {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		if !slug.Valid(trimmed) {
			return "", apperror.New(apperror.CodeValidation,
				"slug must be lowercase alphanumeric with single hyphens")
		}
		return trimmed, nil
	}

	derived := slug.Make(name)
	if derived == "" {
		return "", apperror.New(apperror.CodeValidation,
			"could not derive a slug from the name; supply one explicitly")
	}
	return derived, nil
}

func (s *Service) recordProjectAction(ctx context.Context, entry audit.Entry, action, verb string, project *Project) {
	if s.audit == nil {
		return
	}

	projectID := project.ID
	entry.Action = action
	entry.TargetType = audit.TargetProject
	entry.TargetID = &projectID
	entry.Metadata = map[string]any{"name": project.Name, "slug": project.Slug, "status": project.Status}
	s.audit.Record(ctx, entry)

	s.audit.RecordActivity(ctx, audit.ActivityEntry{
		TenantID:   project.TenantID,
		ActorID:    entry.ActorID,
		TargetType: audit.TargetProject,
		TargetID:   projectID,
		Verb:       verb,
		Metadata:   map[string]any{"name": project.Name},
	})
}

func (s *Service) recordMembership(ctx context.Context, entry audit.Entry, tenantID, projectID, userID uuid.UUID, verb string) {
	if s.audit == nil {
		return
	}

	s.audit.RecordActivity(ctx, audit.ActivityEntry{
		TenantID:   tenantID,
		ActorID:    entry.ActorID,
		TargetType: audit.TargetProject,
		TargetID:   projectID,
		Verb:       verb,
		Metadata:   map[string]any{"user_id": userID.String()},
	})
}

func translateNotFound(err error, message string) error {
	if errors.Is(err, ErrNotFound) {
		return apperror.New(apperror.CodeNotFound, "project not found")
	}
	return apperror.Wrap(apperror.CodeInternal, message, err)
}

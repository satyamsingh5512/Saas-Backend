package teams

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

// Recorder is the slice of internal/audit this module depends on. Declared here
// as a consumer-side interface so tests can substitute a no-op recorder without
// a database.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
	RecordActivity(ctx context.Context, entry audit.ActivityEntry)
}

// Notifier is the slice of internal/notifications used to tell a user they were
// added to a team. Optional: a nil Notifier disables in-app notifications without
// affecting the membership change itself.
type Notifier interface {
	Notify(ctx context.Context, in notifications.NotifyInput) error
}

// Service implements team business logic.
type Service struct {
	repo    *Repository
	audit   Recorder
	notifer Notifier
}

func NewService(repo *Repository, recorder Recorder, notifier Notifier) *Service {
	return &Service{repo: repo, audit: recorder, notifer: notifier}
}

// CreateInput is the validated input for creating a team.
type CreateInput struct {
	Name        string
	Slug        string
	Description string
}

// Create creates a team owned by the calling tenant.
//
// The audit/activity entries carry the caller's IP and user agent supplied by the
// handler, so the returned team is recorded alongside who created it from where.
func (s *Service) Create(ctx context.Context, entry audit.Entry, tenantID, actorID uuid.UUID, in CreateInput) (*Team, error) {
	teamSlug, err := s.resolveSlug(in.Slug, in.Name)
	if err != nil {
		return nil, err
	}

	team := &Team{
		ID:          uuid.New(),
		TenantID:    tenantID,
		Name:        strings.TrimSpace(in.Name),
		Slug:        teamSlug,
		Description: strings.TrimSpace(in.Description),
		CreatedBy:   &actorID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.repo.Create(ctx, team); err != nil {
		if dberr.IsUniqueViolation(err) {
			return nil, apperror.New(apperror.CodeConflict, "a team with that slug already exists")
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to create team", err)
	}

	s.recordTeamAction(ctx, entry, audit.ActionTeamCreated, audit.VerbCreated, team)
	return team, nil
}

// List returns a paginated, optionally name-filtered list of teams.
func (s *Service) List(ctx context.Context, search string, page, pageSize int) ([]TeamDetail, int64, error) {
	details, total, err := s.repo.List(ctx, strings.TrimSpace(search), pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list teams", err)
	}
	return details, total, nil
}

// Get returns a single team.
func (s *Service) Get(ctx context.Context, teamID uuid.UUID) (*Team, error) {
	team, err := s.repo.FindByID(ctx, teamID)
	if err != nil {
		return nil, translateNotFound(err, "failed to load team")
	}
	return team, nil
}

// UpdateInput carries the mutable fields of a team. Nil fields are left unchanged,
// which lets a client PATCH one attribute without having to resend the others and
// risk clobbering a concurrent edit.
type UpdateInput struct {
	Name        *string
	Slug        *string
	Description *string
}

// Update applies a partial update to a team.
func (s *Service) Update(ctx context.Context, entry audit.Entry, teamID uuid.UUID, in UpdateInput) (*Team, error) {
	team, err := s.repo.FindByID(ctx, teamID)
	if err != nil {
		return nil, translateNotFound(err, "failed to load team")
	}

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, apperror.New(apperror.CodeValidation, "name cannot be empty")
		}
		team.Name = name
	}
	if in.Slug != nil {
		newSlug, err := s.resolveSlug(*in.Slug, team.Name)
		if err != nil {
			return nil, err
		}
		team.Slug = newSlug
	}
	if in.Description != nil {
		team.Description = strings.TrimSpace(*in.Description)
	}

	if err := s.repo.Update(ctx, team); err != nil {
		if dberr.IsUniqueViolation(err) {
			return nil, apperror.New(apperror.CodeConflict, "a team with that slug already exists")
		}
		return nil, translateNotFound(err, "failed to update team")
	}

	s.recordTeamAction(ctx, entry, audit.ActionTeamUpdated, audit.VerbUpdated, team)
	return team, nil
}

// Delete soft-deletes a team. Membership rows are removed by the schema's ON
// DELETE CASCADE only on a hard delete, so they are intentionally retained here:
// a soft-deleted team keeps its roster, which makes an accidental deletion
// recoverable by clearing deleted_at.
func (s *Service) Delete(ctx context.Context, entry audit.Entry, teamID uuid.UUID) error {
	team, err := s.repo.FindByID(ctx, teamID)
	if err != nil {
		return translateNotFound(err, "failed to load team")
	}
	if err := s.repo.SoftDelete(ctx, teamID); err != nil {
		return translateNotFound(err, "failed to delete team")
	}

	s.recordTeamAction(ctx, entry, audit.ActionTeamDeleted, audit.VerbDeleted, team)
	return nil
}

// AddMember adds a tenant user to a team.
func (s *Service) AddMember(ctx context.Context, entry audit.Entry, tenantID, teamID, userID uuid.UUID) error {
	if _, err := s.repo.FindByID(ctx, teamID); err != nil {
		return translateNotFound(err, "failed to load team")
	}

	belongs, err := s.repo.UserBelongsToTenant(ctx, userID)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to verify user", err)
	}
	if !belongs {
		// Reported as a plain not-found rather than "wrong tenant", so the
		// response cannot be used to confirm that a user ID exists elsewhere.
		return apperror.New(apperror.CodeNotFound, "user not found in this organization")
	}

	member := &Member{TeamID: teamID, UserID: userID, TenantID: tenantID, JoinedAt: time.Now()}
	if err := s.repo.AddMember(ctx, member); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to add team member", err)
	}

	if s.audit != nil {
		memberEntry := entry
		memberEntry.Action = audit.ActionTeamMemberAdded
		memberEntry.TargetType = audit.TargetTeam
		memberEntry.TargetID = &teamID
		memberEntry.Metadata = map[string]any{"user_id": userID.String()}
		s.audit.Record(ctx, memberEntry)

		s.audit.RecordActivity(ctx, audit.ActivityEntry{
			TenantID:   tenantID,
			ActorID:    entry.ActorID,
			TargetType: audit.TargetTeam,
			TargetID:   teamID,
			Verb:       audit.VerbJoined,
			Metadata:   map[string]any{"user_id": userID.String()},
		})
	}

	// Notification delivery is best effort: the user is already a team member, so a
	// failure to tell them must not roll that back or fail the request.
	if s.notifer != nil {
		team, err := s.repo.FindByID(ctx, teamID)
		if err == nil {
			_ = s.notifer.Notify(ctx, notifications.NotifyInput{
				TenantID: tenantID,
				UserID:   userID,
				Type:     notifications.TypeTeamInvite,
				Title:    "You were added to " + team.Name,
				Body:     "You now have access to the " + team.Name + " team.",
				Metadata: map[string]any{"team_id": teamID.String(), "team_slug": team.Slug},
			})
		}
	}
	return nil
}

// RemoveMember removes a user from a team.
func (s *Service) RemoveMember(ctx context.Context, entry audit.Entry, tenantID, teamID, userID uuid.UUID) error {
	if _, err := s.repo.FindByID(ctx, teamID); err != nil {
		return translateNotFound(err, "failed to load team")
	}
	if err := s.repo.RemoveMember(ctx, teamID, userID); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to remove team member", err)
	}

	if s.audit != nil {
		memberEntry := entry
		memberEntry.Action = audit.ActionTeamMemberRemoved
		memberEntry.TargetType = audit.TargetTeam
		memberEntry.TargetID = &teamID
		memberEntry.Metadata = map[string]any{"user_id": userID.String()}
		s.audit.Record(ctx, memberEntry)

		s.audit.RecordActivity(ctx, audit.ActivityEntry{
			TenantID:   tenantID,
			ActorID:    entry.ActorID,
			TargetType: audit.TargetTeam,
			TargetID:   teamID,
			Verb:       audit.VerbLeft,
			Metadata:   map[string]any{"user_id": userID.String()},
		})
	}
	return nil
}

// ListMembers returns a team's roster.
func (s *Service) ListMembers(ctx context.Context, teamID uuid.UUID, page, pageSize int) ([]MemberDetail, int64, error) {
	if _, err := s.repo.FindByID(ctx, teamID); err != nil {
		return nil, 0, translateNotFound(err, "failed to load team")
	}
	members, total, err := s.repo.ListMembers(ctx, teamID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list team members", err)
	}
	return members, total, nil
}

// --- internal helpers ---

// resolveSlug validates an explicitly supplied slug, or derives one from the
// team name when none was given.
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

func (s *Service) recordTeamAction(ctx context.Context, entry audit.Entry, action, verb string, team *Team) {
	if s.audit == nil {
		return
	}

	teamID := team.ID
	entry.Action = action
	entry.TargetType = audit.TargetTeam
	entry.TargetID = &teamID
	entry.Metadata = map[string]any{"name": team.Name, "slug": team.Slug}
	s.audit.Record(ctx, entry)

	s.audit.RecordActivity(ctx, audit.ActivityEntry{
		TenantID:   team.TenantID,
		ActorID:    entry.ActorID,
		TargetType: audit.TargetTeam,
		TargetID:   teamID,
		Verb:       verb,
		Metadata:   map[string]any{"name": team.Name},
	})
}

// translateNotFound converts the repository's sentinel into a typed 404, wrapping
// anything else as an internal error.
func translateNotFound(err error, message string) error {
	if errors.Is(err, ErrNotFound) {
		return apperror.New(apperror.CodeNotFound, "team not found")
	}
	return apperror.Wrap(apperror.CodeInternal, message, err)
}

package invitations

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/internal/identity"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
	"github.com/satym-in/tenant-saas-backend/pkg/dberr"
)

// Recorder is the slice of internal/audit this module needs.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// SeatQuotaChecker is the slice of internal/billing this module needs, declared
// consumer-side so invitations does not import billing.
type SeatQuotaChecker interface {
	CheckSeatQuota(ctx context.Context, tenantID uuid.UUID) error
}

// EventPublisher matches the shared eventbus.Publisher shape. Invite delivery is
// an event consumer's job (email), not this module's.
type EventPublisher interface {
	Publish(ctx context.Context, topic string, key string, payload any) error
}

// RoleDelegationAuthorizer prevents invitations from bypassing the same rank
// and permission-subset rules used by direct role assignment.
type RoleDelegationAuthorizer interface {
	CanDelegateRole(ctx context.Context, tenantID, actorID, roleID uuid.UUID) error
}

// Config holds invitation tunables.
type Config struct {
	// TTL bounds how long an invite remains redeemable. A short window limits the
	// exposure of a token sitting in an inbox or a forwarded email.
	TTL time.Duration
}

// Service implements invitation business logic.
type Service struct {
	repo   *Repository
	audit  Recorder
	quotas SeatQuotaChecker
	events EventPublisher
	roles  RoleDelegationAuthorizer
	cfg    Config
}

func NewService(repo *Repository, recorder Recorder, quotas SeatQuotaChecker, events EventPublisher, roles RoleDelegationAuthorizer, cfg Config) *Service {
	if cfg.TTL <= 0 {
		cfg.TTL = 7 * 24 * time.Hour
	}
	return &Service{repo: repo, audit: recorder, quotas: quotas, events: events, roles: roles, cfg: cfg}
}

// CreateInput is the validated input for issuing an invitation. Exactly one of
// RoleID or RoleSlug must be supplied.
type CreateInput struct {
	Email    string
	RoleID   *uuid.UUID
	RoleSlug string
}

// CreateResult carries the created invitation plus the one-time plaintext token.
type CreateResult struct {
	Invitation *Invitation `json:"invitation"`
	// Token is returned exactly once, for the caller to deliver out of band. It is
	// never retrievable afterward because only its hash is stored.
	Token string `json:"token"`
}

// Create issues a pending invitation for an email address.
func (s *Service) Create(ctx context.Context, entry audit.Entry, tenantID, actorID uuid.UUID, in CreateInput) (*CreateResult, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" {
		return nil, apperror.New(apperror.CodeValidation, "email is required")
	}

	roleID, err := s.resolveRole(ctx, in)
	if err != nil {
		return nil, err
	}
	if s.roles != nil {
		if err := s.roles.CanDelegateRole(ctx, tenantID, actorID, roleID); err != nil {
			return nil, err
		}
	}

	// Seat quota is checked before the invite is issued, and pending invites count
	// toward it, so a tenant cannot queue up more accepted members than it pays for.
	if s.quotas != nil {
		if err := s.quotas.CheckSeatQuota(ctx, tenantID); err != nil {
			return nil, err
		}
	}

	plaintext, tokenHash, err := GenerateToken()
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to generate invitation token", err)
	}

	invite := &Invitation{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Email:     email,
		RoleID:    roleID,
		TokenHash: tokenHash,
		Status:    StatusPending,
		InvitedBy: &actorID,
		ExpiresAt: time.Now().Add(s.cfg.TTL),
		CreatedAt: time.Now(),
	}

	if err := s.repo.Create(ctx, invite); err != nil {
		if dberr.IsUniqueViolation(err) {
			// The partial unique index on (tenant_id, email) WHERE status='pending'
			// is what surfaces here.
			return nil, apperror.New(apperror.CodeConflict,
				"an invitation for that email is already pending")
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to create invitation", err)
	}

	if s.audit != nil {
		inviteID := invite.ID
		entry.Action = audit.ActionUserInvited
		entry.TargetType = audit.TargetUser
		entry.TargetID = &inviteID
		entry.Metadata = map[string]any{"email": email, "role_id": roleID.String()}
		s.audit.Record(ctx, entry)
	}

	// The plaintext token is published so an email consumer can deliver it. It is
	// deliberately not written to the audit log or application logs.
	if s.events != nil {
		_ = s.events.Publish(ctx, "member.invited", invite.ID.String(), map[string]any{
			"invitation_id": invite.ID,
			"tenant_id":     tenantID,
			"email":         email,
			"token":         plaintext,
			"expires_at":    invite.ExpiresAt,
		})
	}

	return &CreateResult{Invitation: invite, Token: plaintext}, nil
}

// List returns a page of the tenant's invitations.
func (s *Service) List(ctx context.Context, status string, page, pageSize int) ([]Detail, int64, error) {
	if status != "" && !validStatus(status) {
		return nil, 0, apperror.New(apperror.CodeValidation,
			"status must be pending, accepted, revoked, or expired")
	}

	details, total, err := s.repo.List(ctx, status, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list invitations", err)
	}
	return details, total, nil
}

// Revoke cancels a pending invitation, immediately making its token unusable.
func (s *Service) Revoke(ctx context.Context, entry audit.Entry, inviteID uuid.UUID) error {
	invite, err := s.repo.FindByID(ctx, inviteID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return apperror.New(apperror.CodeNotFound, "invitation not found")
		}
		return apperror.Wrap(apperror.CodeInternal, "failed to load invitation", err)
	}
	if invite.Status != StatusPending {
		return apperror.New(apperror.CodeConflict, "only pending invitations can be revoked")
	}

	if err := s.repo.UpdateStatus(ctx, inviteID, StatusPending, StatusRevoked); err != nil {
		if errors.Is(err, ErrNotFound) {
			// Lost a race with an accept or another revoke.
			return apperror.New(apperror.CodeConflict, "invitation is no longer pending")
		}
		return apperror.Wrap(apperror.CodeInternal, "failed to revoke invitation", err)
	}

	if s.audit != nil {
		id := inviteID
		entry.Action = audit.ActionInviteRevoked
		entry.TargetType = audit.TargetUser
		entry.TargetID = &id
		entry.Metadata = map[string]any{"email": invite.Email}
		s.audit.Record(ctx, entry)
	}
	return nil
}

// Preview resolves a raw token into the limited public view of its invitation.
//
// Every failure mode returns the same generic message. Distinguishing "expired"
// from "revoked" from "never existed" would let an unauthenticated caller probe
// token validity, so all invalid tokens are reported identically.
func (s *Service) Preview(ctx context.Context, token string) (*Preview, error) {
	invite, err := s.repo.FindByTokenHash(ctx, HashToken(token))
	if err != nil {
		return nil, apperror.New(apperror.CodeNotFound, "invitation is invalid or has expired")
	}
	if invite.Status != StatusPending || time.Now().After(invite.ExpiresAt) {
		return nil, apperror.New(apperror.CodeNotFound, "invitation is invalid or has expired")
	}

	pctx, err := s.repo.LoadPreviewContext(ctx, invite)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load invitation", err)
	}

	exists, err := s.repo.UserExistsInTenant(ctx, invite.TenantID, invite.Email)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load invitation", err)
	}

	return &Preview{
		Email:            invite.Email,
		OrganizationName: pctx.OrganizationName,
		OrganizationSlug: pctx.OrganizationSlug,
		RoleName:         pctx.RoleName,
		ExpiresAt:        invite.ExpiresAt,
		RequiresPassword: !exists,
	}, nil
}

// AcceptInput is the payload for redeeming an invitation.
type AcceptInput struct {
	Token    string
	FullName string
	Password string
}

// Accept redeems an invitation, creating the user when they are new to the tenant
// or granting the invited role when they already exist.
func (s *Service) Accept(ctx context.Context, in AcceptInput) (*AcceptResult, error) {
	tokenHash := HashToken(in.Token)

	invite, err := s.repo.FindByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, apperror.New(apperror.CodeNotFound, "invitation is invalid or has expired")
	}
	if invite.Status != StatusPending || time.Now().After(invite.ExpiresAt) {
		return nil, apperror.New(apperror.CodeNotFound, "invitation is invalid or has expired")
	}

	exists, err := s.repo.UserExistsInTenant(ctx, invite.TenantID, invite.Email)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to accept invitation", err)
	}

	var passwordHash *string
	if !exists {
		if len(in.Password) < 8 {
			return nil, apperror.New(apperror.CodeValidation,
				"password must be at least 8 characters")
		}
		hashed, err := identity.HashPassword(in.Password)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to process password", err)
		}
		passwordHash = &hashed
	}

	fullName := strings.TrimSpace(in.FullName)
	if fullName == "" {
		fullName = invite.Email
	}

	userID, err := s.repo.Accept(ctx, tokenHash, fullName, passwordHash)
	if err != nil {
		// The SQL function raises for already-accepted/expired invites, including
		// when it loses a redemption race.
		return nil, apperror.Wrap(apperror.CodeConflict,
			"invitation could not be accepted; it may have already been used", err)
	}

	if s.audit != nil {
		id := invite.ID
		actor := userID
		s.audit.Record(ctx, audit.Entry{
			TenantID:   invite.TenantID,
			ActorID:    &actor,
			Action:     audit.ActionInviteAccepted,
			TargetType: audit.TargetUser,
			TargetID:   &id,
			Metadata:   map[string]any{"email": invite.Email},
		})
	}

	if s.events != nil {
		_ = s.events.Publish(ctx, "member.invitation_accepted", userID.String(), map[string]any{
			"user_id": userID, "tenant_id": invite.TenantID, "email": invite.Email,
		})
	}

	return &AcceptResult{TenantID: invite.TenantID, UserID: userID, Email: invite.Email}, nil
}

// --- internal helpers ---

func (s *Service) resolveRole(ctx context.Context, in CreateInput) (uuid.UUID, error) {
	if in.RoleID != nil {
		exists, err := s.repo.RoleExists(ctx, *in.RoleID)
		if err != nil {
			return uuid.Nil, apperror.Wrap(apperror.CodeInternal, "failed to verify role", err)
		}
		if !exists {
			return uuid.Nil, apperror.New(apperror.CodeValidation, "role not found in this organization")
		}
		return *in.RoleID, nil
	}

	slug := strings.TrimSpace(in.RoleSlug)
	if slug == "" {
		return uuid.Nil, apperror.New(apperror.CodeValidation, "role_id or role_slug is required")
	}
	roleID, err := s.repo.FindRoleBySlug(ctx, slug)
	if err != nil {
		return uuid.Nil, apperror.New(apperror.CodeValidation, "role not found in this organization")
	}
	return roleID, nil
}

func validStatus(status string) bool {
	switch status {
	case StatusPending, StatusAccepted, StatusRevoked, StatusExpired:
		return true
	default:
		return false
	}
}

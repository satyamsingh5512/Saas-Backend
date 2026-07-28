package preferences

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/internal/identity"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Recorder is the slice of internal/audit this module needs.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// PermissionReader is the slice of internal/authz needed to include the caller's
// effective permissions in their profile, which is what lets a UI hide controls the
// user cannot use.
type PermissionReader interface {
	UserPermissions(ctx context.Context, tenantID, userID uuid.UUID) ([]string, error)
}

// Service implements preference and self-service profile logic.
type Service struct {
	repo  *Repository
	audit Recorder
	perms PermissionReader
}

func NewService(repo *Repository, recorder Recorder, perms PermissionReader) *Service {
	return &Service{repo: repo, audit: recorder, perms: perms}
}

// Get returns the caller's preferences, falling back to defaults when none are
// stored yet.
func (s *Service) Get(ctx context.Context, tenantID, userID uuid.UUID) (*Preferences, error) {
	prefs, err := s.repo.Find(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Defaults(tenantID, userID), nil
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load preferences", err)
	}
	return prefs, nil
}

// UpdateInput carries preference changes; nil means "leave unchanged".
type UpdateInput struct {
	Timezone           *string
	Locale             *string
	Theme              *string
	EmailNotifications *bool
}

// Update merges changes into the caller's preferences and persists them.
func (s *Service) Update(ctx context.Context, tenantID, userID uuid.UUID, in UpdateInput) (*Preferences, error) {
	prefs, err := s.Get(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}

	if in.Timezone != nil {
		tz := strings.TrimSpace(*in.Timezone)
		if tz == "" || len(tz) > 50 {
			return nil, apperror.New(apperror.CodeValidation, "timezone must be 1-50 characters")
		}
		prefs.Timezone = tz
	}
	if in.Locale != nil {
		locale := strings.TrimSpace(*in.Locale)
		if locale == "" || len(locale) > 10 {
			return nil, apperror.New(apperror.CodeValidation, "locale must be 1-10 characters")
		}
		prefs.Locale = locale
	}
	if in.Theme != nil {
		switch *in.Theme {
		case ThemeSystem, ThemeLight, ThemeDark:
			prefs.Theme = *in.Theme
		default:
			return nil, apperror.New(apperror.CodeValidation, "theme must be system, light, or dark")
		}
	}
	if in.EmailNotifications != nil {
		prefs.EmailNotifications = *in.EmailNotifications
	}

	// TenantID is taken from the validated credential, never from the request body,
	// so a caller cannot write a preference row attributed to another tenant.
	prefs.TenantID = tenantID
	prefs.UserID = userID

	if err := s.repo.Upsert(ctx, prefs); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to save preferences", err)
	}
	return prefs, nil
}

// GetProfile assembles the caller's full self-service profile.
func (s *Service) GetProfile(ctx context.Context, tenantID, userID uuid.UUID) (*Profile, error) {
	user, err := s.repo.FindUser(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, apperror.New(apperror.CodeNotFound, "user not found")
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load profile", err)
	}

	prefs, err := s.Get(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}

	org, err := s.repo.OrganizationSummary(ctx, tenantID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load organization", err)
	}

	roles, err := s.repo.RoleSlugs(ctx, userID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load roles", err)
	}

	permissions := []string{}
	if s.perms != nil {
		granted, err := s.perms.UserPermissions(ctx, tenantID, userID)
		if err == nil {
			permissions = granted
		}
	}

	return &Profile{
		UserID:          user.ID,
		TenantID:        tenantID,
		Email:           user.Email,
		FullName:        user.FullName,
		AvatarURL:       user.AvatarURL,
		Status:          user.Status,
		EmailVerifiedAt: user.EmailVerifiedAt,
		LastLoginAt:     user.LastLoginAt,
		CreatedAt:       user.CreatedAt,
		Roles:           roles,
		Permissions:     permissions,
		Preferences:     prefs,
		Organization:    *org,
	}, nil
}

// ProfileInput carries self-service profile changes.
type ProfileInput struct {
	FullName    *string
	AvatarURL   *string
	ClearAvatar bool
}

// UpdateProfile applies display-name and avatar changes to the caller's own account.
func (s *Service) UpdateProfile(ctx context.Context, entry audit.Entry, tenantID, userID uuid.UUID, in ProfileInput) (*Profile, error) {
	user, err := s.repo.FindUser(ctx, userID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load profile", err)
	}

	fullName := user.FullName
	if in.FullName != nil {
		trimmed := strings.TrimSpace(*in.FullName)
		if trimmed == "" || len(trimmed) > 255 {
			return nil, apperror.New(apperror.CodeValidation, "full_name must be 1-255 characters")
		}
		fullName = trimmed
	}

	avatarURL := user.AvatarURL
	switch {
	case in.ClearAvatar:
		avatarURL = nil
	case in.AvatarURL != nil:
		trimmed := strings.TrimSpace(*in.AvatarURL)
		if err := validateAvatarURL(trimmed); err != nil {
			return nil, err
		}
		avatarURL = &trimmed
	}

	if err := s.repo.UpdateProfile(ctx, userID, fullName, avatarURL); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, apperror.New(apperror.CodeNotFound, "user not found")
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to update profile", err)
	}

	if s.audit != nil {
		id := userID
		entry.Action = audit.ActionUserProfileUpdate
		entry.TargetType = audit.TargetUser
		entry.TargetID = &id
		s.audit.Record(ctx, entry)
	}

	return s.GetProfile(ctx, tenantID, userID)
}

// ChangePassword rotates the caller's own password.
func (s *Service) ChangePassword(ctx context.Context, entry audit.Entry, tenantID, userID uuid.UUID, currentPassword, newPassword string) error {
	user, err := s.repo.FindUser(ctx, userID)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to load user", err)
	}

	// An account created through OAuth has no password to verify against. Allowing
	// a "change" here would let anyone holding the session set a password without
	// proving knowledge of an existing one, so it is refused and directed to the
	// reset flow, which proves control of the mailbox instead.
	if user.PasswordHash == nil {
		return apperror.New(apperror.CodeUnprocessable,
			"this account has no password set; use the password reset flow instead")
	}
	if !identity.CheckPassword(*user.PasswordHash, currentPassword) {
		return apperror.New(apperror.CodeUnauthorized, "current password is incorrect")
	}
	if len(newPassword) < 8 {
		return apperror.New(apperror.CodeValidation, "new password must be at least 8 characters")
	}
	if currentPassword == newPassword {
		return apperror.New(apperror.CodeValidation, "new password must differ from the current password")
	}

	hashed, err := identity.HashPassword(newPassword)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to process password", err)
	}
	if err := s.repo.UpdatePassword(ctx, userID, hashed); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to update password", err)
	}

	// Existing sessions are terminated so a session opened with the old password
	// does not outlive it.
	if err := s.repo.RevokeAllRefreshTokens(ctx, userID); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to revoke existing sessions", err)
	}

	if s.audit != nil {
		id := userID
		entry.Action = audit.ActionPasswordChanged
		entry.TargetType = audit.TargetUser
		entry.TargetID = &id
		s.audit.Record(ctx, entry)
	}
	return nil
}

// validateAvatarURL restricts avatars to absolute HTTPS URLs.
//
// Rejecting other schemes matters because this value is rendered as an image
// source in the dashboard: permitting javascript: or data: URLs here would turn a
// profile field into a stored cross-site-scripting vector.
func validateAvatarURL(raw string) error {
	if raw == "" {
		return apperror.New(apperror.CodeValidation, "avatar_url cannot be empty; omit it or set clear_avatar")
	}
	if len(raw) > 1000 {
		return apperror.New(apperror.CodeValidation, "avatar_url is too long")
	}
	if !strings.HasPrefix(raw, "https://") {
		return apperror.New(apperror.CodeValidation, "avatar_url must be an absolute https:// URL")
	}
	return nil
}

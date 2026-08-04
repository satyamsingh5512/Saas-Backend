package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/tenancy"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
)

// RoleAssigner is the minimal surface identity needs from internal/authz,
// used to assign the Owner role to a newly-registered tenant's first user
// and to read a user's effective primary role for display/JWT purposes.
// Declared here (consumer-defined interface) rather than in authz, per Go
// convention, and to avoid an import cycle (authz will depend on identity
// for user lookups in permission checks, not the other way around).
type RoleAssigner interface {
	AssignSystemRoleTx(tx *gorm.DB, tenantID, userID uuid.UUID, roleSlug string) error
	AssignSystemRole(ctx context.Context, tenantID, userID uuid.UUID, roleSlug string) error
	PrimaryRoleSlug(ctx context.Context, userID uuid.UUID) (string, error)
}

// EventPublisher is the minimal surface identity needs from the eventing
// layer (Kafka, wired up in the event-driven architecture phase). A no-op
// implementation is safe to inject before Kafka exists.
type EventPublisher interface {
	Publish(ctx context.Context, topic string, key string, payload any) error
}

// Config holds identity-module tunables sourced from the app's central
// config (internal/config), kept as a small struct so Service's
// constructor signature doesn't balloon as more settings are added.
type Config struct {
	JWTSecret            string
	AccessTokenTTL       time.Duration
	RefreshTokenTTL      time.Duration
	PasswordResetTTL     time.Duration
	EmailVerificationTTL time.Duration
}

// Service implements the identity module's business logic: registration,
// login, token refresh/rotation, password reset, and email verification.
// Handlers depend only on this type, never on Repository directly (Clean
// Architecture boundary enforced by convention/review, since Go has no
// package-private-to-module visibility beyond the internal/ boundary
// itself).
type Service struct {
	repo       *Repository
	tenantRepo *tenancy.Repository
	roles      RoleAssigner
	events     EventPublisher
	cfg        Config
	oauthCfg   OAuthConfig
	db         *gorm.DB
}

func NewService(repo *Repository, tenantRepo *tenancy.Repository, roles RoleAssigner, events EventPublisher, db *gorm.DB, cfg Config, oauthCfg OAuthConfig) *Service {
	return &Service{repo: repo, tenantRepo: tenantRepo, roles: roles, events: events, db: db, cfg: cfg, oauthCfg: oauthCfg}
}

// ValidateCredentialState is called after JWT signature validation on every
// protected request; database state, not token age, decides whether the subject
// may continue using the credential.
func (s *Service) ValidateCredentialState(ctx context.Context, tenantID, userID uuid.UUID) error {
	return s.repo.ValidateCredentialState(ctx, tenantID, userID)
}

// Register creates a new tenant and its first user (assigned the Owner
// role) atomically, then issues a token pair. This is the only place in the
// codebase that creates a tenant and a user in the same transaction across
// two different repositories -- justified because an orphaned tenant with
// no owner, or a user row referencing a rolled-back tenant, are both
// invalid states we must never observe even under partial failure.
func (s *Service) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	passwordHash, err := HashPassword(req.Password)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to process password", err)
	}

	tenant := &tenancy.Tenant{
		ID:       uuid.New(),
		Name:     req.TenantName,
		Slug:     req.TenantSlug,
		PlanCode: "free",
		Status:   tenancy.StatusActive,
		Settings: map[string]any{},
	}
	user := &User{
		ID:           uuid.New(),
		Email:        req.Email,
		PasswordHash: &passwordHash,
		FullName:     req.FullName,
		Status:       StatusActive,
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.tenantRepo.CreateTx(tx, tenant); err != nil {
			return err
		}

		// The tenants-insert trigger (seed_default_roles, migrations/000008)
		// runs SECURITY DEFINER and has already provisioned the
		// Owner/Admin/Manager/Member/Guest roles by this point, bypassing
		// RLS for its own writes since app.tenant_id cannot be set before
		// the tenant row exists. From here on, though, inserting the
		// user/role-grant rows in THIS transaction does need app.tenant_id
		// set, now that the tenant id is known -- set_config's third
		// argument `true` scopes it to this transaction only (mirrors
		// pkg/txscope.WithTenantTxID, but inlined here since this
		// transaction is not itself opened via txscope: it must create the
		// tenant row first, before any tenant id exists to scope to).
		if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenant.ID.String()).Error; err != nil {
			return fmt.Errorf("set tenant session var: %w", err)
		}

		user.TenantID = tenant.ID
		if err := s.repo.CreateTx(tx, user); err != nil {
			return err
		}

		if err := s.roles.AssignSystemRoleTx(tx, tenant.ID, user.ID, "owner"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		code := classifyDBError(err)
		msg := "failed to create organization"
		if code == apperror.CodeConflict {
			msg = "tenant slug or email already exists"
		}
		return nil, apperror.Wrap(code, msg, err)
	}

	tokens, err := s.issueTokenPair(ctx, tenant.ID, user, "owner")
	if err != nil {
		return nil, err
	}

	if s.events != nil {
		_ = s.events.Publish(ctx, "organization.created", tenant.ID.String(), map[string]any{
			"tenant_id": tenant.ID, "tenant_slug": tenant.Slug,
		})
		_ = s.events.Publish(ctx, "user.created", user.ID.String(), map[string]any{
			"user_id": user.ID, "tenant_id": tenant.ID, "email": user.Email,
		})
	}

	return &RegisterResponse{
		TenantID:   tenant.ID.String(),
		TenantSlug: tenant.Slug,
		User:       ToUserResponse(user),
		Tokens:     *tokens,
	}, nil
}

// Login authenticates a user by email/password. If tenantSlug is provided,
// the lookup is scoped directly to that tenant. If omitted, all tenants
// matching the email are considered; a single match proceeds automatically,
// multiple matches return AmbiguousTenants for the client to disambiguate.
func (s *Service) Login(ctx context.Context, req LoginRequest) (*LoginResponse, error) {
	var candidate *User

	if req.TenantSlug != "" {
		tenant, err := s.tenantRepo.FindBySlug(ctx, req.TenantSlug)
		if err != nil {
			return nil, apperror.New(apperror.CodeUnauthorized, "invalid email or password")
		}
		scopedCtx := ctxWithTenantID(ctx, tenant.ID)
		u, err := s.repo.FindByEmailInTenant(scopedCtx, req.Email)
		if err != nil {
			return nil, apperror.New(apperror.CodeUnauthorized, "invalid email or password")
		}
		candidate = u
	} else {
		users, err := s.repo.FindByEmailAnyTenant(ctx, req.Email)
		if err != nil || len(users) == 0 {
			return nil, apperror.New(apperror.CodeUnauthorized, "invalid email or password")
		}
		if len(users) > 1 {
			choices := make([]TenantChoice, 0, len(users))
			for _, u := range users {
				t, err := s.tenantRepo.FindByID(ctx, u.TenantID)
				if err != nil {
					continue
				}
				choices = append(choices, TenantChoice{TenantSlug: t.Slug, TenantName: t.Name})
			}
			return &LoginResponse{AmbiguousTenants: choices}, nil
		}
		candidate = &users[0]
	}

	if candidate.PasswordHash == nil || !CheckPassword(*candidate.PasswordHash, req.Password) {
		return nil, apperror.New(apperror.CodeUnauthorized, "invalid email or password")
	}
	if candidate.Status == StatusDisabled {
		return nil, apperror.New(apperror.CodeForbidden, "account is disabled")
	}
	currentTenant, err := s.tenantRepo.FindByID(ctx, candidate.TenantID)
	if err != nil || currentTenant.Status != tenancy.StatusActive {
		return nil, apperror.New(apperror.CodeForbidden, "organization is not active")
	}

	roleSlug, err := s.roles.PrimaryRoleSlug(ctxWithTenantID(ctx, candidate.TenantID), candidate.ID)
	if err != nil {
		roleSlug = "member"
	}

	now := time.Now()
	candidate.LastLoginAt = &now
	_ = s.repo.Update(ctxWithTenantID(ctx, candidate.TenantID), candidate)

	tokens, err := s.issueTokenPair(ctx, candidate.TenantID, candidate, roleSlug)
	if err != nil {
		return nil, err
	}

	return &LoginResponse{User: ToUserResponse(candidate), Tokens: *tokens}, nil
}

// Refresh validates and rotates a refresh token, issuing a new access +
// refresh token pair. Implements rotation-on-use with theft detection: if
// the presented token has already been rotated (ReplacedBy is set) or is
// revoked, the entire token family is revoked and the request is rejected,
// since token reuse after rotation is a strong signal of token theft.
func (s *Service) Refresh(ctx context.Context, req RefreshRequest) (*TokenPair, error) {
	hash := HashOpaqueToken(req.RefreshToken)
	stored, err := s.repo.FindRefreshTokenByHash(ctx, hash)
	if err != nil {
		return nil, apperror.New(apperror.CodeUnauthorized, "invalid refresh token")
	}

	newRefreshPlain, newRefreshRow, err := s.newRefreshTokenRow(stored.TenantID, stored.UserID, stored.FamilyID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to generate refresh token", err)
	}
	user, err := s.repo.RotateRefreshToken(ctx, stored.TenantID, stored.ID, newRefreshRow)
	if err != nil {
		switch {
		case errors.Is(err, ErrTenantInactive), errors.Is(err, ErrUserInactive):
			return nil, apperror.New(apperror.CodeForbidden, "account or organization is not active")
		case errors.Is(err, ErrRefreshExpired):
			return nil, apperror.New(apperror.CodeUnauthorized, "refresh token has expired")
		case errors.Is(err, ErrRefreshReused):
			return nil, apperror.New(apperror.CodeUnauthorized, "refresh token has been revoked")
		case errors.Is(err, ErrRefreshInvalid):
			return nil, apperror.New(apperror.CodeUnauthorized, "invalid refresh token")
		default:
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to rotate refresh token", err)
		}
	}

	scopedCtx := ctxWithTenantID(ctx, stored.TenantID)
	roleSlug, err := s.roles.PrimaryRoleSlug(scopedCtx, user.ID)
	if err != nil {
		roleSlug = "member"
	}
	accessToken, accessExpiry, err := s.newAccessToken(user.ID, stored.TenantID, roleSlug)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to generate access token", err)
	}
	return &TokenPair{
		AccessToken: accessToken, RefreshToken: newRefreshPlain,
		AccessTokenExpiresAt: accessExpiry, RefreshTokenExpiresAt: newRefreshRow.ExpiresAt,
		TokenType: "Bearer",
	}, nil
}

// Logout revokes a single refresh token family (all devices sharing that
// login session's lineage), so a stolen refresh token cannot be used again
// after the legitimate user logs out.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	hash := HashOpaqueToken(refreshToken)
	stored, err := s.repo.FindRefreshTokenByHash(ctx, hash)
	if err != nil {
		return nil // already invalid/unknown: logout is idempotent
	}
	return s.repo.RevokeRefreshTokenFamily(ctx, stored.TenantID, stored.FamilyID)
}

// RequestPasswordReset issues a password-reset token for the given email
// within the given tenant. Always returns nil (no error) even if the email
// doesn't exist, to avoid leaking account existence via response timing/
// content -- the caller (handler) returns a generic "check your email"
// message regardless.
func (s *Service) RequestPasswordReset(ctx context.Context, req ForgotPasswordRequest) error {
	tenant, err := s.tenantRepo.FindBySlug(ctx, req.TenantSlug)
	if err != nil || tenant.Status != tenancy.StatusActive {
		return nil
	}
	scopedCtx := ctxWithTenantID(ctx, tenant.ID)
	user, err := s.repo.FindByEmailInTenant(scopedCtx, req.Email)
	if err != nil || user.Status == StatusDisabled {
		return nil
	}

	plain, err := GenerateOpaqueToken()
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to generate reset token", err)
	}

	vt := &VerificationToken{
		ID:        uuid.New(),
		UserID:    user.ID,
		Purpose:   PurposePasswordReset,
		TokenHash: HashOpaqueToken(plain),
		ExpiresAt: time.Now().Add(s.ttlOrDefault(s.cfg.PasswordResetTTL, time.Hour)),
	}
	if err := s.repo.CreateVerificationToken(scopedCtx, tenant.ID, vt); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to store reset token", err)
	}

	// TODO(notification phase): dispatch the plaintext reset token via the
	// email notification channel rather than only logging it. Deferred
	// until internal/notification exists (Phase 11). Publishing a domain
	// event now means the notification consumer can pick this up
	// immediately once it's implemented, without a code change here.
	if s.events != nil {
		_ = s.events.Publish(ctx, "auth.password_reset_requested", user.ID.String(), map[string]any{
			"user_id": user.ID, "tenant_id": tenant.ID, "reset_token": plain,
		})
	}
	return nil
}

// ResetPassword consumes a password-reset token and sets a new password.
func (s *Service) ResetPassword(ctx context.Context, req ResetPasswordRequest) error {
	vt, err := s.repo.FindVerificationTokenByHash(ctx, HashOpaqueToken(req.Token))
	if err != nil || vt.Purpose != PurposePasswordReset {
		return apperror.New(apperror.CodeValidation, "invalid or expired reset token")
	}
	hash, err := HashPassword(req.NewPassword)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to process password", err)
	}
	if err := s.repo.ConsumePasswordReset(ctx, vt.TenantID, vt.ID, vt.UserID, hash); err != nil {
		if errors.Is(err, ErrVerificationInvalid) || errors.Is(err, ErrTenantInactive) || errors.Is(err, ErrUserInactive) {
			return apperror.New(apperror.CodeValidation, "invalid or expired reset token")
		}
		return apperror.Wrap(apperror.CodeInternal, "failed to reset password", err)
	}

	if s.events != nil {
		_ = s.events.Publish(ctx, "auth.password_changed", vt.UserID.String(), map[string]any{
			"user_id": vt.UserID, "tenant_id": vt.TenantID,
		})
	}
	return nil
}

// RequestEmailVerification issues an email-verification token for the given
// already-authenticated user.
func (s *Service) RequestEmailVerification(ctx context.Context, tenantID, userID uuid.UUID) error {
	scopedCtx := ctxWithTenantID(ctx, tenantID)
	user, err := s.repo.FindByID(scopedCtx, userID)
	if err != nil {
		return apperror.New(apperror.CodeNotFound, "user not found")
	}
	if user.EmailVerifiedAt != nil {
		return nil
	}

	plain, err := GenerateOpaqueToken()
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to generate verification token", err)
	}
	vt := &VerificationToken{
		ID:        uuid.New(),
		UserID:    userID,
		Purpose:   PurposeEmailVerification,
		TokenHash: HashOpaqueToken(plain),
		ExpiresAt: time.Now().Add(s.ttlOrDefault(s.cfg.EmailVerificationTTL, 24*time.Hour)),
	}
	if err := s.repo.CreateVerificationToken(scopedCtx, tenantID, vt); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to store verification token", err)
	}

	if s.events != nil {
		_ = s.events.Publish(ctx, "auth.email_verification_requested", userID.String(), map[string]any{
			"user_id": userID, "tenant_id": tenantID, "verification_token": plain,
		})
	}
	return nil
}

// VerifyEmail consumes an email-verification token and marks the user verified.
func (s *Service) VerifyEmail(ctx context.Context, req VerifyEmailRequest) error {
	vt, err := s.repo.FindVerificationTokenByHash(ctx, HashOpaqueToken(req.Token))
	if err != nil || vt.Purpose != PurposeEmailVerification {
		return apperror.New(apperror.CodeValidation, "invalid or expired verification token")
	}
	if err := s.repo.ConsumeEmailVerification(ctx, vt.TenantID, vt.ID, vt.UserID); err != nil {
		if errors.Is(err, ErrVerificationInvalid) || errors.Is(err, ErrTenantInactive) || errors.Is(err, ErrUserInactive) {
			return apperror.New(apperror.CodeValidation, "invalid or expired verification token")
		}
		return apperror.Wrap(apperror.CodeInternal, "failed to verify email", err)
	}

	if s.events != nil {
		_ = s.events.Publish(ctx, "user.email_verified", vt.UserID.String(), map[string]any{
			"user_id": vt.UserID, "tenant_id": vt.TenantID,
		})
	}
	return nil
}

// ListUsers returns a paginated list of users in the current tenant. This
// is a thin passthrough to the repository; more elaborate filtering/sorting
// (per the API requirements) is layered on in the membership/platform
// modules' list endpoints where richer query params apply. Kept here for
// the base "list org members" identity use case.
func (s *Service) ListUsers(ctx context.Context, page, pageSize int) ([]UserResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	users, total, err := s.repo.ListByTenant(ctx, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list users", err)
	}
	out := make([]UserResponse, 0, len(users))
	for i := range users {
		out = append(out, ToUserResponse(&users[i]))
	}
	return out, total, nil
}

// --- internal helpers ---

func (s *Service) issueTokenPair(ctx context.Context, tenantID uuid.UUID, user *User, roleSlug string) (*TokenPair, error) {
	accessToken, accessExpiry, err := s.newAccessToken(user.ID, tenantID, roleSlug)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to generate access token", err)
	}

	refreshPlain, refreshRow, err := s.newRefreshTokenRow(tenantID, user.ID, uuid.New())
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to generate refresh token", err)
	}
	if err := s.repo.CreateRefreshToken(ctx, tenantID, refreshRow); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to persist refresh token", err)
	}

	return &TokenPair{
		AccessToken:           accessToken,
		RefreshToken:          refreshPlain,
		AccessTokenExpiresAt:  accessExpiry,
		RefreshTokenExpiresAt: refreshRow.ExpiresAt,
		TokenType:             "Bearer",
	}, nil
}

func (s *Service) newAccessToken(userID, tenantID uuid.UUID, roleSlug string) (string, time.Time, error) {
	ttl := s.ttlOrDefault(s.cfg.AccessTokenTTL, 15*time.Minute)
	expiry := time.Now().Add(ttl)
	token, err := GenerateAccessToken(s.cfg.JWTSecret, int(ttl.Minutes()), userID, tenantID, roleSlug)
	return token, expiry, err
}

func (s *Service) newRefreshTokenRow(tenantID, userID, familyID uuid.UUID) (string, *RefreshToken, error) {
	plain, err := GenerateOpaqueToken()
	if err != nil {
		return "", nil, err
	}
	ttl := s.ttlOrDefault(s.cfg.RefreshTokenTTL, 30*24*time.Hour)
	row := &RefreshToken{
		ID:        uuid.New(),
		TenantID:  tenantID,
		UserID:    userID,
		TokenHash: HashOpaqueToken(plain),
		FamilyID:  familyID,
		ExpiresAt: time.Now().Add(ttl),
	}
	return plain, row, nil
}

func (s *Service) ttlOrDefault(ttl, def time.Duration) time.Duration {
	if ttl <= 0 {
		return def
	}
	return ttl
}

// ctxWithTenantID attaches a tenant ID to ctx via pkg/txscope, so
// repository calls immediately after (e.g. within the same service method)
// have an active tenant scope without requiring a full tenancy.Middleware
// request round-trip -- needed here because login/refresh/reset flows
// resolve the tenant themselves mid-request, before any per-request
// middleware has had a chance to.
func ctxWithTenantID(ctx context.Context, tenantID uuid.UUID) context.Context {
	return txscope.WithTenantID(ctx, tenantID)
}

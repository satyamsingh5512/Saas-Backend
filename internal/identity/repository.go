package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrUserNotFound is returned when a user lookup yields no live row within
// the caller's tenant scope (which, thanks to RLS, is the only scope any
// query can ever see).
var ErrUserNotFound = errors.New("identity: user not found")

var (
	ErrTenantInactive      = errors.New("identity: tenant inactive")
	ErrUserInactive        = errors.New("identity: user inactive")
	ErrRefreshInvalid      = errors.New("identity: refresh token invalid")
	ErrRefreshReused       = errors.New("identity: refresh token reused")
	ErrRefreshExpired      = errors.New("identity: refresh token expired")
	ErrVerificationInvalid = errors.New("identity: verification token invalid")
)

// Repository provides tenant-scoped data access for users and their related
// auth artifacts (refresh tokens, OAuth links, verification tokens). Every
// method takes a context carrying a tenant ID (see pkg/txscope) and runs its
// query inside a transaction with the RLS session variable set, EXCEPT the
// handful of pre-authentication lookups (by email during login, by token
// hash for refresh/verification/OAuth) which cannot have a tenant in context
// yet and so use explicit WithoutTenantScope + application-level filtering
// documented at each call site.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a new user within the tenant scoped by ctx.
func (r *Repository) Create(ctx context.Context, user *User) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Create(user).Error; err != nil {
			return fmt.Errorf("identity: create user: %w", err)
		}
		return nil
	})
}

// CreateTx inserts a user using an already-open transaction (e.g. the
// tenant-creation flow, where the first user must be created atomically
// with the tenant itself, before a tenant scope even exists to set).
func (r *Repository) CreateTx(tx *gorm.DB, user *User) error {
	if err := tx.Create(user).Error; err != nil {
		return fmt.Errorf("identity: create user tx: %w", err)
	}
	return nil
}

// FindByID looks up a user by ID within the current tenant scope.
func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var user User
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("id = ? AND deleted_at IS NULL", id).First(&user).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindByEmailInTenant looks up a user by email within the current tenant
// scope. Used for login when the tenant is already known (e.g. resolved
// from subdomain before the credentials are checked).
func (r *Repository) FindByEmailInTenant(ctx context.Context, email string) (*User, error) {
	var user User
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("email = ? AND deleted_at IS NULL", email).First(&user).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindByEmailAnyTenant looks up a user by email across ALL tenants, for the
// case where the client hasn't told us which tenant they mean yet (e.g. a
// login form on the marketing site that only asks for email/password, not a
// subdomain). Calls the find_users_by_email SECURITY DEFINER function
// (migrations/000011) rather than querying the table directly, for the same
// reason FindRefreshTokenByHash does: users has FORCE RLS, and there is no
// tenant to scope this lookup to -- discovering the tenant is the entire
// point of the query.
//
// If multiple tenants share the same email (legitimate: same person, two
// orgs), all matching rows are returned and the caller (service layer) must
// disambiguate, e.g. by asking the user to pick an org.
func (r *Repository) FindByEmailAnyTenant(ctx context.Context, email string) ([]User, error) {
	var users []User
	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw("SELECT * FROM find_users_by_email(?)", email).Scan(&users).Error
	})
	if err != nil {
		return nil, fmt.Errorf("identity: find by email any tenant: %w", err)
	}
	return users, nil
}

// ListByTenant returns a page of users in the current tenant scope.
func (r *Repository) ListByTenant(ctx context.Context, limit, offset int) ([]User, int64, error) {
	var users []User
	var total int64

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("deleted_at IS NULL").Count(&total).Error; err != nil {
			return err
		}
		return tx.Where("deleted_at IS NULL").
			Order("created_at DESC").
			Limit(limit).Offset(offset).
			Find(&users).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("identity: list by tenant: %w", err)
	}
	return users, total, nil
}

// Update persists changes to an existing user within the current tenant scope.
func (r *Repository) Update(ctx context.Context, user *User) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Save(user).Error
	})
}

// UpdateTx is the explicit-transaction variant of Update.
func (r *Repository) UpdateTx(tx *gorm.DB, user *User) error {
	return tx.Save(user).Error
}

// ValidateCredentialState re-reads the tenant and user for every protected
// request. JWT claims and API credentials identify the subject; they do not
// override current suspension, disablement, or soft deletion state.
func (r *Repository) ValidateCredentialState(ctx context.Context, tenantID, userID uuid.UUID) error {
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		_, err := r.activeCredentialUserTx(tx, tenantID, userID, false)
		return err
	})
}

func (r *Repository) activeCredentialUserTx(tx *gorm.DB, tenantID, userID uuid.UUID, lock bool) (*User, error) {
	var tenant struct {
		Status    string
		DeletedAt *time.Time
	}
	tenantQuery := tx.Table("tenants").Select("status, deleted_at").Where("id = ?", tenantID)
	if lock {
		tenantQuery = tenantQuery.Clauses(clause.Locking{Strength: "SHARE"})
	}
	if err := tenantQuery.Scan(&tenant).Error; err != nil {
		return nil, err
	}
	if tenant.Status != "active" || tenant.DeletedAt != nil {
		return nil, ErrTenantInactive
	}

	query := tx.Where("id = ? AND deleted_at IS NULL", userID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var user User
	if err := query.First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserInactive
		}
		return nil, err
	}
	if user.Status == StatusDisabled {
		return nil, ErrUserInactive
	}
	return &user, nil
}

// --- Refresh tokens ---

// CreateRefreshToken inserts a new refresh token row within the given
// tenant scope (explicit tenant ID, since this is called right after login,
// before any per-request tenant middleware has run for a *subsequent*
// request).
func (r *Repository) CreateRefreshToken(ctx context.Context, tenantID uuid.UUID, rt *RefreshToken) error {
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		return tx.Create(rt).Error
	})
}

// FindRefreshTokenByHash looks up a refresh token by its hash, across
// tenants (the caller does not know the tenant until the token is resolved
// -- the token IS the credential proving which tenant/user this is).
//
// This calls the find_refresh_token_by_hash SQL function (migrations/000011)
// rather than querying the table directly: refresh_tokens has FORCE ROW
// LEVEL SECURITY, so a plain SELECT from the low-privilege app_user
// connection would return zero rows without app.tenant_id set, and there is
// no tenant to set it to until this very lookup resolves one. The
// SECURITY DEFINER function is scoped to exactly this one equality lookup,
// preserving RLS for every other access path to this table.
func (r *Repository) FindRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error) {
	var rt RefreshToken
	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Raw("SELECT * FROM find_refresh_token_by_hash(?)", tokenHash).Scan(&rt).Error
		if e == nil && rt.ID == uuid.Nil {
			return errors.New("identity: refresh token not found")
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	return &rt, nil
}

// RevokeRefreshTokenFamily revokes every token descended from familyID, used
// when token reuse is detected (a strong signal of token theft).
func (r *Repository) RevokeRefreshTokenFamily(ctx context.Context, tenantID, familyID uuid.UUID) error {
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		return tx.Model(&RefreshToken{}).
			Where("family_id = ? AND revoked_at IS NULL", familyID).
			Update("revoked_at", gorm.Expr("now()")).Error
	})
}

// RotateRefreshToken locks and consumes the old token, validates current
// tenant/user state, and inserts its replacement in one transaction. Concurrent
// uses can therefore create at most one descendant.
func (r *Repository) RotateRefreshToken(ctx context.Context, tenantID, oldID uuid.UUID, replacement *RefreshToken) (*User, error) {
	var (
		user    *User
		outcome error
	)
	err := txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		activeUser, err := r.activeCredentialUserTx(tx, tenantID, replacement.UserID, true)
		if err != nil {
			outcome = err
			return nil
		}

		var current RefreshToken
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", oldID).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				outcome = ErrRefreshInvalid
				return nil
			}
			return err
		}
		if current.RevokedAt != nil || current.ReplacedBy != nil {
			if err := tx.Model(&RefreshToken{}).
				Where("family_id = ? AND revoked_at IS NULL", current.FamilyID).
				Update("revoked_at", gorm.Expr("now()")).Error; err != nil {
				return err
			}
			outcome = ErrRefreshReused
			return nil
		}
		if !time.Now().Before(current.ExpiresAt) {
			outcome = ErrRefreshExpired
			return nil
		}
		if replacement.TenantID != current.TenantID || replacement.UserID != current.UserID || replacement.FamilyID != current.FamilyID {
			return ErrRefreshInvalid
		}
		if err := tx.Create(replacement).Error; err != nil {
			return err
		}
		result := tx.Model(&RefreshToken{}).Where("id = ? AND revoked_at IS NULL AND replaced_by IS NULL", current.ID).
			Updates(map[string]any{"revoked_at": gorm.Expr("now()"), "replaced_by": replacement.ID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRefreshInvalid
		}
		user = activeUser
		return nil
	})
	if err != nil {
		return nil, err
	}
	if outcome != nil {
		return nil, outcome
	}
	return user, nil
}

// --- OAuth accounts ---

// FindOAuthAccount looks up a linked OAuth identity by provider + provider
// user ID, across tenants, via the find_oauth_account_by_provider SECURITY
// DEFINER function (migrations/000011) for the same reason
// FindRefreshTokenByHash does: oauth_accounts has FORCE RLS and there is no
// tenant to scope to until this lookup resolves one.
func (r *Repository) FindOAuthAccount(ctx context.Context, provider, providerUserID string) (*OAuthAccount, error) {
	var acct OAuthAccount
	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Raw("SELECT * FROM find_oauth_account_by_provider(?, ?)", provider, providerUserID).Scan(&acct).Error
		if e == nil && acct.ID == uuid.Nil {
			return errors.New("identity: oauth account not found")
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	return &acct, nil
}

// CreateOAuthAccount links a new OAuth identity to a user.
func (r *Repository) CreateOAuthAccount(ctx context.Context, tenantID uuid.UUID, acct *OAuthAccount) error {
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		return tx.Create(acct).Error
	})
}

// --- Verification tokens (email verification / password reset) ---

// CreateVerificationToken inserts a new single-use verification token.
func (r *Repository) CreateVerificationToken(ctx context.Context, tenantID uuid.UUID, vt *VerificationToken) error {
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		return tx.Create(vt).Error
	})
}

// FindVerificationTokenByHash looks up a verification token by hash, across
// tenants, via the find_verification_token_by_hash SECURITY DEFINER
// function (migrations/000011) -- same rationale as refresh tokens.
func (r *Repository) FindVerificationTokenByHash(ctx context.Context, tokenHash string) (*VerificationToken, error) {
	var vt VerificationToken
	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Raw("SELECT * FROM find_verification_token_by_hash(?)", tokenHash).Scan(&vt).Error
		if e == nil && vt.ID == uuid.Nil {
			return errors.New("identity: verification token not found")
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	return &vt, nil
}

// ConsumePasswordReset claims a single-use token, updates the password, and
// revokes every refresh session atomically.
func (r *Repository) ConsumePasswordReset(ctx context.Context, tenantID, tokenID, userID uuid.UUID, passwordHash string) error {
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		if _, err := r.activeCredentialUserTx(tx, tenantID, userID, true); err != nil {
			return err
		}
		if err := r.claimVerificationTokenTx(tx, tokenID, userID, PurposePasswordReset); err != nil {
			return err
		}
		result := tx.Model(&User{}).Where("id = ? AND deleted_at IS NULL", userID).
			Updates(map[string]any{"password_hash": passwordHash, "updated_at": gorm.Expr("now()")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVerificationInvalid
		}
		return tx.Model(&RefreshToken{}).Where("user_id = ? AND revoked_at IS NULL", userID).
			Update("revoked_at", gorm.Expr("now()")).Error
	})
}

// ConsumeEmailVerification claims the token and marks the user verified in the
// same transaction, so two concurrent callbacks cannot both succeed.
func (r *Repository) ConsumeEmailVerification(ctx context.Context, tenantID, tokenID, userID uuid.UUID) error {
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		if _, err := r.activeCredentialUserTx(tx, tenantID, userID, true); err != nil {
			return err
		}
		if err := r.claimVerificationTokenTx(tx, tokenID, userID, PurposeEmailVerification); err != nil {
			return err
		}
		result := tx.Model(&User{}).Where("id = ? AND deleted_at IS NULL", userID).
			Updates(map[string]any{"email_verified_at": gorm.Expr("now()"), "updated_at": gorm.Expr("now()")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVerificationInvalid
		}
		return nil
	})
}

func (r *Repository) claimVerificationTokenTx(tx *gorm.DB, tokenID, userID uuid.UUID, purpose string) error {
	var token VerificationToken
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", tokenID).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVerificationInvalid
		}
		return err
	}
	if token.UserID != userID || token.Purpose != purpose || token.UsedAt != nil || !time.Now().Before(token.ExpiresAt) {
		return ErrVerificationInvalid
	}
	result := tx.Model(&VerificationToken{}).Where("id = ? AND used_at IS NULL", tokenID).
		Update("used_at", gorm.Expr("now()"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVerificationInvalid
	}
	return nil
}

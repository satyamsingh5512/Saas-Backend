package preferences

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

// ErrNotFound indicates the user has no stored preferences yet.
var ErrNotFound = errors.New("preferences: not found")

// Repository provides tenant-scoped access to preferences and self-service
// profile data.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Find returns the user's stored preferences.
func (r *Repository) Find(ctx context.Context, userID uuid.UUID) (*Preferences, error) {
	var prefs Preferences
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("user_id = ?", userID).First(&prefs).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("preferences: find: %w", err)
	}
	return &prefs, nil
}

// Upsert creates or replaces the user's preference row.
//
// A single ON CONFLICT statement is used rather than a read-then-write, so two
// concurrent saves cannot race into a duplicate-key error on the primary key.
func (r *Repository) Upsert(ctx context.Context, prefs *Preferences) error {
	prefs.UpdatedAt = time.Now()

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"timezone", "locale", "theme", "email_notifications", "updated_at",
			}),
		}).Create(prefs).Error
	})
	if err != nil {
		return fmt.Errorf("preferences: upsert: %w", err)
	}
	return nil
}

// UserRecord is the subset of `users` the profile endpoint needs.
type UserRecord struct {
	ID              uuid.UUID
	Email           string
	FullName        string
	AvatarURL       *string
	Status          string
	PasswordHash    *string
	EmailVerifiedAt *time.Time
	LastLoginAt     *time.Time
	CreatedAt       time.Time
}

// FindUser loads the caller's own user row.
func (r *Repository) FindUser(ctx context.Context, userID uuid.UUID) (*UserRecord, error) {
	var user UserRecord
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("users").
			Select("id, email, full_name, avatar_url, status, password_hash, email_verified_at, last_login_at, created_at").
			Where("id = ? AND deleted_at IS NULL", userID).
			Scan(&user).Error
	})
	if err != nil {
		return nil, fmt.Errorf("preferences: find user: %w", err)
	}
	if user.ID == uuid.Nil {
		return nil, ErrNotFound
	}
	return &user, nil
}

// UpdateProfile applies changes to the caller's own display fields.
//
// The column list is fixed rather than caller-supplied, which is what keeps this
// from becoming a mass-assignment hole: a client cannot use the profile endpoint to
// write `status`, `tenant_id`, or `password_hash`.
func (r *Repository) UpdateProfile(ctx context.Context, userID uuid.UUID, fullName string, avatarURL *string) error {
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Table("users").
			Where("id = ? AND deleted_at IS NULL", userID).
			Updates(map[string]any{
				"full_name":  fullName,
				"avatar_url": avatarURL,
				"updated_at": time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("preferences: update profile: %w", err)
	}
	return nil
}

// UpdatePassword sets a new password hash for the caller.
func (r *Repository) UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string) error {
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Table("users").
			Where("id = ? AND deleted_at IS NULL", userID).
			Updates(map[string]any{"password_hash": passwordHash, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("preferences: update password: %w", err)
	}
	return nil
}

// RevokeAllRefreshTokens invalidates every session for the user.
//
// Called after a password change so that any session established with the old
// password -- potentially an attacker's -- is terminated rather than surviving the
// remediation.
func (r *Repository) RevokeAllRefreshTokens(ctx context.Context, userID uuid.UUID) error {
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("refresh_tokens").
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Update("revoked_at", time.Now()).Error
	})
	if err != nil {
		return fmt.Errorf("preferences: revoke refresh tokens: %w", err)
	}
	return nil
}

// OrganizationSummary loads the caller's tenant summary. `tenants` has no RLS
// policy, so it is filtered by explicit primary key.
func (r *Repository) OrganizationSummary(ctx context.Context, tenantID uuid.UUID) (*Organization, error) {
	var org Organization
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("tenants").
			Select("id, name, slug, plan_code").
			Where("id = ?", tenantID).
			Scan(&org).Error
	})
	if err != nil {
		return nil, fmt.Errorf("preferences: organization summary: %w", err)
	}
	return &org, nil
}

// RoleSlugs returns the caller's role slugs, most powerful first.
func (r *Repository) RoleSlugs(ctx context.Context, userID uuid.UUID) ([]string, error) {
	var slugs []string
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("roles r").
			Joins("JOIN user_roles ur ON ur.role_id = r.id").
			Where("ur.user_id = ?", userID).
			Order("r.rank ASC").
			Pluck("r.slug", &slugs).Error
	})
	if err != nil {
		return nil, fmt.Errorf("preferences: role slugs: %w", err)
	}
	return slugs, nil
}

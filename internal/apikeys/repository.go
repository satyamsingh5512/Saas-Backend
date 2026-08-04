package apikeys

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
)

// ErrNotFound indicates no matching key in the caller's tenant.
var ErrNotFound = errors.New("apikeys: key not found")
var ErrCredentialInactive = errors.New("apikeys: credential subject inactive")

// Repository provides data access for API keys.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a new API key.
func (r *Repository) Create(ctx context.Context, key *Key) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Create(key).Error
	})
}

// List returns the tenant's API keys, newest first. The key hash is excluded from
// the model's JSON, so listing is safe to expose to any holder of apikey:view.
func (r *Repository) List(ctx context.Context, limit, offset int) ([]Key, int64, error) {
	var (
		keys  []Key
		total int64
	)

	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Model(&Key{}).Count(&total).Error; err != nil {
			return err
		}
		return tx.Order("created_at DESC").Limit(limit).Offset(offset).Find(&keys).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("apikeys: list: %w", err)
	}
	return keys, total, nil
}

// FindByID returns one key within the caller's tenant.
func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*Key, error) {
	var key Key
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("id = ?", id).First(&key).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("apikeys: find by id: %w", err)
	}
	return &key, nil
}

// Revoke marks a key revoked. Revocation is a timestamp rather than a row delete
// so the key remains visible in the tenant's history and in audit records.
func (r *Repository) Revoke(ctx context.Context, id uuid.UUID) error {
	return txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		result := tx.Model(&Key{}).
			Where("id = ? AND revoked_at IS NULL", id).
			Update("revoked_at", time.Now())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// FindByHash resolves a key from its hash with no tenant context, via the
// find_api_key_by_hash SECURITY DEFINER function (migrations/000012).
//
// A machine client presents only the key, and the tenant is an attribute of the
// matched row, so the tenant cannot be established before this lookup. The
// function limits the elevated privilege to one indexed equality match against a
// 256-bit secret.
func (r *Repository) FindByHash(ctx context.Context, keyHash string) (*Key, error) {
	var keys []Key
	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw("SELECT * FROM find_api_key_by_hash(?)", keyHash).Scan(&keys).Error
	})
	if err != nil {
		return nil, fmt.Errorf("apikeys: find by hash: %w", err)
	}
	if len(keys) == 0 {
		return nil, ErrNotFound
	}
	return &keys[0], nil
}

// TouchLastUsed records that a key was just used.
//
// Failures are the caller's to ignore: this is telemetry that helps operators find
// unused keys to retire, and a write error must never turn a valid authenticated
// request into a failed one.
func (r *Repository) TouchLastUsed(ctx context.Context, tenantID, keyID uuid.UUID) error {
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		return tx.Model(&Key{}).Where("id = ?", keyID).Update("last_used_at", time.Now()).Error
	})
}

// ValidateCredentialState authoritatively checks the tenant and, when present,
// the key's owning user after the key itself has established tenant identity.
func (r *Repository) ValidateCredentialState(ctx context.Context, tenantID uuid.UUID, userID *uuid.UUID) error {
	return txscope.WithTenantTxID(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		var tenant struct {
			Status    string
			DeletedAt *time.Time
		}
		if err := tx.Table("tenants").Select("status, deleted_at").Where("id = ?", tenantID).Scan(&tenant).Error; err != nil {
			return err
		}
		if tenant.Status != "active" || tenant.DeletedAt != nil {
			return ErrCredentialInactive
		}
		if userID == nil {
			return nil
		}
		var count int64
		if err := tx.Table("users").Where("id = ? AND deleted_at IS NULL AND status <> ?", *userID, "disabled").Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrCredentialInactive
		}
		return nil
	})
}

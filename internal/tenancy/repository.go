package tenancy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrTenantNotFound is returned when a tenant lookup (by ID or slug) yields
// no live (non-deleted) row.
var ErrTenantNotFound = errors.New("tenancy: tenant not found")

// Repository provides data access for the tenants table. Unlike every other
// repository in this codebase, Repository does NOT go through
// pkg/txscope.WithTenantTx, because tenants has no tenant_id column and no
// RLS policy -- it IS the tenant table. All other repositories reading
// tenant-scoped data must use txscope.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// FindBySlug resolves a tenant by its public slug (subdomain). Used by the
// tenant-resolution middleware before authentication, so this is a plain
// (non-transactional, non-tenant-scoped) read.
func (r *Repository) FindBySlug(ctx context.Context, slug string) (*Tenant, error) {
	var tenant Tenant
	err := r.db.WithContext(ctx).
		Where("slug = ? AND deleted_at IS NULL", slug).
		First(&tenant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tenancy: find by slug: %w", err)
	}
	return &tenant, nil
}

// FindByID resolves a tenant by primary key.
func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	var tenant Tenant
	err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&tenant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tenancy: find by id: %w", err)
	}
	return &tenant, nil
}

// Create inserts a new tenant row. The seed_default_roles trigger
// (migrations/000008) fires automatically on insert, provisioning the five
// system roles for this tenant.
func (r *Repository) Create(ctx context.Context, tenant *Tenant) error {
	if err := r.db.WithContext(ctx).Create(tenant).Error; err != nil {
		return fmt.Errorf("tenancy: create: %w", err)
	}
	return nil
}

// CreateTx is the transactional variant of Create, used when tenant creation
// must be atomic with the first user's creation (see identity.Service.Register).
func (r *Repository) CreateTx(tx *gorm.DB, tenant *Tenant) error {
	if err := tx.Create(tenant).Error; err != nil {
		return fmt.Errorf("tenancy: create tx: %w", err)
	}
	return nil
}

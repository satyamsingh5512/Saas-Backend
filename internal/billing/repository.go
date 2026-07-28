package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
)

var (
	// ErrPlanNotFound indicates an unknown or inactive plan code.
	ErrPlanNotFound = errors.New("billing: plan not found")
	// ErrSubscriptionNotFound indicates the tenant has no subscription row yet,
	// which the service layer treats as "implicitly on the free plan" rather than
	// as an error surfaced to clients.
	ErrSubscriptionNotFound = errors.New("billing: subscription not found")
)

// Repository provides access to the global plan catalog and tenant subscriptions.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// ListPlans returns the active plan catalog, cheapest first.
//
// subscription_plans is platform metadata with no RLS policy, so this
// deliberately runs outside tenant scope: the catalog is identical for every
// tenant, and requiring a tenant context would break the pre-signup pricing view.
func (r *Repository) ListPlans(ctx context.Context) ([]Plan, error) {
	var plans []Plan
	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Where("is_active = ?", true).Order("price_cents ASC").Find(&plans).Error
	})
	if err != nil {
		return nil, fmt.Errorf("billing: list plans: %w", err)
	}
	return plans, nil
}

// FindPlanByCode looks up a single active plan by its stable code.
func (r *Repository) FindPlanByCode(ctx context.Context, code string) (*Plan, error) {
	var plan Plan
	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("code = ? AND is_active = ?", code, true).First(&plan).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrPlanNotFound
		}
		return e
	})
	if err != nil {
		if errors.Is(err, ErrPlanNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("billing: find plan by code: %w", err)
	}
	return &plan, nil
}

// FindPlanByID looks up a plan by primary key, used to resolve a subscription's
// plan for display.
func (r *Repository) FindPlanByID(ctx context.Context, id uuid.UUID) (*Plan, error) {
	var plan Plan
	err := txscope.WithoutTenantScope(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.Where("id = ?", id).First(&plan).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrPlanNotFound
		}
		return e
	})
	if err != nil {
		if errors.Is(err, ErrPlanNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("billing: find plan by id: %w", err)
	}
	return &plan, nil
}

// FindSubscription returns the caller tenant's subscription.
func (r *Repository) FindSubscription(ctx context.Context) (*Subscription, error) {
	var sub Subscription
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		e := tx.First(&sub).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrSubscriptionNotFound
		}
		return e
	})
	if err != nil {
		if errors.Is(err, ErrSubscriptionNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("billing: find subscription: %w", err)
	}
	return &sub, nil
}

// UpsertSubscription creates or updates the tenant's single subscription row and
// keeps tenants.plan_code in step with it, inside one transaction.
//
// The denormalized tenants.plan_code exists so the tenant-resolution middleware
// can expose a plan on every request without joining subscriptions. Writing both
// in one transaction is what prevents the two from disagreeing -- a split write
// could leave a tenant paying for Pro while every request still evaluates Free
// entitlements.
func (r *Repository) UpsertSubscription(ctx context.Context, sub *Subscription, planCode string) error {
	err := txscope.WithTenantTxID(ctx, r.db, sub.TenantID, func(tx *gorm.DB) error {
		var existing Subscription
		findErr := tx.Where("tenant_id = ?", sub.TenantID).First(&existing).Error

		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			if err := tx.Create(sub).Error; err != nil {
				return err
			}
		case findErr != nil:
			return findErr
		default:
			sub.ID = existing.ID
			sub.CreatedAt = existing.CreatedAt
			if err := tx.Model(&Subscription{}).
				Where("id = ?", existing.ID).
				Updates(map[string]any{
					"plan_id":                  sub.PlanID,
					"status":                   sub.Status,
					"provider_subscription_id": sub.ProviderSubscriptionID,
					"current_period_start":     sub.CurrentPeriodStart,
					"current_period_end":       sub.CurrentPeriodEnd,
					"cancel_at_period_end":     sub.CancelAtPeriodEnd,
					"canceled_at":              sub.CanceledAt,
					"updated_at":               time.Now(),
				}).Error; err != nil {
				return err
			}
		}

		// tenants has no RLS policy of its own, so this update is scoped by an
		// explicit WHERE on the primary key.
		return tx.Table("tenants").
			Where("id = ?", sub.TenantID).
			Updates(map[string]any{"plan_code": planCode, "updated_at": time.Now()}).Error
	})
	if err != nil {
		return fmt.Errorf("billing: upsert subscription: %w", err)
	}
	return nil
}

// CountSeats returns the number of active (non-deleted) users in the tenant,
// which is the unit plans are priced by.
func (r *Repository) CountSeats(ctx context.Context) (int64, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("users").Where("deleted_at IS NULL AND status <> ?", "disabled").Count(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("billing: count seats: %w", err)
	}
	return count, nil
}

// CountProjects returns the tenant's live project count.
func (r *Repository) CountProjects(ctx context.Context) (int64, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("projects").Where("deleted_at IS NULL").Count(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("billing: count projects: %w", err)
	}
	return count, nil
}

// CountTeams returns the tenant's live team count.
func (r *Repository) CountTeams(ctx context.Context) (int64, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("teams").Where("deleted_at IS NULL").Count(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("billing: count teams: %w", err)
	}
	return count, nil
}

// CountPendingInvitations counts unexpired pending invites, which consume a seat
// prospectively so a tenant cannot oversubscribe by inviting past its limit.
func (r *Repository) CountPendingInvitations(ctx context.Context) (int64, error) {
	var count int64
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Table("invitations").
			Where("status = ? AND expires_at > ?", "pending", time.Now()).
			Count(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("billing: count pending invitations: %w", err)
	}
	return count, nil
}

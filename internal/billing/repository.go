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

// UsageCounts holds every per-tenant total the billing views and quota checks
// need.
type UsageCounts struct {
	Seats              int64
	Projects           int64
	Teams              int64
	PendingInvitations int64
}

// CountUsage gathers all four tenant totals in a single round trip.
//
// These were four separate methods, each opening its own transaction. Because
// txscope has to BEGIN, set app.tenant_id, query, then COMMIT, every one of them
// cost four round trips -- sixteen in total to produce four integers. On a
// database a few milliseconds away that is invisible; against a cross-region
// instance at ~240ms RTT it was roughly four seconds of a single request, and
// GET /billing/usage was observed taking twelve.
//
// Scalar subqueries keep RLS intact: the policy is evaluated per referenced
// table exactly as it would be in a standalone query, so this stays scoped to
// the tenant set on the surrounding transaction. Counting pending invitations
// here too costs nothing measurable and means one code path serves both the
// usage view and the seat-quota check.
func (r *Repository) CountUsage(ctx context.Context, now time.Time) (*UsageCounts, error) {
	var counts UsageCounts
	err := txscope.WithTenantTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT
			    (SELECT count(*) FROM users
			      WHERE deleted_at IS NULL AND status <> 'disabled')     AS seats,
			    (SELECT count(*) FROM projects WHERE deleted_at IS NULL) AS projects,
			    (SELECT count(*) FROM teams    WHERE deleted_at IS NULL) AS teams,
			    (SELECT count(*) FROM invitations
			      WHERE status = 'pending' AND expires_at > ?)           AS pending_invitations
		`, now).Scan(&counts).Error
	})
	if err != nil {
		return nil, fmt.Errorf("billing: count usage: %w", err)
	}
	return &counts, nil
}

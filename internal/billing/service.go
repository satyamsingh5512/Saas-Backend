package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Recorder is the slice of internal/audit this module needs. Plan changes are
// security- and money-relevant, so they are always audited.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// Service implements subscription reads, plan changes, and quota enforcement.
type Service struct {
	repo  *Repository
	audit Recorder
}

func NewService(repo *Repository, recorder Recorder) *Service {
	return &Service{repo: repo, audit: recorder}
}

// ListPlans returns the public plan catalog.
func (s *Service) ListPlans(ctx context.Context) ([]Plan, error) {
	plans, err := s.repo.ListPlans(ctx)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to list plans", err)
	}
	return plans, nil
}

// GetSubscription returns the tenant's subscription joined with its plan.
//
// A tenant with no subscription row is reported as being on the free plan with a
// nil Subscription, rather than as a 404. Registration intentionally does not
// create a subscription row, so "no row" is the normal state for the majority of
// tenants and must not read as an error.
func (s *Service) GetSubscription(ctx context.Context) (*SubscriptionView, error) {
	sub, err := s.repo.FindSubscription(ctx)
	if err != nil {
		if errors.Is(err, ErrSubscriptionNotFound) {
			plan, planErr := s.repo.FindPlanByCode(ctx, PlanFree)
			if planErr != nil {
				return nil, apperror.Wrap(apperror.CodeInternal, "failed to load default plan", planErr)
			}
			return &SubscriptionView{Subscription: nil, Plan: *plan}, nil
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load subscription", err)
	}

	plan, err := s.repo.FindPlanByID(ctx, sub.PlanID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load plan", err)
	}
	return &SubscriptionView{Subscription: sub, Plan: *plan}, nil
}

// ChangePlan moves the tenant onto a different plan.
//
// A downgrade is rejected when current usage already exceeds the target plan's
// limits. Silently accepting it would leave the tenant persistently over quota
// with no legal action available to fix it, so the caller is told what to reduce
// first.
func (s *Service) ChangePlan(ctx context.Context, entry audit.Entry, tenantID uuid.UUID, planCode string) (*SubscriptionView, error) {
	plan, err := s.repo.FindPlanByCode(ctx, planCode)
	if err != nil {
		if errors.Is(err, ErrPlanNotFound) {
			return nil, apperror.New(apperror.CodeValidation, "unknown or inactive plan")
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load plan", err)
	}

	usage, err := s.currentUsage(ctx, planCode, plan)
	if err != nil {
		return nil, err
	}
	if plan.MaxSeats != nil && usage.Seats > int64(*plan.MaxSeats) {
		return nil, apperror.New(apperror.CodeUnprocessable, fmt.Sprintf(
			"cannot switch to %s: %d seats in use exceeds the plan limit of %d; remove members first",
			plan.Name, usage.Seats, *plan.MaxSeats))
	}
	if plan.MaxProjects != nil && usage.Projects > int64(*plan.MaxProjects) {
		return nil, apperror.New(apperror.CodeUnprocessable, fmt.Sprintf(
			"cannot switch to %s: %d projects exceeds the plan limit of %d; archive or delete projects first",
			plan.Name, usage.Projects, *plan.MaxProjects))
	}

	now := time.Now()
	periodEnd := now.AddDate(0, 1, 0)
	if plan.BillingPeriod == "yearly" {
		periodEnd = now.AddDate(1, 0, 0)
	}

	sub := &Subscription{
		ID:                 uuid.New(),
		TenantID:           tenantID,
		PlanID:             plan.ID,
		Status:             StatusActive,
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   periodEnd,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.repo.UpsertSubscription(ctx, sub, plan.Code); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to change plan", err)
	}

	if s.audit != nil {
		entry.Action = audit.ActionSubscriptionChange
		entry.TargetType = audit.TargetOrganization
		entry.TargetID = &tenantID
		entry.Metadata = map[string]any{"plan_code": plan.Code, "plan_name": plan.Name}
		s.audit.Record(ctx, entry)
	}

	return &SubscriptionView{Subscription: sub, Plan: *plan}, nil
}

// Cancel schedules the subscription to lapse at the end of the paid period rather
// than terminating access immediately, which is what the customer has already
// paid for.
func (s *Service) Cancel(ctx context.Context, entry audit.Entry, tenantID uuid.UUID) (*SubscriptionView, error) {
	sub, err := s.repo.FindSubscription(ctx)
	if err != nil {
		if errors.Is(err, ErrSubscriptionNotFound) {
			return nil, apperror.New(apperror.CodeNotFound, "no active paid subscription to cancel")
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load subscription", err)
	}

	now := time.Now()
	sub.CancelAtPeriodEnd = true
	sub.CanceledAt = &now
	sub.UpdatedAt = now

	plan, err := s.repo.FindPlanByID(ctx, sub.PlanID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load plan", err)
	}

	if err := s.repo.UpsertSubscription(ctx, sub, plan.Code); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to cancel subscription", err)
	}

	if s.audit != nil {
		entry.Action = audit.ActionSubscriptionChange
		entry.TargetType = audit.TargetOrganization
		entry.TargetID = &tenantID
		entry.Metadata = map[string]any{"canceled": true, "effective": sub.CurrentPeriodEnd}
		s.audit.Record(ctx, entry)
	}

	return &SubscriptionView{Subscription: sub, Plan: *plan}, nil
}

// GetUsage reports consumption against the active plan's limits.
func (s *Service) GetUsage(ctx context.Context) (*Usage, error) {
	view, err := s.GetSubscription(ctx)
	if err != nil {
		return nil, err
	}
	return s.currentUsage(ctx, view.Plan.Code, &view.Plan)
}

// CheckProjectQuota satisfies the projects module's QuotaChecker. currentCount is
// supplied by the caller, which already counted its own rows, avoiding a
// redundant query.
func (s *Service) CheckProjectQuota(ctx context.Context, tenantID uuid.UUID, currentCount int64) error {
	plan, err := s.activePlan(ctx)
	if err != nil {
		return err
	}
	if plan.MaxProjects == nil {
		return nil // unlimited
	}
	if currentCount >= int64(*plan.MaxProjects) {
		return apperror.New(apperror.CodeForbidden, fmt.Sprintf(
			"the %s plan allows %d projects; upgrade to add more",
			plan.Name, *plan.MaxProjects))
	}
	return nil
}

// CheckSeatQuota enforces the plan seat limit before a new member is invited.
// Pending invitations count toward the limit, so a tenant cannot exceed its plan
// by issuing more invites than it has seats and letting them all be accepted.
func (s *Service) CheckSeatQuota(ctx context.Context, tenantID uuid.UUID) error {
	plan, err := s.activePlan(ctx)
	if err != nil {
		return err
	}
	if plan.MaxSeats == nil {
		return nil // unlimited
	}

	seats, err := s.repo.CountSeats(ctx)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to count seats", err)
	}
	pending, err := s.repo.CountPendingInvitations(ctx)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to count pending invitations", err)
	}

	if seats+pending >= int64(*plan.MaxSeats) {
		return apperror.New(apperror.CodeForbidden, fmt.Sprintf(
			"the %s plan allows %d seats (%d used, %d pending invites); upgrade to invite more",
			plan.Name, *plan.MaxSeats, seats, pending))
	}
	return nil
}

// --- internal helpers ---

// activePlan resolves the tenant's effective plan, defaulting to free.
func (s *Service) activePlan(ctx context.Context) (*Plan, error) {
	sub, err := s.repo.FindSubscription(ctx)
	if err != nil {
		if errors.Is(err, ErrSubscriptionNotFound) {
			plan, planErr := s.repo.FindPlanByCode(ctx, PlanFree)
			if planErr != nil {
				return nil, apperror.Wrap(apperror.CodeInternal, "failed to load default plan", planErr)
			}
			return plan, nil
		}
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load subscription", err)
	}

	plan, err := s.repo.FindPlanByID(ctx, sub.PlanID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load plan", err)
	}
	return plan, nil
}

func (s *Service) currentUsage(ctx context.Context, planCode string, plan *Plan) (*Usage, error) {
	seats, err := s.repo.CountSeats(ctx)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to count seats", err)
	}
	projectCount, err := s.repo.CountProjects(ctx)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to count projects", err)
	}
	teamCount, err := s.repo.CountTeams(ctx)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to count teams", err)
	}

	return &Usage{
		PlanCode:    planCode,
		Seats:       seats,
		MaxSeats:    plan.MaxSeats,
		Projects:    projectCount,
		MaxProjects: plan.MaxProjects,
		Teams:       teamCount,
	}, nil
}

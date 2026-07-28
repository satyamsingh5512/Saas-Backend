// Package billing implements the subscription plan catalog, each tenant's
// subscription, and the plan quota checks other modules enforce against.
//
// Payment processing is deliberately out of scope: Subscription carries an opaque
// ProviderSubscriptionID for a processor such as Stripe, but this package never
// handles card data. Keeping the application entirely outside the cardholder data
// environment is what makes PCI scope tractable -- the processor's hosted checkout
// owns those fields, and only the resulting subscription identifier is stored here.
package billing

import (
	"time"

	"github.com/google/uuid"
)

// Subscription status values permitted by the schema's CHECK constraint.
const (
	StatusTrialing = "trialing"
	StatusActive   = "active"
	StatusPastDue  = "past_due"
	StatusCanceled = "canceled"
)

// PlanFree is the plan every tenant starts on, and the fallback used for quota
// evaluation when a tenant has no explicit subscription row yet.
const PlanFree = "free"

// Plan mirrors the `subscription_plans` table: a global, platform-defined
// catalog, not tenant-scoped and not RLS-protected.
//
// The Max* fields are nullable in the schema, where NULL means unlimited. They
// are modeled as *int rather than int so "unlimited" stays distinguishable from
// a genuine limit of zero, which would otherwise silently block all usage.
type Plan struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	Code          string         `json:"code"`
	Name          string         `json:"name"`
	PriceCents    int            `gorm:"column:price_cents" json:"price_cents"`
	BillingPeriod string         `gorm:"column:billing_period" json:"billing_period"`
	MaxSeats      *int           `gorm:"column:max_seats" json:"max_seats"`
	MaxProjects   *int           `gorm:"column:max_projects" json:"max_projects"`
	MaxStorageMB  *int           `gorm:"column:max_storage_mb" json:"max_storage_mb"`
	Features      map[string]any `gorm:"serializer:json" json:"features"`
	IsActive      bool           `gorm:"column:is_active" json:"is_active"`
	CreatedAt     time.Time      `json:"created_at"`
}

func (Plan) TableName() string { return "subscription_plans" }

// Subscription mirrors the `subscriptions` table. One row per tenant, enforced by
// a unique index on tenant_id.
type Subscription struct {
	ID                     uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID               uuid.UUID  `gorm:"column:tenant_id" json:"tenant_id"`
	PlanID                 uuid.UUID  `gorm:"column:plan_id" json:"plan_id"`
	Status                 string     `json:"status"`
	ProviderSubscriptionID *string    `gorm:"column:provider_subscription_id" json:"provider_subscription_id,omitempty"`
	CurrentPeriodStart     time.Time  `gorm:"column:current_period_start" json:"current_period_start"`
	CurrentPeriodEnd       time.Time  `gorm:"column:current_period_end" json:"current_period_end"`
	CancelAtPeriodEnd      bool       `gorm:"column:cancel_at_period_end" json:"cancel_at_period_end"`
	CanceledAt             *time.Time `gorm:"column:canceled_at" json:"canceled_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

func (Subscription) TableName() string { return "subscriptions" }

// SubscriptionView is the client-facing subscription representation: the
// subscription joined with its plan, so a UI can render entitlements without a
// second request.
type SubscriptionView struct {
	Subscription *Subscription `json:"subscription"`
	Plan         Plan          `json:"plan"`
}

// Usage reports current consumption against the active plan's limits, which is
// what a billing screen needs to render "3 of 5 seats used".
type Usage struct {
	PlanCode    string `json:"plan_code"`
	Seats       int64  `json:"seats"`
	MaxSeats    *int   `json:"max_seats"`
	Projects    int64  `json:"projects"`
	MaxProjects *int   `json:"max_projects"`
	Teams       int64  `json:"teams"`
}

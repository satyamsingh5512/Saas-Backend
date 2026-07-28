// Package txscope provides the single chokepoint through which every
// tenant-scoped database operation must pass. It sets the Postgres session
// variable app.tenant_id inside a transaction before running any query,
// which activates the Row-Level Security policies defined in the migrations
// (see migrations/000002_tenants_and_users.up.sql for the policy pattern).
//
// This exists so that "forgetting to filter by tenant_id" in a repository
// method is a defense-in-depth non-issue: even if application code omits a
// WHERE tenant_id = ? clause, Postgres itself will not return or accept rows
// for any tenant other than the one set on the current transaction.
package txscope

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrNoTenantInContext is returned when a tenant-scoped operation is
// attempted without a tenant ID available in the context.
var ErrNoTenantInContext = errors.New("txscope: no tenant id in context")

type ctxKey string

const tenantIDKey ctxKey = "tenant_id"

// WithTenantID returns a new context carrying the given tenant ID for later
// retrieval by WithTenantTx. Set once by tenant-resolution middleware
// (internal/tenancy) per request.
func WithTenantID(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantIDKey, tenantID)
}

// TenantIDFromContext extracts the tenant ID set by WithTenantID.
func TenantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(tenantIDKey).(uuid.UUID)
	return v, ok
}

// WithTenantTx runs fn inside a database transaction that has the
// Postgres session variable app.tenant_id set to the tenant ID found in ctx.
// Every repository method in this codebase MUST go through this helper (or
// WithTenantTxID below) rather than calling db.WithContext(ctx) directly, so
// that RLS policies are always active.
//
// SET LOCAL is used (not SET) so the setting is automatically reset at the
// end of the transaction regardless of commit/rollback/panic, preventing
// tenant-id leakage across pooled connections.
func WithTenantTx(ctx context.Context, database *gorm.DB, fn func(tx *gorm.DB) error) error {
	tenantID, ok := TenantIDFromContext(ctx)
	if !ok {
		return ErrNoTenantInContext
	}
	return WithTenantTxID(ctx, database, tenantID, fn)
}

// WithTenantTxID is the explicit-tenant-ID variant of WithTenantTx, used by
// code paths that have a tenant ID from a source other than request context
// (e.g. a Kafka consumer processing an event for a specific tenant, or an
// invitation-accept flow that resolves the tenant from the invite token
// before any auth context exists).
func WithTenantTxID(ctx context.Context, database *gorm.DB, tenantID uuid.UUID, fn func(tx *gorm.DB) error) error {
	return database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Parameterized via Exec's placeholder to avoid any injection risk,
		// even though tenantID is a uuid.UUID (not attacker-controlled
		// string) by the time it reaches here.
		if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID.String()).Error; err != nil {
			return fmt.Errorf("txscope: set tenant session var: %w", err)
		}
		return fn(tx)
	})
}

// WithoutTenantScope runs fn in a transaction with NO app.tenant_id set,
// meaning RLS policies will hide all rows on every tenant-scoped table
// (fail-closed, per the migrations' `current_setting(..., true)` default).
// This is intentionally awkward to call and must only be used for genuinely
// cross-tenant operations: platform-operator admin tooling, and the global
// catalog tables (tenants, permissions, subscription_plans) which have no
// RLS policy at all. Every call site using this function must be
// individually justified in code review.
func WithoutTenantScope(ctx context.Context, database *gorm.DB, fn func(tx *gorm.DB) error) error {
	return database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})
}

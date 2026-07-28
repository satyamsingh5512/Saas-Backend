package tenancy

import (
	"context"

	"github.com/google/uuid"
)

// ctxKey namespaces context values to this package to avoid collisions with
// other packages' context keys.
type ctxKey string

const tenantCtxKey ctxKey = "tenancy.tenant"

// Context carries the resolved tenant information for the lifetime of a
// request. Attached by Middleware, read by handlers/services via FromContext.
type Context struct {
	TenantID uuid.UUID
	Slug     string
	PlanCode string
	Status   string
}

// WithContext attaches tenant info to ctx.
func WithContext(ctx context.Context, tc Context) context.Context {
	return context.WithValue(ctx, tenantCtxKey, tc)
}

// FromContext retrieves tenant info previously attached by WithContext.
func FromContext(ctx context.Context) (Context, bool) {
	tc, ok := ctx.Value(tenantCtxKey).(Context)
	return tc, ok
}

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
// request. Attached by Middleware (and the Gin bridge FromGinContext), read
// by the credential-override path in this package.
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

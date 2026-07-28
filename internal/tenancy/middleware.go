package tenancy

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
)

// Resolver resolves a tenant from a request's subdomain or X-Tenant-ID
// header BEFORE authentication has happened, per the architecture design
// (Phase 2.2): the header/subdomain is only used to route to the right
// tenant's public info/login page pre-auth. Once a request is authenticated,
// the JWT's tenant_id claim is the sole source of truth (see the auth
// middleware, which overwrites the context's tenant with the JWT's claim
// and 403s on mismatch, rather than trusting the header past that point).
type Resolver struct {
	repo  *Repository
	cache CacheReader
}

// CacheReader is satisfied by the Redis-backed tenant metadata cache
// (internal/platform/cache, added in the Redis integration phase). Kept as a
// minimal interface here so this package has no Redis dependency; a
// nil-cache Resolver simply always queries Postgres, which is correct (if
// slower) and lets this package be usable before Redis is wired up.
type CacheReader interface {
	GetTenantBySlug(ctx context.Context, slug string) (*Tenant, bool)
	SetTenantBySlug(ctx context.Context, slug string, tenant *Tenant, ttl time.Duration)
}

func NewResolver(repo *Repository, cache CacheReader) *Resolver {
	return &Resolver{repo: repo, cache: cache}
}

const tenantCacheTTL = 5 * time.Minute

// Resolve looks up a tenant by slug, checking the cache first when available.
func (r *Resolver) Resolve(ctx context.Context, slug string) (*Tenant, error) {
	if r.cache != nil {
		if t, ok := r.cache.GetTenantBySlug(ctx, slug); ok {
			return t, nil
		}
	}

	tenant, err := r.repo.FindBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		r.cache.SetTenantBySlug(ctx, slug, tenant, tenantCacheTTL)
	}
	return tenant, nil
}

// slugFromRequest extracts a tenant slug from either the X-Tenant-ID header
// (API clients) or the leftmost subdomain label (browser clients hitting
// acme.ourapp.com). Header takes precedence since API clients rarely set a
// meaningful Host subdomain.
func slugFromRequest(c *gin.Context) string {
	if header := strings.TrimSpace(c.GetHeader("X-Tenant-ID")); header != "" {
		return header
	}

	host := c.Request.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	labels := strings.Split(host, ".")
	if len(labels) > 2 { // e.g. acme.ourapp.com -> "acme"
		return labels[0]
	}
	return ""
}

func splitHostPort(host string) (string, string, error) {
	if i := strings.LastIndex(host, ":"); i != -1 {
		return host[:i], host[i+1:], nil
	}
	return host, "", nil
}

// Middleware resolves the tenant for every incoming request and attaches it
// to the Gin/request context as tenancy.Context, and to the standard
// context via txscope.WithTenantID so repositories can use it immediately.
// It does NOT perform authentication -- for protected routes, the identity
// auth middleware runs afterward and re-derives/validates the tenant from
// the JWT claim, overriding what this middleware resolved if they disagree.
//
// Public routes (register, login, invite-accept, OAuth callback) rely solely
// on this middleware's resolution since there is no JWT yet.
func (r *Resolver) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := slugFromRequest(c)
		if slug == "" {
			// No tenant hint on the request at all. Some routes (health
			// checks, OAuth provider callbacks that embed tenant in state)
			// legitimately have none; downstream handlers that require a
			// tenant will error explicitly rather than this middleware
			// guessing incorrectly.
			c.Next()
			return
		}

		tenant, err := r.Resolve(c.Request.Context(), slug)
		if err != nil {
			apiresponse.Error(c, apperror.CodeNotFound.HTTPStatus(), string(apperror.CodeNotFound), "tenant not found")
			return
		}

		if tenant.Status == StatusSuspended {
			apiresponse.Error(c, http.StatusForbidden, string(apperror.CodeForbidden), "organization is suspended")
			return
		}

		tc := Context{TenantID: tenant.ID, Slug: tenant.Slug, PlanCode: tenant.PlanCode, Status: tenant.Status}
		ctx := WithContext(c.Request.Context(), tc)
		ctx = txscope.WithTenantID(ctx, tenant.ID)
		c.Request = c.Request.WithContext(ctx)
		c.Set(string(tenantCtxKey), tc)
		c.Next()
	}
}

// FromGinContext is a convenience accessor for handlers that have a
// *gin.Context rather than a context.Context.
func FromGinContext(c *gin.Context) (Context, bool) {
	v, ok := c.Get(string(tenantCtxKey))
	if !ok {
		return Context{}, false
	}
	tc, ok := v.(Context)
	return tc, ok
}

// OverrideFromJWT is called by the identity auth middleware once a JWT has
// been validated. It replaces whatever tenant the pre-auth resolver found
// with the tenant ID embedded in the token, and returns an error if the
// pre-auth resolution (if any occurred) disagreed with the token -- the
// cross-tenant-replay defense described in the architecture doc.
func OverrideFromJWT(c *gin.Context, jwtTenantID uuid.UUID) error {
	return OverrideFromCredential(c, jwtTenantID)
}

// OverrideFromCredential establishes the tenant from an authenticated
// credential, which is authoritative over anything the pre-auth resolver
// inferred from a header or subdomain.
//
// Shared by both authentication paths (JWT and API key) so the cross-tenant
// replay defense is implemented once: a credential valid for tenant A must not
// be usable to read tenant B by pairing it with B's X-Tenant-ID header or
// subdomain, regardless of which credential type was presented.
func OverrideFromCredential(c *gin.Context, credentialTenantID uuid.UUID) error {
	if existing, ok := FromGinContext(c); ok && existing.TenantID != uuid.Nil && existing.TenantID != credentialTenantID {
		return apperror.New(apperror.CodeTenantMismatch, "token tenant does not match resolved tenant")
	}

	tc := Context{TenantID: credentialTenantID}
	if existing, ok := FromGinContext(c); ok {
		tc.Slug = existing.Slug
		tc.PlanCode = existing.PlanCode
		tc.Status = existing.Status
	}

	ctx := WithContext(c.Request.Context(), tc)
	ctx = txscope.WithTenantID(ctx, credentialTenantID)
	c.Request = c.Request.WithContext(ctx)
	c.Set(string(tenantCtxKey), tc)
	return nil
}

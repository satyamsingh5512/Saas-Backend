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

	// baseDomain is the apex under which tenant subdomains live, e.g.
	// "ourapp.com" so that "acme.ourapp.com" resolves tenant "acme". Empty
	// disables subdomain inference entirely, leaving X-Tenant-ID as the only
	// pre-auth hint.
	//
	// This must be configured rather than guessed. A "more than two labels
	// means the first one is a tenant" heuristic breaks on every shared
	// hosting domain -- on foo.onrender.com or foo.vercel.app it reads the
	// service name as a tenant slug, fails to resolve it, and rejects every
	// request to the deployment.
	baseDomain string
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

// NewResolver builds the pre-auth tenant resolver. baseDomain enables
// subdomain-based tenant routing; pass "" to rely solely on X-Tenant-ID, which
// is the correct setting on any host whose subdomain is not a tenant name.
func NewResolver(repo *Repository, cache CacheReader, baseDomain string) *Resolver {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(baseDomain), "."))
	return &Resolver{repo: repo, cache: cache, baseDomain: normalized}
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

// reservedSubdomains are labels that are never tenant slugs, even directly
// under the configured base domain, so that conventional infrastructure
// hostnames cannot be shadowed by (or mistaken for) a tenant.
var reservedSubdomains = map[string]struct{}{
	"www": {}, "api": {}, "app": {}, "admin": {}, "static": {},
	"assets": {}, "cdn": {}, "mail": {}, "status": {},
}

// slugFromRequest extracts a tenant slug from either the X-Tenant-ID header
// (API clients) or the leftmost subdomain label of a host under the configured
// base domain (browser clients hitting acme.ourapp.com). Header takes
// precedence since API clients rarely set a meaningful Host subdomain.
//
// fromHeader reports which source produced the slug. The two are not equally
// authoritative: a header is the client explicitly asserting a tenant, whereas
// a hostname label is an inference the caller may know nothing about.
func (r *Resolver) slugFromRequest(c *gin.Context) (slug string, fromHeader bool) {
	if header := strings.TrimSpace(c.GetHeader("X-Tenant-ID")); header != "" {
		return header, true
	}

	if r.baseDomain == "" {
		return "", false
	}

	host := c.Request.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "."))

	suffix := "." + r.baseDomain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}

	label := strings.TrimSuffix(host, suffix)
	// An empty label is the apex itself; a dotted one is deeper nesting than
	// the single-label tenant scheme describes. Neither names a tenant.
	if label == "" || strings.Contains(label, ".") {
		return "", false
	}
	if _, reserved := reservedSubdomains[label]; reserved {
		return "", false
	}
	return label, false
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
		slug, fromHeader := r.slugFromRequest(c)
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
			if !fromHeader {
				// The slug came from the hostname, which this design treats as
				// a routing hint rather than an assertion. An unknown label
				// must fall through as "no tenant" and let authentication
				// establish one, not reject the request -- otherwise a
				// deployment on an unexpected hostname 404s every route.
				c.Next()
				return
			}
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

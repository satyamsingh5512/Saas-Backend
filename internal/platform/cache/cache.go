// Package cache is the Redis-backed implementation of the cache seams the
// domain packages declare: tenancy.CacheReader (tenant metadata) and
// authz.PermissionCache (per-user permission sets).
//
// Both consumers were built cache-optional: a nil cache always queries
// Postgres, which is correct but slower. This package fills those seams when
// REDIS_ADDR is configured, and gets out of the way when it is not.
//
// Failure policy is fail-open everywhere. A cache is a performance
// optimization, never a correctness dependency: a Redis error on read is a
// miss, on write/invalidate it is dropped. Free-tier Redis sleeps and drops
// connections; the application must not notice beyond latency. The only log
// emitted is one line at startup when Redis is unreachable, after which the
// constructor returns nil and the services run exactly as they did before
// Redis existed. Nothing below logs per request.
package cache

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/satym-in/tenant-saas-backend/internal/tenancy"
)

// TTLs bound every staleness window. Tenant metadata and permission sets
// change rarely and are invalidated explicitly on mutation; the TTL is the
// backstop for the paths that cannot invalidate precisely.
const (
	// TenantTTL mirrors tenancy's own tenantCacheTTL: a slug rename or status
	// change takes at most this long to reach pre-auth resolution.
	TenantTTL = 5 * time.Minute
	// PermissionsTTL bounds the one window this package cannot close
	// synchronously: none currently, since every role/assignment mutation
	// invalidates its holders, but a shorter TTL than the tenant one keeps a
	// future missed path from lingering.
	PermissionsTTL = 5 * time.Minute
)

// Key namespaces. The "t:"/"p:" prefixes keep tenant metadata and permission
// sets from colliding with each other or with anything else sharing the
// Redis database (free tiers are often a single shared DB index).
func tenantKey(slug string) string { return "t:slug:" + slug }

func permissionsKey(tenantID, userID uuid.UUID) string {
	return "p:perms:" + tenantID.String() + ":" + userID.String()
}

// Cache is a thin fail-open Redis adapter. The zero value is unusable;
// construct with New, which returns nil when caching is disabled or
// unreachable. All methods are safe on a nil *Cache, so call sites can pass
// the result straight into tenancy.NewResolver and authz.NewService: a nil
// *Cache wrapped in either interface behaves exactly like the historic
// nil cache, because every method degrades to miss/no-op.
type Cache struct {
	client *redis.Client
}

var (
	_ tenancy.CacheReader = (*Cache)(nil)
	// authz.PermissionCache is satisfied structurally (Get/Set/InvalidateUserPermissions)
	// without importing authz, keeping this package a leaf.
)

// New connects to Redis and returns a usable Cache, or nil when caching
// should stay off: empty addr (the default, per config), or unreachable
// Redis. Unreachable is a warning, not a fatal -- the services run
// uncached, which is the same behavior as before this package existed.
func New(addr, password string, db int, logger *slog.Logger) *Cache {
	if addr == "" {
		return nil
	}
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
		// Free tiers idle-timeout aggressively. Short dial timeouts keep a
		// sleeping instance from stalling requests; the pool re-dials on
		// next use and reads fail open to Postgres meanwhile.
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolSize:     10,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		logger.Warn("redis unavailable, running without cache", "addr", addr, "error", err)
		_ = client.Close()
		return nil
	}
	logger.Info("redis cache enabled", "addr", addr)
	return &Cache{client: client}
}

// Close releases the pool. Safe on nil; callers that never enabled caching
// can defer it unconditionally.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.client.Close()
}

// GetTenantBySlug implements tenancy.CacheReader. Any error is a miss.
func (c *Cache) GetTenantBySlug(ctx context.Context, slug string) (*tenancy.Tenant, bool) {
	if c == nil || slug == "" {
		return nil, false
	}
	raw, err := c.client.Get(ctx, tenantKey(slug)).Bytes()
	if err != nil {
		return nil, false
	}
	var tenant tenancy.Tenant
	if err := json.Unmarshal(raw, &tenant); err != nil {
		return nil, false
	}
	return &tenant, true
}

// SetTenantBySlug implements tenancy.CacheReader. Errors are dropped.
func (c *Cache) SetTenantBySlug(ctx context.Context, slug string, tenant *tenancy.Tenant, ttl time.Duration) {
	if c == nil || slug == "" || tenant == nil {
		return
	}
	if ttl <= 0 {
		ttl = TenantTTL
	}
	raw, err := json.Marshal(tenant)
	if err != nil {
		return
	}
	_ = c.client.Set(ctx, tenantKey(slug), raw, ttl).Err()
}

// GetUserPermissions implements authz.PermissionCache. Any error is a miss.
func (c *Cache) GetUserPermissions(ctx context.Context, tenantID, userID uuid.UUID) ([]string, bool) {
	if c == nil {
		return nil, false
	}
	raw, err := c.client.Get(ctx, permissionsKey(tenantID, userID)).Bytes()
	if err != nil {
		return nil, false
	}
	var codes []string
	if err := json.Unmarshal(raw, &codes); err != nil {
		return nil, false
	}
	return codes, true
}

// SetUserPermissions implements authz.PermissionCache. Errors are dropped.
func (c *Cache) SetUserPermissions(ctx context.Context, tenantID, userID uuid.UUID, codes []string) {
	if c == nil {
		return
	}
	raw, err := json.Marshal(codes)
	if err != nil {
		return
	}
	_ = c.client.Set(ctx, permissionsKey(tenantID, userID), raw, PermissionsTTL).Err()
}

// InvalidateUserPermissions implements authz.PermissionCache. Errors are
// dropped: the TTL bounds the window instead.
func (c *Cache) InvalidateUserPermissions(ctx context.Context, tenantID, userID uuid.UUID) {
	if c == nil {
		return
	}
	_ = c.client.Del(ctx, permissionsKey(tenantID, userID)).Err()
}

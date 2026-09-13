package cache

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/tenancy"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }

// A nil *Cache must behave exactly like the historic nil cache: misses and
// no-ops, never a panic. This is the typed-nil contract New relies on when
// Redis is disabled or unreachable.
func TestNilCacheIsSafe(t *testing.T) {
	var c *Cache
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()

	if _, ok := c.GetTenantBySlug(ctx, "acme"); ok {
		t.Error("nil cache hit on tenant lookup")
	}
	c.SetTenantBySlug(ctx, "acme", &tenancy.Tenant{}, time.Minute)

	if _, ok := c.GetUserPermissions(ctx, tenantID, userID); ok {
		t.Error("nil cache hit on permissions lookup")
	}
	c.SetUserPermissions(ctx, tenantID, userID, []string{"project:view"})
	c.InvalidateUserPermissions(ctx, tenantID, userID)

	if err := c.Close(); err != nil {
		t.Errorf("nil cache Close: %v", err)
	}
}

func TestNewDisabledWithoutURL(t *testing.T) {
	if c := New("", testLogger()); c != nil {
		t.Error("expected nil cache with empty URL")
		_ = c.Close()
	}
}

func TestNewInvalidURLFallsBackToNil(t *testing.T) {
	if c := New("not a redis URL", testLogger()); c != nil {
		t.Error("expected nil cache for invalid Redis URL")
		_ = c.Close()
	}
}

func TestNewUnreachableFallsBackToNil(t *testing.T) {
	// Nothing listens here; the constructor must warn and return nil fast,
	// not hang the startup on dial timeouts.
	if c := New("redis://127.0.0.1:1/0", testLogger()); c != nil {
		t.Error("expected nil cache for unreachable Redis")
		_ = c.Close()
	}
}

// liveCache dials TEST_REDIS_URL when the environment provides one and skips
// otherwise, mirroring the repo's skip-without-dependency test convention.
func liveCache(t *testing.T) *Cache {
	t.Helper()
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("TEST_REDIS_URL is unset; skipping live Redis test")
	}
	c := New(redisURL, testLogger())
	if c == nil {
		t.Skip("Redis is unreachable or TEST_REDIS_URL is invalid; skipping live Redis test")
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestLiveTenantRoundTrip(t *testing.T) {
	c := liveCache(t)
	ctx := context.Background()

	tenant := &tenancy.Tenant{Name: "Acme Inc", Slug: "acme-cache-test", PlanCode: "pro", Status: tenancy.StatusActive}
	c.SetTenantBySlug(ctx, tenant.Slug, tenant, time.Minute)

	got, ok := c.GetTenantBySlug(ctx, tenant.Slug)
	if !ok {
		t.Fatal("expected cache hit after set")
	}
	if got.Name != tenant.Name || got.Slug != tenant.Slug || got.PlanCode != tenant.PlanCode {
		t.Errorf("round trip mismatch: %+v", got)
	}

	if _, ok := c.GetTenantBySlug(ctx, "no-such-slug"); ok {
		t.Error("expected miss for unknown slug")
	}
}

func TestLivePermissionsRoundTripAndInvalidate(t *testing.T) {
	c := liveCache(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()

	codes := []string{"project:view", "team:manage"}
	c.SetUserPermissions(ctx, tenantID, userID, codes)

	got, ok := c.GetUserPermissions(ctx, tenantID, userID)
	if !ok {
		t.Fatal("expected cache hit after set")
	}
	if len(got) != len(codes) || got[0] != codes[0] || got[1] != codes[1] {
		t.Errorf("round trip mismatch: %v", got)
	}

	c.InvalidateUserPermissions(ctx, tenantID, userID)
	if _, ok := c.GetUserPermissions(ctx, tenantID, userID); ok {
		t.Error("expected miss after invalidate")
	}
}

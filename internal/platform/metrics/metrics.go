// Package metrics exposes Prometheus-compatible operational metrics for the
// platform: request volume/latency, cache hit rate, storage usage, active
// SSE sessions, and domain counts (tenants, users, projects, ...).
//
// Design constraints:
//   - Zero new dependencies: exposition is hand-rolled Prometheus text format
//     over atomic counters, so there is nothing new to audit or keep updated.
//   - Fail-open: every database-backed gauge degrades to "absent" when the
//     platform_stats() function (migrations/000016) is unavailable, e.g. on a
//     database migrated by an older release. Process-level counters always work.
//   - Cross-tenant safety: domain counts come from the SECURITY DEFINER
//     platform_stats() function, the same sanctioned pattern as the token
//     lookup functions (migrations/000011). The handler never disables RLS
//     itself, and /metrics carries no tenant PII -- only aggregate counts.
package metrics

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	gin "github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Registry holds process-lifetime counters. The zero value is usable;
// construct with New for a registry stamped with a start time.
type Registry struct {
	startedAt time.Time

	requestsTotal   atomic.Uint64
	requests2xx     atomic.Uint64
	requests4xx     atomic.Uint64
	requests5xx     atomic.Uint64
	inFlight        atomic.Int64
	latencyNanosSum atomic.Uint64

	cacheHits   atomic.Uint64
	cacheMisses atomic.Uint64

	sseClients atomic.Int64
}

// New returns a Registry stamped with the current time.
func New() *Registry {
	return &Registry{startedAt: time.Now()}
}

// Middleware records per-request volume, status class, in-flight gauge, and
// latency sum for average-latency computation. Register it before tenant
// resolution so health probes are counted too -- probe traffic is part of the
// honest request story, and excluding it would undercount load.
func (r *Registry) Middleware() gin.HandlerFunc {
	if r == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		start := time.Now()
		r.inFlight.Add(1)
		c.Next()
		r.inFlight.Add(-1)

		r.requestsTotal.Add(1)
		r.latencyNanosSum.Add(uint64(time.Since(start).Nanoseconds()))
		switch status := c.Writer.Status(); {
		case status >= 200 && status < 300:
			r.requests2xx.Add(1)
		case status >= 400 && status < 500:
			r.requests4xx.Add(1)
		case status >= 500:
			r.requests5xx.Add(1)
		}
	}
}

// RecordCacheHit records a Redis cache hit; RecordCacheMiss a miss (including
// fail-open fallbacks to Postgres). Hit rate = hits / (hits + misses).
func (r *Registry) RecordCacheHit() {
	if r != nil {
		r.cacheHits.Add(1)
	}
}

// RecordCacheMiss records a Redis cache miss.
func (r *Registry) RecordCacheMiss() {
	if r != nil {
		r.cacheMisses.Add(1)
	}
}

// AddSSEClients adjusts the live SSE subscriber gauge. Safe on nil.
func (r *Registry) AddSSEClients(delta int64) {
	if r != nil {
		r.sseClients.Add(delta)
	}
}

// Snapshot is the point-in-time process-level view, used by the admin
// overview endpoint as well as the Prometheus handler.
type Snapshot struct {
	UptimeSeconds    float64
	RequestsTotal    uint64
	Requests2xx      uint64
	Requests4xx      uint64
	Requests5xx      uint64
	InFlight         int64
	AvgLatencyMillis float64
	CacheHits        uint64
	CacheMisses      uint64
	CacheHitRate     float64
	SSEClients       int64
}

// Snapshot captures current counters.
func (r *Registry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	total := r.requestsTotal.Load()
	latencySum := r.latencyNanosSum.Load()
	hits := r.cacheHits.Load()
	misses := r.cacheMisses.Load()
	snap := Snapshot{
		UptimeSeconds: time.Since(r.startedAt).Seconds(),
		RequestsTotal: total,
		Requests2xx:   r.requests2xx.Load(),
		Requests4xx:   r.requests4xx.Load(),
		Requests5xx:   r.requests5xx.Load(),
		InFlight:      r.inFlight.Load(),
		CacheHits:     hits,
		CacheMisses:   misses,
		SSEClients:    r.sseClients.Load(),
	}
	if total > 0 {
		snap.AvgLatencyMillis = float64(latencySum) / float64(total) / 1e6
	}
	if hits+misses > 0 {
		snap.CacheHitRate = float64(hits) / float64(hits+misses)
	}
	return snap
}

// platformStats mirrors the platform_stats() SECURITY DEFINER function
// (migrations/000016). All fields are aggregate counts -- no tenant PII.
type platformStats struct {
	Tenants            int64 `gorm:"column:tenants"`
	Users              int64 `gorm:"column:users"`
	Teams              int64 `gorm:"column:teams"`
	Projects           int64 `gorm:"column:projects"`
	PendingInvitations int64 `gorm:"column:pending_invitations"`
	ActiveAPIKeys      int64 `gorm:"column:active_api_keys"`
	Files              int64 `gorm:"column:files"`
	StorageBytes       int64 `gorm:"column:storage_bytes"`
	AuditEvents30d     int64 `gorm:"column:audit_events_30d"`
	ActiveUsers7d      int64 `gorm:"column:active_users_7d"`
}

// queryPlatformStats calls platform_stats(), returning ok=false when the
// function does not exist yet (older database) so the caller can degrade
// instead of failing the whole scrape.
func queryPlatformStats(db *gorm.DB) (platformStats, bool) {
	var stats platformStats
	if db == nil {
		return stats, false
	}
	sqlDB, err := db.DB()
	if err != nil {
		return stats, false
	}
	// Short timeout: a scrape must never pile up behind a slow database.
	done := make(chan error, 1)
	go func() {
		done <- sqlDB.QueryRow(`SELECT * FROM platform_stats()`).Scan(
			&stats.Tenants, &stats.Users, &stats.Teams, &stats.Projects,
			&stats.PendingInvitations, &stats.ActiveAPIKeys, &stats.Files,
			&stats.StorageBytes, &stats.AuditEvents30d, &stats.ActiveUsers7d,
		)
	}()
	select {
	case err := <-done:
		if err != nil {
			if err == sql.ErrNoRows {
				return stats, true
			}
			return platformStats{}, false
		}
		return stats, true
	case <-time.After(3 * time.Second):
		return platformStats{}, false
	}
}

// diskUsage returns used/total bytes of the filesystem holding path, or
// ok=false when the path cannot be statted (e.g. S3-backed deployments where
// local disk usage is meaningless).
func diskUsage(path string) (usedBytes, totalBytes uint64, ok bool) {
	return statfsUsage(path)
}

// Handler serves /metrics in Prometheus text exposition format. When
// metricsToken is non-empty, requests must present it as
// `Authorization: Bearer <token>`; otherwise the endpoint is open (correct
// behind nginx allow-lists or a private network -- see deploy docs).
func (r *Registry) Handler(db *gorm.DB, storageRoot, metricsToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if metricsToken != "" {
			header := c.GetHeader("Authorization")
			if !strings.EqualFold(strings.TrimSpace(header), "Bearer "+metricsToken) {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}
		}
		snap := r.Snapshot()
		stats, haveStats := queryPlatformStats(db)

		var b strings.Builder
		write := func(name, help, typ, labels string, value any) {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s%s %v\n", name, help, name, typ, name, labels, value)
		}
		write("saas_uptime_seconds", "Process uptime in seconds.", "gauge", "", fmt.Sprintf("%.0f", snap.UptimeSeconds))
		write("saas_api_requests_total", "Total HTTP requests served since start.", "counter", "", snap.RequestsTotal)
		write("saas_api_requests_2xx_total", "HTTP requests answered 2xx since start.", "counter", "", snap.Requests2xx)
		write("saas_api_requests_4xx_total", "HTTP requests answered 4xx since start.", "counter", "", snap.Requests4xx)
		write("saas_api_requests_5xx_total", "HTTP requests answered 5xx since start.", "counter", "", snap.Requests5xx)
		write("saas_api_in_flight", "HTTP requests currently being served.", "gauge", "", snap.InFlight)
		write("saas_api_latency_avg_millis", "Mean request latency in milliseconds since start.", "gauge", "", fmt.Sprintf("%.3f", snap.AvgLatencyMillis))
		write("saas_cache_hits_total", "Redis cache hits since start.", "counter", "", snap.CacheHits)
		write("saas_cache_misses_total", "Redis cache misses since start (includes fail-open fallbacks).", "counter", "", snap.CacheMisses)
		write("saas_cache_hit_rate", "Cache hit rate in [0,1] since start.", "gauge", "", fmt.Sprintf("%.4f", snap.CacheHitRate))
		write("saas_sse_clients", "Currently connected SSE notification subscribers.", "gauge", "", snap.SSEClients)

		if haveStats {
			write("saas_tenants", "Total organizations.", "gauge", "", stats.Tenants)
			write("saas_users", "Total users across all organizations.", "gauge", "", stats.Users)
			write("saas_teams", "Total teams across all organizations.", "gauge", "", stats.Teams)
			write("saas_projects", "Total projects across all organizations.", "gauge", "", stats.Projects)
			write("saas_invitations_pending", "Pending (unredeemed, unexpired) invitations.", "gauge", "", stats.PendingInvitations)
			write("saas_api_keys_active", "Unrevoked, unexpired API keys.", "gauge", "", stats.ActiveAPIKeys)
			write("saas_files", "Total stored files.", "gauge", "", stats.Files)
			write("saas_storage_bytes", "Total file bytes recorded in the catalog.", "gauge", "", stats.StorageBytes)
			write("saas_audit_events_30d", "Audit events recorded in the last 30 days.", "gauge", "", stats.AuditEvents30d)
			write("saas_active_users_7d", "Users with a login in the last 7 days.", "gauge", "", stats.ActiveUsers7d)
		}

		if used, total, ok := diskUsage(storageRoot); ok {
			write("saas_storage_disk_used_bytes", "Bytes used on the filesystem holding the upload volume.", "gauge", "", used)
			write("saas_storage_disk_total_bytes", "Total bytes on the filesystem holding the upload volume.", "gauge", "", total)
		}
		if db != nil {
			if sqlDB, err := db.DB(); err == nil {
				st := sqlDB.Stats()
				write("saas_db_open_connections", "Open DB connections in the pool.", "gauge", "", st.OpenConnections)
				write("saas_db_in_use", "DB connections currently in use.", "gauge", "", st.InUse)
				write("saas_db_idle", "Idle DB connections in the pool.", "gauge", "", st.Idle)
				write("saas_db_wait_count_total", "Total waits for a pooled DB connection.", "counter", "", st.WaitCount)
			}
		}

		c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
	}
}

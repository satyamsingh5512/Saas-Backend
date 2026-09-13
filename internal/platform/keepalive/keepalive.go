// Package keepalive keeps free-tier infrastructure from idling out.
//
// Two independent sleep mechanisms, one goroutine:
//
//  1. Hosting (Render free): the service spins down after ~15 minutes without
//     *incoming* traffic. Every tick GETs the public base URL's /health, which
//     arrives as a real inbound request and resets the idle timer.
//     Loopback base URLs (local development) are skipped on purpose: pinging
//     localhost proves nothing and keeps nothing awake.
//  2. Database (managed Postgres): idle pools are reaped by the server and
//     free-tier proxies drop silent connections. Every tick pings the pool,
//     which re-dials dead connections before real traffic needs them.
//
// Honest limits: no amount of pinging prevents a trial service from being
// deleted at trial end, or a free Render Postgres from expiring after 30
// days. This package prevents idle-sleep and stale-pool failures, not
// billing-driven removal.
package keepalive

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Start begins the keep-alive loop and returns a stop function. Ticks run on
// interval; a tick never overlaps the previous one (a slow tick delays the
// next rather than piling up). Stopped via the returned func, which the
// server calls during graceful shutdown before closing the pool.
func Start(db *gorm.DB, baseURL string, interval time.Duration, logger *slog.Logger) (stop func()) {
	if interval <= 0 {
		interval = time.Minute
	}
	target := publicHealthURL(baseURL)
	logger.Info("keepalive enabled",
		slog.Duration("interval", interval),
		slog.Bool("http_ping", target != ""),
		slog.Bool("db_ping", db != nil),
	)

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		client := &http.Client{Timeout: 15 * time.Second}
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				tick(client, db, target, logger)
			}
		}
	}()
	return func() { close(done) }
}

func tick(client *http.Client, db *gorm.DB, target string, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if db != nil {
		if sqlDB, err := db.DB(); err != nil {
			logger.Warn("keepalive: database handle unavailable", slog.Any("error", err))
		} else if err := sqlDB.PingContext(ctx); err != nil {
			logger.Warn("keepalive: database ping failed", slog.Any("error", err))
		} else {
			logger.Debug("keepalive: database ping ok")
		}
	}

	if target == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		logger.Warn("keepalive: health request build failed", slog.Any("error", err))
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		logger.Warn("keepalive: health ping failed", slog.String("target", target), slog.Any("error", err))
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Warn("keepalive: health ping non-200", slog.String("target", target), slog.Int("status", resp.StatusCode))
		return
	}
	logger.Debug("keepalive: health ping ok", slog.String("target", target))
}

// publicHealthURL resolves the /health URL to ping, or "" when no HTTP ping
// should run: empty base URL, unparseable URL, or a loopback host. Only a
// request that traverses the public edge counts as activity to the host.
func publicHealthURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	if !strings.Contains(baseURL, "://") {
		baseURL = "https://" + baseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := parsed.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), ".")
	if host == "" || host == "localhost" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return strings.TrimSuffix(baseURL, "/") + "/health"
}

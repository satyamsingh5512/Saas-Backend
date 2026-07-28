// Package platform hosts cross-cutting operational concerns that don't
// belong to a single business-domain module: health/readiness/liveness
// checks now, API keys and admin/cross-tenant operator tooling in later
// phases.
package platform

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// HealthHandler exposes liveness/readiness/health endpoints for use by load
// balancers, Kubernetes probes, and uptime monitors.
type HealthHandler struct {
	db *gorm.DB
}

func NewHealthHandler(db *gorm.DB) *HealthHandler {
	return &HealthHandler{db: db}
}

// Health is a basic "is the process up" check with no dependency checks,
// kept for backward compatibility with the original /health route and for
// use as a cheap load-balancer target.
func (h *HealthHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Live is the Kubernetes liveness probe target: process is running and the
// Go runtime is responsive. Deliberately does NOT check the database --
// a database outage should not cause Kubernetes to kill and restart pods
// (that would compound an outage with unnecessary churn); it should instead
// fail Ready below, which pulls the pod out of the load balancer without
// killing it.
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "alive"})
}

// Ready is the Kubernetes readiness probe target: verifies the database
// connection pool can actually reach Postgres within a short timeout,
// signaling whether this pod should receive traffic.
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	sqlDB, err := h.db.DB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "reason": "database handle unavailable"})
		return
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "reason": "database unreachable"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

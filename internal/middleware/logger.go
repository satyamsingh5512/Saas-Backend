package middleware

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// NewLogger builds the application's structured logger. Production emits JSON
// so log aggregators (CloudWatch, Loki, Datadog) can index fields without
// regex parsing; development emits human-readable text.
func NewLogger(environment string) *slog.Logger {
	level := slog.LevelDebug
	if strings.EqualFold(environment, "production") {
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(environment, "production") {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// RequestLogger replaces gin.Logger() with structured, correlation-aware
// access logging. Every line carries the request ID, so an error surfaced to a
// client can be tied back to its server-side log entry.
//
// Deliberately omitted from log output: request bodies, query strings, and the
// Authorization header. Those routinely contain passwords, reset tokens, and
// bearer credentials, and logging them would move secrets into a lower-trust
// system (log storage) than the one they were sent to.
func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}

		c.Next()

		attrs := []slog.Attr{
			slog.String("request_id", RequestIDFromContext(c)),
			slog.String("method", c.Request.Method),
			slog.String("route", path),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("latency", time.Since(start)),
			slog.String("client_ip", c.ClientIP()),
		}

		if tenantID, ok := c.Get("tenant_id"); ok {
			attrs = append(attrs, slog.Any("tenant_id", tenantID))
		}

		status := c.Writer.Status()
		switch {
		case status >= 500:
			logger.LogAttrs(c.Request.Context(), slog.LevelError, "request failed", attrs...)
		case status >= 400:
			logger.LogAttrs(c.Request.Context(), slog.LevelWarn, "request rejected", attrs...)
		default:
			logger.LogAttrs(c.Request.Context(), slog.LevelInfo, "request completed", attrs...)
		}
	}
}

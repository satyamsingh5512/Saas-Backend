package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// SSEClientGauge tracks live subscriber count for /metrics. The metrics
// package is deliberately not imported here (it would make realtime depend
// on the observability layer); routes.Setup wires this to the registry.
type SSEClientGauge interface {
	AddSSEClients(delta int64)
}

// Handler serves the SSE notification stream.
type Handler struct {
	broker *Broker
	gauge  SSEClientGauge
}

// NewHandler builds the stream handler. gauge may be nil (no metric).
func NewHandler(broker *Broker, gauge SSEClientGauge) *Handler {
	return &Handler{broker: broker, gauge: gauge}
}

// Stream handles GET /api/v1/notifications/stream.
//
// Protocol:
//   - `event: ready` once on connect, so the client can mark the badge live.
//   - `event: notification` per delivery, data = Event JSON. The client
//     refetches GET /notifications/unread-count on each one, which is what
//     updates the badge without a second round trip baked into the stream.
//   - `: ping` heartbeat every 25s, keeping nginx (proxy_read_timeout 60s)
//     and NAT middleboxes from silently killing idle streams.
//
// Authentication is the standard JWT/API-key chain; the subscription channel
// is derived from the caller's own IDs, so a client can only ever receive
// its own tenant-scoped notifications. apiKey-authenticated machine clients
// may also stream; user scoping still applies via the credential identity.
func (h *Handler) Stream(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	// Critical behind the Phase-3 nginx config: without it nginx buffers
	// SSE chunks until the buffer fills, turning "live" into "every 4KB".
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	ctx := c.Request.Context()
	events, unsubscribe := h.broker.Subscribe(ctx, caller.TenantID, caller.UserID)
	defer unsubscribe()

	if h.gauge != nil {
		h.gauge.AddSSEClients(1)
		defer h.gauge.AddSSEClients(-1)
	}

	write := func(event, data string) bool {
		if event != "" {
			if _, err := fmt.Fprintf(c.Writer, "event: %s\n", event); err != nil {
				return false
			}
		}
		if data != "" {
			if _, err := fmt.Fprintf(c.Writer, "data: %s\n", data); err != nil {
				return false
			}
		}
		if _, err := fmt.Fprint(c.Writer, "\n"); err != nil {
			return false
		}
		c.Writer.Flush()
		return true
	}

	if !write("ready", `{"status":"connected"}`) {
		return
	}

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		case e, ok := <-events:
			if !ok {
				return
			}
			// Defense in depth: the broker addresses channels per
			// recipient, but the stream double-checks the payload's
			// tenant/user before writing it to this connection, so a
			// mis-addressed publish can never cross a tenant boundary
			// at the last hop.
			if e.TenantID != caller.TenantID.String() || e.UserID != caller.UserID.String() {
				continue
			}
			raw, err := json.Marshal(e)
			if err != nil {
				continue
			}
			if !write("notification", string(raw)) {
				return
			}
		}
	}
}

// QueryTokenAuth promotes ?access_token= to an Authorization header when no
// header is present. Browser EventSource cannot set request headers, so the
// SSE stream is unreachable from it without this bridge. Scope is deliberately
// narrow: it runs only on the stream route (wired in routes.Setup), applies
// only when the header is absent (a real header always wins), and the value
// still goes through the standard JWT/API-key validation chain -- this changes
// transport, not trust. Query-string tokens can linger in access logs, so
// header auth remains the documented default and this exists for EventSource
// compatibility only.
func QueryTokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			if token := c.Query("access_token"); token != "" {
				c.Request.Header.Set("Authorization", "Bearer "+token)
			}
		}
		c.Next()
	}
}

// Wired in routes.Setup as the notifications service publisher so domain
// code never imports the transport layer.
// PublishNotification adapts a persisted notification to a broker Event.
// Wired in routes.Setup as the notifications service publisher so domain
// code never imports the transport layer.
func (b *Broker) PublishNotification(tenantID, userID, notificationID uuid.UUID, notifType, title, body string, createdAt time.Time) {
	b.Publish(context.Background(), Event{
		ID:        notificationID.String(),
		TenantID:  tenantID.String(),
		UserID:    userID.String(),
		Type:      notifType,
		Title:     title,
		Body:      body,
		CreatedAt: createdAt,
	})
}

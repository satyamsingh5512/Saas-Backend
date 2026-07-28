package audit

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// Handler exposes the read side of the audit log and activity feed. There is no
// write endpoint: entries are only ever created as a side effect of a real
// domain action, so a client-writable audit API would let a caller forge history.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// ListAuditLogs handles GET /api/v1/audit-logs, gated by audit:view.
// Supports ?actor_id=, ?action=, ?from=, ?to=, ?page=, ?page_size=.
func (h *Handler) ListAuditLogs(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}

	var filter AuditFilter
	if raw := c.Query("actor_id"); raw != "" {
		actorID, err := uuid.Parse(raw)
		if err != nil {
			reqctx.BadRequest(c, "invalid actor_id")
			return
		}
		filter.ActorID = &actorID
	}
	filter.Action = c.Query("action")

	if raw := c.Query("from"); raw != "" {
		from, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			reqctx.BadRequest(c, "from must be an RFC3339 timestamp")
			return
		}
		filter.From = &from
	}
	if raw := c.Query("to"); raw != "" {
		to, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			reqctx.BadRequest(c, "to must be an RFC3339 timestamp")
			return
		}
		filter.To = &to
	}

	page, pageSize := apiresponse.ParsePageQuery(c)
	logs, total, err := h.svc.ListAuditLogs(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, logs, apiresponse.NewPagination(page, pageSize, total))
}

// ListActivity handles GET /api/v1/activity, optionally scoped to one resource
// via ?target_type= and ?target_id=.
func (h *Handler) ListActivity(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}

	var targetID *uuid.UUID
	if raw := c.Query("target_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			reqctx.BadRequest(c, "invalid target_id")
			return
		}
		targetID = &id
	}

	page, pageSize := apiresponse.ParsePageQuery(c)
	events, total, err := h.svc.ListActivity(c.Request.Context(), c.Query("target_type"), targetID, page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, events, apiresponse.NewPagination(page, pageSize, total))
}

// EntryFromRequest seeds an audit Entry with the transport-level forensic detail
// (client IP and user agent) that makes an audit trail useful in an incident
// review, so each call site does not re-derive it.
//
// c.ClientIP() honors Gin's trusted-proxy configuration, so a spoofed
// X-Forwarded-For from an untrusted hop is not recorded as the client address.
func EntryFromRequest(c *gin.Context, action string) Entry {
	caller, _ := reqctx.CallerFrom(c)

	entry := Entry{
		TenantID:  caller.TenantID,
		Action:    action,
		IPAddress: c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
		Metadata:  map[string]any{},
	}
	if caller.UserID != uuid.Nil {
		actorID := caller.UserID
		entry.ActorID = &actorID
	}
	return entry
}

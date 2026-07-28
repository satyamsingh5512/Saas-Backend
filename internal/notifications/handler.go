package notifications

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// Handler is the Gin binding layer for notification endpoints.
//
// Every route operates on the authenticated caller's own notifications, taken from
// the validated credential. No endpoint accepts a user_id parameter, which is what
// prevents one user from reading or acknowledging another's notifications.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// List handles GET /api/v1/notifications?unread_only=true.
func (h *Handler) List(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	unreadOnly := c.Query("unread_only") == "true"
	page, pageSize := apiresponse.ParsePageQuery(c)

	items, total, err := h.svc.List(c.Request.Context(), caller.UserID, unreadOnly, page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, items, apiresponse.NewPagination(page, pageSize, total))
}

// UnreadCount handles GET /api/v1/notifications/unread-count.
func (h *Handler) UnreadCount(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	count, err := h.svc.UnreadCount(c.Request.Context(), caller.UserID)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"unread": count})
}

// MarkRead handles POST /api/v1/notifications/:notificationID/read.
func (h *Handler) MarkRead(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}
	notificationID, ok := reqctx.UUIDParam(c, "notificationID")
	if !ok {
		return
	}

	if err := h.svc.MarkRead(c.Request.Context(), caller.UserID, notificationID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "notification marked read"})
}

// MarkAllRead handles POST /api/v1/notifications/read-all.
func (h *Handler) MarkAllRead(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	count, err := h.svc.MarkAllRead(c.Request.Context(), caller.UserID)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"marked_read": count})
}

// Delete handles DELETE /api/v1/notifications/:notificationID.
func (h *Handler) Delete(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}
	notificationID, ok := reqctx.UUIDParam(c, "notificationID")
	if !ok {
		return
	}

	if err := h.svc.Delete(c.Request.Context(), caller.UserID, notificationID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "notification deleted"})
}

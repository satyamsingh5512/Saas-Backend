package invitations

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// Handler is the Gin binding layer for invitation endpoints.
//
// Preview and Accept are mounted on public routes because the invitee is by
// definition not yet authenticated; both are rate-limited at the route layer to
// keep the token endpoints from being used as an oracle.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

type createInviteRequest struct {
	Email    string     `json:"email" binding:"required,email"`
	RoleID   *uuid.UUID `json:"role_id"`
	RoleSlug string     `json:"role_slug" binding:"omitempty,max=100"`
}

// Create handles POST /api/v1/invitations, gated by member:invite.
func (h *Handler) Create(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	var req createInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	result, err := h.svc.Create(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionUserInvited),
		caller.TenantID, caller.UserID,
		CreateInput{Email: req.Email, RoleID: req.RoleID, RoleSlug: req.RoleSlug})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusCreated, result)
}

// List handles GET /api/v1/invitations?status=, gated by member:view.
func (h *Handler) List(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}

	page, pageSize := apiresponse.ParsePageQuery(c)
	list, total, err := h.svc.List(c.Request.Context(), c.Query("status"), page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, list, apiresponse.NewPagination(page, pageSize, total))
}

// Revoke handles DELETE /api/v1/invitations/:inviteID, gated by member:invite.
func (h *Handler) Revoke(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	inviteID, ok := reqctx.UUIDParam(c, "inviteID")
	if !ok {
		return
	}

	if err := h.svc.Revoke(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionInviteRevoked), inviteID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "invitation revoked"})
}

// Preview handles GET /api/v1/invitations/preview?token=... on a public route, so
// the invite landing page can show who invited them before they sign up.
func (h *Handler) Preview(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		reqctx.BadRequest(c, "token is required")
		return
	}

	preview, err := h.svc.Preview(c.Request.Context(), token)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, preview)
}

type acceptInviteRequest struct {
	Token    string `json:"token" binding:"required"`
	FullName string `json:"full_name" binding:"omitempty,max=255"`
	// Password is required only for an invitee who has no account in the
	// organization yet; the service decides which case applies.
	Password string `json:"password" binding:"omitempty,min=8,max=128"`
}

// Accept handles POST /api/v1/invitations/accept on a public route.
//
// It returns only the resolved organization and email, not a session. The invitee
// authenticates through the normal login flow afterward, which keeps token
// issuance in one place (internal/identity) instead of duplicating session
// creation in a second module.
func (h *Handler) Accept(c *gin.Context) {
	var req acceptInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	result, err := h.svc.Accept(c.Request.Context(), AcceptInput{
		Token:    req.Token,
		FullName: req.FullName,
		Password: req.Password,
	})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}

	apiresponse.Success(c, http.StatusOK, gin.H{
		"message":   "invitation accepted, you can now sign in",
		"email":     result.Email,
		"tenant_id": result.TenantID,
	})
}

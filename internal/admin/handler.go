package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// Handler is the Gin binding layer for the admin analytics endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the admin handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Overview handles GET /api/v1/admin/overview.
//
// Returns the calling organization’s analytics snapshot. The tenant scope
// always comes from the validated credential -- there is deliberately no
// tenant_id parameter, so a caller cannot request another organization's
// numbers. Route-level org:view permission is enforced in routes.Setup.
//
// @Summary      Organization analytics overview
// @Description  Tenant-scoped dashboard snapshot: users, teams, projects, invitations, API keys, storage, and audit volume for the caller's organization.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  Overview
// @Failure      401  {object}  map[string]any
// @Failure      403  {object}  map[string]any
// @Router       /api/v1/admin/overview [get]
func (h *Handler) Overview(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}
	out, err := h.svc.GetOverview(c.Request.Context(), caller.TenantID)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, out)
}

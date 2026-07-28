package billing

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// Handler is the Gin binding layer for billing endpoints. Reads are gated by
// billing:view and mutations by billing:manage at the route layer.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// ListPlans handles GET /api/v1/billing/plans.
func (h *Handler) ListPlans(c *gin.Context) {
	plans, err := h.svc.ListPlans(c.Request.Context())
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, plans)
}

// GetSubscription handles GET /api/v1/billing/subscription.
func (h *Handler) GetSubscription(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}

	view, err := h.svc.GetSubscription(c.Request.Context())
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, view)
}

// GetUsage handles GET /api/v1/billing/usage.
func (h *Handler) GetUsage(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}

	usage, err := h.svc.GetUsage(c.Request.Context())
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, usage)
}

type changePlanRequest struct {
	PlanCode string `json:"plan_code" binding:"required,min=1,max=50"`
}

// ChangePlan handles POST /api/v1/billing/subscription.
func (h *Handler) ChangePlan(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	var req changePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	view, err := h.svc.ChangePlan(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionSubscriptionChange),
		caller.TenantID, req.PlanCode)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, view)
}

// Cancel handles DELETE /api/v1/billing/subscription.
func (h *Handler) Cancel(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	view, err := h.svc.Cancel(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionSubscriptionChange), caller.TenantID)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, view)
}

package apikeys

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// Handler is the Gin binding layer for API key management.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

type createKeyRequest struct {
	Name      string     `json:"name" binding:"required,min=1,max=255"`
	Scopes    []string   `json:"scopes" binding:"required,min=1,dive,max=150"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// Create handles POST /api/v1/api-keys, gated by apikey:manage and restricted to
// user sessions so a key cannot mint further keys.
func (h *Handler) Create(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	var req createKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	result, err := h.svc.Create(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionAPIKeyCreated),
		caller.TenantID,
		CreateInput{
			Name:        req.Name,
			Scopes:      req.Scopes,
			ExpiresAt:   req.ExpiresAt,
			OwnerUserID: caller.UserID,
		})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}

	// The secret appears in this response only. Clients must store it now.
	apiresponse.Success(c, http.StatusCreated, result)
}

// List handles GET /api/v1/api-keys, gated by apikey:view.
func (h *Handler) List(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}

	page, pageSize := apiresponse.ParsePageQuery(c)
	keys, total, err := h.svc.List(c.Request.Context(), page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, keys, apiresponse.NewPagination(page, pageSize, total))
}

// Revoke handles DELETE /api/v1/api-keys/:keyID, gated by apikey:manage.
func (h *Handler) Revoke(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	keyID, ok := reqctx.UUIDParam(c, "keyID")
	if !ok {
		return
	}

	if err := h.svc.Revoke(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionAPIKeyRevoked), keyID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "api key revoked"})
}

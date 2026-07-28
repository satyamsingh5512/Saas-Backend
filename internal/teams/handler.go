package teams

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// Handler is the Gin binding layer for team endpoints. Permission gating
// (team:view / team:create / team:manage) is applied at the route layer, not here.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

type createTeamRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=255"`
	Slug        string `json:"slug" binding:"omitempty,max=63"`
	Description string `json:"description" binding:"max=2000"`
}

// Create handles POST /api/v1/teams.
func (h *Handler) Create(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	var req createTeamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	team, err := h.svc.Create(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionTeamCreated),
		caller.TenantID, caller.UserID,
		CreateInput{Name: req.Name, Slug: req.Slug, Description: req.Description})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusCreated, team)
}

// List handles GET /api/v1/teams, supporting ?search=, ?page=, ?page_size=.
func (h *Handler) List(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}

	page, pageSize := apiresponse.ParsePageQuery(c)
	teams, total, err := h.svc.List(c.Request.Context(), c.Query("search"), page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, teams, apiresponse.NewPagination(page, pageSize, total))
}

// Get handles GET /api/v1/teams/:teamID.
func (h *Handler) Get(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	teamID, ok := reqctx.UUIDParam(c, "teamID")
	if !ok {
		return
	}

	team, err := h.svc.Get(c.Request.Context(), teamID)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, team)
}

// updateTeamRequest uses pointer fields so an omitted key means "leave unchanged"
// and an explicit null/empty value is still distinguishable from absence.
type updateTeamRequest struct {
	Name        *string `json:"name" binding:"omitempty,min=1,max=255"`
	Slug        *string `json:"slug" binding:"omitempty,max=63"`
	Description *string `json:"description" binding:"omitempty,max=2000"`
}

// Update handles PATCH /api/v1/teams/:teamID.
func (h *Handler) Update(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	teamID, ok := reqctx.UUIDParam(c, "teamID")
	if !ok {
		return
	}

	var req updateTeamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	team, err := h.svc.Update(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionTeamUpdated),
		teamID,
		UpdateInput{Name: req.Name, Slug: req.Slug, Description: req.Description})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, team)
}

// Delete handles DELETE /api/v1/teams/:teamID.
func (h *Handler) Delete(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	teamID, ok := reqctx.UUIDParam(c, "teamID")
	if !ok {
		return
	}

	if err := h.svc.Delete(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionTeamDeleted), teamID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "team deleted"})
}

// ListMembers handles GET /api/v1/teams/:teamID/members.
func (h *Handler) ListMembers(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	teamID, ok := reqctx.UUIDParam(c, "teamID")
	if !ok {
		return
	}

	page, pageSize := apiresponse.ParsePageQuery(c)
	members, total, err := h.svc.ListMembers(c.Request.Context(), teamID, page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, members, apiresponse.NewPagination(page, pageSize, total))
}

type addMemberRequest struct {
	UserID uuid.UUID `json:"user_id" binding:"required"`
}

// AddMember handles POST /api/v1/teams/:teamID/members.
func (h *Handler) AddMember(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}
	teamID, ok := reqctx.UUIDParam(c, "teamID")
	if !ok {
		return
	}

	var req addMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	if err := h.svc.AddMember(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionTeamMemberAdded),
		caller.TenantID, teamID, req.UserID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "member added to team"})
}

// RemoveMember handles DELETE /api/v1/teams/:teamID/members/:userID.
func (h *Handler) RemoveMember(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}
	teamID, ok := reqctx.UUIDParam(c, "teamID")
	if !ok {
		return
	}
	userID, ok := reqctx.UUIDParam(c, "userID")
	if !ok {
		return
	}

	if err := h.svc.RemoveMember(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionTeamMemberRemoved),
		caller.TenantID, teamID, userID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "member removed from team"})
}

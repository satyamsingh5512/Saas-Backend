package projects

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// Handler is the Gin binding layer for project endpoints.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

type createProjectRequest struct {
	Name        string     `json:"name" binding:"required,min=1,max=255"`
	Slug        string     `json:"slug" binding:"omitempty,max=63"`
	Description string     `json:"description" binding:"max=5000"`
	TeamID      *uuid.UUID `json:"team_id"`
}

// Create handles POST /api/v1/projects.
func (h *Handler) Create(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	var req createProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	project, err := h.svc.Create(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionProjectCreated),
		caller.TenantID, caller.UserID,
		CreateInput{Name: req.Name, Slug: req.Slug, Description: req.Description, TeamID: req.TeamID})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusCreated, project)
}

// List handles GET /api/v1/projects with ?search=, ?status=, ?team_id=, ?page=.
func (h *Handler) List(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}

	filter := ListFilter{Search: c.Query("search"), Status: c.Query("status")}
	if raw := c.Query("team_id"); raw != "" {
		teamID, err := uuid.Parse(raw)
		if err != nil {
			reqctx.BadRequest(c, "invalid team_id")
			return
		}
		filter.TeamID = &teamID
	}

	page, pageSize := apiresponse.ParsePageQuery(c)
	list, total, err := h.svc.List(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, list, apiresponse.NewPagination(page, pageSize, total))
}

// Get handles GET /api/v1/projects/:projectID.
func (h *Handler) Get(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	projectID, ok := reqctx.UUIDParam(c, "projectID")
	if !ok {
		return
	}

	project, err := h.svc.Get(c.Request.Context(), projectID)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, project)
}

type updateProjectRequest struct {
	Name        *string    `json:"name" binding:"omitempty,min=1,max=255"`
	Slug        *string    `json:"slug" binding:"omitempty,max=63"`
	Description *string    `json:"description" binding:"omitempty,max=5000"`
	Status      *string    `json:"status" binding:"omitempty,oneof=active archived"`
	TeamID      *uuid.UUID `json:"team_id"`
	ClearTeam   bool       `json:"clear_team"`
}

// Update handles PATCH /api/v1/projects/:projectID.
func (h *Handler) Update(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	projectID, ok := reqctx.UUIDParam(c, "projectID")
	if !ok {
		return
	}

	var req updateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	project, err := h.svc.Update(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionProjectUpdated),
		projectID,
		UpdateInput{
			Name:        req.Name,
			Slug:        req.Slug,
			Description: req.Description,
			Status:      req.Status,
			TeamID:      req.TeamID,
			ClearTeam:   req.ClearTeam,
		})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, project)
}

// Delete handles DELETE /api/v1/projects/:projectID.
func (h *Handler) Delete(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	projectID, ok := reqctx.UUIDParam(c, "projectID")
	if !ok {
		return
	}

	if err := h.svc.Delete(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionProjectDeleted), projectID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "project deleted"})
}

// ListMembers handles GET /api/v1/projects/:projectID/members.
func (h *Handler) ListMembers(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	projectID, ok := reqctx.UUIDParam(c, "projectID")
	if !ok {
		return
	}

	page, pageSize := apiresponse.ParsePageQuery(c)
	members, total, err := h.svc.ListMembers(c.Request.Context(), projectID, page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, members, apiresponse.NewPagination(page, pageSize, total))
}

type addMemberRequest struct {
	UserID uuid.UUID `json:"user_id" binding:"required"`
}

// AddMember handles POST /api/v1/projects/:projectID/members.
func (h *Handler) AddMember(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}
	projectID, ok := reqctx.UUIDParam(c, "projectID")
	if !ok {
		return
	}

	var req addMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	if err := h.svc.AddMember(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionProjectUpdated),
		caller.TenantID, projectID, req.UserID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "member added to project"})
}

// RemoveMember handles DELETE /api/v1/projects/:projectID/members/:userID.
func (h *Handler) RemoveMember(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}
	projectID, ok := reqctx.UUIDParam(c, "projectID")
	if !ok {
		return
	}
	userID, ok := reqctx.UUIDParam(c, "userID")
	if !ok {
		return
	}

	if err := h.svc.RemoveMember(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionProjectUpdated),
		caller.TenantID, projectID, userID); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "member removed from project"})
}

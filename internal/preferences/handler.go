package preferences

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

// Handler is the Gin binding layer for self-service preference and profile
// endpoints. Every route acts on the caller's own account, identified from the
// credential rather than from any request parameter.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GetProfile handles GET /api/v1/profile.
func (h *Handler) GetProfile(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	profile, err := h.svc.GetProfile(c.Request.Context(), caller.TenantID, caller.UserID)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, profile)
}

type updateProfileRequest struct {
	FullName    *string `json:"full_name" binding:"omitempty,min=1,max=255"`
	AvatarURL   *string `json:"avatar_url" binding:"omitempty,url,max=1000"`
	ClearAvatar bool    `json:"clear_avatar"`
}

// UpdateProfile handles PATCH /api/v1/profile.
func (h *Handler) UpdateProfile(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	var req updateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	profile, err := h.svc.UpdateProfile(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionUserProfileUpdate),
		caller.TenantID, caller.UserID,
		ProfileInput{FullName: req.FullName, AvatarURL: req.AvatarURL, ClearAvatar: req.ClearAvatar})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, profile)
}

// Get handles GET /api/v1/preferences.
func (h *Handler) Get(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	prefs, err := h.svc.Get(c.Request.Context(), caller.TenantID, caller.UserID)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, prefs)
}

type updatePreferencesRequest struct {
	Timezone           *string `json:"timezone" binding:"omitempty,max=50"`
	Locale             *string `json:"locale" binding:"omitempty,max=10"`
	Theme              *string `json:"theme" binding:"omitempty,oneof=system light dark"`
	EmailNotifications *bool   `json:"email_notifications"`
}

// Update handles PATCH /api/v1/preferences.
func (h *Handler) Update(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	var req updatePreferencesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	prefs, err := h.svc.Update(c.Request.Context(), caller.TenantID, caller.UserID, UpdateInput{
		Timezone:           req.Timezone,
		Locale:             req.Locale,
		Theme:              req.Theme,
		EmailNotifications: req.EmailNotifications,
	})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, prefs)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=8,max=128"`
}

// ChangePassword handles POST /api/v1/profile/change-password.
//
// Mounted behind apikeys.RequireUserSession: an API key must never be able to
// rotate its owner's password, which would let a single leaked key take over the
// account permanently.
func (h *Handler) ChangePassword(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}

	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		reqctx.BadRequest(c, err.Error())
		return
	}

	if err := h.svc.ChangePassword(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionPasswordChanged),
		caller.TenantID, caller.UserID, req.CurrentPassword, req.NewPassword); err != nil {
		reqctx.RespondError(c, err)
		return
	}

	apiresponse.Success(c, http.StatusOK, gin.H{
		"message": "password changed; other sessions have been signed out",
	})
}

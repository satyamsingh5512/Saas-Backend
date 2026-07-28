package authz

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Handler is the thin Gin binding layer for role/permission administration.
// Every route here must be gated by RequirePermission(PermRoleManage) (wired
// in internal/routes), since these endpoints can grant/revoke access to
// everything else in the tenant.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func respondErr(c *gin.Context, err error) {
	if appErr, ok := apperror.As(err); ok {
		apiresponse.Error(c, appErr.Code.HTTPStatus(), string(appErr.Code), appErr.Message)
		return
	}
	apiresponse.Error(c, http.StatusInternalServerError, string(apperror.CodeInternal), "internal server error")
}

// ListRoles handles GET /api/v1/roles.
func (h *Handler) ListRoles(c *gin.Context) {
	roles, err := h.svc.ListRoles(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, roles)
}

// ListPermissionCatalog handles GET /api/v1/permissions: the global catalog
// used to build role-editing UI (checkboxes for each grantable permission).
func (h *Handler) ListPermissionCatalog(c *gin.Context) {
	perms, err := h.svc.ListPermissionCatalog(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, perms)
}

type createRoleRequest struct {
	Name            string   `json:"name" binding:"required,min=1,max=100"`
	Description     string   `json:"description"`
	PermissionCodes []string `json:"permission_codes"`
}

// CreateRole handles POST /api/v1/roles.
func (h *Handler) CreateRole(c *gin.Context) {
	tenantID, ok := tenantIDFromGin(c)
	if !ok {
		apiresponse.Error(c, http.StatusUnauthorized, string(apperror.CodeUnauthorized), "authentication required")
		return
	}

	var req createRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}

	role, err := h.svc.CreateRole(c.Request.Context(), tenantID, req.Name, req.Description, req.PermissionCodes)
	if err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusCreated, role)
}

type updateRolePermissionsRequest struct {
	PermissionCodes []string `json:"permission_codes" binding:"required"`
}

// UpdateRolePermissions handles PUT /api/v1/roles/:roleID/permissions.
// Deliberately the ONLY mutation exposed for existing roles (see
// Service.UpdateRolePermissions for why system roles can have permissions
// edited but never their slug/IsSystem flag).
func (h *Handler) UpdateRolePermissions(c *gin.Context) {
	tenantID, ok := tenantIDFromGin(c)
	if !ok {
		apiresponse.Error(c, http.StatusUnauthorized, string(apperror.CodeUnauthorized), "authentication required")
		return
	}
	roleID, err := uuid.Parse(c.Param("roleID"))
	if err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), "invalid role id")
		return
	}

	var req updateRolePermissionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}

	if err := h.svc.UpdateRolePermissions(c.Request.Context(), tenantID, roleID, req.PermissionCodes); err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "role permissions updated"})
}

// DeleteRole handles DELETE /api/v1/roles/:roleID.
func (h *Handler) DeleteRole(c *gin.Context) {
	roleID, err := uuid.Parse(c.Param("roleID"))
	if err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), "invalid role id")
		return
	}
	if err := h.svc.DeleteRole(c.Request.Context(), roleID); err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "role deleted"})
}

type assignRoleRequest struct {
	UserID uuid.UUID `json:"user_id" binding:"required"`
	RoleID uuid.UUID `json:"role_id" binding:"required"`
}

// AssignRole handles POST /api/v1/roles/assign.
func (h *Handler) AssignRole(c *gin.Context) {
	assignerID, tenantID, ok := requesterIDsFromGin(c)
	if !ok {
		apiresponse.Error(c, http.StatusUnauthorized, string(apperror.CodeUnauthorized), "authentication required")
		return
	}

	var req assignRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}

	if err := h.svc.AssignRole(c.Request.Context(), tenantID, req.UserID, req.RoleID, assignerID); err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "role assigned"})
}

// RevokeRole handles POST /api/v1/roles/revoke.
func (h *Handler) RevokeRole(c *gin.Context) {
	_, tenantID, ok := requesterIDsFromGin(c)
	if !ok {
		apiresponse.Error(c, http.StatusUnauthorized, string(apperror.CodeUnauthorized), "authentication required")
		return
	}

	var req assignRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}

	if err := h.svc.RevokeRole(c.Request.Context(), tenantID, req.UserID, req.RoleID); err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "role revoked"})
}

func tenantIDFromGin(c *gin.Context) (uuid.UUID, bool) {
	v, ok := c.Get(CtxTenantID)
	if !ok {
		return uuid.Nil, false
	}
	id, ok := v.(uuid.UUID)
	return id, ok
}

func requesterIDsFromGin(c *gin.Context) (userID, tenantID uuid.UUID, ok bool) {
	uv, ok1 := c.Get(CtxUserID)
	tv, ok2 := c.Get(CtxTenantID)
	if !ok1 || !ok2 {
		return uuid.Nil, uuid.Nil, false
	}
	userID, _ = uv.(uuid.UUID)
	tenantID, _ = tv.(uuid.UUID)
	return userID, tenantID, true
}

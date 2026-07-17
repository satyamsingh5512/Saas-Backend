package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/middleware"
	"github.com/satym-in/tenant-saas-backend/internal/models"
	"gorm.io/gorm"
)

// UserHandler groups handlers for tenant-scoped user management.
type UserHandler struct {
	DB *gorm.DB
}

func NewUserHandler(db *gorm.DB) *UserHandler {
	return &UserHandler{DB: db}
}

// ListUsers returns all users belonging to the authenticated caller's tenant.
// Tenant scoping comes strictly from the JWT claim, never from client input.
func (h *UserHandler) ListUsers(c *gin.Context) {
	tenantID, ok := c.Get(middleware.CtxTenantID)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "tenant context missing"})
		return
	}

	var users []models.User
	if err := h.DB.Where("tenant_id = ?", tenantID.(uuid.UUID)).Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch users"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"users": users})
}

// Me returns the authenticated user's own basic identity info.
func (h *UserHandler) Me(c *gin.Context) {
	userID, _ := c.Get(middleware.CtxUserID)
	tenantID, _ := c.Get(middleware.CtxTenantID)
	role, _ := c.Get(middleware.CtxRole)

	c.JSON(http.StatusOK, gin.H{
		"user_id":   userID,
		"tenant_id": tenantID,
		"role":      role,
	})
}

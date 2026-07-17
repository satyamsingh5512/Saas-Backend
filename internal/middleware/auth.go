package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/auth"
	"github.com/satym-in/tenant-saas-backend/internal/config"
)

// Context keys used to pass authenticated user/tenant info to handlers.
const (
	CtxUserID   = "user_id"
	CtxTenantID = "tenant_id"
	CtxRole     = "role"
)

// RequireAuth validates the Bearer JWT on incoming requests and injects
// the authenticated user's ID, tenant ID, and role into the request context.
// This enforces tenant isolation: every downstream handler/query is scoped
// to the tenant_id extracted from the token, not from client-supplied input.
func RequireAuth(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header format"})
			return
		}

		claims, err := auth.ParseToken(cfg.JWTSecret, parts[1])
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		c.Set(CtxUserID, claims.UserID)
		c.Set(CtxTenantID, claims.TenantID)
		c.Set(CtxRole, claims.Role)
		c.Next()
	}
}

// RequireRole restricts access to users whose role is in the allowed list.
// Must be used after RequireAuth.
func RequireRole(allowed ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get(CtxRole)
		if !exists {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "role not found in context"})
			return
		}

		roleStr, _ := role.(string)
		for _, r := range allowed {
			if r == roleStr {
				c.Next()
				return
			}
		}

		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
	}
}

package identity

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/authz"
	"github.com/satym-in/tenant-saas-backend/internal/tenancy"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// RequireAuth validates the Bearer JWT on incoming requests, injects the
// authenticated user/tenant/role into the Gin context under the keys
// authz.CtxUserID/CtxTenantID/CtxRole (shared constants so authz's
// permission middleware can read them without importing this package), and
// overrides whatever tenant tenancy.Resolver found pre-auth with the JWT's
// tenant_id claim -- rejecting the request if they disagree. This is the
// concrete implementation of the "JWT claim is the sole source of truth
// post-auth" rule from the architecture design (Phase 2.2), which exists
// specifically to prevent a valid token for tenant A being replayed with an
// X-Tenant-ID/subdomain for tenant B to read tenant B's data.
func RequireAuth(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// An earlier middleware in the chain (internal/apikeys) may have already
		// authenticated this request with an API key. Requiring a JWT as well would
		// make machine clients impossible, so this middleware stands down once some
		// credential has been validated. Authorization is unaffected: the permission
		// gates still run, and for an API key they evaluate its scopes.
		if authenticated, ok := c.Get(authz.CtxAuthenticated); ok {
			if done, _ := authenticated.(bool); done {
				c.Next()
				return
			}
		}

		header := c.GetHeader("Authorization")
		if header == "" {
			apiresponse.Error(c, apperror.CodeUnauthorized.HTTPStatus(), string(apperror.CodeUnauthorized), "missing authorization header")
			return
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			apiresponse.Error(c, apperror.CodeUnauthorized.HTTPStatus(), string(apperror.CodeUnauthorized), "invalid authorization header format")
			return
		}

		claims, err := ParseAccessToken(jwtSecret, parts[1])
		if err != nil {
			apiresponse.Error(c, apperror.CodeUnauthorized.HTTPStatus(), string(apperror.CodeUnauthorized), "invalid or expired token")
			return
		}

		if err := tenancy.OverrideFromJWT(c, claims.TenantID); err != nil {
			apiresponse.Error(c, apperror.CodeTenantMismatch.HTTPStatus(), string(apperror.CodeTenantMismatch), err.Error())
			return
		}

		c.Set(authz.CtxUserID, claims.UserID)
		c.Set(authz.CtxTenantID, claims.TenantID)
		c.Set(authz.CtxRole, claims.Role)
		c.Set(authz.CtxAuthenticated, true)
		c.Next()
	}
}

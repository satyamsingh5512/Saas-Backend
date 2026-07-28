package apikeys

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/authz"
	"github.com/satym-in/tenant-saas-backend/internal/tenancy"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Authenticate returns middleware that authenticates an API key when one is
// presented, and otherwise does nothing.
//
// It is mounted ahead of identity.RequireAuth on protected routes. A request
// carrying an API key is authenticated here and marked as such; a request carrying
// a JWT falls through untouched for the JWT middleware to handle. A request with
// neither is also passed through, so it is identity.RequireAuth that produces the
// single, consistent 401 -- this middleware never rejects for absence, only for a
// key that was presented and found invalid.
//
// Accepted forms are `X-API-Key: sk_live_...` and `Authorization: Bearer
// sk_live_...`. The bearer form is distinguished from a JWT by the key's prefix,
// so no token is ever parsed as both credential types.
func Authenticate(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		presented := presentedKey(c)
		if presented == "" {
			c.Next()
			return
		}

		auth, err := svc.Authenticate(c.Request.Context(), presented)
		if err != nil {
			apiresponse.Error(c, apperror.CodeUnauthorized.HTTPStatus(),
				string(apperror.CodeUnauthorized), "invalid api key")
			return
		}

		// The key's tenant is authoritative, overriding any tenant the pre-auth
		// resolver inferred from a header or subdomain, and rejecting the request
		// if the two disagree.
		if err := tenancy.OverrideFromCredential(c, auth.TenantID); err != nil {
			apiresponse.Error(c, apperror.CodeTenantMismatch.HTTPStatus(),
				string(apperror.CodeTenantMismatch), "api key tenant does not match resolved tenant")
			return
		}

		c.Set(authz.CtxTenantID, auth.TenantID)
		c.Set(authz.CtxAPIKeyScopes, auth.Scopes)
		c.Set(authz.CtxAuthenticated, true)
		c.Set(CtxAPIKeyID, auth.KeyID)

		// The owning user is attached only for attribution in audit records. It does
		// not widen the key's authority, which remains exactly its scope list.
		if auth.UserID != nil {
			c.Set(authz.CtxUserID, *auth.UserID)
		}

		c.Next()
	}
}

// CtxAPIKeyID identifies which key authenticated the request, for audit records.
const CtxAPIKeyID = "api_key_id"

// presentedKey extracts an API key from the request, returning "" when none is
// present.
func presentedKey(c *gin.Context) string {
	if header := strings.TrimSpace(c.GetHeader("X-API-Key")); header != "" {
		return header
	}

	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	parts := strings.SplitN(authorization, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		credential := strings.TrimSpace(parts[1])
		if LooksLikeAPIKey(credential) {
			return credential
		}
	}
	return ""
}

// RequireUserSession blocks API-key credentials from reaching an endpoint.
//
// Mounted on routes that only make sense for a human session -- changing your own
// password, managing your profile, minting further API keys. Allowing a key to
// mint keys or rotate its owner's password would turn a single leaked credential
// into permanent, self-renewing account access.
func RequireUserSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, isAPIKey := c.Get(authz.CtxAPIKeyScopes); isAPIKey {
			apiresponse.Error(c, apperror.CodeForbidden.HTTPStatus(),
				string(apperror.CodeForbidden),
				"this endpoint requires a user session and cannot be used with an API key")
			return
		}
		c.Next()
	}
}

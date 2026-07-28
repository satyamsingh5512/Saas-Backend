package authz

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Context keys shared with internal/identity's auth middleware, which sets
// these after validating a JWT. Declared here (not re-exported from
// identity) to avoid authz depending on identity's package for two string
// constants; both packages depend on the same literal keys by convention,
// documented at both definition sites.
const (
	CtxUserID   = "user_id"
	CtxTenantID = "tenant_id"
	CtxRole     = "role"

	// CtxAPIKeyScopes is set by internal/apikeys' middleware when a request was
	// authenticated with an API key instead of a user JWT. Its presence switches
	// the permission gates below from role-derived permissions to the key's
	// explicitly granted scopes.
	CtxAPIKeyScopes = "api_key_scopes"

	// CtxAuthenticated marks that some credential has already been validated for
	// this request, so a second authentication middleware in the chain stands down
	// rather than rejecting an API-key request for lacking a JWT.
	CtxAuthenticated = "authenticated"
)

// scopesFromContext returns the API key scopes attached to the request, and
// whether the request was API-key authenticated at all.
func scopesFromContext(c *gin.Context) (map[string]struct{}, bool) {
	raw, ok := c.Get(CtxAPIKeyScopes)
	if !ok {
		return nil, false
	}
	scopes, ok := raw.([]string)
	if !ok {
		return nil, false
	}

	set := make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		set[s] = struct{}{}
	}
	return set, true
}

// RequirePermission returns Gin middleware that 403s unless the
// authenticated caller holds the given permission code within their tenant.
// Must run after the identity auth middleware (which populates CtxUserID/
// CtxTenantID). This is the enforcement point for the entire dynamic RBAC
// model: it never checks a hardcoded role name, only a permission code
// resolved from the database/cache at request time, so role/permission
// edits made through the admin API take effect immediately (subject to
// cache TTL/invalidation) without redeploying code.
func (s *Service) RequirePermission(code string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// An API-key request is authorized purely from the key's scopes. The key's
		// owning user is deliberately not consulted: a key must not silently gain
		// capability when its creator is promoted, nor keep it after they are
		// demoted. The scope list is the whole grant.
		if scopes, isAPIKey := scopesFromContext(c); isAPIKey {
			if _, ok := scopes[code]; !ok {
				apiresponse.Error(c, apperror.CodeForbidden.HTTPStatus(), string(apperror.CodeForbidden),
					"api key is not scoped for "+code)
				return
			}
			c.Next()
			return
		}

		userIDVal, ok := c.Get(CtxUserID)
		if !ok {
			apiresponse.Error(c, apperror.CodeUnauthorized.HTTPStatus(), string(apperror.CodeUnauthorized), "authentication required")
			return
		}
		tenantIDVal, ok := c.Get(CtxTenantID)
		if !ok {
			apiresponse.Error(c, apperror.CodeUnauthorized.HTTPStatus(), string(apperror.CodeUnauthorized), "tenant context missing")
			return
		}

		userID, _ := userIDVal.(uuid.UUID)
		tenantID, _ := tenantIDVal.(uuid.UUID)

		allowed, err := s.HasPermission(c.Request.Context(), tenantID, userID, code)
		if err != nil {
			apiresponse.Error(c, apperror.CodeInternal.HTTPStatus(), string(apperror.CodeInternal), "failed to check permissions")
			return
		}
		if !allowed {
			apiresponse.Error(c, apperror.CodeForbidden.HTTPStatus(), string(apperror.CodeForbidden), "insufficient permissions: requires "+code)
			return
		}
		c.Next()
	}
}

// RequireAnyPermission allows the request through if the caller holds AT
// LEAST ONE of the given permission codes (logical OR), useful for
// endpoints reachable via more than one capability (e.g. viewing a project
// is allowed for project:view OR project:manage holders).
func (s *Service) RequireAnyPermission(codes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if scopes, isAPIKey := scopesFromContext(c); isAPIKey {
			for _, code := range codes {
				if _, ok := scopes[code]; ok {
					c.Next()
					return
				}
			}
			apiresponse.Error(c, apperror.CodeForbidden.HTTPStatus(), string(apperror.CodeForbidden),
				"api key is not scoped for this operation")
			return
		}

		userIDVal, ok := c.Get(CtxUserID)
		if !ok {
			apiresponse.Error(c, apperror.CodeUnauthorized.HTTPStatus(), string(apperror.CodeUnauthorized), "authentication required")
			return
		}
		tenantIDVal, ok := c.Get(CtxTenantID)
		if !ok {
			apiresponse.Error(c, apperror.CodeUnauthorized.HTTPStatus(), string(apperror.CodeUnauthorized), "tenant context missing")
			return
		}

		userID, _ := userIDVal.(uuid.UUID)
		tenantID, _ := tenantIDVal.(uuid.UUID)

		granted, err := s.UserPermissions(c.Request.Context(), tenantID, userID)
		if err != nil {
			apiresponse.Error(c, apperror.CodeInternal.HTTPStatus(), string(apperror.CodeInternal), "failed to check permissions")
			return
		}
		grantedSet := make(map[string]struct{}, len(granted))
		for _, g := range granted {
			grantedSet[g] = struct{}{}
		}
		for _, code := range codes {
			if _, ok := grantedSet[code]; ok {
				c.Next()
				return
			}
		}
		apiresponse.Error(c, apperror.CodeForbidden.HTTPStatus(), string(apperror.CodeForbidden), "insufficient permissions")
	}
}

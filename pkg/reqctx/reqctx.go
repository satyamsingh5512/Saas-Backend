// Package reqctx centralizes extraction of the authenticated caller's identity
// from a Gin context, along with the standard error-to-envelope translation
// every handler performs.
//
// Without this, each module re-declares its own tenantIDFromGin/respondErr pair
// (as internal/authz does), and each copy is an opportunity for one of them to
// diverge -- for example by defaulting a missing tenant to uuid.Nil and querying
// with it instead of rejecting the request. Cross-tenant safety depends on that
// decision being made identically everywhere, so it lives in exactly one place.
package reqctx

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Context keys populated by identity.RequireAuth after JWT validation. These
// mirror the constants in internal/authz (CtxUserID/CtxTenantID/CtxRole); both
// definitions intentionally describe the same literal keys so neither package
// needs to import the other.
const (
	CtxUserID   = "user_id"
	CtxTenantID = "tenant_id"
	CtxRole     = "role"
)

// Caller is the authenticated principal behind a request.
type Caller struct {
	UserID   uuid.UUID
	TenantID uuid.UUID
	Role     string
}

// CallerFrom extracts the authenticated caller. ok is false when either ID is
// absent or not a UUID, which callers must treat as "reject the request" -- never
// as "proceed with a zero tenant", since a zero tenant paired with a query that
// forgot its own WHERE clause is exactly the cross-tenant read RLS exists to stop.
func CallerFrom(c *gin.Context) (Caller, bool) {
	userVal, userOK := c.Get(CtxUserID)
	tenantVal, tenantOK := c.Get(CtxTenantID)
	if !userOK || !tenantOK {
		return Caller{}, false
	}

	userID, ok := userVal.(uuid.UUID)
	if !ok || userID == uuid.Nil {
		return Caller{}, false
	}
	tenantID, ok := tenantVal.(uuid.UUID)
	if !ok || tenantID == uuid.Nil {
		return Caller{}, false
	}

	caller := Caller{UserID: userID, TenantID: tenantID}
	if roleVal, ok := c.Get(CtxRole); ok {
		caller.Role, _ = roleVal.(string)
	}
	return caller, true
}

// RequireCaller extracts the caller, writing a 401 envelope and returning
// ok=false when the request is not authenticated. Handlers return immediately
// when ok is false.
func RequireCaller(c *gin.Context) (Caller, bool) {
	caller, ok := CallerFrom(c)
	if !ok {
		apiresponse.Error(c, http.StatusUnauthorized,
			string(apperror.CodeUnauthorized), "authentication required")
		return Caller{}, false
	}
	return caller, true
}

// RespondError translates a service-layer error into the API error envelope,
// mapping typed *apperror.Error values to their HTTP status and collapsing
// everything else to a generic 500. Unrecognized errors never have their text
// returned to the client, since they routinely wrap database messages
// containing schema and query details.
func RespondError(c *gin.Context, err error) {
	if appErr, ok := apperror.As(err); ok {
		apiresponse.Error(c, appErr.Code.HTTPStatus(), string(appErr.Code), appErr.Message)
		return
	}
	apiresponse.Error(c, http.StatusInternalServerError,
		string(apperror.CodeInternal), "internal server error")
}

// BadRequest writes a 400 validation envelope, used for malformed bodies and
// unparseable path/query parameters.
func BadRequest(c *gin.Context, message string) {
	apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), message)
}

// UUIDParam parses a UUID path parameter, writing a 400 and returning ok=false
// when it is malformed.
func UUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		BadRequest(c, "invalid "+name)
		return uuid.Nil, false
	}
	return id, true
}

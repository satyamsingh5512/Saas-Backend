package identity

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/authz"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Handler is the thin Gin binding layer for the identity module: it parses
// requests, calls Service, and translates results/errors into the
// standardized API response envelope. No business logic lives here, per
// the Clean Architecture rule stated in the project brief.
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

// Register handles POST /api/v1/auth/register.
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}

	resp, err := h.svc.Register(c.Request.Context(), req)
	if err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusCreated, resp)
}

// Login handles POST /api/v1/auth/login.
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}

	resp, err := h.svc.Login(c.Request.Context(), req)
	if err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, resp)
}

// Refresh handles POST /api/v1/auth/refresh.
func (h *Handler) Refresh(c *gin.Context) {
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}

	tokens, err := h.svc.Refresh(c.Request.Context(), req)
	if err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, tokens)
}

// Logout handles POST /api/v1/auth/logout.
func (h *Handler) Logout(c *gin.Context) {
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}
	_ = h.svc.Logout(c.Request.Context(), req.RefreshToken)
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "logged out"})
}

// ForgotPassword handles POST /api/v1/auth/forgot-password. Always returns
// 200 with a generic message regardless of whether the email exists, to
// avoid leaking account existence.
func (h *Handler) ForgotPassword(c *gin.Context) {
	var req ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}
	_ = h.svc.RequestPasswordReset(c.Request.Context(), req)
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "if an account exists, a password reset email has been sent"})
}

// ResetPassword handles POST /api/v1/auth/reset-password.
func (h *Handler) ResetPassword(c *gin.Context) {
	var req ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}
	if err := h.svc.ResetPassword(c.Request.Context(), req); err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "password has been reset"})
}

// RequestEmailVerification handles POST /api/v1/auth/verify-email/request (authenticated).
func (h *Handler) RequestEmailVerification(c *gin.Context) {
	userID, tenantID, ok := requesterIDs(c)
	if !ok {
		apiresponse.Error(c, http.StatusUnauthorized, string(apperror.CodeUnauthorized), "authentication required")
		return
	}
	if err := h.svc.RequestEmailVerification(c.Request.Context(), tenantID, userID); err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "verification email sent"})
}

// VerifyEmail handles POST /api/v1/auth/verify-email.
func (h *Handler) VerifyEmail(c *gin.Context) {
	var req VerifyEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), err.Error())
		return
	}
	if err := h.svc.VerifyEmail(c.Request.Context(), req); err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "email verified"})
}

// Me handles GET /api/v1/me (authenticated): returns the caller's own identity.
func (h *Handler) Me(c *gin.Context) {
	userID, _, ok := requesterIDs(c)
	if !ok {
		apiresponse.Error(c, http.StatusUnauthorized, string(apperror.CodeUnauthorized), "authentication required")
		return
	}
	// c.Request.Context() already carries the tenant ID attached by
	// tenancy.OverrideFromJWT inside RequireAuth, so repository calls made
	// with it are correctly RLS-scoped without re-deriving anything here.
	user, err := h.svc.repo.FindByID(c.Request.Context(), userID)
	if err != nil {
		apiresponse.Error(c, http.StatusNotFound, string(apperror.CodeNotFound), "user not found")
		return
	}
	apiresponse.Success(c, http.StatusOK, ToUserResponse(user))
}

func requesterIDs(c *gin.Context) (userID, tenantID uuid.UUID, ok bool) {
	uv, ok1 := c.Get(authz.CtxUserID)
	tv, ok2 := c.Get(authz.CtxTenantID)
	if !ok1 || !ok2 {
		return uuid.Nil, uuid.Nil, false
	}
	userID, _ = uv.(uuid.UUID)
	tenantID, _ = tv.(uuid.UUID)
	return userID, tenantID, true
}

// ListUsers handles GET /api/v1/users (authenticated): lists users in the
// caller's tenant. Tenant scoping comes strictly from the JWT claim via
// RequireAuth + tenancy.OverrideFromJWT, never from client-supplied input.
func (h *Handler) ListUsers(c *gin.Context) {
	page := parseIntQuery(c, "page", 1)
	pageSize := parseIntQuery(c, "page_size", 20)

	users, total, err := h.svc.ListUsers(c.Request.Context(), page, pageSize)
	if err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, users, apiresponse.NewPagination(page, pageSize, total))
}

func parseIntQuery(c *gin.Context, key string, def int) int {
	v := c.Query(key)
	if v == "" {
		return def
	}
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

// OAuthAuthorize handles GET /api/v1/auth/oauth/:provider: redirects the
// browser to the provider's consent screen. tenant_slug is required as a
// query param since this route runs before authentication and the
// tenancy.Resolver middleware only resolves a tenant from subdomain/header,
// neither of which a plain "sign in with GitHub" link naturally
// carries.
func (h *Handler) OAuthAuthorize(c *gin.Context) {
	provider := c.Param("provider")
	tenantSlug := c.Query("tenant_slug")
	if tenantSlug == "" {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), "tenant_slug query parameter is required")
		return
	}

	url, err := h.svc.OAuthAuthorizeURL(provider, tenantSlug)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.Redirect(http.StatusTemporaryRedirect, url)
}

// OAuthCallback handles GET /api/v1/auth/oauth/:provider/callback: the
// provider redirects the browser back here with `code` and `state` query
// params after the user approves access.
func (h *Handler) OAuthCallback(c *gin.Context) {
	provider := c.Param("provider")
	code := c.Query("code")
	state := c.Query("state")
	if code == "" || state == "" {
		apiresponse.Error(c, http.StatusBadRequest, string(apperror.CodeValidation), "missing code or state")
		return
	}

	resp, err := h.svc.OAuthCallback(c.Request.Context(), provider, code, state)
	if err != nil {
		respondErr(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, resp)
}

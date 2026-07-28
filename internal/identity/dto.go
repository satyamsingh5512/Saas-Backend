package identity

import "time"

// RegisterRequest is the payload for POST /api/v1/auth/register: creates a
// new tenant plus its first user (who is assigned the Owner role).
type RegisterRequest struct {
	TenantName string `json:"tenant_name" binding:"required,min=2,max=255"`
	TenantSlug string `json:"tenant_slug" binding:"required,min=2,max=63,alphanum"`
	Email      string `json:"email" binding:"required,email"`
	Password   string `json:"password" binding:"required,min=8,max=128"`
	FullName   string `json:"full_name" binding:"required,min=1,max=255"`
}

// LoginRequest is the payload for POST /api/v1/auth/login.
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
	// TenantSlug is optional: if omitted, the service looks up all tenants
	// the email belongs to; if exactly one match exists it proceeds, if
	// multiple exist it asks the client to disambiguate.
	TenantSlug string `json:"tenant_slug,omitempty"`
}

// RefreshRequest is the payload for POST /api/v1/auth/refresh.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// ForgotPasswordRequest is the payload for POST /api/v1/auth/forgot-password.
type ForgotPasswordRequest struct {
	Email      string `json:"email" binding:"required,email"`
	TenantSlug string `json:"tenant_slug" binding:"required"`
}

// ResetPasswordRequest is the payload for POST /api/v1/auth/reset-password.
type ResetPasswordRequest struct {
	Token       string `json:"token" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=8,max=128"`
}

// VerifyEmailRequest is the payload for POST /api/v1/auth/verify-email.
type VerifyEmailRequest struct {
	Token string `json:"token" binding:"required"`
}

// TokenPair is the response payload for register/login/refresh: a
// short-lived access token plus a long-lived, rotatable refresh token.
type TokenPair struct {
	AccessToken           string    `json:"access_token"`
	RefreshToken          string    `json:"refresh_token"`
	AccessTokenExpiresAt  time.Time `json:"access_token_expires_at"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
	TokenType             string    `json:"token_type"`
}

// UserResponse is the public-facing representation of a user.
type UserResponse struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
	Email           string     `json:"email"`
	FullName        string     `json:"full_name"`
	Status          string     `json:"status"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

func ToUserResponse(u *User) UserResponse {
	return UserResponse{
		ID:              u.ID.String(),
		TenantID:        u.TenantID.String(),
		Email:           u.Email,
		FullName:        u.FullName,
		Status:          u.Status,
		EmailVerifiedAt: u.EmailVerifiedAt,
		CreatedAt:       u.CreatedAt,
	}
}

// RegisterResponse bundles the created tenant/user summary with a token pair.
type RegisterResponse struct {
	TenantID   string       `json:"tenant_id"`
	TenantSlug string       `json:"tenant_slug"`
	User       UserResponse `json:"user"`
	Tokens     TokenPair    `json:"tokens"`
}

// LoginResponse mirrors RegisterResponse's shape for a successful login, or
// carries a disambiguation list when the email matches multiple tenants.
type LoginResponse struct {
	User             UserResponse   `json:"user"`
	Tokens           TokenPair      `json:"tokens"`
	AmbiguousTenants []TenantChoice `json:"ambiguous_tenants,omitempty"`
}

// TenantChoice is offered to the client when a login email matches users in
// more than one tenant, so the client can re-submit with tenant_slug set.
type TenantChoice struct {
	TenantSlug string `json:"tenant_slug"`
	TenantName string `json:"tenant_name"`
}

package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
)

// OAuthConfig holds the client credentials for each supported provider.
// A provider with an empty ClientID is treated as disabled: its login/
// callback routes return a clear error rather than the server refusing to
// start, so environments that haven't configured OAuth yet remain usable
// for the rest of the API.
type OAuthConfig struct {
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	GitHubClientID     string
	GitHubClientSecret string
	GitHubRedirectURL  string
	// StateSecret signs the OAuth `state` parameter (HMAC-SHA256) so it
	// can safely carry the initiating tenant slug through the redirect to
	// the provider and back, without a server-side session store: the
	// provider round-trips the state value verbatim, and the callback
	// verifies the HMAC before trusting its contents (CSRF protection +
	// tamper-evidence, standard OAuth2 state-parameter practice).
	StateSecret string
}

func (c OAuthConfig) googleOAuth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.GoogleClientID,
		ClientSecret: c.GoogleClientSecret,
		RedirectURL:  c.GoogleRedirectURL,
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}
}

func (c OAuthConfig) githubOAuth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.GitHubClientID,
		ClientSecret: c.GitHubClientSecret,
		RedirectURL:  c.GitHubRedirectURL,
		Scopes:       []string{"read:user", "user:email"},
		Endpoint:     github.Endpoint,
	}
}

// oauthState is the payload signed into the `state` query parameter.
type oauthState struct {
	TenantSlug string `json:"tenant_slug"`
	Nonce      string `json:"nonce"`
}

func (s *Service) encodeOAuthState(st oauthState) (string, error) {
	payload, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	sig := hmacSign(s.oauthCfg.StateSecret, payload)
	combined := append(payload, '.')
	combined = append(combined, []byte(sig)...)
	return base64.RawURLEncoding.EncodeToString(combined), nil
}

func (s *Service) decodeOAuthState(raw string) (oauthState, error) {
	var st oauthState
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return st, errors.New("invalid state")
	}
	sepIdx := -1
	for i := len(decoded) - 1; i >= 0; i-- {
		if decoded[i] == '.' {
			sepIdx = i
			break
		}
	}
	if sepIdx == -1 {
		return st, errors.New("invalid state")
	}
	payload, sig := decoded[:sepIdx], decoded[sepIdx+1:]
	if !hmac.Equal([]byte(hmacSign(s.oauthCfg.StateSecret, payload)), sig) {
		return st, errors.New("state signature mismatch")
	}
	if err := json.Unmarshal(payload, &st); err != nil {
		return st, errors.New("invalid state payload")
	}
	return st, nil
}

func hmacSign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// OAuthAuthorizeURL builds the provider redirect URL for starting an OAuth2
// login, embedding the initiating tenant's slug (so the callback knows
// which tenant to create/attach the user under) in a signed state param.
func (s *Service) OAuthAuthorizeURL(provider, tenantSlug string) (string, error) {
	nonce, err := GenerateOpaqueToken()
	if err != nil {
		return "", apperror.Wrap(apperror.CodeInternal, "failed to build oauth state", err)
	}
	state, err := s.encodeOAuthState(oauthState{TenantSlug: tenantSlug, Nonce: nonce})
	if err != nil {
		return "", apperror.Wrap(apperror.CodeInternal, "failed to build oauth state", err)
	}

	switch provider {
	case ProviderGoogle:
		if s.oauthCfg.GoogleClientID == "" {
			return "", apperror.New(apperror.CodeUnprocessable, "google oauth is not configured")
		}
		return s.oauthCfg.googleOAuth2Config().AuthCodeURL(state), nil
	case ProviderGitHub:
		if s.oauthCfg.GitHubClientID == "" {
			return "", apperror.New(apperror.CodeUnprocessable, "github oauth is not configured")
		}
		return s.oauthCfg.githubOAuth2Config().AuthCodeURL(state), nil
	default:
		return "", apperror.New(apperror.CodeUnprocessable, "unsupported oauth provider")
	}
}

// providerProfile is the normalized identity info extracted from each
// provider's distinct userinfo API response shape.
type providerProfile struct {
	ProviderUserID string
	Email          string
	FullName       string
	AvatarURL      string
}

func fetchGoogleProfile(ctx context.Context, token *oauth2.Token, cfg *oauth2.Config) (*providerProfile, error) {
	client := cfg.Client(ctx, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("google userinfo: status %d: %s", resp.StatusCode, body)
	}
	var payload struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &providerProfile{ProviderUserID: payload.Sub, Email: payload.Email, FullName: payload.Name, AvatarURL: payload.Picture}, nil
}

func fetchGitHubProfile(ctx context.Context, token *oauth2.Token, cfg *oauth2.Config) (*providerProfile, error) {
	client := cfg.Client(ctx, token)
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github user: status %d: %s", resp.StatusCode, body)
	}
	var payload struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	email := payload.Email
	if email == "" {
		// GitHub only returns a primary email in /user if the user has made
		// it public; otherwise it must be fetched from /user/emails
		// separately (requires the user:email scope, which we request).
		emailResp, err := client.Get("https://api.github.com/user/emails")
		if err == nil {
			defer emailResp.Body.Close()
			var emails []struct {
				Email    string `json:"email"`
				Primary  bool   `json:"primary"`
				Verified bool   `json:"verified"`
			}
			if json.NewDecoder(emailResp.Body).Decode(&emails) == nil {
				for _, e := range emails {
					if e.Primary && e.Verified {
						email = e.Email
						break
					}
				}
			}
		}
	}

	name := payload.Name
	if name == "" {
		name = payload.Login
	}
	return &providerProfile{
		ProviderUserID: fmt.Sprintf("%d", payload.ID),
		Email:          email,
		FullName:       name,
		AvatarURL:      payload.AvatarURL,
	}, nil
}

// OAuthCallback exchanges an authorization code for a provider profile,
// then either links it to an existing user (matched by email within the
// initiating tenant), creates a new user, or logs in an already-linked
// user -- and issues a token pair in every case. This is the single entry
// point for both providers; provider-specific quirks are isolated in
// fetchGoogleProfile/fetchGitHubProfile above.
func (s *Service) OAuthCallback(ctx context.Context, provider, code, stateRaw string) (*LoginResponse, error) {
	state, err := s.decodeOAuthState(stateRaw)
	if err != nil {
		return nil, apperror.New(apperror.CodeValidation, "invalid oauth state")
	}

	tenant, err := s.tenantRepo.FindBySlug(ctx, state.TenantSlug)
	if err != nil {
		return nil, apperror.New(apperror.CodeNotFound, "tenant not found")
	}
	if tenant.Status != "active" {
		return nil, apperror.New(apperror.CodeForbidden, "organization is not active")
	}

	var oauth2Cfg *oauth2.Config
	switch provider {
	case ProviderGoogle:
		oauth2Cfg = s.oauthCfg.googleOAuth2Config()
	case ProviderGitHub:
		oauth2Cfg = s.oauthCfg.githubOAuth2Config()
	default:
		return nil, apperror.New(apperror.CodeValidation, "unsupported oauth provider")
	}

	token, err := oauth2Cfg.Exchange(ctx, code)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeUnauthorized, "failed to exchange oauth code", err)
	}

	var profile *providerProfile
	switch provider {
	case ProviderGoogle:
		profile, err = fetchGoogleProfile(ctx, token, oauth2Cfg)
	case ProviderGitHub:
		profile, err = fetchGitHubProfile(ctx, token, oauth2Cfg)
	}
	if err != nil || profile == nil || profile.Email == "" {
		return nil, apperror.Wrap(apperror.CodeUnauthorized, "failed to fetch oauth profile", err)
	}

	scopedCtx := ctxWithTenantID(ctx, tenant.ID)

	// Already linked? Log the existing user in directly.
	if existing, err := s.repo.FindOAuthAccount(ctx, provider, profile.ProviderUserID); err == nil {
		if existing.TenantID != tenant.ID {
			return nil, apperror.New(apperror.CodeUnauthorized, "oauth identity is linked to another organization")
		}
		if err := s.repo.ValidateCredentialState(ctx, existing.TenantID, existing.UserID); err != nil {
			return nil, apperror.New(apperror.CodeForbidden, "account or organization is not active")
		}
		user, err := s.repo.FindByID(ctxWithTenantID(ctx, existing.TenantID), existing.UserID)
		if err != nil {
			return nil, apperror.New(apperror.CodeUnauthorized, "linked account no longer exists")
		}
		roleSlug, _ := s.roles.PrimaryRoleSlug(ctxWithTenantID(ctx, existing.TenantID), user.ID)
		tokens, err := s.issueTokenPair(ctxWithTenantID(ctx, existing.TenantID), existing.TenantID, user, roleSlug)
		if err != nil {
			return nil, err
		}
		return &LoginResponse{User: ToUserResponse(user), Tokens: *tokens}, nil
	}

	// Not yet linked: match by email within the initiating tenant (account
	// linking), or provision a brand-new user if no match exists.
	var user *User
	if existingUser, err := s.repo.FindByEmailInTenant(scopedCtx, profile.Email); err == nil {
		if existingUser.Status == StatusDisabled {
			return nil, apperror.New(apperror.CodeForbidden, "account is disabled")
		}
		user = existingUser
	} else {
		user = &User{
			ID:       uuid.New(),
			TenantID: tenant.ID,
			Email:    profile.Email,
			FullName: profile.FullName,
			Status:   StatusActive,
		}
		if profile.AvatarURL != "" {
			user.AvatarURL = &profile.AvatarURL
		}
		now := time.Now()
		user.EmailVerifiedAt = &now // OAuth providers have already verified the email
		if err := s.repo.Create(scopedCtx, user); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to create user", err)
		}
		if err := s.roles.AssignSystemRole(scopedCtx, tenant.ID, user.ID, "member"); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to assign default role", err)
		}
	}

	acct := &OAuthAccount{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		UserID:         user.ID,
		Provider:       provider,
		ProviderUserID: profile.ProviderUserID,
	}
	if err := s.repo.CreateOAuthAccount(scopedCtx, tenant.ID, acct); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to link oauth account", err)
	}

	if err := s.repo.ValidateCredentialState(ctx, tenant.ID, user.ID); err != nil {
		return nil, apperror.New(apperror.CodeForbidden, "account or organization is not active")
	}
	roleSlug, _ := s.roles.PrimaryRoleSlug(scopedCtx, user.ID)
	tokens, err := s.issueTokenPair(scopedCtx, tenant.ID, user, roleSlug)
	if err != nil {
		return nil, err
	}
	return &LoginResponse{User: ToUserResponse(user), Tokens: *tokens}, nil
}

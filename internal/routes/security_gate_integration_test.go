package routes_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/internal/db"
	"github.com/satym-in/tenant-saas-backend/internal/identity"
	"gorm.io/gorm"
)

type registeredSession struct {
	TenantID     string
	UserID       string
	Email        string
	AccessToken  string
	RefreshToken string
}

func registerSession(t *testing.T, router *gin.Engine, slug string) registeredSession {
	t.Helper()
	email := fmt.Sprintf("owner-%s@example.com", slug)
	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"tenant_name": "Security Gate Co", "tenant_slug": slug, "email": email,
		"password": "supersecret123", "full_name": "Owner User",
	}, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("register failed: %d %s", w.Code, w.Body.String())
	}
	data := dataObject(t, w)
	user, _ := data["user"].(map[string]interface{})
	tokens, _ := data["tokens"].(map[string]interface{})
	return registeredSession{
		TenantID: data["tenant_id"].(string), UserID: user["id"].(string), Email: email,
		AccessToken: tokens["access_token"].(string), RefreshToken: tokens["refresh_token"].(string),
	}
}

func inviteAndLogin(t *testing.T, router *gin.Engine, ownerToken, tenantSlug, roleSlug, label string) (string, string) {
	t.Helper()
	// The local part comes from the role slug, not the display label: labels
	// carry a space ("Admin User"), which the handler's `email` binding tag
	// correctly rejects with a 400 before the invitation is ever created.
	email := fmt.Sprintf("%s-%s@example.com", roleSlug, tenantSlug)
	w := doJSON(router, http.MethodPost, "/api/v1/invitations", map[string]string{
		"email": email, "role_slug": roleSlug,
	}, ownerToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create %s invitation failed: %d %s", roleSlug, w.Code, w.Body.String())
	}
	inviteToken, _ := dataObject(t, w)["token"].(string)
	w = doJSON(router, http.MethodPost, "/api/v1/invitations/accept", map[string]string{
		"token": inviteToken, "full_name": label, "password": "supersecret123",
	}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("accept %s invitation failed: %d %s", roleSlug, w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": "supersecret123", "tenant_slug": tenantSlug,
	}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("login %s failed: %d %s", roleSlug, w.Code, w.Body.String())
	}
	token, _ := dataObject(t, w)["tokens"].(map[string]interface{})["access_token"].(string)
	w = doJSON(router, http.MethodGet, "/api/v1/me", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("load %s user failed: %d %s", roleSlug, w.Code, w.Body.String())
	}
	userID, _ := dataObject(t, w)["id"].(string)
	return token, userID
}

func signedOAuthState(t *testing.T, secret, tenantSlug string) string {
	t.Helper()
	payload, err := json.Marshal(struct {
		TenantSlug string `json:"tenant_slug"`
		Nonce      string `json:"nonce"`
	}{TenantSlug: tenantSlug, Nonce: "test-nonce"})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	combined := append(append(append([]byte(nil), payload...), '.'), signature...)
	return base64.RawURLEncoding.EncodeToString(combined)
}

func migrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	loadRootEnv()
	cfg := config.Load()
	cfg.ApplyMigrationOverrides()
	database, err := db.Connect(cfg)
	if err != nil {
		t.Fatalf("connect migration database: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := database.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return database
}

func TestRBACAntiEscalationAndLastOwner(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("rbacgate")
	defer cleanupDomainTenant(t, database, slug)

	ownerToken, _ := registerOwner(t, router, slug)
	adminToken, _ := inviteAndLogin(t, router, ownerToken, slug, "admin", "Admin User")
	_, managerID := inviteAndLogin(t, router, ownerToken, slug, "manager", "Manager User")

	roles := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/roles", nil, ownerToken))
	roleIDs := map[string]string{}
	for _, role := range roles {
		roleIDs[role["slug"].(string)] = role["id"].(string)
	}
	ownerMe := dataObject(t, doJSON(router, http.MethodGet, "/api/v1/me", nil, ownerToken))
	ownerID := ownerMe["id"].(string)

	w := doJSON(router, http.MethodPost, "/api/v1/roles", map[string]interface{}{
		"name": "Forbidden Org Manager", "permission_codes": []string{"org:manage"},
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin delegated org:manage: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/roles/assign", map[string]string{
		"user_id": ownerID, "role_id": roleIDs["owner"],
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin managed Owner role: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/invitations", map[string]string{
		"email": "next-owner-" + slug + "@example.com", "role_slug": "owner",
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin invited an Owner: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPut, "/api/v1/roles/"+roleIDs["owner"]+"/permissions", map[string]interface{}{
		"permission_codes": []string{"org:manage", "role:manage"},
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin edited Owner role: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPut, "/api/v1/roles/"+roleIDs["owner"]+"/permissions", map[string]interface{}{
		"permission_codes": []string{"role:manage"},
	}, ownerToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("Owner role lost org:manage: %d %s", w.Code, w.Body.String())
	}
	// Remove one ordinary permission from Admin, then prove every delegation
	// path rejects that permission even though Admin still has role:manage.
	grantsResponse := dataObject(t, doJSON(router, http.MethodGet,
		"/api/v1/roles/"+roleIDs["admin"]+"/permissions", nil, ownerToken))
	adminCodesRaw, _ := grantsResponse["permission_codes"].([]interface{})
	adminCodes := make([]string, 0, len(adminCodesRaw))
	for _, raw := range adminCodesRaw {
		if code, _ := raw.(string); code != "billing:manage" {
			adminCodes = append(adminCodes, code)
		}
	}
	w = doJSON(router, http.MethodPut, "/api/v1/roles/"+roleIDs["admin"]+"/permissions", map[string]interface{}{
		"permission_codes": adminCodes, "revision": grantsResponse["revision"],
	}, ownerToken)
	if w.Code != http.StatusOK {
		t.Fatalf("remove Admin billing permission: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/roles", map[string]interface{}{
		"name": "Admin Escalation", "permission_codes": []string{"billing:manage"},
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin created a role with an unheld permission: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/invitations", map[string]string{
		"email": "peer-admin-" + slug + "@example.com", "role_slug": "admin",
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin invited a peer-ranked Admin: %d %s", w.Code, w.Body.String())
	}

	w = doJSON(router, http.MethodPost, "/api/v1/roles", map[string]interface{}{
		"name": "Billing Operator", "permission_codes": []string{"billing:manage"},
	}, ownerToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("owner created billing role: %d %s", w.Code, w.Body.String())
	}
	billingRoleID, _ := dataObject(t, w)["id"].(string)
	w = doJSON(router, http.MethodPut, "/api/v1/roles/"+billingRoleID+"/permissions", map[string]interface{}{
		"permission_codes": []string{"billing:manage"},
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin updated role with an unheld permission: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodDelete, "/api/v1/roles/"+billingRoleID, nil, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin deleted role containing unheld permission: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/roles/assign", map[string]string{
		"user_id": managerID, "role_id": billingRoleID,
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin assigned role containing unheld permission: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/roles/assign", map[string]string{
		"user_id": managerID, "role_id": billingRoleID,
	}, ownerToken)
	if w.Code != http.StatusOK {
		t.Fatalf("owner assigned billing role: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/roles/revoke", map[string]string{
		"user_id": managerID, "role_id": billingRoleID,
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin revoked role containing unheld permission: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/invitations", map[string]string{
		"email": "billing-user-" + slug + "@example.com", "role_id": billingRoleID,
	}, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin invited role containing unheld permission: %d %s", w.Code, w.Body.String())
	}

	w = doJSON(router, http.MethodPost, "/api/v1/roles/revoke", map[string]string{
		"user_id": ownerID, "role_id": roleIDs["owner"],
	}, ownerToken)
	if w.Code != http.StatusConflict {
		t.Fatalf("last Owner was revocable: %d %s", w.Code, w.Body.String())
	}
}

func TestConcurrentRefreshHasSingleWinner(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("refreshconcurrent")
	defer cleanupDomainTenant(t, database, slug)
	session := registerSession(t, router, slug)

	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w := doJSON(router, http.MethodPost, "/api/v1/auth/refresh", map[string]string{
				"refresh_token": session.RefreshToken,
			}, "")
			statuses <- w.Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)

	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusUnauthorized] != 1 {
		t.Fatalf("concurrent refresh statuses = %#v, want one 200 and one 401", counts)
	}
}

func TestConcurrentSingleUseTokensAndPasswordResetRevocation(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("singleusetokens")
	defer cleanupDomainTenant(t, database, slug)
	session := registerSession(t, router, slug)
	adminDB := migrationTestDB(t)

	resetPlain := "known-reset-token-" + uuid.NewString()
	resetID := uuid.New()
	if err := adminDB.Exec(`INSERT INTO verification_tokens
		(id, tenant_id, user_id, purpose, token_hash, expires_at)
		VALUES (?, ?, ?, 'password_reset', ?, ?)`, resetID, session.TenantID, session.UserID,
		identity.HashOpaqueToken(resetPlain), time.Now().Add(time.Hour)).Error; err != nil {
		t.Fatalf("insert reset token: %v", err)
	}

	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w := doJSON(router, http.MethodPost, "/api/v1/auth/reset-password", map[string]string{
				"token": resetPlain, "new_password": "new-supersecret456",
			}, "")
			statuses <- w.Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusBadRequest] != 1 {
		t.Fatalf("concurrent reset statuses = %#v, want one 200 and one 400", counts)
	}

	w := doJSON(router, http.MethodPost, "/api/v1/auth/refresh", map[string]string{
		"refresh_token": session.RefreshToken,
	}, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("password reset did not revoke refresh sessions: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": session.Email, "password": "new-supersecret456", "tenant_slug": slug,
	}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("new password login failed: %d %s", w.Code, w.Body.String())
	}

}

func TestConcurrentEmailVerificationHasSingleWinner(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("verifyconcurrent")
	defer cleanupDomainTenant(t, database, slug)
	session := registerSession(t, router, slug)
	adminDB := migrationTestDB(t)

	verifyPlain := "known-verify-token-" + uuid.NewString()
	if err := adminDB.Exec(`INSERT INTO verification_tokens
		(id, tenant_id, user_id, purpose, token_hash, expires_at)
		VALUES (?, ?, ?, 'email_verification', ?, ?)`, uuid.New(), session.TenantID, session.UserID,
		identity.HashOpaqueToken(verifyPlain), time.Now().Add(time.Hour)).Error; err != nil {
		t.Fatalf("insert verification token: %v", err)
	}

	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses <- doJSON(router, http.MethodPost, "/api/v1/auth/verify-email", map[string]string{
				"token": verifyPlain,
			}, "").Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusBadRequest] != 1 {
		t.Fatalf("concurrent verification statuses = %#v, want one 200 and one 400", counts)
	}
}

func TestProtectedCredentialsHonorTenantAndUserStatus(t *testing.T) {
	router, database, cfg := setupTestRouter(t)
	slug := uniqueSlug("credentialstate")
	defer cleanupDomainTenant(t, database, slug)
	session := registerSession(t, router, slug)
	adminDB := migrationTestDB(t)

	w := doJSON(router, http.MethodPost, "/api/v1/api-keys", map[string]interface{}{
		"name": "state-test", "scopes": []string{"project:view"},
	}, session.AccessToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create api key: %d %s", w.Code, w.Body.String())
	}
	apiKey, _ := dataObject(t, w)["secret"].(string)

	if err := adminDB.Exec("UPDATE tenants SET status = 'suspended' WHERE id = ?", session.TenantID).Error; err != nil {
		t.Fatal(err)
	}
	if w = doJSON(router, http.MethodGet, "/api/v1/me", nil, session.AccessToken); w.Code != http.StatusForbidden {
		t.Fatalf("suspended tenant accepted JWT: %d %s", w.Code, w.Body.String())
	}
	oauthState := signedOAuthState(t, cfg.JWTSecret, slug)
	if w = doJSON(router, http.MethodGet, "/api/v1/auth/oauth/github/callback?state="+oauthState+"&code=unused", nil, ""); w.Code != http.StatusForbidden {
		t.Fatalf("suspended tenant reached OAuth exchange: %d %s", w.Code, w.Body.String())
	}
	if w = doJSON(router, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": session.RefreshToken}, ""); w.Code != http.StatusForbidden {
		t.Fatalf("suspended tenant refreshed: %d %s", w.Code, w.Body.String())
	}
	if w = doJSON(router, http.MethodGet, "/api/v1/projects", nil, apiKey); w.Code != http.StatusUnauthorized {
		t.Fatalf("suspended tenant accepted API key: %d %s", w.Code, w.Body.String())
	}

	if err := adminDB.Exec("UPDATE tenants SET status = 'active' WHERE id = ?", session.TenantID).Error; err != nil {
		t.Fatal(err)
	}
	if err := adminDB.Exec("UPDATE users SET status = 'disabled' WHERE id = ?", session.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if w = doJSON(router, http.MethodGet, "/api/v1/me", nil, session.AccessToken); w.Code != http.StatusForbidden {
		t.Fatalf("disabled user accepted JWT: %d %s", w.Code, w.Body.String())
	}
	if w = doJSON(router, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": session.RefreshToken}, ""); w.Code != http.StatusForbidden {
		t.Fatalf("disabled user refreshed: %d %s", w.Code, w.Body.String())
	}
	if w = doJSON(router, http.MethodGet, "/api/v1/projects", nil, apiKey); w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user accepted API key: %d %s", w.Code, w.Body.String())
	}

	if err := adminDB.Exec("UPDATE users SET status = 'active', deleted_at = now() WHERE id = ?", session.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if w = doJSON(router, http.MethodGet, "/api/v1/me", nil, session.AccessToken); w.Code != http.StatusForbidden {
		t.Fatalf("deleted user accepted JWT: %d %s", w.Code, w.Body.String())
	}
	if w = doJSON(router, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": session.RefreshToken}, ""); w.Code != http.StatusForbidden {
		t.Fatalf("deleted user refreshed: %d %s", w.Code, w.Body.String())
	}
	if w = doJSON(router, http.MethodGet, "/api/v1/projects", nil, apiKey); w.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user accepted API key: %d %s", w.Code, w.Body.String())
	}
}

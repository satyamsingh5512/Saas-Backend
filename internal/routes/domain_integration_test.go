package routes_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/internal/db"
	"github.com/satym-in/tenant-saas-backend/internal/tenancy"
	"gorm.io/gorm"
)

// cleanupDomainTenant removes rows from the domain tables added alongside the
// teams/projects/billing modules before delegating to cleanupTenant.
//
// The explicit invitations delete is required, not defensive: invitations.role_id
// is ON DELETE RESTRICT, so cleanupTenant's `DELETE FROM roles` would be blocked by
// any invitation a test created.
func cleanupDomainTenant(t *testing.T, database *gorm.DB, slug string) {
	t.Helper()

	loadRootEnv()
	cfg := config.Load()
	cfg.ApplyMigrationOverrides()
	superuserDB, err := db.Connect(cfg)
	if err != nil {
		return
	}
	defer func() {
		if sqlDB, err := superuserDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	var tenant tenancy.Tenant
	if err := superuserDB.Where("slug = ?", slug).First(&tenant).Error; err == nil {
		for _, table := range []string{
			"invitations", "api_keys", "notifications", "activity_events",
			"audit_logs", "user_preferences", "subscriptions",
			"team_members", "project_members", "projects", "teams",
		} {
			superuserDB.Exec(fmt.Sprintf("DELETE FROM %s WHERE tenant_id = ?", table), tenant.ID)
		}
	}

	cleanupTenant(t, database, slug)
}

// registerOwner creates a tenant with an owner user and returns the owner's access
// token plus their email. Owner holds every permission, so the returned token can
// exercise every gate.
func registerOwner(t *testing.T, router *gin.Engine, slug string) (token, email string) {
	t.Helper()

	email = fmt.Sprintf("owner-%s@example.com", slug)
	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"tenant_name": "Domain Co",
		"tenant_slug": slug,
		"email":       email,
		"password":    "supersecret123",
		"full_name":   "Owner User",
	}, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("setup register failed: %d %s", w.Code, w.Body.String())
	}

	var resp envelope
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse register response: %v", err)
	}
	tokens, _ := resp.Data["tokens"].(map[string]interface{})
	token, _ = tokens["access_token"].(string)
	if token == "" {
		t.Fatalf("expected an access token: %s", w.Body.String())
	}
	return token, email
}

// dataObject extracts the `data` object from a success envelope.
func dataObject(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()

	var resp envelope
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v (body=%s)", err, w.Body.String())
	}
	if !resp.Success {
		t.Fatalf("expected success envelope, got %s", w.Body.String())
	}
	return resp.Data
}

// dataArray extracts the `data` array from a success envelope.
func dataArray(t *testing.T, w *httptest.ResponseRecorder) []map[string]interface{} {
	t.Helper()

	var resp struct {
		Success bool                     `json:"success"`
		Data    []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse list response: %v (body=%s)", err, w.Body.String())
	}
	if !resp.Success {
		t.Fatalf("expected success envelope, got %s", w.Body.String())
	}
	return resp.Data
}

func TestTeams_CRUDAndMembership(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("team")
	defer cleanupDomainTenant(t, database, slug)

	token, _ := registerOwner(t, router, slug)

	// Create, with the slug derived from the name rather than supplied.
	w := doJSON(router, http.MethodPost, "/api/v1/teams", map[string]string{
		"name":        "Platform Engineering",
		"description": "Owns the core platform",
	}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating team, got %d: %s", w.Code, w.Body.String())
	}
	team := dataObject(t, w)
	if team["slug"] != "platform-engineering" {
		t.Errorf("expected slug derived from name, got %v", team["slug"])
	}
	teamID, _ := team["id"].(string)

	// A duplicate slug must be reported as a conflict, not a 500.
	w = doJSON(router, http.MethodPost, "/api/v1/teams", map[string]string{
		"name": "Platform Engineering",
	}, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate team slug, got %d: %s", w.Code, w.Body.String())
	}

	// List returns the team with a member count.
	w = doJSON(router, http.MethodGet, "/api/v1/teams", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 listing teams, got %d: %s", w.Code, w.Body.String())
	}
	list := dataArray(t, w)
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 team, got %d: %s", len(list), w.Body.String())
	}

	// Partial update leaves unspecified fields untouched.
	w = doJSON(router, http.MethodPatch, "/api/v1/teams/"+teamID, map[string]string{
		"description": "Owns platform and infra",
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 updating team, got %d: %s", w.Code, w.Body.String())
	}
	updated := dataObject(t, w)
	if updated["name"] != "Platform Engineering" {
		t.Errorf("expected name to be preserved by partial update, got %v", updated["name"])
	}
	if updated["description"] != "Owns platform and infra" {
		t.Errorf("expected description to change, got %v", updated["description"])
	}

	// Add the owner to their own team, then confirm the roster.
	meResp := dataObject(t, doJSON(router, http.MethodGet, "/api/v1/profile", nil, token))
	ownerID, _ := meResp["user_id"].(string)

	w = doJSON(router, http.MethodPost, "/api/v1/teams/"+teamID+"/members",
		map[string]string{"user_id": ownerID}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 adding team member, got %d: %s", w.Code, w.Body.String())
	}

	members := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/teams/"+teamID+"/members", nil, token))
	if len(members) != 1 {
		t.Fatalf("expected 1 team member, got %d", len(members))
	}

	// Re-adding the same member must be idempotent, not a primary-key error.
	w = doJSON(router, http.MethodPost, "/api/v1/teams/"+teamID+"/members",
		map[string]string{"user_id": ownerID}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected repeat member add to be idempotent, got %d: %s", w.Code, w.Body.String())
	}

	// Remove, then delete the team.
	w = doJSON(router, http.MethodDelete, "/api/v1/teams/"+teamID+"/members/"+ownerID, nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 removing member, got %d: %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodDelete, "/api/v1/teams/"+teamID, nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting team, got %d: %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodGet, "/api/v1/teams/"+teamID, nil, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a deleted team, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProjects_QuotaEnforcedOnFreePlan(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("proj")
	defer cleanupDomainTenant(t, database, slug)

	token, _ := registerOwner(t, router, slug)

	// The free plan seeded in migrations/000006 allows 3 projects.
	for i := 1; i <= 3; i++ {
		w := doJSON(router, http.MethodPost, "/api/v1/projects", map[string]string{
			"name": fmt.Sprintf("Project %d", i),
		}, token)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201 creating project %d, got %d: %s", i, w.Code, w.Body.String())
		}
	}

	// The fourth must be refused by the plan quota rather than silently accepted.
	w := doJSON(router, http.MethodPost, "/api/v1/projects", map[string]string{
		"name": "Project 4",
	}, token)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 exceeding the free plan project quota, got %d: %s", w.Code, w.Body.String())
	}

	// Archiving frees no quota (archived projects still exist), but the status
	// filter must reflect the change.
	list := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/projects", nil, token))
	if len(list) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(list))
	}
	firstID, _ := list[0]["id"].(string)

	w = doJSON(router, http.MethodPatch, "/api/v1/projects/"+firstID,
		map[string]string{"status": "archived"}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 archiving project, got %d: %s", w.Code, w.Body.String())
	}

	archived := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/projects?status=archived", nil, token))
	if len(archived) != 1 {
		t.Fatalf("expected 1 archived project, got %d", len(archived))
	}

	// An unknown status is a client error, not an empty result set.
	w = doJSON(router, http.MethodGet, "/api/v1/projects?status=bogus", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid status filter, got %d: %s", w.Code, w.Body.String())
	}
}

func TestInvitations_CreatePreviewAccept(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("invite")
	defer cleanupDomainTenant(t, database, slug)

	token, _ := registerOwner(t, router, slug)

	inviteeEmail := fmt.Sprintf("invitee-%s@example.com", slug)
	w := doJSON(router, http.MethodPost, "/api/v1/invitations", map[string]string{
		"email":     inviteeEmail,
		"role_slug": "member",
	}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating invitation, got %d: %s", w.Code, w.Body.String())
	}
	created := dataObject(t, w)
	inviteToken, _ := created["token"].(string)
	if inviteToken == "" {
		t.Fatalf("expected a one-time invite token in the response: %s", w.Body.String())
	}

	// A second pending invite for the same email is a conflict, enforced by the
	// partial unique index.
	w = doJSON(router, http.MethodPost, "/api/v1/invitations", map[string]string{
		"email":     inviteeEmail,
		"role_slug": "member",
	}, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a duplicate pending invite, got %d: %s", w.Code, w.Body.String())
	}

	// Preview is unauthenticated and must resolve the tenant from the token alone,
	// which is what exercises the find_invitation_by_hash SECURITY DEFINER function.
	w = doJSON(router, http.MethodGet, "/api/v1/invitations/preview?token="+inviteToken, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 previewing invitation, got %d: %s", w.Code, w.Body.String())
	}
	preview := dataObject(t, w)
	if preview["email"] != inviteeEmail {
		t.Errorf("expected preview email %s, got %v", inviteeEmail, preview["email"])
	}
	if preview["requires_password"] != true {
		t.Errorf("expected requires_password=true for a brand-new invitee, got %v", preview["requires_password"])
	}

	// Accept creates the user and grants the invited role via accept_invitation.
	w = doJSON(router, http.MethodPost, "/api/v1/invitations/accept", map[string]string{
		"token":     inviteToken,
		"full_name": "Invited Member",
		"password":  "invitedpassword123",
	}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 accepting invitation, got %d: %s", w.Code, w.Body.String())
	}

	// Redeeming the same token twice must fail: the invite is no longer pending.
	w = doJSON(router, http.MethodPost, "/api/v1/invitations/accept", map[string]string{
		"token":     inviteToken,
		"full_name": "Invited Member",
		"password":  "invitedpassword123",
	}, "")
	if w.Code == http.StatusOK {
		t.Fatalf("expected reuse of an accepted invite token to fail, got 200: %s", w.Body.String())
	}

	// The invited user now appears in the member list.
	users := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/users", nil, token))
	if len(users) != 2 {
		t.Fatalf("expected 2 users after invite acceptance, got %d: %s", len(users), w.Body.String())
	}
}

func TestAPIKeys_ScopeEnforcementAndRevocation(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("apikey")
	defer cleanupDomainTenant(t, database, slug)

	token, _ := registerOwner(t, router, slug)

	// Mint a key scoped only to reading projects.
	w := doJSON(router, http.MethodPost, "/api/v1/api-keys", map[string]interface{}{
		"name":   "CI pipeline",
		"scopes": []string{"project:view"},
	}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating api key, got %d: %s", w.Code, w.Body.String())
	}
	created := dataObject(t, w)
	secret, _ := created["secret"].(string)
	if secret == "" {
		t.Fatalf("expected a one-time secret: %s", w.Body.String())
	}
	keyObj, _ := created["key"].(map[string]interface{})
	keyID, _ := keyObj["id"].(string)

	// An unknown scope must be rejected rather than stored.
	w = doJSON(router, http.MethodPost, "/api/v1/api-keys", map[string]interface{}{
		"name":   "Bad scope",
		"scopes": []string{"not:a:real:permission"},
	}, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown scope, got %d: %s", w.Code, w.Body.String())
	}

	// The key authenticates via X-API-Key and can read projects.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("X-API-Key", secret)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 using api key within scope, got %d: %s", rec.Code, rec.Body.String())
	}

	// The same key must be refused on an out-of-scope route.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/teams", nil)
	req.Header.Set("X-API-Key", secret)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 using api key out of scope, got %d: %s", rec.Code, rec.Body.String())
	}

	// A key must not be able to mint another key, even though its owner could.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", nil)
	req.Header.Set("X-API-Key", secret)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when an api key tries to mint a key, got %d: %s", rec.Code, rec.Body.String())
	}

	// After revocation the key stops working immediately.
	w = doJSON(router, http.MethodDelete, "/api/v1/api-keys/"+keyID, nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 revoking api key, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("X-API-Key", secret)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 using a revoked api key, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBilling_DefaultsToFreeAndReportsUsage(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("bill")
	defer cleanupDomainTenant(t, database, slug)

	token, _ := registerOwner(t, router, slug)

	// The plan catalog is public.
	plans := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/billing/plans", nil, ""))
	if len(plans) < 3 {
		t.Fatalf("expected at least 3 seeded plans, got %d", len(plans))
	}

	// A tenant with no subscription row reads as being on the free plan rather
	// than 404ing.
	view := dataObject(t, doJSON(router, http.MethodGet, "/api/v1/billing/subscription", nil, token))
	plan, _ := view["plan"].(map[string]interface{})
	if plan["code"] != "free" {
		t.Errorf("expected default plan free, got %v", plan["code"])
	}
	if view["subscription"] != nil {
		t.Errorf("expected no subscription row for a new tenant, got %v", view["subscription"])
	}

	usage := dataObject(t, doJSON(router, http.MethodGet, "/api/v1/billing/usage", nil, token))
	if usage["seats"] != float64(1) {
		t.Errorf("expected 1 seat in use, got %v", usage["seats"])
	}

	// Upgrading writes a subscription row and syncs tenants.plan_code.
	w := doJSON(router, http.MethodPost, "/api/v1/billing/subscription",
		map[string]string{"plan_code": "pro"}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 changing plan, got %d: %s", w.Code, w.Body.String())
	}
	upgraded := dataObject(t, w)
	upgradedPlan, _ := upgraded["plan"].(map[string]interface{})
	if upgradedPlan["code"] != "pro" {
		t.Errorf("expected plan pro after upgrade, got %v", upgradedPlan["code"])
	}

	// An unknown plan is a validation error.
	w = doJSON(router, http.MethodPost, "/api/v1/billing/subscription",
		map[string]string{"plan_code": "unobtainium"}, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown plan, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPreferencesProfileAndNotifications(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("prefs")
	defer cleanupDomainTenant(t, database, slug)

	token, _ := registerOwner(t, router, slug)

	// Preferences read back as defaults before anything is saved.
	prefs := dataObject(t, doJSON(router, http.MethodGet, "/api/v1/preferences", nil, token))
	if prefs["theme"] != "system" || prefs["timezone"] != "UTC" {
		t.Errorf("expected default preferences, got %v", prefs)
	}

	// Saving creates the row.
	w := doJSON(router, http.MethodPatch, "/api/v1/preferences", map[string]interface{}{
		"theme":               "dark",
		"timezone":            "Asia/Kolkata",
		"email_notifications": false,
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 updating preferences, got %d: %s", w.Code, w.Body.String())
	}
	saved := dataObject(t, w)
	if saved["theme"] != "dark" || saved["timezone"] != "Asia/Kolkata" {
		t.Errorf("expected preferences to persist, got %v", saved)
	}

	// An invalid theme is rejected by binding validation.
	w = doJSON(router, http.MethodPatch, "/api/v1/preferences",
		map[string]string{"theme": "neon"}, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid theme, got %d: %s", w.Code, w.Body.String())
	}

	// Profile includes roles and effective permissions, which the UI needs to hide
	// controls the caller cannot use.
	profile := dataObject(t, doJSON(router, http.MethodGet, "/api/v1/profile", nil, token))
	roles, _ := profile["roles"].([]interface{})
	if len(roles) != 1 || roles[0] != "owner" {
		t.Errorf("expected the owner role, got %v", profile["roles"])
	}
	perms, _ := profile["permissions"].([]interface{})
	if len(perms) == 0 {
		t.Error("expected a non-empty permission list for an owner")
	}

	// A javascript: avatar URL must be refused: it is rendered as an image source.
	w = doJSON(router, http.MethodPatch, "/api/v1/profile",
		map[string]string{"avatar_url": "javascript:alert(1)"}, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-https avatar_url, got %d: %s", w.Code, w.Body.String())
	}

	// Adding a user to a team produces an in-app notification for them.
	teamID, _ := dataObject(t, doJSON(router, http.MethodPost, "/api/v1/teams",
		map[string]string{"name": "Notify Team"}, token))["id"].(string)
	ownerID, _ := profile["user_id"].(string)

	w = doJSON(router, http.MethodPost, "/api/v1/teams/"+teamID+"/members",
		map[string]string{"user_id": ownerID}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 adding team member, got %d: %s", w.Code, w.Body.String())
	}

	notes := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/notifications", nil, token))
	if len(notes) != 1 {
		t.Fatalf("expected 1 notification after being added to a team, got %d", len(notes))
	}

	count := dataObject(t, doJSON(router, http.MethodGet, "/api/v1/notifications/unread-count", nil, token))
	if count["unread"] != float64(1) {
		t.Errorf("expected 1 unread notification, got %v", count["unread"])
	}

	w = doJSON(router, http.MethodPost, "/api/v1/notifications/read-all", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 marking all read, got %d: %s", w.Code, w.Body.String())
	}
	count = dataObject(t, doJSON(router, http.MethodGet, "/api/v1/notifications/unread-count", nil, token))
	if count["unread"] != float64(0) {
		t.Errorf("expected 0 unread after read-all, got %v", count["unread"])
	}
}

func TestAuditLog_RecordsDomainActions(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("audit")
	defer cleanupDomainTenant(t, database, slug)

	token, _ := registerOwner(t, router, slug)

	w := doJSON(router, http.MethodPost, "/api/v1/teams", map[string]string{"name": "Audited Team"}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating team, got %d: %s", w.Code, w.Body.String())
	}

	logs := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/audit-logs?action=team.created", nil, token))
	if len(logs) != 1 {
		t.Fatalf("expected 1 team.created audit entry, got %d", len(logs))
	}
	if logs[0]["action"] != "team.created" {
		t.Errorf("expected action team.created, got %v", logs[0]["action"])
	}
	// The forensic fields that make an audit trail useful must be populated.
	if logs[0]["ip_address"] == nil {
		t.Error("expected the audit entry to record a client IP")
	}
	if logs[0]["actor_id"] == nil {
		t.Error("expected the audit entry to record an actor")
	}

	// The activity feed records the same action in product-facing form.
	activity := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/activity?target_type=team", nil, token))
	if len(activity) != 1 {
		t.Fatalf("expected 1 team activity event, got %d", len(activity))
	}
	if activity[0]["verb"] != "created" {
		t.Errorf("expected verb created, got %v", activity[0]["verb"])
	}
}

func TestTeams_CrossTenantIsolation(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slugA := uniqueSlug("isoa")
	slugB := uniqueSlug("isob")
	defer cleanupDomainTenant(t, database, slugA)
	defer cleanupDomainTenant(t, database, slugB)

	tokenA, _ := registerOwner(t, router, slugA)
	tokenB, _ := registerOwner(t, router, slugB)

	teamA := dataObject(t, doJSON(router, http.MethodPost, "/api/v1/teams",
		map[string]string{"name": "Tenant A Team"}, tokenA))
	teamAID, _ := teamA["id"].(string)

	// Tenant B must not see tenant A's team in a list.
	listB := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/teams", nil, tokenB))
	if len(listB) != 0 {
		t.Fatalf("expected tenant B to see no teams, got %d", len(listB))
	}

	// Nor fetch it directly by its ID, which is the case RLS specifically defends:
	// a valid token for one tenant plus a known ID from another.
	w := doJSON(router, http.MethodGet, "/api/v1/teams/"+teamAID, nil, tokenB)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a cross-tenant team fetch, got %d: %s", w.Code, w.Body.String())
	}

	// Nor delete it.
	w = doJSON(router, http.MethodDelete, "/api/v1/teams/"+teamAID, nil, tokenB)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a cross-tenant team delete, got %d: %s", w.Code, w.Body.String())
	}

	// Tenant A still has its team intact.
	listA := dataArray(t, doJSON(router, http.MethodGet, "/api/v1/teams", nil, tokenA))
	if len(listA) != 1 {
		t.Fatalf("expected tenant A to still have 1 team, got %d", len(listA))
	}
}

package routes_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRBAC_OwnerCanManageRolesAndListPermissions proves the dynamic RBAC
// engine's enforcement middleware actually gates requests end-to-end: the
// registering user (auto-granted Owner) can list the permission catalog and
// create a custom role, both of which require role:view / role:manage.
func TestRBAC_OwnerCanManageRolesAndListPermissions(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("rbacowner")
	defer cleanupTenant(t, database, slug)

	email := "owner-" + slug + "@example.com"
	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"tenant_name": "RBAC Owner Co", "tenant_slug": slug, "email": email, "password": "supersecret123", "full_name": "Owner",
	}, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("register failed: %d %s", w.Code, w.Body.String())
	}
	var resp envelope
	json.Unmarshal(w.Body.Bytes(), &resp)
	tokens, _ := resp.Data["tokens"].(map[string]interface{})
	accessToken, _ := tokens["access_token"].(string)
	if accessToken == "" {
		t.Fatalf("expected access token: %s", w.Body.String())
	}

	// Owner should be able to list the permission catalog.
	w = doJSON(router, http.MethodGet, "/api/v1/permissions", nil, accessToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 listing permissions as Owner, got %d: %s", w.Code, w.Body.String())
	}

	// Owner should be able to list roles (should include the 5 seeded system roles).
	w = doJSON(router, http.MethodGet, "/api/v1/roles", nil, accessToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 listing roles as Owner, got %d: %s", w.Code, w.Body.String())
	}
	var rolesResp struct {
		Data []map[string]interface{} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &rolesResp)
	if len(rolesResp.Data) != 5 {
		t.Fatalf("expected 5 seeded system roles, got %d: %s", len(rolesResp.Data), w.Body.String())
	}

	// Owner should be able to create a custom role.
	w = doJSON(router, http.MethodPost, "/api/v1/roles", map[string]interface{}{
		"name": "Billing Viewer", "description": "Read-only billing access",
		"permission_codes": []string{"billing:view"},
	}, accessToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating custom role as Owner, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRBAC_GuestCannotManageRoles proves the permission gate actually
// rejects callers lacking role:manage, not just that it accepts callers
// holding it -- both directions must be tested for the enforcement claim to
// mean anything.
func TestRBAC_GuestCannotManageRoles(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("rbacguest")
	defer cleanupTenant(t, database, slug)

	ownerEmail := "owner-" + slug + "@example.com"
	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"tenant_name": "RBAC Guest Co", "tenant_slug": slug, "email": ownerEmail, "password": "supersecret123", "full_name": "Owner",
	}, "")
	var resp envelope
	json.Unmarshal(w.Body.Bytes(), &resp)
	tokens, _ := resp.Data["tokens"].(map[string]interface{})
	ownerToken, _ := tokens["access_token"].(string)

	// Find the seeded Guest role's ID.
	w = doJSON(router, http.MethodGet, "/api/v1/roles", nil, ownerToken)
	var rolesResp struct {
		Data []map[string]interface{} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &rolesResp)
	var guestRoleID string
	for _, r := range rolesResp.Data {
		if r["slug"] == "guest" {
			guestRoleID, _ = r["id"].(string)
		}
	}
	if guestRoleID == "" {
		t.Fatalf("could not find seeded guest role: %s", w.Body.String())
	}

	// Register a second user in the same tenant is not directly supported by
	// this API surface yet (invitations module is a later phase), so instead
	// we downgrade the owner's OWN role set to Guest-only by revoking Owner
	// and assigning Guest, then verify the now-Guest-only user is rejected
	// by the role-management endpoints.
	var meResp envelope
	w = doJSON(router, http.MethodGet, "/api/v1/me", nil, ownerToken)
	json.Unmarshal(w.Body.Bytes(), &meResp)
	userID, _ := meResp.Data["id"].(string)

	var ownerRoleID string
	for _, r := range rolesResp.Data {
		if r["slug"] == "owner" {
			ownerRoleID, _ = r["id"].(string)
		}
	}

	w = doJSON(router, http.MethodPost, "/api/v1/roles/assign", map[string]string{"user_id": userID, "role_id": guestRoleID}, ownerToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 assigning guest role, got %d: %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPost, "/api/v1/roles/revoke", map[string]string{"user_id": userID, "role_id": ownerRoleID}, ownerToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 revoking owner role, got %d: %s", w.Code, w.Body.String())
	}

	// The access token's embedded role claim is stale (still says "owner"),
	// but permission checks re-verify against the database on every request
	// rather than trusting the JWT's role claim (see identity.Claims'
	// documented rationale) -- so this request must now be rejected despite
	// presenting a token that still LOOKS like an owner token.
	w = doJSON(router, http.MethodPost, "/api/v1/roles", map[string]interface{}{
		"name": "Should Not Be Created", "permission_codes": []string{},
	}, ownerToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 creating a role after downgrade to guest-only, got %d: %s", w.Code, w.Body.String())
	}
}

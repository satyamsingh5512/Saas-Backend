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

	// Create a real Guest through the invitation flow. The previous version of
	// this test downgraded the sole Owner, which is now correctly forbidden by
	// the last-Owner invariant.
	guestToken, _ := inviteAndLogin(t, router, ownerToken, slug, "guest", "Guest User")

	// A Guest lacks role:manage, so the route-level permission gate rejects the
	// mutation before the actor-aware service checks are reached.
	w = doJSON(router, http.MethodPost, "/api/v1/roles", map[string]interface{}{
		"name": "Should Not Be Created", "permission_codes": []string{},
	}, guestToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 creating a role as guest, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRBAC_RolePermissionEditingIsLosslessAndTenantScoped covers the editor's
// complete read/replace contract. In particular, every rejected write is
// followed by another GET so the test proves the grants were not partially
// deleted before validation failed.
func TestRBAC_RolePermissionEditingIsLosslessAndTenantScoped(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slugA := uniqueSlug("rbacpermsa")
	slugB := uniqueSlug("rbacpermsb")
	defer cleanupTenant(t, database, slugA)
	defer cleanupTenant(t, database, slugB)

	tokenA, _ := registerOwner(t, router, slugA)
	tokenB, _ := registerOwner(t, router, slugB)

	findRoleID := func(token, slug string) string {
		t.Helper()
		w := doJSON(router, http.MethodGet, "/api/v1/roles", nil, token)
		if w.Code != http.StatusOK {
			t.Fatalf("list roles failed: %d %s", w.Code, w.Body.String())
		}
		for _, role := range dataArray(t, w) {
			if role["slug"] == slug {
				id, _ := role["id"].(string)
				return id
			}
		}
		t.Fatalf("role %q not found", slug)
		return ""
	}

	type grants struct {
		RoleID          string   `json:"role_id"`
		PermissionCodes []string `json:"permission_codes"`
		Revision        string   `json:"revision"`
	}
	getGrants := func(token, roleID string) grants {
		t.Helper()
		w := doJSON(router, http.MethodGet, "/api/v1/roles/"+roleID+"/permissions", nil, token)
		if w.Code != http.StatusOK {
			t.Fatalf("get role permissions failed: %d %s", w.Code, w.Body.String())
		}
		var response struct {
			Data grants `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode role permissions: %v", err)
		}
		return response.Data
	}
	equalCodes := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		counts := make(map[string]int, len(want))
		for _, code := range want {
			counts[code]++
		}
		for _, code := range got {
			counts[code]--
		}
		for _, count := range counts {
			if count != 0 {
				return false
			}
		}
		return true
	}

	guestA := findRoleID(tokenA, "guest")
	initial := getGrants(tokenA, guestA)
	seeded := []string{"org:view", "member:view", "team:view", "project:view"}
	if initial.RoleID != guestA || initial.Revision == "" || !equalCodes(initial.PermissionCodes, seeded) {
		t.Fatalf("unexpected seeded grants: %+v", initial)
	}

	// Saving the loaded set, including a duplicate code, must preserve the
	// exact grants and create only one join row per permission.
	withDuplicate := append(append([]string(nil), initial.PermissionCodes...), initial.PermissionCodes[0])
	w := doJSON(router, http.MethodPut, "/api/v1/roles/"+guestA+"/permissions", map[string]interface{}{
		"permission_codes": withDuplicate,
		"revision":         initial.Revision,
	}, tokenA)
	if w.Code != http.StatusOK {
		t.Fatalf("unchanged save failed: %d %s", w.Code, w.Body.String())
	}
	unchanged := getGrants(tokenA, guestA)
	if !equalCodes(unchanged.PermissionCodes, seeded) {
		t.Fatalf("unchanged save altered grants: %+v", unchanged)
	}
	if unchanged.Revision == initial.Revision {
		t.Fatal("successful replacement did not advance the role revision")
	}

	// A missing field differs from an explicit empty list and must be rejected.
	w = doJSON(router, http.MethodPut, "/api/v1/roles/"+guestA+"/permissions", map[string]interface{}{
		"revision": unchanged.Revision,
	}, tokenA)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing permission_codes returned %d, want 400: %s", w.Code, w.Body.String())
	}
	if afterMissing := getGrants(tokenA, guestA); !equalCodes(afterMissing.PermissionCodes, seeded) || afterMissing.Revision != unchanged.Revision {
		t.Fatalf("missing field changed grants: %+v", afterMissing)
	}

	w = doJSON(router, http.MethodPut, "/api/v1/roles/"+guestA+"/permissions", map[string]interface{}{
		"permission_codes": []string{"project:view", "not:a-permission"},
		"revision":         unchanged.Revision,
	}, tokenA)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown permission returned %d, want 400: %s", w.Code, w.Body.String())
	}
	if afterUnknown := getGrants(tokenA, guestA); !equalCodes(afterUnknown.PermissionCodes, seeded) || afterUnknown.Revision != unchanged.Revision {
		t.Fatalf("unknown permission changed grants: %+v", afterUnknown)
	}

	// An explicitly present empty array is intentional remove-all.
	w = doJSON(router, http.MethodPut, "/api/v1/roles/"+guestA+"/permissions", map[string]interface{}{
		"permission_codes": []string{},
		"revision":         unchanged.Revision,
	}, tokenA)
	if w.Code != http.StatusOK {
		t.Fatalf("explicit empty replacement failed: %d %s", w.Code, w.Body.String())
	}
	empty := getGrants(tokenA, guestA)
	if len(empty.PermissionCodes) != 0 || empty.Revision == unchanged.Revision {
		t.Fatalf("remove-all did not persist or advance revision: %+v", empty)
	}

	// The pre-remove revision is stale and cannot restore data over the newer edit.
	w = doJSON(router, http.MethodPut, "/api/v1/roles/"+guestA+"/permissions", map[string]interface{}{
		"permission_codes": []string{"project:view"},
		"revision":         unchanged.Revision,
	}, tokenA)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale revision returned %d, want 409: %s", w.Code, w.Body.String())
	}
	if afterStale := getGrants(tokenA, guestA); len(afterStale.PermissionCodes) != 0 || afterStale.Revision != empty.Revision {
		t.Fatalf("stale write changed grants: %+v", afterStale)
	}

	// A valid owner credential from tenant A cannot read or mutate tenant B's role.
	guestB := findRoleID(tokenB, "guest")
	beforeB := getGrants(tokenB, guestB)
	w = doJSON(router, http.MethodGet, "/api/v1/roles/"+guestB+"/permissions", nil, tokenA)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant GET returned %d, want 404: %s", w.Code, w.Body.String())
	}
	w = doJSON(router, http.MethodPut, "/api/v1/roles/"+guestB+"/permissions", map[string]interface{}{
		"permission_codes": []string{},
	}, tokenA)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant PUT returned %d, want 404: %s", w.Code, w.Body.String())
	}
	if afterB := getGrants(tokenB, guestB); !equalCodes(afterB.PermissionCodes, beforeB.PermissionCodes) || afterB.Revision != beforeB.Revision {
		t.Fatalf("cross-tenant write changed tenant B grants: %+v", afterB)
	}
}

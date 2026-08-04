package authz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const testRoleRevision = "2026-07-30T18:31:02.338123Z"

func TestParseBodyRoleRevision(t *testing.T) {
	want, err := time.Parse(time.RFC3339Nano, testRoleRevision)
	if err != nil {
		t.Fatal(err)
	}

	got, err := parseBodyRoleRevision("  " + testRoleRevision + "  ")
	if err != nil {
		t.Fatalf("parse body revision: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("parsed revision = %s, want %s", got, want)
	}

	for _, value := range []string{
		`"` + testRoleRevision + `"`,
		`W/"` + testRoleRevision + `"`,
		"not-a-timestamp",
		"",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseBodyRoleRevision(value); err == nil {
				t.Fatalf("parseBodyRoleRevision(%q) succeeded, want error", value)
			}
		})
	}
}

func TestParseIfMatchRoleRevisionRequiresOneStrongEntityTag(t *testing.T) {
	want, err := time.Parse(time.RFC3339Nano, testRoleRevision)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{
		`"` + testRoleRevision + `"`,
		`  "` + testRoleRevision + `"  `,
	} {
		t.Run("valid_"+value, func(t *testing.T) {
			got, err := parseIfMatchRoleRevision(value)
			if err != nil {
				t.Fatalf("parseIfMatchRoleRevision(%q): %v", value, err)
			}
			if !got.Equal(want) {
				t.Fatalf("parsed revision = %s, want %s", got, want)
			}
		})
	}

	for _, value := range []string{
		testRoleRevision,
		`W/"` + testRoleRevision + `"`,
		`w/"` + testRoleRevision + `"`,
		"*",
		`"` + testRoleRevision + `", "2026-07-30T18:31:03Z"`,
		`"` + testRoleRevision,
		testRoleRevision + `"`,
		`""` + testRoleRevision + `""`,
		`""`,
		`"not-a-timestamp"`,
		`"` + testRoleRevision + `" trailing`,
		"",
	} {
		t.Run("invalid_"+value, func(t *testing.T) {
			if _, err := parseIfMatchRoleRevision(value); err == nil {
				t.Fatalf("parseIfMatchRoleRevision(%q) succeeded, want error", value)
			}
		})
	}
}

func TestUpdateRolePermissionsRejectsMalformedIfMatchHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	roleID := uuid.New()
	tenantID := uuid.New()
	actorID := uuid.New()

	for _, value := range []string{
		testRoleRevision,
		`W/"` + testRoleRevision + `"`,
		"*",
		`"` + testRoleRevision + `", "2026-07-30T18:31:03Z"`,
		`"` + testRoleRevision,
		`""` + testRoleRevision + `""`,
	} {
		t.Run(value, func(t *testing.T) {
			handler := NewHandler(nil)
			router := gin.New()
			router.PUT("/roles/:roleID/permissions", func(c *gin.Context) {
				c.Set(CtxTenantID, tenantID)
				c.Set(CtxUserID, actorID)
				handler.UpdateRolePermissions(c)
			})

			req := httptest.NewRequest(
				http.MethodPut,
				"/roles/"+roleID.String()+"/permissions",
				strings.NewReader(`{"permission_codes":[]}`),
			)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", value)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("If-Match %q returned %d, want 400: %s", value, response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), "invalid If-Match revision") {
				t.Fatalf("If-Match %q returned unexpected error: %s", value, response.Body.String())
			}
		})
	}

	handler := NewHandler(nil)
	router := gin.New()
	router.PUT("/roles/:roleID/permissions", func(c *gin.Context) {
		c.Set(CtxTenantID, tenantID)
		c.Set(CtxUserID, actorID)
		handler.UpdateRolePermissions(c)
	})
	req := httptest.NewRequest(
		http.MethodPut,
		"/roles/"+roleID.String()+"/permissions",
		strings.NewReader(`{"permission_codes":[]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("If-Match", `"`+testRoleRevision+`"`)
	req.Header.Add("If-Match", `"2026-07-30T18:31:03Z"`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("repeated If-Match fields returned %d, want 400: %s", response.Code, response.Body.String())
	}
}

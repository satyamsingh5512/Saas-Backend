package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const testCORSOrigin = "https://dashboard.example.com"

func newCORSTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS([]string{testCORSOrigin}))
	router.GET("/roles/:roleID/permissions", func(c *gin.Context) {
		c.Header("ETag", `"2026-07-30T18:31:02.338123Z"`)
		c.Status(http.StatusOK)
	})
	return router
}

func headerContainsToken(value, want string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), want) {
			return true
		}
	}
	return false
}

func TestCORSPreflightAllowsIfMatch(t *testing.T) {
	router := newCORSTestRouter()
	req := httptest.NewRequest(http.MethodOptions, "/roles/role-1/permissions", nil)
	req.Header.Set("Origin", testCORSOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPut)
	req.Header.Set("Access-Control-Request-Headers", "content-type, if-match")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, req)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight returned %d, want 204", response.Code)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != testCORSOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, testCORSOrigin)
	}
	if got := response.Header().Get("Access-Control-Allow-Headers"); !headerContainsToken(got, "If-Match") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want If-Match", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Methods"); !headerContainsToken(got, http.MethodPut) {
		t.Fatalf("Access-Control-Allow-Methods = %q, want PUT", got)
	}
}

func TestCORSResponseExposesETag(t *testing.T) {
	router := newCORSTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/roles/role-1/permissions", nil)
	req.Header.Set("Origin", testCORSOrigin)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("request returned %d, want 200", response.Code)
	}
	if got := response.Header().Get("ETag"); got == "" {
		t.Fatal("response is missing the handler's ETag")
	}
	if got := response.Header().Get("Access-Control-Expose-Headers"); !headerContainsToken(got, "ETag") {
		t.Fatalf("Access-Control-Expose-Headers = %q, want ETag", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != testCORSOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, testCORSOrigin)
	}
}

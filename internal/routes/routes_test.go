package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/config"
)

// TestSetupServesEmbeddedWeb pins the two-document split: "/" is the landing
// page and "/app" is the workspace. Both are embedded in the binary, so a
// missing or misnamed file is a startup panic rather than a 404 in production —
// this catches that at test time instead.
//
// The two documents are told apart by the script each one loads rather than by
// prose, because copy is expected to change and the asset graph is the actual
// contract: the landing page must not pull in app.js (it has no session to
// manage), and the dashboard must.
func TestSetupServesEmbeddedWeb(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := Setup(nil, &config.Config{Environment: "test"})

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d, want %d", path, recorder.Code, http.StatusOK)
		}
		return recorder
	}

	for _, path := range []string{
		"/assets/app.css", "/assets/app.js",
		"/assets/landing.css", "/assets/landing.js", "/assets/theme.js",
	} {
		get(path)
	}

	landing := get("/").Body.String()
	if !strings.Contains(landing, "Tenancy") {
		t.Error("landing document does not carry the application branding")
	}
	if !strings.Contains(landing, "/assets/landing.js") {
		t.Error("landing document does not load landing.js")
	}
	if strings.Contains(landing, "/assets/app.js") {
		t.Error("landing document loads the dashboard bundle; the split has regressed")
	}
	// Every call to action has to reach the workspace, or the page is a dead end.
	if !strings.Contains(landing, `href="/app"`) {
		t.Error("landing document has no link to /app")
	}

	dashboard := get("/app").Body.String()
	if !strings.Contains(dashboard, "/assets/app.js") {
		t.Error("dashboard document does not load app.js")
	}

	// A stale bookmark or hand-typed client route still resolves to the SPA,
	// which then routes on the fragment.
	if body := get("/projects").Body.String(); !strings.Contains(body, "/assets/app.js") {
		t.Error("unknown non-API path did not fall through to the dashboard document")
	}
}

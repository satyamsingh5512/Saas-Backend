package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/config"
)

func TestSetupServesEmbeddedDashboard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := Setup(nil, &config.Config{Environment: "test"})

	for _, path := range []string{"/", "/assets/app.css", "/assets/app.js"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d, want %d", path, recorder.Code, http.StatusOK)
		}
	}

	indexRecorder := httptest.NewRecorder()
	router.ServeHTTP(indexRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(indexRecorder.Body.String(), "tenancy") {
		t.Fatal("dashboard document does not contain the application branding")
	}
}

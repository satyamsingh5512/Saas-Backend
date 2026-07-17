package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/internal/db"
	"github.com/satym-in/tenant-saas-backend/internal/handlers"
	"github.com/satym-in/tenant-saas-backend/internal/middleware"
	"github.com/satym-in/tenant-saas-backend/internal/models"
	"gorm.io/gorm"
)

// loadRootEnv loads the .env file from the project root regardless of the
// working directory `go test` was invoked from (which is the package dir).
func loadRootEnv() {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	_ = godotenv.Load(filepath.Join(root, ".env"))
}

// setupTestRouter connects to the test database (configured via env / .env),
// runs migrations, and wires up a full router. Tests are skipped automatically
// if no database is reachable, so this suite doesn't break environments
// without Postgres available.
func setupTestRouter(t *testing.T) (*gin.Engine, *gorm.DB, *config.Config) {
	t.Helper()

	loadRootEnv()
	cfg := config.Load()
	cfg.JWTSecret = "test-secret-for-integration-tests"
	cfg.Environment = "test"

	database, err := db.Connect(cfg)
	if err != nil {
		t.Skipf("skipping integration test, database not reachable: %v", err)
	}

	sqlDB, err := database.DB()
	if err != nil || sqlDB.Ping() != nil {
		t.Skipf("skipping integration test, database not reachable")
	}

	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()

	authHandler := handlers.NewAuthHandler(database, cfg)
	userHandler := handlers.NewUserHandler(database)

	api := router.Group("/api/v1")
	api.POST("/auth/register", authHandler.Register)
	api.POST("/auth/login", authHandler.Login)

	protected := api.Group("/")
	protected.Use(middleware.RequireAuth(cfg))
	protected.GET("/me", userHandler.Me)
	protected.GET("/users", userHandler.ListUsers)

	return router, database, cfg
}

// cleanupTenant removes a tenant and its users created during a test.
func cleanupTenant(t *testing.T, database *gorm.DB, slug string) {
	t.Helper()
	var tenant models.Tenant
	if err := database.Where("slug = ?", slug).First(&tenant).Error; err == nil {
		database.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(&models.User{})
		database.Unscoped().Delete(&tenant)
	}
}

func uniqueSlug(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, os.Getpid())
}

func doJSON(router *gin.Engine, method, path string, body interface{}, token string) *httptest.ResponseRecorder {
	var reader *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewBuffer(b)
	} else {
		reader = bytes.NewBuffer(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestRegister_Success(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("test-register")
	defer cleanupTenant(t, database, slug)

	body := map[string]string{
		"tenant_name": "Test Register Co",
		"tenant_slug": slug,
		"email":       fmt.Sprintf("admin-%s@example.com", slug),
		"password":    "supersecret123",
	}

	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", body, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["token"] == "" || resp["token"] == nil {
		t.Error("expected a non-empty token in register response")
	}
}

func TestRegister_DuplicateSlugFails(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("test-dup")
	defer cleanupTenant(t, database, slug)

	body := map[string]string{
		"tenant_name": "Dup Co",
		"tenant_slug": slug,
		"email":       fmt.Sprintf("admin-%s@example.com", slug),
		"password":    "supersecret123",
	}

	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", body, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected first register to succeed with 201, got %d: %s", w.Code, w.Body.String())
	}

	body2 := map[string]string{
		"tenant_name": "Dup Co Again",
		"tenant_slug": slug,
		"email":       fmt.Sprintf("admin2-%s@example.com", slug),
		"password":    "supersecret123",
	}
	w2 := doJSON(router, http.MethodPost, "/api/v1/auth/register", body2, "")
	if w2.Code != http.StatusConflict {
		t.Fatalf("expected duplicate slug to return 409, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestLogin_SuccessAndFailure(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("test-login")
	defer cleanupTenant(t, database, slug)

	email := fmt.Sprintf("admin-%s@example.com", slug)
	registerBody := map[string]string{
		"tenant_name": "Login Co",
		"tenant_slug": slug,
		"email":       email,
		"password":    "supersecret123",
	}
	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", registerBody, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("setup register failed: %d %s", w.Code, w.Body.String())
	}

	// correct credentials
	loginBody := map[string]string{"email": email, "password": "supersecret123"}
	w = doJSON(router, http.MethodPost, "/api/v1/auth/login", loginBody, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid login, got %d: %s", w.Code, w.Body.String())
	}

	// wrong password
	badLogin := map[string]string{"email": email, "password": "wrongpassword"}
	w = doJSON(router, http.MethodPost, "/api/v1/auth/login", badLogin, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on invalid password, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProtectedRoutes_TenantIsolation(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slugA := uniqueSlug("test-tenant-a")
	slugB := uniqueSlug("test-tenant-b")
	defer cleanupTenant(t, database, slugA)
	defer cleanupTenant(t, database, slugB)

	// Register tenant A
	emailA := fmt.Sprintf("admin-%s@example.com", slugA)
	wA := doJSON(router, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"tenant_name": "Tenant A", "tenant_slug": slugA, "email": emailA, "password": "supersecret123",
	}, "")
	var respA map[string]interface{}
	json.Unmarshal(wA.Body.Bytes(), &respA)
	tokenA, _ := respA["token"].(string)

	// Register tenant B
	emailB := fmt.Sprintf("admin-%s@example.com", slugB)
	doJSON(router, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"tenant_name": "Tenant B", "tenant_slug": slugB, "email": emailB, "password": "supersecret123",
	}, "")

	// Tenant A's token should only see tenant A's user in /users
	w := doJSON(router, http.MethodGet, "/api/v1/users", nil, tokenA)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var listResp map[string][]map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("failed to parse users response: %v", err)
	}

	users := listResp["users"]
	if len(users) != 1 {
		t.Fatalf("expected tenant A to see exactly 1 user (isolation), got %d", len(users))
	}
	if users[0]["email"] != emailA {
		t.Errorf("expected tenant A's own user, got email %v", users[0]["email"])
	}
}

func TestProtectedRoutes_RequireAuth(t *testing.T) {
	router, _, _ := setupTestRouter(t)

	w := doJSON(router, http.MethodGet, "/api/v1/me", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}

	w = doJSON(router, http.MethodGet, "/api/v1/me", nil, "not-a-valid-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with invalid token, got %d", w.Code)
	}
}

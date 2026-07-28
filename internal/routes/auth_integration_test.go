package routes_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/internal/db"
	"github.com/satym-in/tenant-saas-backend/internal/routes"
	"github.com/satym-in/tenant-saas-backend/internal/tenancy"
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
// runs migrations, and wires up the full application router via
// routes.Setup -- the same construction path production uses. Tests are
// skipped automatically if no database is reachable.
func setupTestRouter(t *testing.T) (*gin.Engine, *gorm.DB, *config.Config) {
	t.Helper()

	loadRootEnv()
	cfg := config.Load()
	cfg.JWTSecret = "test-secret-for-integration-tests-0123456789"
	cfg.Environment = "test"

	// Migrations require the superuser credential (MIGRATE_DB_*), not the
	// app's restricted runtime credential -- see config.ApplyMigrationOverrides
	// and scripts/provision_app_role.sql for why the two must differ.
	migrationCfg := *cfg
	migrationCfg.ApplyMigrationOverrides()
	migrationDB, err := db.Connect(&migrationCfg)
	if err != nil {
		t.Skipf("skipping integration test, migration database not reachable: %v", err)
	}
	migrationSQLDB, err := migrationDB.DB()
	if err != nil || migrationSQLDB.Ping() != nil {
		t.Skipf("skipping integration test, migration database not reachable")
	}
	if err := db.Migrate(migrationDB); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}
	_ = migrationSQLDB.Close()

	// The actual application (and this test's HTTP requests) connect with
	// the app's own runtime credential, so tests exercise the exact same
	// RLS enforcement path production traffic does -- if DB_USER is left
	// pointed at a superuser, tenant-isolation tests below would pass for
	// the wrong reason (RLS silently bypassed) rather than proving
	// isolation actually holds.
	database, err := db.Connect(cfg)
	if err != nil {
		t.Skipf("skipping integration test, database not reachable: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil || sqlDB.Ping() != nil {
		t.Skipf("skipping integration test, database not reachable")
	}

	gin.SetMode(gin.TestMode)
	router := routes.Setup(database, cfg)
	return router, database, cfg
}

// cleanupTenant removes a tenant and its users/roles created during a test.
// Runs with superuser privileges (bypassing RLS) since this is out-of-band
// test teardown across arbitrary tenants, not a request the application
// itself would ever serve.
func cleanupTenant(t *testing.T, database *gorm.DB, slug string) {
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
		superuserDB.Exec("DELETE FROM user_roles WHERE tenant_id = ?", tenant.ID)
		superuserDB.Exec("DELETE FROM role_permissions WHERE tenant_id = ?", tenant.ID)
		superuserDB.Exec("DELETE FROM roles WHERE tenant_id = ?", tenant.ID)
		superuserDB.Exec("DELETE FROM refresh_tokens WHERE tenant_id = ?", tenant.ID)
		superuserDB.Exec("DELETE FROM users WHERE tenant_id = ?", tenant.ID)
		superuserDB.Exec("DELETE FROM tenants WHERE id = ?", tenant.ID)
	}
}

var slugCounter int64

func uniqueSlug(prefix string) string {
	slugCounter++
	return fmt.Sprintf("%stst%d%d", prefix, time.Now().UnixNano(), slugCounter)
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

// envelope mirrors pkg/apiresponse's {success, data, meta} / {success,
// error, meta} shape loosely enough to extract fields generically in tests.
type envelope struct {
	Success bool                   `json:"success"`
	Data    map[string]interface{} `json:"data"`
	Error   map[string]interface{} `json:"error"`
}

func TestRegister_Success(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("reg")
	defer cleanupTenant(t, database, slug)

	body := map[string]string{
		"tenant_name": "Test Register Co",
		"tenant_slug": slug,
		"email":       fmt.Sprintf("admin-%s@example.com", slug),
		"password":    "supersecret123",
		"full_name":   "Admin User",
	}

	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", body, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp envelope
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success=true, got %s", w.Body.String())
	}
	tokens, ok := resp.Data["tokens"].(map[string]interface{})
	if !ok || tokens["access_token"] == "" {
		t.Errorf("expected a non-empty access_token in register response: %s", w.Body.String())
	}
	if tokens["refresh_token"] == "" {
		t.Errorf("expected a non-empty refresh_token in register response: %s", w.Body.String())
	}
}

func TestRegister_DuplicateSlugFails(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("dup")
	defer cleanupTenant(t, database, slug)

	body := map[string]string{
		"tenant_name": "Dup Co",
		"tenant_slug": slug,
		"email":       fmt.Sprintf("admin-%s@example.com", slug),
		"password":    "supersecret123",
		"full_name":   "Admin User",
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
		"full_name":   "Admin User Two",
	}
	w2 := doJSON(router, http.MethodPost, "/api/v1/auth/register", body2, "")
	if w2.Code != http.StatusConflict {
		t.Fatalf("expected duplicate slug to return 409, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestLogin_SuccessAndFailure(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("login")
	defer cleanupTenant(t, database, slug)

	email := fmt.Sprintf("admin-%s@example.com", slug)
	registerBody := map[string]string{
		"tenant_name": "Login Co",
		"tenant_slug": slug,
		"email":       email,
		"password":    "supersecret123",
		"full_name":   "Admin User",
	}
	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", registerBody, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("setup register failed: %d %s", w.Code, w.Body.String())
	}

	// correct credentials, tenant disambiguated via tenant_slug
	loginBody := map[string]string{"email": email, "password": "supersecret123", "tenant_slug": slug}
	w = doJSON(router, http.MethodPost, "/api/v1/auth/login", loginBody, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid login, got %d: %s", w.Code, w.Body.String())
	}

	// wrong password
	badLogin := map[string]string{"email": email, "password": "wrongpassword", "tenant_slug": slug}
	w = doJSON(router, http.MethodPost, "/api/v1/auth/login", badLogin, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on invalid password, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProtectedRoutes_TenantIsolation(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slugA := uniqueSlug("tena")
	slugB := uniqueSlug("tenb")
	defer cleanupTenant(t, database, slugA)
	defer cleanupTenant(t, database, slugB)

	// Register tenant A
	emailA := fmt.Sprintf("admin-%s@example.com", slugA)
	wA := doJSON(router, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"tenant_name": "Tenant A", "tenant_slug": slugA, "email": emailA, "password": "supersecret123", "full_name": "Admin A",
	}, "")
	var respA envelope
	if err := json.Unmarshal(wA.Body.Bytes(), &respA); err != nil {
		t.Fatalf("failed to parse tenant A register response: %v", err)
	}
	tokensA, _ := respA.Data["tokens"].(map[string]interface{})
	tokenA, _ := tokensA["access_token"].(string)
	if tokenA == "" {
		t.Fatalf("expected tenant A to receive an access token: %s", wA.Body.String())
	}

	// Register tenant B
	emailB := fmt.Sprintf("admin-%s@example.com", slugB)
	doJSON(router, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"tenant_name": "Tenant B", "tenant_slug": slugB, "email": emailB, "password": "supersecret123", "full_name": "Admin B",
	}, "")

	// Tenant A's token should only see tenant A's user in /users, proving
	// RLS + the JWT-tenant-claim-is-source-of-truth design actually holds
	// at the HTTP layer, not just in the raw SQL test performed manually
	// against Postgres during the migrations phase.
	w := doJSON(router, http.MethodGet, "/api/v1/users", nil, tokenA)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var listResp struct {
		Success bool                     `json:"success"`
		Data    []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("failed to parse users response: %v", err)
	}

	if len(listResp.Data) != 1 {
		t.Fatalf("expected tenant A to see exactly 1 user (isolation), got %d: %s", len(listResp.Data), w.Body.String())
	}
	if listResp.Data[0]["email"] != emailA {
		t.Errorf("expected tenant A's own user, got email %v", listResp.Data[0]["email"])
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

func TestRefreshTokenRotation(t *testing.T) {
	router, database, _ := setupTestRouter(t)
	slug := uniqueSlug("refresh")
	defer cleanupTenant(t, database, slug)

	email := fmt.Sprintf("admin-%s@example.com", slug)
	w := doJSON(router, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"tenant_name": "Refresh Co", "tenant_slug": slug, "email": email, "password": "supersecret123", "full_name": "Admin",
	}, "")
	var resp envelope
	json.Unmarshal(w.Body.Bytes(), &resp)
	tokens, _ := resp.Data["tokens"].(map[string]interface{})
	refreshToken, _ := tokens["refresh_token"].(string)
	if refreshToken == "" {
		t.Fatalf("expected a refresh token from register: %s", w.Body.String())
	}

	// First use should succeed and rotate.
	w = doJSON(router, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": refreshToken}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on refresh, got %d: %s", w.Code, w.Body.String())
	}

	// Reusing the now-rotated-out token must fail (theft-detection / reuse
	// rejection), proving rotation-on-use is actually enforced end-to-end.
	w = doJSON(router, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": refreshToken}, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected reused refresh token to be rejected with 401, got %d: %s", w.Code, w.Body.String())
	}
}

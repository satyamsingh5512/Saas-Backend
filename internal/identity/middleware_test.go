package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/authz"
)

type credentialValidatorStub struct {
	err      error
	called   int
	tenantID uuid.UUID
	userID   uuid.UUID
}

func (s *credentialValidatorStub) ValidateCredentialState(_ context.Context, tenantID, userID uuid.UUID) error {
	s.called++
	s.tenantID = tenantID
	s.userID = userID
	return s.err
}

func TestRequireAuthRejectsInactiveDatabaseSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "middleware-test-secret-012345678901234"
	tenantID, userID := uuid.New(), uuid.New()
	token, err := GenerateAccessToken(secret, 15, userID, tenantID, "owner")
	if err != nil {
		t.Fatal(err)
	}

	validator := &credentialValidatorStub{err: ErrUserInactive}
	router := gin.New()
	router.GET("/protected", RequireAuth(secret, validator), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", response.Code, response.Body.String())
	}
	if validator.called != 1 || validator.tenantID != tenantID || validator.userID != userID {
		t.Fatalf("validator received calls=%d tenant=%s user=%s", validator.called, validator.tenantID, validator.userID)
	}
}

func TestRequireAuthUsesValidatedCredentialIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "middleware-test-secret-012345678901234"
	tenantID, userID := uuid.New(), uuid.New()
	token, err := GenerateAccessToken(secret, 15, userID, tenantID, "member")
	if err != nil {
		t.Fatal(err)
	}

	validator := &credentialValidatorStub{}
	router := gin.New()
	router.GET("/protected", RequireAuth(secret, validator), func(c *gin.Context) {
		gotTenant, _ := c.Get(authz.CtxTenantID)
		gotUser, _ := c.Get(authz.CtxUserID)
		if gotTenant != tenantID || gotUser != userID {
			t.Fatalf("context tenant=%v user=%v", gotTenant, gotUser)
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", response.Code, response.Body.String())
	}
	if validator.called != 1 {
		t.Fatalf("validator calls = %d, want 1", validator.called)
	}
}

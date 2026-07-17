package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestIPRateLimiter_AllowsWithinBurst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limiter := NewIPRateLimiter(60, 3) // 1 req/sec, burst of 3

	router := gin.New()
	router.Use(limiter.Middleware())
	router.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, w.Code)
		}
	}
}

func TestIPRateLimiter_BlocksOverBurst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limiter := NewIPRateLimiter(60, 2) // 1 req/sec, burst of 2

	router := gin.New()
	router.Use(limiter.Middleware())
	router.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	var lastCode int
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "5.6.7.8:1234"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		lastCode = w.Code
	}

	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("expected final request to be rate limited with 429, got %d", lastCode)
	}
}

func TestIPRateLimiter_SeparateIPsIndependentLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limiter := NewIPRateLimiter(60, 1) // burst of 1

	router := gin.New()
	router.Use(limiter.Middleware())
	router.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.RemoteAddr = "9.9.9.9:1234"
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "8.8.8.8:1234"
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
		t.Fatalf("expected both distinct IPs to succeed independently, got %d and %d", w1.Code, w2.Code)
	}
}

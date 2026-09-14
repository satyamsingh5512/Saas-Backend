package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMiddlewareCountsRequestsByStatusClass(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := New()
	router := gin.New()
	router.Use(r.Middleware())
	router.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/missing", func(c *gin.Context) { c.Status(http.StatusNotFound) })
	router.GET("/boom", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })

	for _, path := range []string{"/ok", "/ok", "/missing", "/boom"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	snap := r.Snapshot()
	if snap.RequestsTotal != 4 {
		t.Fatalf("RequestsTotal = %d, want 4", snap.RequestsTotal)
	}
	if snap.Requests2xx != 2 || snap.Requests4xx != 1 || snap.Requests5xx != 1 {
		t.Fatalf("status classes = 2xx:%d 4xx:%d 5xx:%d, want 2/1/1",
			snap.Requests2xx, snap.Requests4xx, snap.Requests5xx)
	}
	if snap.InFlight != 0 {
		t.Fatalf("InFlight = %d, want 0 after requests complete", snap.InFlight)
	}
}

func TestCacheHitRateMath(t *testing.T) {
	r := New()
	r.RecordCacheHit()
	r.RecordCacheHit()
	r.RecordCacheMiss()
	if got := r.Snapshot().CacheHitRate; got < 0.666 || got > 0.667 {
		t.Fatalf("CacheHitRate = %v, want ~0.6667", got)
	}
}

func TestHandlerServesPrometheusTextWithoutDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := New()
	router := gin.New()
	router.GET("/metrics", r.Handler(nil, "", ""))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"saas_api_requests_total",
		"saas_cache_hit_rate",
		"saas_sse_clients",
		"# TYPE saas_api_requests_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q", want)
		}
	}
	// No database: domain gauges must be absent, not zero-valued lies.
	if strings.Contains(body, "saas_tenants") {
		t.Error("metrics body contains saas_tenants without a database; gauges must be absent on degrade")
	}
}

func TestHandlerEnforcesMetricsToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := New()
	router := gin.New()
	router.GET("/metrics", r.Handler(nil, "", "secret-token"))

	anon := httptest.NewRecorder()
	router.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", anon.Code)
	}

	authed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	router.ServeHTTP(authed, req)
	if authed.Code != http.StatusOK {
		t.Fatalf("authed status = %d, want 200", authed.Code)
	}
}

func TestNilRegistryIsSafe(t *testing.T) {
	var r *Registry
	r.RecordCacheHit()
	r.RecordCacheMiss()
	r.AddSSEClients(1)
	_ = r.Snapshot()
	// Middleware on nil must pass through without recording.
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(r.Middleware())
	router.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

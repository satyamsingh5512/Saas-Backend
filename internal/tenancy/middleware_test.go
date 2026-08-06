package tenancy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSlugFromRequestIgnoresNonTenantHosts is a regression test for a
// deployment-breaking inference. The original rule was "a host with more than
// two labels means the first label is a tenant slug", which reads the service
// name out of saas-backend-d58c.onrender.com, fails to resolve it as a tenant,
// and rejects every request to the deployment with 404 -- including the landing
// page and every asset.
//
// A subdomain only names a tenant relative to a base domain we actually own, so
// the base domain is configuration and an unset one means no inference at all.
func TestSlugFromRequestIgnoresNonTenantHosts(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name       string
		baseDomain string
		host       string
		header     string
		wantSlug   string
		wantHeader bool
	}{
		{
			name:       "shared hosting domain is not a tenant when no base domain is set",
			baseDomain: "",
			host:       "saas-backend-d58c.onrender.com",
			wantSlug:   "",
		},
		{
			name:       "vercel preview host is not a tenant",
			baseDomain: "",
			host:       "tenant-saas-git-main-acme.vercel.app",
			wantSlug:   "",
		},
		{
			name:       "a host outside the base domain yields no tenant",
			baseDomain: "ourapp.com",
			host:       "saas-backend-d58c.onrender.com",
			wantSlug:   "",
		},
		{
			name:       "subdomain under the base domain resolves",
			baseDomain: "ourapp.com",
			host:       "acme.ourapp.com",
			wantSlug:   "acme",
		},
		{
			name:       "port is stripped before matching",
			baseDomain: "ourapp.com",
			host:       "acme.ourapp.com:8443",
			wantSlug:   "acme",
		},
		{
			name:       "the apex itself is not a tenant",
			baseDomain: "ourapp.com",
			host:       "ourapp.com",
			wantSlug:   "",
		},
		{
			name:       "reserved infrastructure labels are not tenants",
			baseDomain: "ourapp.com",
			host:       "www.ourapp.com",
			wantSlug:   "",
		},
		{
			name:       "deeper nesting is not a single-label tenant",
			baseDomain: "ourapp.com",
			host:       "a.b.ourapp.com",
			wantSlug:   "",
		},
		{
			name:       "header wins over host and is marked authoritative",
			baseDomain: "ourapp.com",
			host:       "acme.ourapp.com",
			header:     "globex",
			wantSlug:   "globex",
			wantHeader: true,
		},
		{
			name:       "header alone works with no base domain configured",
			baseDomain: "",
			host:       "saas-backend-d58c.onrender.com",
			header:     "acme",
			wantSlug:   "acme",
			wantHeader: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := NewResolver(nil, nil, tc.baseDomain)

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
			c.Request.Host = tc.host
			if tc.header != "" {
				c.Request.Header.Set("X-Tenant-ID", tc.header)
			}

			slug, fromHeader := resolver.slugFromRequest(c)
			if slug != tc.wantSlug {
				t.Errorf("slug = %q, want %q", slug, tc.wantSlug)
			}
			if fromHeader != tc.wantHeader {
				t.Errorf("fromHeader = %v, want %v", fromHeader, tc.wantHeader)
			}
		})
	}
}

// TestMiddlewarePassesThroughUnknownHost pins the other half of the fix: even
// when a base domain is configured, an unresolvable host label must fall
// through as "no tenant" rather than abort. The hostname is a routing hint, so
// authentication still gets its chance to establish the real tenant.
//
// A nil repository is deliberate. Reaching a lookup at all is the failure this
// asserts against, and a nil-pointer panic makes that unmistakable.
func TestMiddlewarePassesThroughUnknownHost(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(NewResolver(nil, nil, "").Middleware())
	router.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "landing") })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "saas-backend-d58c.onrender.com"
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / on a non-tenant host returned %d, want %d", recorder.Code, http.StatusOK)
	}
}

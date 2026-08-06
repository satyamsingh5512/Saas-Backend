package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders applies defensive response headers to every request.
//
// The Content-Security-Policy is tuned for the embedded dashboard, which ships
// its own CSS/JS as separate same-origin asset files and therefore needs no
// 'unsafe-inline'. connect-src is limited to 'self' because the UI only ever
// talks to the API that served it.
//
// HSTS is only emitted when the request arrived over TLS. Sending it over plain
// HTTP is ignored by browsers at best, and pinning HTTPS for a local
// development host at worst.
// ContentSecurityPolicy is the policy served with every response.
//
// connect-src is 'self' rather than an API origin because the UI only ever
// talks to the API that served it. That stays true on the split Vercel/Render
// deployment: Vercel proxies /api and /health to Render as same-origin
// rewrites, so no cross-origin grant is needed there either. It is exported so
// vercel.json can be checked against it -- the frontend host serves its own
// headers, and a policy that drifts from this one would only fail in
// production.
const ContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'self'; " +
	"object-src 'none'"

func SecurityHeaders() gin.HandlerFunc {
	const csp = ContentSecurityPolicy

	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", csp)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		c.Next()
	}
}

// CORS returns middleware that permits cross-origin API access only from an
// explicit allow-list of origins.
//
// There is intentionally no wildcard support. Responses here are
// credential-bearing (Authorization: Bearer), and `Access-Control-Allow-Origin:
// *` combined with credentials is both rejected by browsers and unsafe. An
// empty allowedOrigins list disables CORS entirely, which is the correct
// default: the dashboard is served from the same origin as the API, so no
// cross-origin grant is required until an external frontend is deployed.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if trimmed := strings.TrimSpace(origin); trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}

	const maxAge = 12 * time.Hour

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}

		if _, ok := allowed[origin]; !ok {
			// Unknown origin: no CORS headers are emitted, so the browser
			// blocks the response. Preflights are short-circuited rather than
			// passed to a handler that would reject them anyway.
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Next()
			return
		}

		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+HeaderRequestID+", X-Tenant-ID, If-Match")
		h.Set("Access-Control-Expose-Headers", HeaderRequestID+", ETag")
		h.Set("Access-Control-Max-Age", strconv.Itoa(int(maxAge.Seconds())))
		// Responses vary by Origin, so caches must not serve one origin's
		// response to another.
		h.Add("Vary", "Origin")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

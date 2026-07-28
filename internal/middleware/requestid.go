package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ContextRequestID is the Gin context key under which the per-request
// correlation ID is stored. pkg/apiresponse reads this exact key to populate
// the response envelope's meta.request_id, so the two must stay in sync --
// this constant is the single definition both sides refer to.
const ContextRequestID = "request_id"

// HeaderRequestID is the inbound/outbound header carrying the correlation ID.
const HeaderRequestID = "X-Request-ID"

// RequestID assigns every request a correlation ID, reusing a client- or
// upstream-proxy-supplied X-Request-ID when present so a single ID follows a
// request across service and load-balancer hops. The ID is echoed back on the
// response header and embedded in every API response envelope, which is what
// makes a user-reported "request X failed" traceable to exact server logs.
//
// A supplied value is validated as a UUID rather than trusted verbatim: the ID
// is written into structured logs and response headers, so accepting arbitrary
// client input would allow log injection and header-splitting style noise.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderRequestID)
		if parsed, err := uuid.Parse(id); err == nil {
			id = parsed.String()
		} else {
			id = uuid.NewString()
		}

		c.Set(ContextRequestID, id)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// RequestIDFromContext returns the correlation ID assigned by RequestID, or an
// empty string when the middleware has not run (e.g. in a unit test that
// exercises a handler directly).
func RequestIDFromContext(c *gin.Context) string {
	if v, ok := c.Get(ContextRequestID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

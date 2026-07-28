package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Recovery converts a panic in any handler into a 500 response using the
// standard API envelope, instead of gin's default plaintext output. Keeping the
// envelope consistent even on catastrophic failure means clients never need a
// second parsing path for "the server broke".
//
// The panic value and stack trace go to the structured log only. They can embed
// SQL fragments, struct contents, and file paths, so returning them to the
// caller would leak internal implementation detail to a potential attacker; the
// client receives only the request ID needed to report the incident.
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic recovered",
					slog.String("request_id", RequestIDFromContext(c)),
					slog.String("method", c.Request.Method),
					slog.String("route", c.FullPath()),
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())),
				)

				apiresponse.Error(c, http.StatusInternalServerError,
					string(apperror.CodeInternal),
					"internal server error")
			}
		}()
		c.Next()
	}
}

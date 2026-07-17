package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthCheck is a simple liveness endpoint.
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

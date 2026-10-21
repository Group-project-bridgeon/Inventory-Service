package middleware

import (
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/gin-gonic/gin"
	"net/http"
)

// MaxBodyBytes rejects known oversized bodies and bounds reads of streaming bodies.
// Readers must handle *http.MaxBytesError; unread streaming bodies cannot be measured.
func MaxBodyBytes(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > limit {
			logger.Respond(c, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

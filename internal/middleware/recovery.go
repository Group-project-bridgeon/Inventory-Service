package middleware

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"net/http"
	"runtime/debug"
)

func Recovery(log zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if value := recover(); value != nil {
				log.Error().Str("panic_type", fmt.Sprintf("%T", value)).Str("stack", string(debug.Stack())).Msg("panic recovered")
				c.Abort()
				if !c.Writer.Written() {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
				}
			}
		}()
		c.Next()
	}
}

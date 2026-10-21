package middleware

import (
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"runtime"
	"strings"
)

func Recovery(fallback zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				log := logger.Request(c)
				if _, ok := c.Get(logger.RequestLoggerKey); !ok {
					log = fallback
				}
				// Function names only: never arbitrary panic values or stack arguments.
				pcs := make([]uintptr, 32)
				n := runtime.Callers(3, pcs)
				frames := runtime.CallersFrames(pcs[:n])
				var stack strings.Builder
				for {
					frame, more := frames.Next()
					stack.WriteString(frame.Function)
					stack.WriteByte('\n')
					if !more {
						break
					}
				}
				log.Error().Str("component", "http").Str("operation", "recover").Str("reason", "panic").Str("stack", stack.String()).Msg("Panic recovered")
				c.Set("inventory.diagnosed", true)
				c.Abort()
				if !c.Writer.Written() {
					logger.Respond(c, 500, "internal_error", "internal server error")
				}
			}
		}()
		c.Next()
	}
}

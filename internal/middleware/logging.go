package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"time"
)

func RequestLogger(log zerolog.Logger, slow time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		if c.Request.URL.Path == "/health" || c.Request.URL.Path == "/ready" {
			return
		}
		status := c.Writer.Status()
		elapsed := time.Since(start)
		var event *zerolog.Event
		switch {
		case status >= 500:
			event = log.Error()
		case status >= 400:
			event = log.Warn()
		case elapsed >= slow:
			event = log.Info()
		default:
			return
		}
		// Route templates avoid recording sensitive client-controlled URL segments or queries.
		event.Str("method", c.Request.Method).Str("route", c.FullPath()).Int("status", status).Int64("duration_ms", elapsed.Milliseconds()).Msg("http request")
	}
}

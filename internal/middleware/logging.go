package middleware

import (
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"regexp"
	"sync"
	"time"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func RequestLogger(log zerolog.Logger, slow time.Duration) gin.HandlerFunc {
	var mu sync.Mutex
	var lastProbeFailure time.Time
	return func(c *gin.Context) {
		start := time.Now()
		id := ""
		values := c.Request.Header.Values("X-Request-ID")
		if len(values) == 1 && requestIDPattern.MatchString(values[0]) {
			id = values[0]
		}
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(logger.RequestIDKey, id)
		c.Header("X-Request-ID", id)
		requestLog := log.With().Str("request_id", id).Str("method", c.Request.Method).Str("route", logger.Route(c)).Logger()
		c.Set(logger.RequestLoggerKey, requestLog)
		c.Next()
		status := c.Writer.Status()
		elapsed := time.Since(start)
		probe := c.Request.Method == "GET" && (c.FullPath() == "/health" || c.FullPath() == "/ready")
		if probe {
			if status < 500 {
				return
			}
			mu.Lock()
			emit := time.Since(lastProbeFailure) >= time.Minute
			if emit {
				lastProbeFailure = time.Now()
			}
			mu.Unlock()
			if emit {
				requestLog.Warn().Str("component", "http").Str("operation", "probe").Str("reason", "probe_failed").Int("status", status).Int64("duration_ms", elapsed.Milliseconds()).Msg("Probe failed")
			}
			return
		}
		// Expected client errors remain silent even when slow.
		if status >= 400 && status < 500 {
			return
		}
		if status < 500 && elapsed < slow {
			return
		}
		event := requestLog.Debug()
		if elapsed >= slow {
			event = requestLog.Warn()
		}
		event.Str("component", "http").Str("operation", "complete").Int("status", status).Int64("duration_ms", elapsed.Milliseconds()).Msg("HTTP request completed")
	}
}

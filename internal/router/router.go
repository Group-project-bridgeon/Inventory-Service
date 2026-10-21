package router

import (
	"context"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/config"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"net/http"
)

type DatabaseHealthChecker interface{ Ping(context.Context) error }

func New(db DatabaseHealthChecker, log zerolog.Logger, cfg config.HTTPConfig) *gin.Engine {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(middleware.RequestLogger(log, cfg.SlowRequestThreshold), middleware.Recovery(log), middleware.MaxBodyBytes(cfg.MaxBodyBytes))
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "inventory"}) })
	r.GET("/ready", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.ReadinessTimeout)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "service": "inventory"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready", "service": "inventory"})
	})
	return r
}

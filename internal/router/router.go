package router

import (
	"context"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/config"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/handler"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/middleware"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"net/http"
	"strings"
)

type DatabaseHealthChecker interface{ Ping(context.Context) error }

func New(db DatabaseHealthChecker, log zerolog.Logger, cfg config.HTTPConfig) *gin.Engine {
	return NewWithBusiness(db, log, cfg, nil, nil)
}

func NewWithBusiness(db DatabaseHealthChecker, log zerolog.Logger, cfg config.HTTPConfig, verifier *security.Verifier, warehouses repository.WarehouseRepository, reads ...*service.StockReadService) *gin.Engine {
	var read *service.StockReadService
	if len(reads) == 1 {
		read = reads[0]
	}
	return NewWithOpening(db, log, cfg, verifier, warehouses, read, nil)
}

func NewWithOpening(db DatabaseHealthChecker, log zerolog.Logger, cfg config.HTTPConfig, verifier *security.Verifier, warehouses repository.WarehouseRepository, read *service.StockReadService, opening *service.OpeningStockService) *gin.Engine {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(middleware.RequestLogger(log, cfg.SlowRequestThreshold), middleware.Recovery(log), middleware.MaxBodyBytes(cfg.MaxBodyBytes))
	policy := map[string][]string{
		"POST /api/v1/inventory/warehouses":                    {"inventory.warehouses:create"},
		"GET /api/v1/inventory/warehouses":                     {"inventory.warehouses:read"},
		"GET /api/v1/inventory/warehouses/:id":                 {"inventory.warehouses:read"},
		"PUT /api/v1/inventory/warehouses/:id":                 {"inventory.warehouses:update"},
		"PATCH /api/v1/inventory/warehouses/:id/status":        {"inventory.warehouses:update"},
		"GET /api/v1/inventory/warehouses/:id/stock-balances":  {"inventory.stock-balances:read"},
		"GET /api/v1/inventory/warehouses/:id/stock-movements": {"inventory.stock-movements:read"},
	}
	if opening != nil {
		policy["POST /api/v1/inventory/warehouses/:id/opening-stock"] = []string{"inventory.opening-stock:post"}
		policy["POST /api/v1/inventory/warehouses/:id/opening-stock/prepare"] = []string{"inventory.opening-stock:post"}
	}
	r.Use(func(c *gin.Context) {
		if strings.Contains(c.FullPath(), "/opening-stock") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})
	r.Use(security.Protect(verifier, policy))
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
	if warehouses != nil {
		h := handler.NewWarehouseHandler(service.NewWarehouseService(warehouses))
		g := r.Group("/api/v1/inventory/warehouses")
		g.POST("", h.Create)
		g.GET("", h.List)
		g.GET("/:id", h.GetByID)
		g.PUT("/:id", h.Update)
		g.PATCH("/:id/status", h.SetActive)
		if opening != nil {
			h := handler.NewOpeningStockHandler(opening)
			g.POST("/:id/opening-stock", h.Post)
			g.POST("/:id/opening-stock/prepare", h.Prepare)
		}
		if read != nil {
			h := handler.NewStockReadHandler(read)
			g.GET("/:id/stock-balances", h.ListBalances)
			g.GET("/:id/stock-movements", h.ListMovements)
		}
	}
	return r
}

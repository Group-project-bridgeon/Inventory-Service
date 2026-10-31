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
	return NewWithStockAdjustments(db, log, cfg, verifier, warehouses, read, opening, nil)
}
func NewWithStockAdjustments(db DatabaseHealthChecker, log zerolog.Logger, cfg config.HTTPConfig, verifier *security.Verifier, warehouses repository.WarehouseRepository, read *service.StockReadService, opening *service.OpeningStockService, adjustments *service.StockAdjustmentService) *gin.Engine {
	return NewWithReservations(db, log, cfg, verifier, warehouses, read, opening, adjustments, nil, nil)
}
func NewWithReservations(db DatabaseHealthChecker, log zerolog.Logger, cfg config.HTTPConfig, verifier *security.Verifier, warehouses repository.WarehouseRepository, read *service.StockReadService, opening *service.OpeningStockService, adjustments *service.StockAdjustmentService, reservations *service.StockReservationService, machine *security.ReservationMachineVerifier) *gin.Engine {
	return NewWithFulfillment(db, log, cfg, verifier, warehouses, read, opening, adjustments, reservations, machine, nil, nil)
}
func NewWithFulfillment(db DatabaseHealthChecker, log zerolog.Logger, cfg config.HTTPConfig, verifier *security.Verifier, warehouses repository.WarehouseRepository, read *service.StockReadService, opening *service.OpeningStockService, adjustments *service.StockAdjustmentService, reservations *service.StockReservationService, machine *security.ReservationMachineVerifier, fulfillment *service.FulfillmentService, fulfillmentMachine *security.FulfillmentMachineVerifier) *gin.Engine {
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
	anyRoutes := map[string]bool{}
	if adjustments != nil {
		for _, suffix := range []string{"/stock-adjustments", "/stock-adjustments/prepare"} {
			path := "POST /api/v1/inventory/warehouses/:id" + suffix
			policy[path] = []string{"inventory.stock-adjustments:increase", "inventory.stock-adjustments:decrease"}
			anyRoutes[path] = true
		}
		for _, suffix := range []string{"/stock-corrections", "/stock-corrections/prepare"} {
			policy["POST /api/v1/inventory/warehouses/:id"+suffix] = []string{"inventory.stock-corrections:reverse"}
		}
	}
	r.Use(func(c *gin.Context) {
		if strings.Contains(c.FullPath(), "/dispatches") || strings.Contains(c.FullPath(), "/opening-stock") || strings.Contains(c.FullPath(), "/stock-adjustments") || strings.Contains(c.FullPath(), "/stock-corrections") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})

	if fulfillment != nil {
		policy["POST /api/v1/inventory/warehouses/:id/dispatches/:dispatch_id/dispatch"] = []string{"inventory.dispatches:dispatch"}
		policy["POST /api/v1/inventory/warehouses/:id/dispatches/:dispatch_id/delivery-outcomes"] = []string{"inventory.deliveries:update"}
		for _, suffix := range []string{"", "/:dispatch_id", "/:dispatch_id/transitions"} {
			policy["GET /api/v1/inventory/dispatches"+suffix] = []string{"inventory.dispatches:read"}
		}
	}
	fulfillmentPolicy := map[string]string{}
	if fulfillment != nil {
		fulfillmentPolicy["POST /api/v1/inventory/sales/warehouses/:id/dispatches"] = "inventory.dispatches:create"
		fulfillmentPolicy["POST /api/v1/inventory/sales/warehouses/:id/dispatches/:dispatch_id/cancel"] = "inventory.dispatches:cancel"
		for _, suffix := range []string{"", "/:dispatch_id", "/:dispatch_id/transitions"} {
			fulfillmentPolicy["GET /api/v1/inventory/sales/dispatches"+suffix] = "inventory.dispatches:read"
		}
	}
	human := security.Protect(verifier, policy, anyRoutes)
	reservationPolicy := map[string]string{}
	if reservations != nil {
		reservationPolicy["POST /api/v1/inventory/warehouses/:id/reservations"] = "inventory.reservations:reserve"
		reservationPolicy["POST /api/v1/inventory/warehouses/:id/reservations/:reservation_id/release"] = "inventory.reservations:release"
		reservationPolicy["POST /api/v1/inventory/warehouses/:id/reservations/:reservation_id/consume"] = "inventory.reservations:consume"
		reservationPolicy["GET /api/v1/inventory/reservations/:reservation_id"] = "inventory.reservations:read"
		reservationPolicy["GET /api/v1/inventory/reservations"] = "inventory.reservations:read"
	}
	r.Use(func(c *gin.Context) {
		if permission, ok := fulfillmentPolicy[c.Request.Method+" "+c.FullPath()]; ok {
			security.ProtectFulfillments(fulfillmentMachine, permission)(c)
			return
		}
		if permission, ok := reservationPolicy[c.Request.Method+" "+c.FullPath()]; ok {
			security.ProtectReservations(machine, permission)(c)
			return
		}
		human(c)
	})
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

	if fulfillment != nil {
		h := handler.NewFulfillmentHandler(fulfillment)
		r.POST("/api/v1/inventory/sales/warehouses/:id/dispatches", h.Create)
		r.POST("/api/v1/inventory/sales/warehouses/:id/dispatches/:dispatch_id/cancel", h.Cancel)
		r.POST("/api/v1/inventory/warehouses/:id/dispatches/:dispatch_id/dispatch", h.Dispatch)
		r.POST("/api/v1/inventory/warehouses/:id/dispatches/:dispatch_id/delivery-outcomes", h.Delivery)
		for _, prefix := range []string{"/api/v1/inventory/dispatches", "/api/v1/inventory/sales/dispatches"} {
			r.GET(prefix, h.List)
			r.GET(prefix+"/:dispatch_id", h.Get)
			r.GET(prefix+"/:dispatch_id/transitions", h.Audit)
		}
	}
	if reservations != nil {
		h := handler.NewStockReservationHandler(reservations)
		r.GET("/api/v1/inventory/reservations", h.List)
		r.GET("/api/v1/inventory/reservations/:reservation_id", h.Get)
		r.POST("/api/v1/inventory/warehouses/:id/reservations", h.Reserve)
		r.POST("/api/v1/inventory/warehouses/:id/reservations/:reservation_id/release", h.Release)
		r.POST("/api/v1/inventory/warehouses/:id/reservations/:reservation_id/consume", h.Consume)
	}
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
		if adjustments != nil {
			h := handler.NewStockAdjustmentHandler(adjustments)
			g.POST("/:id/stock-adjustments/prepare", h.Prepare)
			g.POST("/:id/stock-adjustments", h.Post)
			g.POST("/:id/stock-corrections/prepare", h.PrepareReverse)
			g.POST("/:id/stock-corrections", h.Reverse)
		}
		if read != nil {
			h := handler.NewStockReadHandler(read)
			g.GET("/:id/stock-balances", h.ListBalances)
			g.GET("/:id/stock-movements", h.ListMovements)
		}
	}
	return r
}

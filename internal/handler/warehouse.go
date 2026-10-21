package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strconv"
	"time"
)

type warehouseService interface {
	Create(context.Context, service.CreateWarehouseInput) (domain.Warehouse, error)
	GetByID(context.Context, uuid.UUID) (domain.Warehouse, error)
	List(context.Context, service.PageInput) (service.WarehousePage, error)
	Update(context.Context, uuid.UUID, service.UpdateWarehouseInput) (domain.Warehouse, error)
	SetActive(context.Context, uuid.UUID, bool) (domain.Warehouse, error)
}
type WarehouseHandler struct{ service warehouseService }

func NewWarehouseHandler(s warehouseService) *WarehouseHandler { return &WarehouseHandler{service: s} }

type createWarehouseRequest struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Address string `json:"address"`
}
type updateWarehouseRequest struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	IsDefault *bool  `json:"is_default"`
}
type warehouseStatusRequest struct {
	IsActive *bool `json:"is_active"`
}
type warehouseResponse struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	IsActive  bool      `json:"is_active"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type warehousePageResponse struct {
	Items      []warehouseResponse `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

func response(w domain.Warehouse) warehouseResponse {
	return warehouseResponse{w.ID, w.Code, w.Name, w.Address, w.IsActive, w.IsDefault, w.CreatedAt, w.UpdatedAt}
}
func decode(c *gin.Context, target any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if err == nil {
		var extra any
		err = decoder.Decode(&extra)
		if err == io.EOF {
			return true
		}
	}
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		logger.Respond(c, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
	} else {
		logger.Respond(c, http.StatusBadRequest, "invalid_input", "invalid request body")
	}
	return false
}
func parseID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		logger.Respond(c, http.StatusBadRequest, "invalid_input", "invalid warehouse id")
		return uuid.Nil, false
	}
	return id, true
}
func writeError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidArgument):
		logger.Respond(c, http.StatusBadRequest, "invalid_input", "invalid warehouse input")
	case errors.Is(err, domain.ErrNotFound):
		logger.Respond(c, http.StatusNotFound, "warehouse_not_found", "warehouse not found")
	case errors.Is(err, domain.ErrDuplicateCode):
		logger.Respond(c, http.StatusConflict, "warehouse_code_exists", "warehouse code already exists")
	case errors.Is(err, domain.ErrDefaultConflict):
		logger.Respond(c, http.StatusConflict, "default_warehouse_conflict", "select another active default warehouse first")
	case errors.Is(err, domain.ErrInactiveDefault):
		logger.Respond(c, http.StatusConflict, "inactive_default", "default warehouse must be active")
	default:
		f := logger.DiagnoseHTTP(c, operation, err)
		switch f.Reason {
		case "context_canceled":
			logger.Respond(c, http.StatusRequestTimeout, "request_canceled", "request canceled")
		case "timeout", "query_canceled":
			logger.Respond(c, http.StatusGatewayTimeout, "request_timeout", "request timed out")
		default:
			logger.Respond(c, http.StatusInternalServerError, "internal_error", "internal server error")
		}
	}
}
func (h *WarehouseHandler) Create(c *gin.Context) {
	var in createWarehouseRequest
	if !decode(c, &in) {
		return
	}
	w, err := h.service.Create(c.Request.Context(), service.CreateWarehouseInput{Code: in.Code, Name: in.Name, Address: in.Address})
	if err != nil {
		writeError(c, "create_warehouse", err)
		return
	}
	c.JSON(http.StatusCreated, response(w))
}
func (h *WarehouseHandler) GetByID(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	w, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		writeError(c, "get_warehouse", err)
		return
	}
	c.JSON(http.StatusOK, response(w))
}
func (h *WarehouseHandler) Update(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var in updateWarehouseRequest
	if !decode(c, &in) {
		return
	}
	w, err := h.service.Update(c.Request.Context(), id, service.UpdateWarehouseInput{Name: in.Name, Address: in.Address, IsDefault: in.IsDefault})
	if err != nil {
		writeError(c, "update_warehouse", err)
		return
	}
	c.JSON(http.StatusOK, response(w))
}
func (h *WarehouseHandler) SetActive(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var in warehouseStatusRequest
	if !decode(c, &in) {
		return
	}
	if in.IsActive == nil {
		logger.Respond(c, http.StatusBadRequest, "invalid_input", "is_active is required")
		return
	}
	w, err := h.service.SetActive(c.Request.Context(), id, *in.IsActive)
	if err != nil {
		writeError(c, "set_warehouse_status", err)
		return
	}
	c.JSON(http.StatusOK, response(w))
}
func (h *WarehouseHandler) List(c *gin.Context) {
	limit := service.DefaultPageLimit
	if raw, exists := c.GetQuery("limit"); exists {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > service.MaxPageLimit {
			logger.Respond(c, http.StatusBadRequest, "invalid_input", "limit must be between 1 and 100")
			return
		}
		limit = n
	}
	page, err := h.service.List(c.Request.Context(), service.PageInput{Limit: limit, Cursor: c.Query("cursor")})
	if err != nil {
		writeError(c, "list_warehouses", err)
		return
	}
	items := make([]warehouseResponse, 0, len(page.Items))
	for _, w := range page.Items {
		items = append(items, response(w))
	}
	c.JSON(http.StatusOK, warehousePageResponse{Items: items, NextCursor: page.NextCursor})
}

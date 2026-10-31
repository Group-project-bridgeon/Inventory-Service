package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type StockReadHandler struct{ service *service.StockReadService }

func NewStockReadHandler(s *service.StockReadService) *StockReadHandler { return &StockReadHandler{s} }

type stockBalanceResponse struct {
	ReservedPieces     string    `json:"reserved_pieces"`
	AvailablePieces    string    `json:"available_pieces"`
	ReservationVersion string    `json:"reservation_version"`
	WarehouseID        uuid.UUID `json:"warehouse_id"`
	VariantID          uuid.UUID `json:"variant_id"`
	BaseUnitID         uuid.UUID `json:"base_unit_id"`
	OnHandPieces       string    `json:"on_hand_pieces"`
	Version            string    `json:"version"`
	UpdatedAt          time.Time `json:"updated_at"`
}
type stockMovementResponse struct {
	ReasonCode             *string    `json:"reason_code,omitempty"`
	Explanation            *string    `json:"explanation,omitempty"`
	OriginalOperationID    *uuid.UUID `json:"reverses_operation_id,omitempty"`
	ID                     uuid.UUID  `json:"id"`
	OperationID            uuid.UUID  `json:"operation_id"`
	WarehouseID            uuid.UUID  `json:"warehouse_id"`
	VariantID              uuid.UUID  `json:"variant_id"`
	Direction              string     `json:"direction"`
	InputCount             string     `json:"input_count"`
	PiecesPerInput         string     `json:"pieces_per_input"`
	Pieces                 string     `json:"pieces"`
	BeforePieces           string     `json:"before_pieces"`
	AfterPieces            string     `json:"after_pieces"`
	BalanceVersion         string     `json:"balance_version"`
	PostedAt               time.Time  `json:"posted_at"`
	ProductID              uuid.UUID  `json:"product_id"`
	BaseUnitID             uuid.UUID  `json:"base_unit_id"`
	PackID                 *uuid.UUID `json:"pack_id"`
	PackRevision           *string    `json:"pack_revision"`
	PackLabel              *string    `json:"pack_label"`
	ProductCode            string     `json:"product_code"`
	ProductName            string     `json:"product_name"`
	SKU                    string     `json:"sku"`
	BaseUnitCode           string     `json:"base_unit_code"`
	StockReferenceRevision string     `json:"stock_reference_revision"`
	ReferenceCheckedAt     time.Time  `json:"reference_checked_at"`
}

func stockReadInput(c *gin.Context, balances bool) (service.StockReadInput, bool) {
	fail := func() (service.StockReadInput, bool) {
		logger.Respond(c, 400, "invalid_input", "invalid stock read input")
		return service.StockReadInput{}, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil || id.String() != c.Param("id") || len(c.Request.URL.RawQuery) > 4096 {
		return fail()
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return fail()
	}
	in := service.StockReadInput{WarehouseID: id, PageInput: service.PageInput{Limit: service.DefaultPageLimit}}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			return fail()
		}
		value := values[0]
		switch key {
		case "variant_id":
			variant, err := uuid.Parse(value)
			if err != nil || variant == uuid.Nil || variant.String() != value {
				return fail()
			}
			in.VariantID = &variant
		case "nonzero_only":
			if !balances || (value != "true" && value != "false") {
				return fail()
			}
			in.NonzeroOnly = value == "true"
		case "limit":
			if len(value) > 3 {
				return fail()
			}
			for _, ch := range value {
				if ch < '0' || ch > '9' {
					return fail()
				}
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > service.MaxPageLimit {
				return fail()
			}
			in.Limit = n
		case "cursor":
			if len(value) > 1024 {
				return fail()
			}
			in.Cursor = value
		default:
			return fail()
		}
	}
	return in, true
}
func stockReadError(c *gin.Context, op string, err error) {
	if errors.Is(err, domain.ErrInvalidArgument) {
		logger.Respond(c, 400, "invalid_input", "invalid stock read input")
		return
	}
	writeError(c, op, err)
}
func (h *StockReadHandler) ListBalances(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	in, ok := stockReadInput(c, true)
	if !ok {
		return
	}
	page, err := h.service.ListBalances(c.Request.Context(), in)
	if err != nil {
		stockReadError(c, "list_stock_balances", err)
		return
	}
	items := make([]stockBalanceResponse, 0, len(page.Items))
	for _, b := range page.Items {
		items = append(items, stockBalanceResponse{strconv.FormatInt(b.ReservedPieces, 10), strconv.FormatInt(b.AvailablePieces, 10), strconv.FormatInt(b.ReservationVersion, 10), b.WarehouseID, b.VariantID, b.BaseUnitID, strconv.FormatInt(b.OnHandPieces, 10), strconv.FormatInt(b.Version, 10), b.UpdatedAt})
	}
	c.JSON(http.StatusOK, struct {
		Items      []stockBalanceResponse `json:"items"`
		NextCursor *string                `json:"next_cursor"`
	}{items, page.NextCursor})
}
func (h *StockReadHandler) ListMovements(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	in, ok := stockReadInput(c, false)
	if !ok {
		return
	}
	page, err := h.service.ListMovements(c.Request.Context(), in)
	if err != nil {
		stockReadError(c, "list_stock_movements", err)
		return
	}
	items := make([]stockMovementResponse, 0, len(page.Items))
	for _, m := range page.Items {
		items = append(items, stockMovementResponse{m.ReasonCode, m.Explanation, m.OriginalOperationID, m.ID, m.OperationID, m.WarehouseID, m.VariantID, m.Direction, strconv.FormatInt(m.InputCount, 10), strconv.FormatInt(m.PiecesPerInput, 10), strconv.FormatInt(m.Pieces, 10), strconv.FormatInt(m.BeforePieces, 10), strconv.FormatInt(m.AfterPieces, 10), strconv.FormatInt(m.BalanceVersion, 10), m.PostedAt, m.ProductID, m.BaseUnitID, m.PackID, m.PackRevision, m.PackLabel, m.ProductCode, m.ProductName, m.SKU, m.BaseUnitCode, m.StockReferenceRevision, m.ReferenceCheckedAt})
	}
	c.JSON(http.StatusOK, struct {
		Items      []stockMovementResponse `json:"items"`
		NextCursor *string                 `json:"next_cursor"`
	}{items, page.NextCursor})
}

package handler

import (
	"net/url"
	"strconv"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type StockReservationHandler struct {
	s *service.StockReservationService
}

func NewStockReservationHandler(s *service.StockReservationService) *StockReservationHandler {
	return &StockReservationHandler{s}
}

type reservationInputLine struct {
	OrderLine string `json:"order_line_id"`
	Variant   string `json:"variant_id"`
	Pieces    string `json:"pieces"`
}
type reservationRequest struct {
	Command             string                 `json:"command_id"`
	Reservation         *string                `json:"reservation_id,omitempty"`
	Order               string                 `json:"order_id"`
	Generation          string                 `json:"generation"`
	ExpectedOrder       string                 `json:"expected_order_version"`
	ExpectedReservation *string                `json:"expected_reservation_version,omitempty"`
	Previous            *string                `json:"expected_previous_generation,omitempty"`
	Lines               []reservationInputLine `json:"lines,omitempty"`
}

func parseReservation(c *gin.Context, action stock.ReservationAction) (stock.ReservationCommand, bool) {
	var in reservationRequest
	wh, ok := adjustmentInput(c, &in)
	if !ok {
		return stock.ReservationCommand{}, false
	}
	fail := func() (stock.ReservationCommand, bool) {
		openingError(c, stock.ErrInvalidArgument)
		return stock.ReservationCommand{}, false
	}
	cmd, valid := operationID(in.Command)
	if !valid {
		return fail()
	}
	headers := c.Request.Header.Values("Idempotency-Key")
	if len(headers) != 1 {
		return fail()
	}
	key, valid := operationID(headers[0])
	if !valid {
		return fail()
	}
	order, valid := openingID(in.Order)
	if !valid {
		return fail()
	}
	generation, err := stock.ParsePositiveQuantity(in.Generation)
	if err != nil {
		return fail()
	}
	orderVersion, err := stock.ParseBalance(in.ExpectedOrder)
	if err != nil {
		return fail()
	}
	result := stock.ReservationCommand{CommandID: cmd, Key: key, OrderID: order, WarehouseID: wh, Generation: generation, ExpectedOrderVersion: orderVersion, Action: action, FirstRequestID: c.GetString(logger.RequestIDKey)}
	if action == stock.Reserve {
		if in.Reservation == nil || in.Previous == nil || in.ExpectedReservation != nil {
			return fail()
		}
		result.ReservationID, valid = openingID(*in.Reservation)
		if !valid {
			return fail()
		}
		result.ExpectedPreviousGeneration, err = stock.ParseBalance(*in.Previous)
		if err != nil {
			return fail()
		}
		for _, line := range in.Lines {
			orderLine, valid := openingID(line.OrderLine)
			if !valid {
				return fail()
			}
			variant, valid := openingID(line.Variant)
			if !valid {
				return fail()
			}
			pieces, err := stock.ParsePositiveQuantity(line.Pieces)
			if err != nil {
				return fail()
			}
			result.Lines = append(result.Lines, stock.ReservationLine{OrderLineID: orderLine, VariantID: variant, Pieces: pieces})
		}
	} else {
		if in.Reservation != nil || in.Previous != nil || in.Lines != nil || in.ExpectedReservation == nil {
			return fail()
		}
		result.ReservationID, valid = openingID(c.Param("reservation_id"))
		if !valid {
			return fail()
		}
		result.ExpectedReservationVersion, err = stock.ParsePositiveQuantity(*in.ExpectedReservation)
		if err != nil {
			return fail()
		}
	}
	if result.Validate() != nil {
		return fail()
	}
	return result, true
}
func (h *StockReservationHandler) mutate(c *gin.Context, action stock.ReservationAction) {
	command, ok := parseReservation(c, action)
	if !ok {
		return
	}
	access, ok := security.ReservationIdentity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	log := logger.Request(c).With().Str("operation_id", command.CommandID.String()).Str("reservation_id", command.ReservationID.String()).Logger()
	c.Set(logger.RequestLoggerKey, log)
	result, err := h.s.Mutate(c.Request.Context(), access, command)
	if err != nil {
		openingError(c, err)
		return
	}
	status := 200
	if action == stock.Reserve && !result.Replayed {
		status = 201
	}
	c.JSON(status, result)
}
func (h *StockReservationHandler) Reserve(c *gin.Context) { h.mutate(c, stock.Reserve) }
func (h *StockReservationHandler) Release(c *gin.Context) { h.mutate(c, stock.Release) }
func (h *StockReservationHandler) Consume(c *gin.Context) { h.mutate(c, stock.Consume) }

type reservationStatusLine struct {
	Number    int16          `json:"line_number"`
	OrderLine uuid.UUID      `json:"order_line_id"`
	Variant   uuid.UUID      `json:"variant_id"`
	Pieces    stock.Quantity `json:"pieces"`
	Product   uuid.UUID      `json:"product_id"`
	Unit      uuid.UUID      `json:"base_unit_id"`
}
type reservationStatus struct {
	Reservation  uuid.UUID               `json:"reservation_id"`
	Order        uuid.UUID               `json:"order_id"`
	Warehouse    uuid.UUID               `json:"warehouse_id"`
	Generation   stock.Quantity          `json:"generation"`
	State        stock.ReservationState  `json:"state"`
	Version      stock.Quantity          `json:"version"`
	OrderVersion stock.Quantity          `json:"order_version"`
	CreatedAt    string                  `json:"created_at"`
	UpdatedAt    string                  `json:"updated_at"`
	Lines        []reservationStatusLine `json:"lines,omitempty"`
}

func reservationStatusDTO(r stock.ReservationSnapshot) reservationStatus {
	out := reservationStatus{Reservation: r.ID, Order: r.OrderID, Warehouse: r.WarehouseID, Generation: r.Generation, State: r.State, Version: r.Version, OrderVersion: r.OrderVersion, CreatedAt: r.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), UpdatedAt: r.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
	for _, l := range r.Lines {
		out.Lines = append(out.Lines, reservationStatusLine{l.LineNumber, l.OrderLineID, l.ReservationLine.VariantID, l.Pieces, l.ProductID, l.BaseUnitID})
	}
	return out
}
func (h *StockReservationHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := openingID(c.Param("reservation_id"))
	if !ok || c.Request.URL.RawQuery != "" {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	access, ok := security.ReservationIdentity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	result, err := h.s.Get(c.Request.Context(), access, id)
	if err != nil {
		openingError(c, err)
		return
	}
	c.JSON(200, reservationStatusDTO(result))
}
func (h *StockReservationHandler) List(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.RawQuery) > 2048 {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	in := service.PageInput{Limit: service.DefaultPageLimit}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			openingError(c, stock.ErrInvalidArgument)
			return
		}
		switch key {
		case "limit":
			v, err := strconv.Atoi(values[0])
			if err != nil || strconv.Itoa(v) != values[0] || v < 1 || v > service.MaxPageLimit {
				openingError(c, stock.ErrInvalidArgument)
				return
			}
			in.Limit = v
		case "cursor":
			in.Cursor = values[0]
		default:
			openingError(c, stock.ErrInvalidArgument)
			return
		}
	}
	access, ok := security.ReservationIdentity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	page, err := h.s.List(c.Request.Context(), access, in)
	if err != nil {
		openingError(c, err)
		return
	}
	items := make([]reservationStatus, 0, len(page.Items))
	for _, row := range page.Items {
		items = append(items, reservationStatusDTO(row))
	}
	c.JSON(200, gin.H{"items": items, "next_cursor": page.NextCursor, "state_filter": "ACTIVE"})
}

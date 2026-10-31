package handler

import (
	f "github.com/Group-project-bridgeon/Inventory-Service/internal/fulfillment"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/url"
	"strconv"
	"strings"
)

type FulfillmentHandler struct{ s *service.FulfillmentService }

func NewFulfillmentHandler(s *service.FulfillmentService) *FulfillmentHandler {
	return &FulfillmentHandler{s}
}

type fulfillmentRequest struct {
	Command             string  `json:"command_id"`
	ID                  *string `json:"dispatch_id,omitempty"`
	Reservation         *string `json:"reservation_id,omitempty"`
	Order               *string `json:"order_id,omitempty"`
	Generation          *string `json:"generation,omitempty"`
	Expected            *string `json:"expected_dispatch_version,omitempty"`
	ExpectedReservation *string `json:"expected_reservation_version,omitempty"`
	ExpectedOrder       *string `json:"expected_order_version,omitempty"`
	Outcome             *string `json:"outcome,omitempty"`
	External            *string `json:"external_reference,omitempty"`
	Note                *string `json:"note,omitempty"`
}

func fulfillmentIdentity(c *gin.Context) (f.Access, bool) {
	if strings.HasPrefix(c.FullPath(), "/api/v1/inventory/sales/") {
		return security.FulfillmentIdentity(c)
	}
	return security.HumanFulfillmentIdentity(c)
}
func parseFulfillment(c *gin.Context, action f.Action) (f.Command, bool) {
	var in fulfillmentRequest
	warehouse, ok := adjustmentInput(c, &in)
	if !ok {
		return f.Command{}, false
	}
	fail := func() (f.Command, bool) { openingError(c, stock.ErrInvalidArgument); return f.Command{}, false }
	command, ok := operationID(in.Command)
	if !ok {
		return fail()
	}
	headers := c.Request.Header.Values("Idempotency-Key")
	if len(headers) != 1 {
		return fail()
	}
	key, ok := operationID(headers[0])
	if !ok {
		return fail()
	}
	out := f.Command{CommandID: command, Key: key, WarehouseID: warehouse, Action: action, FirstRequestID: c.GetString(logger.RequestIDKey)}
	parseID := func(s *string) (uuid.UUID, bool) {
		if s == nil {
			return uuid.Nil, false
		}
		return openingID(*s)
	}
	parseQty := func(s *string) (stock.Quantity, error) {
		if s == nil {
			return 0, stock.ErrInvalidArgument
		}
		return stock.ParsePositiveQuantity(*s)
	}
	var e error
	if action == f.Create {
		if in.Expected != nil || in.Note != nil || in.Outcome != nil {
			return fail()
		}
		out.ID, ok = parseID(in.ID)
		if !ok {
			return fail()
		}
		out.ReservationID, ok = parseID(in.Reservation)
		if !ok {
			return fail()
		}
		out.OrderID, ok = parseID(in.Order)
		if !ok {
			return fail()
		}
		out.Generation, e = parseQty(in.Generation)
		if e != nil {
			return fail()
		}
	} else {
		if in.ID != nil || in.Reservation != nil || in.Order != nil || in.Generation != nil {
			return fail()
		}
		out.ID, ok = openingID(c.Param("dispatch_id"))
		if !ok {
			return fail()
		}
		out.ExpectedVersion, e = parseQty(in.Expected)
		if e != nil {
			return fail()
		}
	}
	if action == f.Create || action == f.Dispatch {
		out.ExpectedReservationVersion, e = parseQty(in.ExpectedReservation)
		if e != nil {
			return fail()
		}
		out.ExpectedOrderVersion, e = parseQty(in.ExpectedOrder)
		if e != nil {
			return fail()
		}
	} else if in.ExpectedReservation != nil || in.ExpectedOrder != nil {
		return fail()
	}
	if action == f.Dispatch && (in.Note != nil || in.External != nil || in.Outcome != nil) {
		return fail()
	}
	if action == f.Cancel && (in.External != nil || in.Outcome != nil) {
		return fail()
	}
	if action == f.Delivery {
		if in.Outcome == nil {
			return fail()
		}
		out.Outcome = *in.Outcome
	}
	if in.External != nil {
		out.ExternalReference, e = f.Text(*in.External, 100)
		if e != nil {
			return fail()
		}
	}
	if in.Note != nil {
		out.Note, e = f.Text(*in.Note, 500)
		if e != nil {
			return fail()
		}
	}
	if out.Validate() != nil {
		return fail()
	}
	return out, true
}
func (h *FulfillmentHandler) mutate(c *gin.Context, action f.Action) {
	command, ok := parseFulfillment(c, action)
	if !ok {
		return
	}
	access, ok := fulfillmentIdentity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	log := logger.Request(c).With().Str("dispatch_id", command.ID.String()).Str("operation_id", command.CommandID.String()).Logger()
	c.Set(logger.RequestLoggerKey, log)
	out, e := h.s.Mutate(c.Request.Context(), access, command)
	if e != nil {
		openingError(c, e)
		return
	}
	status := 200
	if action == f.Create && !out.Replayed {
		status = 201
	}
	c.JSON(status, out)
}
func (h *FulfillmentHandler) Create(c *gin.Context)   { h.mutate(c, f.Create) }
func (h *FulfillmentHandler) Cancel(c *gin.Context)   { h.mutate(c, f.Cancel) }
func (h *FulfillmentHandler) Dispatch(c *gin.Context) { h.mutate(c, f.Dispatch) }
func (h *FulfillmentHandler) Delivery(c *gin.Context) { h.mutate(c, f.Delivery) }
func (h *FulfillmentHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := openingID(c.Param("dispatch_id"))
	if !ok || c.Request.URL.RawQuery != "" {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	a, ok := fulfillmentIdentity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	out, e := h.s.Get(c.Request.Context(), a, id)
	if e != nil {
		openingError(c, e)
		return
	}
	c.JSON(200, struct {
		service.DispatchDetail
		TransitionsURL string `json:"transitions_url"`
	}{out, c.Request.URL.Path + "/transitions"})
}
func fulfillmentPageQuery(c *gin.Context, audit bool) (f.Filter, bool) {
	fail := func() (f.Filter, bool) { openingError(c, stock.ErrInvalidArgument); return f.Filter{}, false }
	if len(c.Request.URL.RawQuery) > 2048 {
		return fail()
	}
	q, e := url.ParseQuery(c.Request.URL.RawQuery)
	if e != nil {
		return fail()
	}
	out := f.Filter{Limit: 50}
	for key, vs := range q {
		if len(vs) != 1 || vs[0] == "" {
			return fail()
		}
		v := vs[0]
		switch key {
		case "limit":
			n, e := strconv.Atoi(v)
			if e != nil || strconv.Itoa(n) != v || n < 1 || n > 100 {
				return fail()
			}
			out.Limit = n
		case "cursor":
			if len(v) > 1024 {
				return fail()
			}
			out.Cursor = v
		default:
			if audit {
				return fail()
			}
			if key == "state" {
				out.State = v
				continue
			}
			id, ok := openingID(v)
			if !ok {
				return fail()
			}
			switch key {
			case "warehouse_id":
				out.WarehouseID = id
			case "order_id":
				out.OrderID = id
			case "reservation_id":
				out.ReservationID = id
			case "owner_machine_id":
				out.OwnerMachineID = id
			default:
				return fail()
			}
		}
	}
	return out, true
}
func (h *FulfillmentHandler) List(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	in, ok := fulfillmentPageQuery(c, false)
	if !ok {
		return
	}
	a, ok := fulfillmentIdentity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	out, e := h.s.List(c.Request.Context(), a, in)
	if e != nil {
		openingError(c, e)
		return
	}
	c.JSON(200, out)
}
func (h *FulfillmentHandler) Audit(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := openingID(c.Param("dispatch_id"))
	if !ok {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	in, ok := fulfillmentPageQuery(c, true)
	if !ok {
		return
	}
	a, ok := fulfillmentIdentity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	out, e := h.s.Audit(c.Request.Context(), a, id, service.PageInput{Limit: in.Limit, Cursor: in.Cursor})
	if e != nil {
		openingError(c, e)
		return
	}
	c.JSON(200, out)
}

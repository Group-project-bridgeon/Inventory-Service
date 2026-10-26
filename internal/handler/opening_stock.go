package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/strictjson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type OpeningStockHandler struct{ s *service.OpeningStockService }

func NewOpeningStockHandler(s *service.OpeningStockService) *OpeningStockHandler {
	return &OpeningStockHandler{s}
}

type openingLine struct {
	Number       int16   `json:"line_number"`
	Variant      string  `json:"variant_id"`
	Count        string  `json:"input_count"`
	Pack         *string `json:"pack_id,omitempty"`
	PackRevision *string `json:"pack_revision,omitempty"`
	Expected     *string `json:"expected_stock_reference_revision,omitempty"`
}
type openingRequest struct {
	Operation *string       `json:"operation_id,omitempty"`
	Lines     []openingLine `json:"lines"`
}

func openingID(s string) (uuid.UUID, bool) {
	id, e := uuid.Parse(s)
	return id, e == nil && id != uuid.Nil && id.String() == s
}
func operationID(s string) (uuid.UUID, bool) {
	id, ok := openingID(s)
	return id, ok && id.Variant() == uuid.RFC4122 && (id.Version() == 4 || id.Version() == 7)
}
func openingInput(c *gin.Context, prepare bool) (uuid.UUID, uuid.UUID, uuid.UUID, []stock.CommandLine, bool) {
	fail := func(status int) (uuid.UUID, uuid.UUID, uuid.UUID, []stock.CommandLine, bool) {
		code := "invalid_input"
		if status == 413 {
			code = "request_too_large"
		}
		if status == 415 {
			code = "unsupported_media_type"
		}
		logger.Respond(c, status, code, "invalid opening stock request")
		return uuid.Nil, uuid.Nil, uuid.Nil, nil, false
	}
	c.Header("Cache-Control", "no-store")
	wh, ok := openingID(c.Param("id"))
	if !ok || c.Request.URL.RawQuery != "" {
		return fail(400)
	}
	if len(c.Request.Header.Values("Content-Type")) != 1 || c.GetHeader("Content-Type") != "application/json" {
		return fail(415)
	}
	data, e := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if e != nil {
		var limit *http.MaxBytesError
		if errors.As(e, &limit) {
			return fail(413)
		}
		return fail(400)
	}
	var in openingRequest
	if strictjson.Decode(data, &in) != nil {
		return fail(400)
	}
	// Null and omission have different meanings even for optional fields.
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return fail(400)
	}
	for _, v := range raw {
		if strings.TrimSpace(string(v)) == "null" {
			return fail(400)
		}
	}
	var raws []map[string]json.RawMessage
	if json.Unmarshal(raw["lines"], &raws) != nil {
		return fail(400)
	}
	for _, r := range raws {
		for _, v := range r {
			if strings.TrimSpace(string(v)) == "null" {
				return fail(400)
			}
		}
	}
	var op, key uuid.UUID
	if prepare {
		if in.Operation != nil || len(c.Request.Header.Values("Idempotency-Key")) != 0 {
			return fail(400)
		}
	} else {
		if in.Operation == nil {
			return fail(400)
		}
		op, ok = operationID(*in.Operation)
		if !ok {
			return fail(400)
		}
		headers := c.Request.Header.Values("Idempotency-Key")
		if len(headers) != 1 {
			return fail(400)
		}
		key, ok = operationID(headers[0])
		if !ok {
			return fail(400)
		}
	}
	lines := make([]stock.CommandLine, 0, len(in.Lines))
	selections := make([]stock.Selection, 0, len(in.Lines))
	for _, l := range in.Lines {
		variant, valid := openingID(l.Variant)
		if !valid {
			return fail(400)
		}
		count, e := stock.ParsePositiveQuantity(l.Count)
		if e != nil {
			return fail(400)
		}
		var pack uuid.UUID
		revision := ""
		if (l.Pack == nil) != (l.PackRevision == nil) {
			return fail(400)
		}
		if l.Pack != nil {
			pack, valid = openingID(*l.Pack)
			if !valid {
				return fail(400)
			}
			revision = *l.PackRevision
			if _, e := stock.ParsePositiveQuantity(revision); e != nil {
				return fail(400)
			}
		}
		expected := ""
		if prepare {
			if l.Expected != nil {
				return fail(400)
			}
		} else {
			if l.Expected == nil || len(*l.Expected) != 77 || !strings.HasPrefix(*l.Expected, "cat-stock-v1:") {
				return fail(400)
			}
			expected = *l.Expected
			for _, r := range expected[13:] {
				if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
					return fail(400)
				}
			}
		}
		selections = append(selections, stock.Selection{LineNumber: l.Number, VariantID: variant, InputCount: count, PackID: pack, PackRevision: revision})
		lines = append(lines, stock.CommandLine{LineNumber: l.Number, VariantID: variant, InputCount: count, PackID: pack, PackRevision: revision, ExpectedStockReferenceRevision: expected})
	}
	if stock.ValidateSelections(selections) != nil {
		return fail(400)
	}
	return wh, op, key, lines, true
}

type openingPostedLine struct {
	Number   int16          `json:"line_number"`
	Variant  uuid.UUID      `json:"variant_id"`
	Movement uuid.UUID      `json:"movement_id"`
	After    stock.Quantity `json:"after_pieces"`
	Version  string         `json:"balance_version"`
}
type openingPosted struct {
	Operation uuid.UUID           `json:"operation_id"`
	Version   string              `json:"result_version"`
	Posted    time.Time           `json:"posted_at"`
	Replayed  bool                `json:"replayed"`
	Lines     []openingPostedLine `json:"lines"`
}

func (h *OpeningStockHandler) Post(c *gin.Context) {
	wh, op, key, lines, ok := openingInput(c, false)
	if !ok {
		return
	}
	cl, ok := security.Identity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	log := logger.Request(c).With().Str("operation_id", op.String()).Logger()
	c.Set(logger.RequestLoggerKey, log)
	result, err := h.s.Post(c.Request.Context(), cl, wh, op, key, lines, c.GetString(logger.RequestIDKey))
	if err != nil {
		openingError(c, err)
		return
	}
	out := openingPosted{Operation: result.OperationID, Version: strconv.Itoa(int(result.ResultVersion)), Posted: result.PostedAt, Replayed: result.Replayed, Lines: make([]openingPostedLine, 0, len(result.Lines))}
	for _, l := range result.Lines {
		out.Lines = append(out.Lines, openingPostedLine{l.LineNumber, l.VariantID, l.MovementID, l.AfterPieces, strconv.FormatInt(l.BalanceVersion, 10)})
	}
	status := 201
	if result.Replayed {
		status = 200
	}
	c.JSON(status, out)
}

type openingPreparedLine struct {
	Number       int16          `json:"line_number"`
	Variant      uuid.UUID      `json:"variant_id"`
	Product      uuid.UUID      `json:"product_id"`
	Unit         uuid.UUID      `json:"base_unit_id"`
	ProductCode  string         `json:"product_code"`
	ProductName  string         `json:"product_name"`
	SKU          string         `json:"sku"`
	UnitCode     string         `json:"base_unit_code"`
	Count        stock.Quantity `json:"input_count"`
	Factor       stock.Quantity `json:"pieces_per_input"`
	Pieces       stock.Quantity `json:"pieces"`
	Expected     string         `json:"expected_stock_reference_revision"`
	Pack         *uuid.UUID     `json:"pack_id,omitempty"`
	PackRevision *string        `json:"pack_revision,omitempty"`
	PackLabel    *string        `json:"pack_label,omitempty"`
}

func (h *OpeningStockHandler) Prepare(c *gin.Context) {
	wh, _, _, lines, ok := openingInput(c, true)
	if !ok {
		return
	}
	cl, ok := security.Identity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	selected := make([]stock.Selection, 0, len(lines))
	for _, l := range lines {
		selected = append(selected, stock.Selection{LineNumber: l.LineNumber, VariantID: l.VariantID, InputCount: l.InputCount, PackID: l.PackID, PackRevision: l.PackRevision})
	}
	prepared, err := h.s.Prepare(c.Request.Context(), cl, wh, selected)
	if err != nil {
		openingError(c, err)
		return
	}
	out := make([]openingPreparedLine, 0, len(prepared))
	for _, l := range prepared {
		v := openingPreparedLine{Number: l.LineNumber, Variant: l.CommandLine.VariantID, Product: l.ProductID, Unit: l.BaseUnitID, ProductCode: l.ProductCode, ProductName: l.ProductName, SKU: l.SKU, UnitCode: l.BaseUnitCode, Count: l.InputCount, Factor: l.Factor, Pieces: l.Pieces, Expected: l.StockReferenceRevision}
		if l.Pack != nil {
			v.Pack = &l.Pack.ID
			v.PackRevision = &l.Pack.Revision
			v.PackLabel = l.PackLabel
		}
		out = append(out, v)
	}
	c.JSON(200, gin.H{"warehouse_id": wh, "checked_at": prepared[0].CheckedAt, "lines": out})
}
func openingError(c *gin.Context, err error) {
	status, code := 500, "internal_error"
	switch {
	case errors.Is(err, stock.ErrOutcomeUnknown):
		status, code = 503, "outcome_unknown"
	case errors.Is(err, stock.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, stock.ErrInvalidArgument), errors.Is(err, stock.ErrQuantityOverflow):
		status, code = 400, "invalid_input"
	case errors.Is(err, stock.ErrWarehouseNotFound):
		status, code = 404, "warehouse_not_found"
	case errors.Is(err, stock.ErrReferenceNotFound):
		status, code = 404, "reference_not_found"
	case errors.Is(err, stock.ErrOpeningNotEligible):
		status, code = 409, "opening_not_eligible"
	case errors.Is(err, stock.ErrWarehouseInactive):
		status, code = 409, "warehouse_inactive"
	case errors.Is(err, stock.ErrReferenceInactive):
		status, code = 409, "reference_inactive"
	case errors.Is(err, stock.ErrReferenceIneligible):
		status, code = 409, "reference_ineligible"
	case errors.Is(err, stock.ErrReferenceChanged):
		status, code = 409, "reference_changed"
	case errors.Is(err, stock.ErrKeyConflict):
		status, code = 409, "idempotency_key_reused"
	case errors.Is(err, stock.ErrOperationConflict):
		status, code = 409, "operation_conflict"
	case errors.Is(err, stock.ErrInProgress):
		status, code = 409, "operation_in_progress"
	case errors.Is(err, stock.ErrVersionExhausted):
		status, code = 409, "version_exhausted"
	case errors.Is(err, context.Canceled):
		status, code = 408, "request_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		status, code = 504, "timeout"
	case errors.Is(err, stock.ErrBusy):
		status, code = 503, "stock_busy"
	case errors.Is(err, stock.ErrReferenceUnavailable), errors.Is(err, stock.ErrReferenceUnauthorized), errors.Is(err, stock.ErrReferenceDenied), errors.Is(err, stock.ErrReferenceMalformed), errors.Is(err, stock.ErrReferenceStale):
		status, code = 503, "reference_unavailable"
	}
	if status >= 500 {
		logger.DiagnoseHTTP(c, "opening_stock", err)
	}
	message := "opening stock request failed"
	if code == "outcome_unknown" {
		message = "posting outcome unknown; reconcile using the same operation, key and request"
	}
	logger.Respond(c, status, code, message)
}

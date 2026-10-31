package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/strictjson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type StockAdjustmentHandler struct {
	s *service.StockAdjustmentService
}

func NewStockAdjustmentHandler(s *service.StockAdjustmentService) *StockAdjustmentHandler {
	return &StockAdjustmentHandler{s}
}

type adjustmentRequest struct {
	Operation   *string         `json:"operation_id,omitempty"`
	Direction   stock.Direction `json:"direction"`
	Reason      string          `json:"reason_code"`
	Explanation string          `json:"explanation"`
	Lines       []openingLine   `json:"lines"`
}
type correctionReference struct {
	Variant  string `json:"variant_id"`
	Expected string `json:"expected_stock_reference_revision"`
}
type correctionRequest struct {
	Operation   *string               `json:"operation_id,omitempty"`
	Original    string                `json:"original_operation_id"`
	Reason      string                `json:"reason_code"`
	Explanation string                `json:"explanation"`
	References  []correctionReference `json:"references,omitempty"`
}

func noJSONNull(raw json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil && obj != nil {
		for _, v := range obj {
			if !noJSONNull(v) {
				return false
			}
		}
		return true
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil && arr != nil {
		for _, v := range arr {
			if !noJSONNull(v) {
				return false
			}
		}
	}
	return true
}
func adjustmentInput(c *gin.Context, dst any) (uuid.UUID, bool) {
	c.Header("Cache-Control", "no-store")
	fail := func(status int) (uuid.UUID, bool) {
		code := "invalid_input"
		if status == 413 {
			code = "request_too_large"
		}
		if status == 415 {
			code = "unsupported_media_type"
		}
		logger.Respond(c, status, code, "invalid stock request")
		return uuid.Nil, false
	}
	wh, ok := openingID(c.Param("id"))
	if !ok || c.Request.URL.RawQuery != "" {
		return fail(400)
	}
	if len(c.Request.Header.Values("Content-Type")) != 1 || c.GetHeader("Content-Type") != "application/json" {
		return fail(415)
	}
	data, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return fail(413)
		}
		return fail(400)
	}
	if strictjson.Decode(data, dst) != nil || !noJSONNull(data) {
		return fail(400)
	}
	return wh, true
}
func adjustmentIdentities(c *gin.Context, op *string, prepare bool) (uuid.UUID, uuid.UUID, bool) {
	if prepare {
		if op != nil || len(c.Request.Header.Values("Idempotency-Key")) != 0 {
			return uuid.Nil, uuid.Nil, false
		}
		return uuid.Nil, uuid.Nil, true
	}
	if op == nil {
		return uuid.Nil, uuid.Nil, false
	}
	id, ok := operationID(*op)
	if !ok {
		return id, uuid.Nil, false
	}
	keys := c.Request.Header.Values("Idempotency-Key")
	if len(keys) != 1 {
		return id, uuid.Nil, false
	}
	key, ok := operationID(keys[0])
	return id, key, ok
}
func preparedStockResponse(lines []stock.PreparedLine) []openingPreparedLine {
	out := make([]openingPreparedLine, 0, len(lines))
	for _, l := range lines {
		v := openingPreparedLine{Number: l.LineNumber, Variant: l.CommandLine.VariantID, Product: l.ProductID, Unit: l.BaseUnitID, ProductCode: l.ProductCode, ProductName: l.ProductName, SKU: l.SKU, UnitCode: l.BaseUnitCode, Count: l.InputCount, Factor: l.Factor, Pieces: l.Pieces, Expected: l.StockReferenceRevision}
		if l.PackID != uuid.Nil {
			pack, revision := l.PackID, l.CommandLine.PackRevision
			v.Pack = &pack
			v.PackRevision = &revision
			v.PackLabel = l.PackLabel
		}
		out = append(out, v)
	}
	return out
}
func postedStockResponse(c *gin.Context, r stock.PostedResult) {
	out := openingPosted{Operation: r.OperationID, Version: strconv.Itoa(int(r.ResultVersion)), Posted: r.PostedAt, Replayed: r.Replayed, Lines: make([]openingPostedLine, 0, len(r.Lines))}
	for _, l := range r.Lines {
		out.Lines = append(out.Lines, openingPostedLine{l.LineNumber, l.VariantID, l.MovementID, l.AfterPieces, strconv.FormatInt(l.BalanceVersion, 10)})
	}
	status := 201
	if r.Replayed {
		status = 200
	}
	c.JSON(status, out)
}
func (h *StockAdjustmentHandler) adjustment(c *gin.Context, prepare bool) {
	var in adjustmentRequest
	wh, ok := adjustmentInput(c, &in)
	if !ok {
		return
	}
	op, key, ok := adjustmentIdentities(c, in.Operation, prepare)
	if !ok {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	lines, ok := parseStockLines(in.Lines, prepare)
	if !ok {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	audit := stock.AdjustmentAudit{ReasonCode: in.Reason, Explanation: strings.TrimSpace(in.Explanation)}
	if stock.ValidateAudit(&audit) != nil || (in.Direction != stock.In && in.Direction != stock.Out) {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	cl, ok := security.Identity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	if prepare {
		selections := make([]stock.Selection, 0, len(lines))
		for _, l := range lines {
			selections = append(selections, stock.Selection{LineNumber: l.LineNumber, VariantID: l.VariantID, InputCount: l.InputCount, PackID: l.PackID, PackRevision: l.PackRevision})
		}
		result, err := h.s.Prepare(c.Request.Context(), cl, wh, in.Direction, audit, selections)
		if err != nil {
			openingError(c, err)
			return
		}
		c.JSON(200, gin.H{"warehouse_id": wh, "direction": in.Direction, "checked_at": result[0].CheckedAt, "lines": preparedStockResponse(result)})
		return
	}
	setStockOperationLogger(c, op)
	result, err := h.s.Post(c.Request.Context(), cl, wh, op, key, in.Direction, audit, lines, c.GetString(logger.RequestIDKey))
	if err != nil {
		openingError(c, err)
		return
	}
	postedStockResponse(c, result)
}
func validStockRevision(s string) bool {
	if len(s) != 77 || !strings.HasPrefix(s, "cat-stock-v1:") {
		return false
	}
	for _, r := range s[13:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func (h *StockAdjustmentHandler) correction(c *gin.Context, prepare bool) {
	var in correctionRequest
	wh, ok := adjustmentInput(c, &in)
	if !ok {
		return
	}
	op, key, ok := adjustmentIdentities(c, in.Operation, prepare)
	if !ok {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	target, ok := openingID(in.Original)
	if !ok || (prepare && in.References != nil) || (!prepare && (len(in.References) < 1 || len(in.References) > stock.MaxLines)) {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	audit := stock.AdjustmentAudit{ReasonCode: in.Reason, Explanation: strings.TrimSpace(in.Explanation)}
	if stock.ValidateAudit(&audit) != nil {
		openingError(c, stock.ErrInvalidArgument)
		return
	}
	cl, ok := security.Identity(c)
	if !ok {
		openingError(c, stock.ErrForbidden)
		return
	}
	if prepare {
		direction, lines, err := h.s.PrepareReverse(c.Request.Context(), cl, wh, target, audit)
		if err != nil {
			openingError(c, err)
			return
		}
		c.JSON(200, gin.H{"warehouse_id": wh, "original_operation_id": target, "direction": direction, "checked_at": lines[0].CheckedAt, "lines": preparedStockResponse(lines)})
		return
	}
	expected := service.ReferenceExpectations{}
	for _, r := range in.References {
		id, ok := openingID(r.Variant)
		if !ok || !validStockRevision(r.Expected) {
			openingError(c, stock.ErrInvalidArgument)
			return
		}
		if _, exists := expected[id]; exists {
			openingError(c, stock.ErrInvalidArgument)
			return
		}
		expected[id] = r.Expected
	}
	setStockOperationLogger(c, op)
	result, err := h.s.Reverse(c.Request.Context(), cl, wh, op, key, target, audit, expected, c.GetString(logger.RequestIDKey))
	if err != nil {
		openingError(c, err)
		return
	}
	postedStockResponse(c, result)
}
func setStockOperationLogger(c *gin.Context, id uuid.UUID) {
	log := logger.Request(c).With().Str("operation_id", id.String()).Logger()
	c.Set(logger.RequestLoggerKey, log)
}
func (h *StockAdjustmentHandler) Prepare(c *gin.Context)        { h.adjustment(c, true) }
func (h *StockAdjustmentHandler) Post(c *gin.Context)           { h.adjustment(c, false) }
func (h *StockAdjustmentHandler) PrepareReverse(c *gin.Context) { h.correction(c, true) }
func (h *StockAdjustmentHandler) Reverse(c *gin.Context)        { h.correction(c, false) }

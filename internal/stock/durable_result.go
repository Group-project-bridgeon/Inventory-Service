package stock

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/google/uuid"
)

// DecodePostedResult decodes exactly SQL stock_expected_result v1. Int64 values
// stay decimal strings; malformed history is never interpreted as a new request.
func DecodePostedResult(data []byte, version int16, c Command) (PostedResult, error) {
	if version != ResultVersion {
		return PostedResult{}, ErrStoredResult
	}
	var wire struct {
		OperationID   uuid.UUID `json:"operation_id"`
		ResultVersion string    `json:"result_version"`
		PostedAt      time.Time `json:"posted_at"`
		Lines         []struct {
			LineNumber     int16     `json:"line_number"`
			VariantID      uuid.UUID `json:"variant_id"`
			MovementID     uuid.UUID `json:"movement_id"`
			AfterPieces    *Quantity `json:"after_pieces"`
			BalanceVersion string    `json:"balance_version"`
		} `json:"lines"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&wire); err != nil {
		return PostedResult{}, ErrStoredResult
	}
	if d.Decode(new(any)) != io.EOF || wire.ResultVersion != "1" {
		return PostedResult{}, ErrStoredResult
	}
	r := PostedResult{OperationID: wire.OperationID, ResultVersion: version, PostedAt: wire.PostedAt, Replayed: true}
	for _, l := range wire.Lines {
		v, err := ParsePositiveQuantity(l.BalanceVersion)
		if err != nil || l.AfterPieces == nil {
			return PostedResult{}, ErrStoredResult
		}
		r.Lines = append(r.Lines, PostedLineResult{l.LineNumber, l.VariantID, l.MovementID, *l.AfterPieces, int64(v)})
	}
	if r.ValidateFor(c) != nil {
		return PostedResult{}, ErrStoredResult
	}
	return r, nil
}

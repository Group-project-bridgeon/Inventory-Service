package stock

import (
	"errors"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

const (
	AdjustmentProducer = "inventory.stock-adjustments.v1"
	CorrectionProducer = "inventory.stock-corrections.v1"
	IncreasePermission = "inventory.stock-adjustments:increase"
	DecreasePermission = "inventory.stock-adjustments:decrease"
	ReversePermission  = "inventory.stock-corrections:reverse"
)

var (
	ErrOriginalNotFound = errors.New("original stock operation not found")
	ErrNotReversible    = errors.New("stock operation not reversible")
	ErrAlreadyReversed  = errors.New("stock operation already reversed")
)

type AdjustmentAudit struct {
	ReasonCode, Explanation string
	OriginalOperationID     uuid.UUID
}

// RecordedLine is server-loaded immutable history, including historical pack semantics.
type RecordedLine struct {
	LineNumber                                  int16
	VariantID, ProductID, BaseUnitID            uuid.UUID
	InputCount, Factor, Pieces                  Quantity
	PackID                                      uuid.UUID
	PackRevision                                string
	ProductCode, ProductName, SKU, BaseUnitCode string
	PackLabel                                   *string
}
type OriginalOperation struct {
	ID, WarehouseID uuid.UUID
	Producer        string
	Direction       Direction
	Lines           []RecordedLine
}

func ValidateAudit(a *AdjustmentAudit) error {
	if a == nil || !boundedText(a.Explanation, 500) || strings.TrimSpace(a.Explanation) != a.Explanation {
		return ErrInvalidArgument
	}
	for _, r := range a.Explanation {
		if unicode.IsControl(r) {
			return ErrInvalidArgument
		}
	}
	switch a.ReasonCode {
	case "DAMAGE", "LOSS", "FOUND", "DATA_CORRECTION", "OTHER":
		return nil
	}
	return ErrInvalidArgument
}
func (o OriginalOperation) ReverseDirection() Direction {
	if o.Direction == In {
		return Out
	}
	return In
}
func (o OriginalOperation) Line(variant uuid.UUID) (RecordedLine, bool) {
	for _, l := range o.Lines {
		if l.VariantID == variant {
			return l, true
		}
	}
	return RecordedLine{}, false
}
func (c Command) validateAdjustment() error {
	newProducer := c.Producer == AdjustmentProducer || c.Producer == CorrectionProducer
	if !newProducer {
		if c.CommandVersion != 1 || c.Audit != nil || c.Original != nil {
			return ErrInvalidArgument
		}
		return nil
	}
	if c.CommandVersion != 2 || c.SourceEventID != c.OperationID || ValidateAudit(c.Audit) != nil {
		return ErrInvalidArgument
	}
	if c.Producer == AdjustmentProducer {
		if c.Audit.OriginalOperationID != uuid.Nil || c.Original != nil {
			return ErrInvalidArgument
		}
		return nil
	}
	o := c.Original
	if o == nil || o.ID == uuid.Nil || o.ID == c.OperationID || o.ID != c.Audit.OriginalOperationID || o.WarehouseID != c.WarehouseID || (o.Producer != OpeningProducer && o.Producer != AdjustmentProducer) || (o.Direction != In && o.Direction != Out) || o.ReverseDirection() != c.Direction || len(o.Lines) != len(c.Lines) {
		return ErrNotReversible
	}
	seen := map[uuid.UUID]bool{}
	for _, l := range c.Lines {
		r, ok := o.Line(l.VariantID)
		if !ok || seen[r.VariantID] || r.LineNumber != l.LineNumber || r.ProductID == uuid.Nil || r.BaseUnitID == uuid.Nil || r.InputCount != l.InputCount || r.PackID != l.PackID || r.PackRevision != l.PackRevision {
			return ErrNotReversible
		}
		seen[r.VariantID] = true
		pieces, err := PackToPieces(r.InputCount, r.Factor)
		if err != nil || pieces != r.Pieces || (r.PackID == uuid.Nil && (r.Factor != 1 || r.PackRevision != "")) {
			return ErrNotReversible
		}
	}
	return nil
}

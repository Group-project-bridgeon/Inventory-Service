package stock

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

const ResultVersion int16 = 1

// PostedResult describes a confirmed posting. Merely constructing this
// value is not evidence of a commit. These are internal types, not HTTP DTOs.
type PostedResult struct {
	OperationID   uuid.UUID
	ResultVersion int16
	PostedAt      time.Time
	Replayed      bool
	Lines         []PostedLineResult
}

type PostedLineResult struct {
	LineNumber     int16
	VariantID      uuid.UUID
	MovementID     uuid.UUID
	AfterPieces    Quantity
	BalanceVersion int64
}

// ValidateFor checks structural correspondence, not ledger truth or commit state.
func (r PostedResult) ValidateFor(command Command) error {
	if err := command.Validate(); err != nil {
		return err
	}
	if r.OperationID != command.OperationID || r.ResultVersion != ResultVersion || r.PostedAt.IsZero() || len(r.Lines) != len(command.Lines) {
		return fmt.Errorf("result contract: %w", ErrInvalidArgument)
	}
	expected := make(map[int16]uuid.UUID, len(command.Lines))
	for _, line := range command.Lines {
		expected[line.LineNumber] = line.VariantID
	}
	movements := make(map[uuid.UUID]bool, len(r.Lines))
	for _, line := range r.Lines {
		variant, exists := expected[line.LineNumber]
		if !exists || variant != line.VariantID || line.MovementID == uuid.Nil || movements[line.MovementID] || line.AfterPieces < 0 || line.BalanceVersion <= 0 {
			return fmt.Errorf("result line: %w", ErrInvalidArgument)
		}
		delete(expected, line.LineNumber)
		movements[line.MovementID] = true
	}
	return nil
}

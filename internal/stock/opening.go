package stock

import (
	"errors"

	"github.com/google/uuid"
)

const OpeningProducer = "inventory.opening-stock.v1"
const OpeningPermission = "inventory.opening-stock:post"

var ErrOpeningNotEligible = errors.New("stock pair has prior activity")

// Selection is preparation intent, not a posting command or trusted observation.
type Selection struct {
	LineNumber   int16
	VariantID    uuid.UUID
	InputCount   Quantity
	PackID       uuid.UUID
	PackRevision string
}

func ValidateSelections(lines []Selection) error {
	if len(lines) < 1 || len(lines) > MaxLines {
		return ErrInvalidArgument
	}
	variants := map[uuid.UUID]bool{}
	numbers := map[int16]bool{}
	for _, l := range lines {
		if l.LineNumber < 1 || int(l.LineNumber) > len(lines) || numbers[l.LineNumber] || l.VariantID == uuid.Nil || variants[l.VariantID] || l.InputCount <= 0 {
			return ErrInvalidArgument
		}
		if l.PackID == uuid.Nil {
			if l.PackRevision != "" {
				return ErrInvalidArgument
			}
		} else {
			if _, err := ParsePositiveQuantity(l.PackRevision); err != nil {
				return err
			}
		}
		variants[l.VariantID] = true
		numbers[l.LineNumber] = true
	}
	return nil
}

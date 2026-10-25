package stock

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	CommandVersion    int16 = 1
	MaxLines                = 100
	maxProducerLength       = 64
	maxRevisionLength       = 128
)

type Direction string

const (
	In  Direction = "IN"
	Out Direction = "OUT"
)

// Principal must originate from a future verified workflow boundary. A nonzero
// UUID is structural validation, not authentication or permission enforcement.
type Principal struct{ UserID uuid.UUID }

type Command struct {
	OperationID    uuid.UUID
	SourceEventID  uuid.UUID
	Producer       string
	WarehouseID    uuid.UUID
	Direction      Direction
	CommandVersion int16
	IdempotencyKey uuid.UUID
	Lines          []CommandLine
}

type CommandLine struct {
	LineNumber                     int16
	VariantID                      uuid.UUID
	InputCount                     Quantity
	PackID                         uuid.UUID // Nil means direct pieces.
	PackRevision                   string
	ExpectedStockReferenceRevision string
}

func boundedText(text string, max int) bool {
	return utf8.ValidString(text) && strings.TrimSpace(text) != "" && utf8.RuneCountInString(text) <= max
}

func (line CommandLine) validate() error {
	if line.LineNumber < 1 || line.LineNumber > MaxLines || line.VariantID == uuid.Nil || line.InputCount <= 0 {
		return fmt.Errorf("line identity or quantity: %w", ErrInvalidArgument)
	}
	if !boundedText(line.ExpectedStockReferenceRevision, maxRevisionLength) {
		return fmt.Errorf("stock reference revision: %w", ErrInvalidArgument)
	}
	if line.PackID == uuid.Nil {
		if line.PackRevision != "" {
			return fmt.Errorf("piece line pack revision: %w", ErrInvalidArgument)
		}
	} else if !boundedText(line.PackRevision, maxRevisionLength) {
		return fmt.Errorf("pack revision: %w", ErrInvalidArgument)
	}
	return nil
}

func (c Command) Validate() error {
	if c.OperationID == uuid.Nil || c.SourceEventID == uuid.Nil || c.WarehouseID == uuid.Nil {
		return fmt.Errorf("operation references: %w", ErrInvalidArgument)
	}
	if c.IdempotencyKey == uuid.Nil || (c.IdempotencyKey.Version() != 4 && c.IdempotencyKey.Version() != 7) || c.IdempotencyKey.Variant() != uuid.RFC4122 {
		return fmt.Errorf("idempotency key: %w", ErrInvalidArgument)
	}
	if !boundedText(c.Producer, maxProducerLength) || c.CommandVersion != CommandVersion || (c.Direction != In && c.Direction != Out) {
		return fmt.Errorf("command contract: %w", ErrInvalidArgument)
	}
	if len(c.Lines) < 1 || len(c.Lines) > MaxLines {
		return fmt.Errorf("line count: %w", ErrInvalidArgument)
	}
	variants := make(map[uuid.UUID]bool, len(c.Lines))
	lineNumbers := make(map[int16]bool, len(c.Lines))
	for _, line := range c.Lines {
		if err := line.validate(); err != nil {
			return err
		}
		if int(line.LineNumber) > len(c.Lines) || variants[line.VariantID] || lineNumbers[line.LineNumber] {
			return fmt.Errorf("duplicate variant or invalid line sequence: %w", ErrInvalidArgument)
		}
		variants[line.VariantID], lineNumbers[line.LineNumber] = true, true
	}
	return nil
}

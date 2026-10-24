package stock

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func testCommand() Command {
	return Command{
		OperationID:   uuid.MustParse("01935f00-0000-7000-8000-000000000005"),
		SourceEventID: uuid.MustParse("01935f00-0000-7000-8000-000000000008"),
		Producer:      "synthetic-test", WarehouseID: uuid.MustParse("01935f00-0000-7000-8000-000000000001"),
		Direction: In, CommandVersion: CommandVersion,
		IdempotencyKey: uuid.MustParse("01935f00-0000-7000-8000-000000000009"),
		Lines: []CommandLine{{LineNumber: 1, VariantID: uuid.MustParse("01935f00-0000-7000-8000-000000000002"), InputCount: 2,
			PackID: uuid.MustParse("01935f00-0000-7000-8000-000000000004"), PackRevision: "pack-1", ExpectedStockReferenceRevision: "reference-1"}},
	}
}

func testPrincipal() Principal {
	return Principal{UserID: uuid.MustParse("01935f00-0000-7000-8000-000000000007")}
}

func TestCommandValidation(t *testing.T) {
	for name, mutate := range map[string]func(*Command){
		"operation":             func(c *Command) { c.OperationID = uuid.Nil },
		"source":                func(c *Command) { c.SourceEventID = uuid.Nil },
		"warehouse":             func(c *Command) { c.WarehouseID = uuid.Nil },
		"key":                   func(c *Command) { c.IdempotencyKey = uuid.Nil },
		"key version":           func(c *Command) { c.IdempotencyKey = uuid.MustParse("01935f00-0000-1000-8000-000000000009") },
		"key variant":           func(c *Command) { c.IdempotencyKey[8] = 0 },
		"empty producer":        func(c *Command) { c.Producer = "" },
		"blank producer":        func(c *Command) { c.Producer = " " },
		"long producer":         func(c *Command) { c.Producer = strings.Repeat("p", 65) },
		"invalid UTF8":          func(c *Command) { c.Producer = string([]byte{255}) },
		"version":               func(c *Command) { c.CommandVersion = 2 },
		"zero version":          func(c *Command) { c.CommandVersion = 0 },
		"direction":             func(c *Command) { c.Direction = "in" },
		"empty lines":           func(c *Command) { c.Lines = nil },
		"too many lines":        func(c *Command) { c.Lines = make([]CommandLine, MaxLines+1) },
		"variant":               func(c *Command) { c.Lines[0].VariantID = uuid.Nil },
		"zero count":            func(c *Command) { c.Lines[0].InputCount = 0 },
		"negative count":        func(c *Command) { c.Lines[0].InputCount = math.MinInt64 },
		"line zero":             func(c *Command) { c.Lines[0].LineNumber = 0 },
		"line gap":              func(c *Command) { c.Lines[0].LineNumber = 2 },
		"reference revision":    func(c *Command) { c.Lines[0].ExpectedStockReferenceRevision = "" },
		"long reference":        func(c *Command) { c.Lines[0].ExpectedStockReferenceRevision = strings.Repeat("r", 129) },
		"pack revision":         func(c *Command) { c.Lines[0].PackRevision = "" },
		"long pack revision":    func(c *Command) { c.Lines[0].PackRevision = strings.Repeat("r", 129) },
		"revision without pack": func(c *Command) { c.Lines[0].PackID = uuid.Nil },
		"duplicate variant":     func(c *Command) { c.Lines = append(c.Lines, c.Lines[0]); c.Lines[1].LineNumber = 2 },
		"duplicate line":        func(c *Command) { c.Lines = append(c.Lines, c.Lines[0]); c.Lines[1].VariantID = uuid.New() },
	} {
		t.Run(name, func(t *testing.T) {
			c := testCommand()
			mutate(&c)
			if err := c.Validate(); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("invalid command accepted: %v", err)
			}
			if _, err := c.Fingerprint(testPrincipal()); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("invalid command hashed: %v", err)
			}
		})
	}
	for _, direction := range []Direction{In, Out} {
		c := testCommand()
		c.Direction = direction
		c.IdempotencyKey = uuid.New() // v4 as well as v7 is permitted.
		c.Producer = strings.Repeat("p", 64)
		c.Lines[0].PackID, c.Lines[0].PackRevision = uuid.Nil, ""
		c.Lines[0].InputCount = math.MaxInt64
		c.Lines[0].ExpectedStockReferenceRevision = strings.Repeat("r", 128)
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	c := testCommand()
	c.Lines = make([]CommandLine, MaxLines)
	for i := range c.Lines {
		c.Lines[i] = CommandLine{LineNumber: int16(MaxLines - i), VariantID: uuid.New(), InputCount: 1, ExpectedStockReferenceRevision: "r"}
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid 100 unordered lines: %v", err)
	}
}

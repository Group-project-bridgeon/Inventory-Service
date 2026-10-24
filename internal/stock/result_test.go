package stock

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testResult() PostedResult {
	c := testCommand()
	return PostedResult{OperationID: c.OperationID, ResultVersion: ResultVersion, PostedAt: testReference().CheckedAt,
		Lines: []PostedLineResult{{LineNumber: 1, VariantID: c.Lines[0].VariantID,
			MovementID: uuid.MustParse("01935f00-0000-7000-8000-000000000006"), AfterPieces: 12, BalanceVersion: 1}}}
}

func TestResultCorrespondence(t *testing.T) {
	for _, q := range []Quantity{0, math.MaxInt64} {
		r := testResult()
		r.Lines[0].AfterPieces, r.Lines[0].BalanceVersion = q, math.MaxInt64
		r.Replayed = true
		if err := r.ValidateFor(testCommand()); err != nil {
			t.Fatal(err)
		}
	}
	for name, edit := range map[string]func(*PostedResult){
		"operation":                func(r *PostedResult) { r.OperationID = uuid.New() },
		"version":                  func(r *PostedResult) { r.ResultVersion = 2 },
		"time":                     func(r *PostedResult) { r.PostedAt = time.Time{} },
		"line count":               func(r *PostedResult) { r.Lines = nil },
		"line number":              func(r *PostedResult) { r.Lines[0].LineNumber = 2 },
		"variant":                  func(r *PostedResult) { r.Lines[0].VariantID = uuid.New() },
		"movement":                 func(r *PostedResult) { r.Lines[0].MovementID = uuid.Nil },
		"negative balance":         func(r *PostedResult) { r.Lines[0].AfterPieces = math.MinInt64 },
		"zero balance version":     func(r *PostedResult) { r.Lines[0].BalanceVersion = 0 },
		"negative balance version": func(r *PostedResult) { r.Lines[0].BalanceVersion = math.MinInt64 },
	} {
		t.Run(name, func(t *testing.T) {
			r := testResult()
			edit(&r)
			if err := r.ValidateFor(testCommand()); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("invalid result: %v", err)
			}
		})
	}
	c := testCommand()
	c.Lines = append(c.Lines, c.Lines[0])
	c.Lines[1].LineNumber, c.Lines[1].VariantID = 2, uuid.New()
	r := testResult()
	r.Lines = append(r.Lines, r.Lines[0])
	r.Lines[1].LineNumber, r.Lines[1].VariantID = 2, c.Lines[1].VariantID
	if err := r.ValidateFor(c); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("duplicate movement accepted")
	}
	r.Lines[1].MovementID = uuid.New()
	r.Lines[1].LineNumber = 1
	if err := r.ValidateFor(c); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("duplicate result line accepted")
	}
	r.Lines[1].LineNumber = 2
	if err := r.ValidateFor(c); err != nil {
		t.Fatal(err)
	}
	c.OperationID = uuid.Nil
	if err := r.ValidateFor(c); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("result accepted invalid command")
	}
}

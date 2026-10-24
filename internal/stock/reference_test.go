package stock

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These fixtures are explicitly synthetic; no Catalog implementation is implied.
func testReference() ReferenceSnapshot {
	line := testCommand().Lines[0]
	return ReferenceSnapshot{
		ProductID: uuid.MustParse("01935f00-0000-7000-8000-000000000010"), VariantID: line.VariantID,
		BaseUnitID: uuid.MustParse("01935f00-0000-7000-8000-000000000003"), QuantityKind: "PIECE",
		ProductActive: true, VariantActive: true, BaseUnitActive: true,
		StockReferenceRevision: line.ExpectedStockReferenceRevision, CheckedAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
		Pack: &PackSnapshot{ID: line.PackID, VariantID: line.VariantID, Revision: line.PackRevision, PiecesPerPack: 6, Active: true},
	}
}

func TestReferenceValidation(t *testing.T) {
	if err := testReference().ValidateFor(testCommand().Lines[0]); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*ReferenceSnapshot)
		err  error
	}{
		{"product ID", func(r *ReferenceSnapshot) { r.ProductID = uuid.Nil }, ErrInvalidArgument},
		{"variant ID", func(r *ReferenceSnapshot) { r.VariantID = uuid.Nil }, ErrInvalidArgument},
		{"unit ID", func(r *ReferenceSnapshot) { r.BaseUnitID = uuid.Nil }, ErrInvalidArgument},
		{"quantity kind", func(r *ReferenceSnapshot) { r.QuantityKind = "BOX" }, ErrInvalidArgument},
		{"check time", func(r *ReferenceSnapshot) { r.CheckedAt = time.Time{} }, ErrInvalidArgument},
		{"revision absent", func(r *ReferenceSnapshot) { r.StockReferenceRevision = "" }, ErrInvalidArgument},
		{"wrong variant", func(r *ReferenceSnapshot) { r.VariantID = uuid.New() }, ErrReferenceChanged},
		{"changed revision", func(r *ReferenceSnapshot) { r.StockReferenceRevision = "new" }, ErrReferenceChanged},
		{"product inactive", func(r *ReferenceSnapshot) { r.ProductActive = false }, ErrReferenceInactive},
		{"variant inactive", func(r *ReferenceSnapshot) { r.VariantActive = false }, ErrReferenceInactive},
		{"unit inactive", func(r *ReferenceSnapshot) { r.BaseUnitActive = false }, ErrReferenceInactive},
		{"missing pack", func(r *ReferenceSnapshot) { r.Pack = nil }, ErrReferenceChanged},
		{"pack ID", func(r *ReferenceSnapshot) { r.Pack.ID = uuid.New() }, ErrReferenceChanged},
		{"pack variant", func(r *ReferenceSnapshot) { r.Pack.VariantID = uuid.New() }, ErrReferenceChanged},
		{"pack revision", func(r *ReferenceSnapshot) { r.Pack.Revision = "new" }, ErrReferenceChanged},
		{"pack inactive", func(r *ReferenceSnapshot) { r.Pack.Active = false }, ErrReferenceInactive},
		{"factor zero", func(r *ReferenceSnapshot) { r.Pack.PiecesPerPack = 0 }, ErrInvalidArgument},
		{"factor negative", func(r *ReferenceSnapshot) { r.Pack.PiecesPerPack = math.MinInt64 }, ErrInvalidArgument},
		{"conversion overflow", func(r *ReferenceSnapshot) { r.Pack.PiecesPerPack = math.MaxInt64 }, ErrQuantityOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testReference()
			tc.edit(&r)
			if err := r.ValidateFor(testCommand().Lines[0]); !errors.Is(err, tc.err) {
				t.Fatalf("got %v want %v", err, tc.err)
			}
		})
	}
	line := testCommand().Lines[0]
	line.PackID, line.PackRevision = uuid.Nil, ""
	r := testReference()
	if err := r.ValidateFor(line); !errors.Is(err, ErrReferenceChanged) {
		t.Fatal("accepted unsolicited pack for piece line")
	}
	r.Pack = nil
	if err := r.ValidateFor(line); err != nil {
		t.Fatal(err)
	}
	line.InputCount = 0
	if err := r.ValidateFor(line); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("reference accepted invalid command line")
	}
}

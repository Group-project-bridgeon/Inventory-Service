package stock

import (
	"time"

	"github.com/google/uuid"
)

// ReferenceSnapshot is observation evidence, separate from caller intent. Only a
// future authenticated Catalog adapter can establish its provenance/coherence.
// Validation here does not check observation age, call Catalog or acquire locks.
type ReferenceSnapshot struct {
	ProductID, VariantID, BaseUnitID uuid.UUID
	QuantityKind                     string
	ProductActive                    bool
	VariantActive                    bool
	BaseUnitActive                   bool
	StockReferenceRevision           string
	CheckedAt                        time.Time
	Pack                             *PackSnapshot
}

type PackSnapshot struct {
	ID, VariantID uuid.UUID
	Revision      string
	PiecesPerPack Quantity
	Active        bool
}

// ValidateFor checks one synthetic/resolved observation against requested intent.
// It cannot establish global unit immutability or current warehouse status.
func (r ReferenceSnapshot) ValidateFor(line CommandLine) error {
	if err := line.validate(); err != nil {
		return err
	}
	if r.ProductID == uuid.Nil || r.VariantID == uuid.Nil || r.BaseUnitID == uuid.Nil || r.QuantityKind != "PIECE" || r.CheckedAt.IsZero() || !boundedText(r.StockReferenceRevision, maxRevisionLength) {
		return ErrInvalidArgument
	}
	if r.VariantID != line.VariantID || r.StockReferenceRevision != line.ExpectedStockReferenceRevision {
		return ErrReferenceChanged
	}
	if !r.ProductActive || !r.VariantActive || !r.BaseUnitActive {
		return ErrReferenceInactive
	}
	if line.PackID == uuid.Nil {
		if r.Pack != nil {
			return ErrReferenceChanged
		}
		return nil // Direct pieces imply factor 1; no inferred Catalog conversion.
	}
	if r.Pack == nil || r.Pack.ID != line.PackID || r.Pack.VariantID != line.VariantID || r.Pack.Revision != line.PackRevision {
		return ErrReferenceChanged
	}
	if !r.Pack.Active {
		return ErrReferenceInactive
	}
	_, err := PackToPieces(line.InputCount, r.Pack.PiecesPerPack)
	return err
}

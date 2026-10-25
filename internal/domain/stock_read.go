package domain

import (
	"time"

	"github.com/google/uuid"
)

type StockBalanceView struct {
	WarehouseID  uuid.UUID
	VariantID    uuid.UUID
	BaseUnitID   uuid.UUID
	OnHandPieces int64
	Version      int64
	UpdatedAt    time.Time
}

// StockMovementView contains durable posting facts, never live Catalog metadata.
type StockMovementView struct {
	ID, OperationID, WarehouseID, VariantID                                       uuid.UUID
	Direction                                                                     string
	InputCount, PiecesPerInput, Pieces, BeforePieces, AfterPieces, BalanceVersion int64
	PostedAt                                                                      time.Time
	ProductID, BaseUnitID                                                         uuid.UUID
	PackID                                                                        *uuid.UUID
	PackRevision, PackLabel                                                       *string
	ProductCode, ProductName, SKU, BaseUnitCode                                   string
	StockReferenceRevision                                                        string
	ReferenceCheckedAt                                                            time.Time
}

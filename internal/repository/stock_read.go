package repository

import (
	"context"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/google/uuid"
)

type ListStockBalancesParams struct {
	WarehouseID    uuid.UUID
	VariantID      *uuid.UUID
	AfterVariantID uuid.UUID
	NonzeroOnly    bool
	Limit          int32
}

type StockBalanceReader interface {
	ListBalances(
		ctx context.Context,
		params ListStockBalancesParams,
	) ([]domain.StockBalanceView, error)
}

type ListStockMovementsParams struct {
	WarehouseID uuid.UUID
	VariantID   *uuid.UUID
	BeforeID    *uuid.UUID
	Limit       int32
}

type StockMovementReader interface {
	ListMovements(context.Context, ListStockMovementsParams) ([]domain.StockMovementView, error)
}

type StockReader interface {
	StockBalanceReader
	StockMovementReader
}

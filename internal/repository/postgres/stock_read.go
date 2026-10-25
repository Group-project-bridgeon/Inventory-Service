package postgres

import (
	"context"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type StockReadRepository struct {
	pool *pgxpool.Pool
}

func NewStockReadRepository(pool *pgxpool.Pool) *StockReadRepository {
	return &StockReadRepository{pool: pool}
}

func (r *StockReadRepository) ListBalances(
	ctx context.Context,
	p repository.ListStockBalancesParams,
) ([]domain.StockBalanceView, error) {
	if p.WarehouseID == uuid.Nil ||
		p.Limit < 1 ||
		p.Limit > 101 ||
		(p.VariantID != nil && *p.VariantID == uuid.Nil) {
		return nil, domain.ErrInvalidArgument
	}

	var variantFilter pgtype.UUID
	if p.VariantID != nil {
		variantFilter = dbID(*p.VariantID)
	}

	rows, err := sqlcdb.New(r.pool).ListStockBalances(
		ctx,
		sqlcdb.ListStockBalancesParams{
			AfterWarehouseID: dbID(p.WarehouseID),
			AfterVariantID:   dbID(p.AfterVariantID),
			WarehouseFilter:  dbID(p.WarehouseID),
			VariantFilter:    variantFilter,
			NonzeroOnly:      p.NonzeroOnly,
			PageSize:         p.Limit,
		},
	)
	if err != nil {
		return nil, logger.Classify("postgres", "list_stock_balances", err)
	}

	balances := make([]domain.StockBalanceView, 0, len(rows))

	for _, row := range rows {
		balances = append(balances, domain.StockBalanceView{
			WarehouseID:  uuid.UUID(row.WarehouseID.Bytes),
			VariantID:    uuid.UUID(row.VariantID.Bytes),
			BaseUnitID:   uuid.UUID(row.BaseUnitID.Bytes),
			OnHandPieces: row.OnHandPieces,
			Version:      row.Version,
			UpdatedAt:    row.UpdatedAt.Time,
		})
	}

	return balances, nil
}

var _ repository.StockBalanceReader = (*StockReadRepository)(nil)

func (r *StockReadRepository) ListMovements(ctx context.Context, p repository.ListStockMovementsParams) ([]domain.StockMovementView, error) {
	if p.WarehouseID == uuid.Nil || p.Limit < 1 || p.Limit > 101 || (p.VariantID != nil && *p.VariantID == uuid.Nil) || (p.BeforeID != nil && *p.BeforeID == uuid.Nil) {
		return nil, domain.ErrInvalidArgument
	}
	var variant, before pgtype.UUID
	if p.VariantID != nil {
		variant = dbID(*p.VariantID)
	}
	if p.BeforeID != nil {
		before = dbID(*p.BeforeID)
	}
	rows, err := sqlcdb.New(r.pool).ListWarehouseStockMovements(ctx, sqlcdb.ListWarehouseStockMovementsParams{WarehouseID: dbID(p.WarehouseID), VariantFilter: variant, BeforeID: before, PageSize: p.Limit})
	if err != nil {
		return nil, logger.Classify("postgres", "list_stock_movements", err)
	}
	out := make([]domain.StockMovementView, 0, len(rows))
	for _, row := range rows {
		m := domain.StockMovementView{ID: uuid.UUID(row.ID.Bytes), OperationID: uuid.UUID(row.OperationID.Bytes), WarehouseID: uuid.UUID(row.WarehouseID.Bytes), VariantID: uuid.UUID(row.VariantID.Bytes), Direction: row.Direction, InputCount: row.InputCount, PiecesPerInput: row.PiecesPerInput, Pieces: row.Pieces, BeforePieces: row.BeforePieces, AfterPieces: row.AfterPieces, BalanceVersion: row.BalanceVersion, PostedAt: row.PostedAt.Time, ProductID: uuid.UUID(row.ProductID.Bytes), BaseUnitID: uuid.UUID(row.BaseUnitID.Bytes), ProductCode: row.ProductCode, ProductName: row.ProductName, SKU: row.Sku, BaseUnitCode: row.BaseUnitCode, StockReferenceRevision: row.StockReferenceRevision, ReferenceCheckedAt: row.ReferenceCheckedAt.Time}
		if row.PackID.Valid {
			id := uuid.UUID(row.PackID.Bytes)
			m.PackID = &id
		}
		if row.PackRevision.Valid {
			revision := row.PackRevision.String
			m.PackRevision = &revision
		}
		if row.PackLabel.Valid {
			label := row.PackLabel.String
			m.PackLabel = &label
		}
		out = append(out, m)
	}
	return out, nil
}

var _ repository.StockReader = (*StockReadRepository)(nil)

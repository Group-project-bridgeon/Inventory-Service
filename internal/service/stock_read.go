package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/strictjson"
	"github.com/google/uuid"
)

type StockReadInput struct {
	WarehouseID uuid.UUID
	VariantID   *uuid.UUID
	NonzeroOnly bool
	PageInput
}
type StockBalancePage struct {
	Items      []domain.StockBalanceView
	NextCursor *string
}
type StockMovementPage struct {
	Items      []domain.StockMovementView
	NextCursor *string
}
type warehouseReader interface {
	GetByID(context.Context, uuid.UUID) (domain.Warehouse, error)
}
type StockReadService struct {
	reader     repository.StockReader
	warehouses warehouseReader
	timeout    time.Duration
}

// timeout is the configured DB statement budget, also bounding pool/warehouse reads.
func NewStockReadService(reader repository.StockReader, warehouses warehouseReader, timeout time.Duration) *StockReadService {
	return &StockReadService{reader, warehouses, timeout}
}

type stockReadCursor struct {
	Version   int        `json:"version"`
	Kind      string     `json:"kind"`
	Warehouse uuid.UUID  `json:"warehouse_id"`
	Variant   *uuid.UUID `json:"variant_id"`
	Nonzero   bool       `json:"nonzero_only"`
	Key       uuid.UUID  `json:"key"`
}

func validateStockRead(in StockReadInput, kind string) (StockReadInput, uuid.UUID, error) {
	if in.Limit == 0 {
		in.Limit = DefaultPageLimit
	}
	if in.WarehouseID == uuid.Nil || (in.VariantID != nil && *in.VariantID == uuid.Nil) || in.Limit < 1 || in.Limit > MaxPageLimit || len(in.Cursor) > 1024 || (kind == "movements" && in.NonzeroOnly) {
		return in, uuid.Nil, domain.ErrInvalidArgument
	}
	if in.Cursor == "" {
		return in, uuid.Nil, nil
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(in.Cursor)
	var c stockReadCursor
	if err != nil || strictjson.Decode(data, &c) != nil {
		return in, uuid.Nil, domain.ErrInvalidArgument
	}
	canonical, _ := json.Marshal(c)
	if !bytes.Equal(canonical, data) || base64.RawURLEncoding.EncodeToString(data) != in.Cursor || c.Version != 1 || c.Kind != kind || c.Warehouse != in.WarehouseID || c.Nonzero != in.NonzeroOnly || c.Key == uuid.Nil || (c.Variant == nil) != (in.VariantID == nil) || (c.Variant != nil && *c.Variant != *in.VariantID) {
		return in, uuid.Nil, domain.ErrInvalidArgument
	}
	return in, c.Key, nil
}
func stockReadNext(in StockReadInput, kind string, key uuid.UUID) *string {
	data, _ := json.Marshal(stockReadCursor{1, kind, in.WarehouseID, in.VariantID, in.NonzeroOnly, key})
	next := base64.RawURLEncoding.EncodeToString(data)
	return &next
}
func (s *StockReadService) ListBalances(ctx context.Context, input StockReadInput) (StockBalancePage, error) {
	in, after, err := validateStockRead(input, "balances")
	if err != nil {
		return StockBalancePage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return StockBalancePage{}, err
	}
	if _, err = s.warehouses.GetByID(ctx, in.WarehouseID); err != nil {
		return StockBalancePage{}, err
	}
	rows, err := s.reader.ListBalances(ctx, repository.ListStockBalancesParams{WarehouseID: in.WarehouseID, VariantID: in.VariantID, AfterVariantID: after, NonzeroOnly: in.NonzeroOnly, Limit: int32(in.Limit + 1)})
	if err != nil {
		return StockBalancePage{}, err
	}
	if rows == nil {
		rows = []domain.StockBalanceView{}
	}
	page := StockBalancePage{Items: rows}
	if len(rows) > in.Limit {
		page.Items = rows[:in.Limit]
		page.NextCursor = stockReadNext(in, "balances", page.Items[in.Limit-1].VariantID)
	}
	return page, nil
}
func (s *StockReadService) ListMovements(ctx context.Context, input StockReadInput) (StockMovementPage, error) {
	in, before, err := validateStockRead(input, "movements")
	if err != nil {
		return StockMovementPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return StockMovementPage{}, err
	}
	if _, err = s.warehouses.GetByID(ctx, in.WarehouseID); err != nil {
		return StockMovementPage{}, err
	}
	var key *uuid.UUID
	if before != uuid.Nil {
		key = &before
	}
	rows, err := s.reader.ListMovements(ctx, repository.ListStockMovementsParams{WarehouseID: in.WarehouseID, VariantID: in.VariantID, BeforeID: key, Limit: int32(in.Limit + 1)})
	if err != nil {
		return StockMovementPage{}, err
	}
	if rows == nil {
		rows = []domain.StockMovementView{}
	}
	page := StockMovementPage{Items: rows}
	if len(rows) > in.Limit {
		page.Items = rows[:in.Limit]
		page.NextCursor = stockReadNext(in, "movements", page.Items[in.Limit-1].ID)
	}
	return page, nil
}

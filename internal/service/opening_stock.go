package service

import (
	"context"
	"errors"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
)

type OpeningReferences interface {
	stock.ReferenceProvider
	Prepare(context.Context, []stock.Selection) ([]stock.Observation, error)
	Freshness() stock.FreshnessPolicy
}
type OpeningStockService struct {
	store      stock.PostingStore
	refs       OpeningReferences
	warehouses repository.WarehouseRepository
}

func NewOpeningStockService(store stock.PostingStore, refs OpeningReferences, warehouses repository.WarehouseRepository) (*OpeningStockService, error) {
	if store == nil || refs == nil || warehouses == nil || refs.Freshness() == nil {
		return nil, stock.ErrInvalidArgument
	}
	return &OpeningStockService{store, refs, warehouses}, nil
}

// Claims are supplied only by verified HTTP middleware, never decoded from input.
type openingAuthority struct{ claims *security.Claims }

func (a openingAuthority) Authorize(ctx context.Context, p stock.Principal, c stock.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cl := a.claims
	if cl == nil || cl.Validate() != nil || p.UserID != cl.UserID || !time.Now().Before(cl.ExpiresAt.Time) || time.Now().Before(cl.NotBefore.Time) || c.Producer != stock.OpeningProducer || c.Direction != stock.In || c.SourceEventID != c.OperationID {
		return stock.ErrForbidden
	}
	for _, v := range cl.Permissions {
		if v == stock.OpeningPermission {
			return nil
		}
	}
	return stock.ErrForbidden
}
func (s *OpeningStockService) Post(ctx context.Context, cl *security.Claims, warehouse, operation, key uuid.UUID, lines []stock.CommandLine, requestID string) (stock.PostedResult, error) {
	if cl == nil {
		return stock.PostedResult{}, stock.ErrForbidden
	}
	c := stock.Command{OperationID: operation, SourceEventID: operation, Producer: stock.OpeningProducer, WarehouseID: warehouse, Direction: stock.In, CommandVersion: 1, IdempotencyKey: key, Lines: lines, FirstRequestID: requestID}
	engine, err := stock.NewPostingService(s.store, openingAuthority{cl}, s.refs, s.refs.Freshness(), stock.DefaultPostingOptions())
	if err != nil {
		return stock.PostedResult{}, err
	}
	return engine.Post(ctx, stock.Principal{UserID: cl.UserID}, c)
}
func (s *OpeningStockService) Prepare(ctx context.Context, cl *security.Claims, warehouse uuid.UUID, lines []stock.Selection) ([]stock.PreparedLine, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if cl == nil {
		return nil, stock.ErrForbidden
	}
	if err := (openingAuthority{cl}).Authorize(ctx, stock.Principal{UserID: cl.UserID}, stock.Command{Producer: stock.OpeningProducer, Direction: stock.In}); err != nil {
		return nil, err
	}
	return prepareStockSelections(ctx, s.refs, s.warehouses, warehouse, lines)
}

func prepareStockSelections(ctx context.Context, refs OpeningReferences, warehouses repository.WarehouseRepository, warehouse uuid.UUID, lines []stock.Selection) ([]stock.PreparedLine, error) {
	if warehouse == uuid.Nil {
		return nil, stock.ErrInvalidArgument
	}
	if err := stock.ValidateSelections(lines); err != nil {
		return nil, err
	}
	w, err := warehouses.GetByID(ctx, warehouse)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, stock.ErrWarehouseNotFound
	}
	if err != nil {
		return nil, err
	}
	if !w.IsActive {
		return nil, stock.ErrWarehouseInactive
	}
	observations, err := refs.Prepare(ctx, lines)
	if err != nil {
		return nil, err
	}
	if len(observations) != len(lines) {
		return nil, stock.ErrReferenceMalformed
	}
	result := make([]stock.PreparedLine, 0, len(lines))
	for i, l := range lines {
		o := observations[i]
		line := stock.CommandLine{LineNumber: l.LineNumber, VariantID: l.VariantID, InputCount: l.InputCount, PackID: l.PackID, PackRevision: l.PackRevision, ExpectedStockReferenceRevision: o.StockReferenceRevision}
		if err := o.ValidateFor(line); err != nil {
			return nil, err
		}
		factor := stock.Quantity(1)
		if o.Pack != nil {
			factor = o.Pack.PiecesPerPack
		}
		pieces, err := stock.PackToPieces(l.InputCount, factor)
		if err != nil {
			return nil, err
		}
		result = append(result, stock.PreparedLine{CommandLine: line, Observation: o, Pieces: pieces, Factor: factor})
	}
	return result, nil
}

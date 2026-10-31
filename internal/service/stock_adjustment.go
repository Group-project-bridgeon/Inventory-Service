package service

import (
	"context"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
)

type OriginalStockReader interface {
	LoadOriginal(context.Context, uuid.UUID, uuid.UUID) (stock.OriginalOperation, error)
}
type StockAdjustmentService struct {
	store      stock.PostingStore
	originals  OriginalStockReader
	refs       OpeningReferences
	warehouses repository.WarehouseRepository
}

func NewStockAdjustmentService(store stock.PostingStore, originals OriginalStockReader, refs OpeningReferences, warehouses repository.WarehouseRepository) (*StockAdjustmentService, error) {
	if store == nil || originals == nil || refs == nil || refs.Freshness() == nil || warehouses == nil {
		return nil, stock.ErrInvalidArgument
	}
	return &StockAdjustmentService{store, originals, refs, warehouses}, nil
}

type adjustmentAuthority struct{ claims *security.Claims }

func (a adjustmentAuthority) check(ctx context.Context, p stock.Principal, permission string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cl := a.claims
	if cl == nil || cl.Validate() != nil || cl.UserID != p.UserID || !time.Now().Before(cl.ExpiresAt.Time) || time.Now().Before(cl.NotBefore.Time) {
		return stock.ErrForbidden
	}
	for _, v := range cl.Permissions {
		if v == permission {
			return nil
		}
	}
	return stock.ErrForbidden
}
func (a adjustmentAuthority) Authorize(ctx context.Context, p stock.Principal, c stock.Command) error {
	if c.Producer != stock.AdjustmentProducer && c.Producer != stock.CorrectionProducer {
		return stock.ErrForbidden
	}
	if c.SourceEventID != c.OperationID || (c.Direction != stock.In && c.Direction != stock.Out) {
		return stock.ErrForbidden
	}
	if c.Producer == stock.CorrectionProducer {
		if err := a.check(ctx, p, stock.ReversePermission); err != nil {
			return err
		}
	}
	permission := stock.IncreasePermission
	if c.Direction == stock.Out {
		permission = stock.DecreasePermission
	}
	return a.check(ctx, p, permission)
}
func adjustmentCommand(wh, op, key uuid.UUID, dir stock.Direction, a stock.AdjustmentAudit, lines []stock.CommandLine, requestID string) stock.Command {
	return stock.Command{WarehouseID: wh, OperationID: op, SourceEventID: op, IdempotencyKey: key, Producer: stock.AdjustmentProducer, CommandVersion: 2, Direction: dir, Audit: &a, Lines: lines, FirstRequestID: requestID}
}
func (s *StockAdjustmentService) post(ctx context.Context, cl *security.Claims, c stock.Command) (stock.PostedResult, error) {
	if cl == nil {
		return stock.PostedResult{}, stock.ErrForbidden
	}
	engine, err := stock.NewPostingService(s.store, adjustmentAuthority{cl}, adjustmentReferences{s.refs}, s.refs.Freshness(), stock.DefaultPostingOptions())
	if err != nil {
		return stock.PostedResult{}, err
	}
	return engine.Post(ctx, stock.Principal{UserID: cl.UserID}, c)
}
func (s *StockAdjustmentService) Post(ctx context.Context, cl *security.Claims, wh, op, key uuid.UUID, dir stock.Direction, a stock.AdjustmentAudit, lines []stock.CommandLine, requestID string) (stock.PostedResult, error) {
	return s.post(ctx, cl, adjustmentCommand(wh, op, key, dir, a, lines, requestID))
}
func (s *StockAdjustmentService) Prepare(ctx context.Context, cl *security.Claims, wh uuid.UUID, dir stock.Direction, a stock.AdjustmentAudit, lines []stock.Selection) ([]stock.PreparedLine, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if cl == nil {
		return nil, stock.ErrForbidden
	}
	if err := (adjustmentAuthority{cl}).Authorize(ctx, stock.Principal{UserID: cl.UserID}, stock.Command{Producer: stock.AdjustmentProducer, Direction: dir}); err != nil {
		return nil, err
	}
	if stock.ValidateAudit(&a) != nil || a.OriginalOperationID != uuid.Nil {
		return nil, stock.ErrInvalidArgument
	}
	return prepareStockSelections(ctx, s.refs, s.warehouses, wh, lines)
}

// ReferenceExpectations contains only reviewed current direct-piece revisions.
// Quantities, packs, source, producer and direction always come from original facts.
type ReferenceExpectations map[uuid.UUID]string

func (s *StockAdjustmentService) correction(ctx context.Context, cl *security.Claims, wh, op, key, target uuid.UUID, a stock.AdjustmentAudit, expected ReferenceExpectations, requestID string) (stock.Command, error) {
	if cl == nil {
		return stock.Command{}, stock.ErrForbidden
	}
	auth := adjustmentAuthority{cl}
	p := stock.Principal{UserID: cl.UserID}
	if err := auth.check(ctx, p, stock.ReversePermission); err != nil {
		return stock.Command{}, err
	}
	original, err := s.originals.LoadOriginal(ctx, wh, target)
	if err != nil {
		return stock.Command{}, err
	}
	a.OriginalOperationID = target
	c := adjustmentCommand(wh, op, key, original.ReverseDirection(), a, nil, requestID)
	c.Producer = stock.CorrectionProducer
	c.Original = &original
	if err := auth.Authorize(ctx, p, c); err != nil {
		return c, err
	}
	if expected != nil && len(expected) != len(original.Lines) {
		return c, stock.ErrInvalidArgument
	}
	for _, l := range original.Lines {
		revision := "preparation" // Preview only; replaced with actual coherent revisions before validation.
		if expected != nil {
			var ok bool
			revision, ok = expected[l.VariantID]
			if !ok {
				return c, stock.ErrInvalidArgument
			}
		}
		c.Lines = append(c.Lines, stock.CommandLine{LineNumber: l.LineNumber, VariantID: l.VariantID, InputCount: l.InputCount, PackID: l.PackID, PackRevision: l.PackRevision, ExpectedStockReferenceRevision: revision})
	}
	return c, c.Validate()
}
func (s *StockAdjustmentService) Reverse(ctx context.Context, cl *security.Claims, wh, op, key, target uuid.UUID, a stock.AdjustmentAudit, expected ReferenceExpectations, requestID string) (stock.PostedResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if expected == nil {
		return stock.PostedResult{}, stock.ErrInvalidArgument
	}
	c, err := s.correction(ctx, cl, wh, op, key, target, a, expected, requestID)
	if err != nil {
		return stock.PostedResult{}, err
	}
	return s.post(ctx, cl, c)
}
func (s *StockAdjustmentService) PrepareReverse(ctx context.Context, cl *security.Claims, wh, target uuid.UUID, a stock.AdjustmentAudit) (stock.Direction, []stock.PreparedLine, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := s.correction(ctx, cl, wh, uuid.New(), uuid.New(), target, a, nil, "")
	if err != nil {
		return "", nil, err
	}
	// Shared preparation checks active warehouse and current Catalog references.
	selected := directSelections(c)
	preview, err := prepareStockSelections(ctx, s.refs, s.warehouses, wh, selected)
	if err != nil {
		return "", nil, err
	}
	observations := make([]stock.Observation, len(preview))
	for i, l := range preview {
		observations[i] = l.Observation
		c.Lines[i].ExpectedStockReferenceRevision = l.StockReferenceRevision
	}
	lines, err := stock.Prepare(c, observations)
	if err != nil {
		return c.Direction, nil, err
	}
	for i := range lines {
		old, _ := c.Original.Line(lines[i].CommandLine.VariantID)
		lines[i].ProductCode, lines[i].ProductName, lines[i].SKU, lines[i].BaseUnitCode = old.ProductCode, old.ProductName, old.SKU, old.BaseUnitCode
		lines[i].PackLabel = old.PackLabel
	}
	return c.Direction, lines, nil
}
func directSelections(c stock.Command) []stock.Selection {
	selected := make([]stock.Selection, 0, len(c.Lines))
	for _, l := range c.Lines {
		selected = append(selected, stock.Selection{LineNumber: l.LineNumber, VariantID: l.VariantID, InputCount: 1})
	}
	return selected
}

type adjustmentReferences struct{ OpeningReferences }

func (r adjustmentReferences) Resolve(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
	if c.Producer != stock.CorrectionProducer {
		return r.OpeningReferences.Resolve(ctx, c)
	}
	return r.Prepare(ctx, directSelections(c))
}

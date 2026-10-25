package service

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/google/uuid"
)

type readStub struct {
	balances  []domain.StockBalanceView
	movements []domain.StockMovementView
	b         repository.ListStockBalancesParams
	m         repository.ListStockMovementsParams
	calls     int
	err       error
}

func (r *readStub) ListBalances(ctx context.Context, p repository.ListStockBalancesParams) ([]domain.StockBalanceView, error) {
	r.calls++
	r.b = p
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return r.balances, r.err
}
func (r *readStub) ListMovements(ctx context.Context, p repository.ListStockMovementsParams) ([]domain.StockMovementView, error) {
	r.calls++
	r.m = p
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return r.movements, r.err
}

type readWarehouse struct {
	calls int
	err   error
}

func (w *readWarehouse) GetByID(ctx context.Context, id uuid.UUID) (domain.Warehouse, error) {
	w.calls++
	return domain.Warehouse{ID: id, IsActive: false}, w.err
}

func TestStockReadValidationAndPaging(t *testing.T) {
	r := &readStub{}
	w := &readWarehouse{}
	s := NewStockReadService(r, w, time.Second)
	ctx := context.Background()
	id := uuid.New()
	variant := uuid.New()
	for _, in := range []StockReadInput{{}, {WarehouseID: id, VariantID: new(uuid.UUID)}, {WarehouseID: id, PageInput: PageInput{Limit: -1}}, {WarehouseID: id, PageInput: PageInput{Limit: 101}}, {WarehouseID: id, PageInput: PageInput{Cursor: strings.Repeat("x", 1025)}}, {WarehouseID: id, PageInput: PageInput{Cursor: "bad"}}} {
		if _, err := s.ListBalances(ctx, in); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatal("invalid balances accepted")
		}
		if _, err := s.ListMovements(ctx, in); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatal("invalid movements accepted")
		}
	}
	if r.calls != 0 || w.calls != 0 {
		t.Fatal("invalid input reached repository")
	}
	in := StockReadInput{WarehouseID: id, VariantID: &variant, NonzeroOnly: true, PageInput: PageInput{Limit: 2}}
	r.balances = []domain.StockBalanceView{{VariantID: uuid.New()}, {VariantID: uuid.New()}, {VariantID: uuid.New()}}
	page, err := s.ListBalances(ctx, in)
	if err != nil || len(page.Items) != 2 || page.NextCursor == nil || r.b.Limit != 3 {
		t.Fatal("lookahead/default contract")
	}
	in.Cursor = *page.NextCursor
	if _, err = s.ListBalances(ctx, in); err != nil || r.b.AfterVariantID != r.balances[1].VariantID {
		t.Fatal("cursor used lookahead row")
	}
	for _, change := range []func(*StockReadInput){func(i *StockReadInput) { i.WarehouseID = uuid.New() }, func(i *StockReadInput) { i.VariantID = nil }, func(i *StockReadInput) { i.NonzeroOnly = false }, func(i *StockReadInput) { v := uuid.New(); i.VariantID = &v }} {
		bad := in
		change(&bad)
		before := r.calls
		if _, err = s.ListBalances(ctx, bad); !errors.Is(err, domain.ErrInvalidArgument) || r.calls != before {
			t.Fatal("cursor filter not bound")
		}
	}
	data, _ := base64.RawURLEncoding.DecodeString(in.Cursor)
	for _, bad := range []string{strings.Replace(string(data), `"version":1`, `"version":2`, 1), strings.Replace(string(data), `"kind":"balances"`, `"kind":"movements"`, 1), string(data) + ` {}`, strings.Replace(string(data), `"version":1`, `"version":1,"version":1`, 1)} {
		badIn := in
		badIn.Cursor = base64.RawURLEncoding.EncodeToString([]byte(bad))
		if _, err = s.ListBalances(ctx, badIn); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatal("invalid cursor structure accepted")
		}
	}
	before := r.calls
	in.NonzeroOnly = false
	if _, err = s.ListMovements(ctx, in); !errors.Is(err, domain.ErrInvalidArgument) || r.calls != before {
		t.Fatal("cross-endpoint cursor accepted")
	}
	in.Cursor = ""
	in.Limit = 0
	r.balances = nil
	page, err = s.ListBalances(ctx, in)
	if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil || r.b.Limit != 51 {
		t.Fatal("empty/default/inactive warehouse read")
	}
	in.Limit = 100
	if _, err = s.ListBalances(ctx, in); err != nil || r.b.Limit != 101 {
		t.Fatal("max lookahead")
	}
	r.movements = []domain.StockMovementView{{ID: uuid.New()}, {ID: uuid.New()}, {ID: uuid.New()}}
	in.Limit = 2
	mp, err := s.ListMovements(ctx, in)
	if err != nil || mp.NextCursor == nil || len(mp.Items) != 2 || r.m.Limit != 3 {
		t.Fatal("movement lookahead")
	}
	in.Cursor = *mp.NextCursor
	if _, err = s.ListMovements(ctx, in); err != nil || r.m.BeforeID == nil || *r.m.BeforeID != r.movements[1].ID {
		t.Fatal("movement cursor")
	}
	changed := in
	changed.WarehouseID = uuid.New()
	if _, err = s.ListMovements(ctx, changed); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatal("movement warehouse binding")
	}
}
func TestStockReadMissingErrorsAndCancellation(t *testing.T) {
	ctx := context.Background()
	in := StockReadInput{WarehouseID: uuid.New()}
	r := &readStub{}
	w := &readWarehouse{err: domain.ErrNotFound}
	s := NewStockReadService(r, w, time.Second)
	for _, read := range []func(context.Context) error{func(c context.Context) error { _, e := s.ListBalances(c, in); return e }, func(c context.Context) error { _, e := s.ListMovements(c, in); return e }} {
		w.err = domain.ErrNotFound
		if err := read(ctx); !errors.Is(err, domain.ErrNotFound) || r.calls != 0 {
			t.Fatal("missing warehouse not distinguished")
		}
		w.err = nil
		r.err = errors.New("synthetic repository failure")
		if err := read(ctx); !errors.Is(err, r.err) {
			t.Fatal("repository failure lost")
		}
		r.calls = 0
		c, cancel := context.WithCancel(ctx)
		cancel()
		before := w.calls
		if err := read(c); !errors.Is(err, context.Canceled) || w.calls != before {
			t.Fatal("canceled request reached repository")
		}
	}
}

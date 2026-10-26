package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type openingWarehouse struct{ repository.WarehouseRepository }

func (openingWarehouse) GetByID(_ context.Context, id uuid.UUID) (domain.Warehouse, error) {
	return domain.Warehouse{ID: id, IsActive: true}, nil
}

type openingStore struct {
	calls   int
	command stock.Command
	actor   stock.Principal
}

func (s *openingStore) Lookup(_ context.Context, p stock.Principal, c stock.Command, _ stock.RequestFingerprint) (stock.PostedResult, bool, error) {
	s.calls++
	s.command = c
	s.actor = p
	return stock.PostedResult{Replayed: true}, true, nil
}
func (*openingStore) Apply(context.Context, stock.Principal, stock.Command, stock.RequestFingerprint, []stock.PreparedLine) (stock.PostedResult, error) {
	panic("replay attempted mutation")
}

type openingRefs struct{}

func (openingRefs) Resolve(context.Context, stock.Command) ([]stock.Observation, error) {
	panic("replay contacted Catalog")
}
func (openingRefs) Freshness() stock.FreshnessPolicy                 { return openingRefs{} }
func (openingRefs) Check(context.Context, []stock.Observation) error { return nil }
func (openingRefs) Prepare(_ context.Context, ls []stock.Selection) ([]stock.Observation, error) {
	out := []stock.Observation{}
	for _, l := range ls {
		o := stock.Observation{ReferenceSnapshot: stock.ReferenceSnapshot{ProductID: uuid.New(), VariantID: l.VariantID, BaseUnitID: uuid.New(), QuantityKind: "PIECE", ProductActive: true, VariantActive: true, BaseUnitActive: true, StockReferenceRevision: "synthetic-v1", CheckedAt: time.Now()}}
		if l.PackID != uuid.Nil {
			o.Pack = &stock.PackSnapshot{ID: l.PackID, VariantID: l.VariantID, Revision: l.PackRevision, PiecesPerPack: 6, Active: true}
		}
		out = append(out, o)
	}
	return out, nil
}
func TestOpeningServiceTrustedBindingAndPreparation(t *testing.T) {
	id := uuid.New()
	v := int32(0)
	cl := &security.Claims{UserID: id, TokenVersion: &v, Permissions: []string{stock.OpeningPermission}, RegisteredClaims: jwt.RegisteredClaims{Subject: id.String(), IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Second)), NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
	store := &openingStore{}
	s, e := NewOpeningStockService(store, openingRefs{}, openingWarehouse{})
	if e != nil {
		t.Fatal(e)
	}
	operation, key, wh, variant := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	line := stock.CommandLine{LineNumber: 1, VariantID: variant, InputCount: 100, ExpectedStockReferenceRevision: "synthetic-v1"}
	r, e := s.Post(context.Background(), cl, wh, operation, key, []stock.CommandLine{line}, "request-a")
	if e != nil || !r.Replayed {
		t.Fatal("replay failed")
	}
	c := store.command
	if c.Producer != stock.OpeningProducer || c.SourceEventID != operation || c.Direction != stock.In || c.WarehouseID != wh || store.actor.UserID != id || c.FirstRequestID != "request-a" {
		t.Fatal("untrusted command mapping")
	}
	cl.Permissions = []string{"inventory.stock-balances:read"}
	before := store.calls
	_, e = s.Post(context.Background(), cl, wh, operation, key, []stock.CommandLine{line}, "request-b")
	if !errors.Is(e, stock.ErrForbidden) || store.calls != before {
		t.Fatal("unauthorized replay lookup")
	}
	cl.Permissions = []string{stock.OpeningPermission}
	selections := []stock.Selection{{LineNumber: 1, VariantID: variant, InputCount: 10, PackID: uuid.New(), PackRevision: "1"}}
	prepared, e := s.Prepare(context.Background(), cl, wh, selections)
	if e != nil || prepared[0].Pieces != 60 || store.calls != before {
		t.Fatal("preparation conversion or write")
	}
	selections[0].InputCount = math.MaxInt64
	_, e = s.Prepare(context.Background(), cl, wh, selections)
	if !errors.Is(e, stock.ErrQuantityOverflow) {
		t.Fatal("overflow accepted")
	}
}

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type syntheticOriginal struct {
	value stock.OriginalOperation
	reads int
}

func (s *syntheticOriginal) LoadOriginal(context.Context, uuid.UUID, uuid.UUID) (stock.OriginalOperation, error) {
	s.reads++
	return s.value, nil
}
func adjustmentClaims(perms ...string) *security.Claims {
	id := uuid.New()
	v := int32(0)
	return &security.Claims{UserID: id, TokenVersion: &v, Permissions: perms, RegisteredClaims: jwt.RegisteredClaims{Subject: id.String(), IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Second)), NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
}
func TestAdjustmentAuthorityAndReplayBinding(t *testing.T) {
	ctx := context.Background()
	wh, op, key, variant := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &openingStore{}
	original := &syntheticOriginal{}
	s, err := NewStockAdjustmentService(store, original, openingRefs{}, openingWarehouse{})
	if err != nil {
		t.Fatal(err)
	}
	cl := adjustmentClaims(stock.IncreasePermission)
	audit := stock.AdjustmentAudit{ReasonCode: "FOUND", Explanation: "Counted extra physical pieces"}
	lines := []stock.CommandLine{{LineNumber: 1, VariantID: variant, InputCount: 5, ExpectedStockReferenceRevision: "synthetic-v1"}}
	r, err := s.Post(ctx, cl, wh, op, key, stock.In, audit, lines, "request-a")
	if err != nil || !r.Replayed {
		t.Fatal("authorized replay", err)
	}
	if store.command.Producer != stock.AdjustmentProducer || store.command.CommandVersion != 2 || store.actor.UserID != cl.UserID || store.command.SourceEventID != op {
		t.Fatal("trusted mapping")
	}
	before := store.calls
	_, err = s.Post(ctx, cl, wh, op, key, stock.Out, audit, lines, "")
	if !errors.Is(err, stock.ErrForbidden) || store.calls != before {
		t.Fatal("wrong direction authorized")
	}
	cl.Permissions = []string{stock.OpeningPermission, "inventory.stock-adjustments:increase-extra"}
	_, err = s.Post(ctx, cl, wh, op, key, stock.In, audit, lines, "")
	if !errors.Is(err, stock.ErrForbidden) || store.calls != before {
		t.Fatal("inferred authority")
	}
}
func TestCorrectionDerivedAuthorityAndCompletedReplay(t *testing.T) {
	ctx := context.Background()
	wh, variant, target := uuid.New(), uuid.New(), uuid.New()
	old := stock.OriginalOperation{ID: target, WarehouseID: wh, Producer: stock.OpeningProducer, Direction: stock.In, Lines: []stock.RecordedLine{{LineNumber: 1, VariantID: variant, ProductID: uuid.New(), BaseUnitID: uuid.New(), InputCount: 2, Factor: 6, Pieces: 12, PackID: uuid.New(), PackRevision: "obsolete-1"}}}
	reader := &syntheticOriginal{value: old}
	store := &openingStore{}
	s, err := NewStockAdjustmentService(store, reader, openingRefs{}, openingWarehouse{})
	if err != nil {
		t.Fatal(err)
	}
	cl := adjustmentClaims(stock.ReversePermission)
	a := stock.AdjustmentAudit{ReasonCode: "DATA_CORRECTION", Explanation: "Reverse mistaken receipt"}
	expected := ReferenceExpectations{variant: "current-direct"}
	_, err = s.Reverse(ctx, cl, wh, uuid.New(), uuid.New(), target, a, expected, "")
	if !errors.Is(err, stock.ErrForbidden) || store.calls != 0 {
		t.Fatal("missing derived decrease allowed")
	}
	cl.Permissions = []string{stock.ReversePermission, stock.DecreasePermission}
	r, err := s.Reverse(ctx, cl, wh, uuid.New(), uuid.New(), target, a, expected, "")
	if err != nil || !r.Replayed {
		t.Fatal("replay contacted Catalog", err)
	}
	if store.command.Direction != stock.Out || store.command.Lines[0].InputCount != 2 || store.command.Original.Lines[0].Factor != 6 {
		t.Fatal("not original conversion")
	}
	reader.value.Producer = stock.AdjustmentProducer
	reader.value.Direction = stock.Out
	_, err = s.Reverse(ctx, cl, wh, uuid.New(), uuid.New(), target, a, expected, "")
	if !errors.Is(err, stock.ErrForbidden) {
		t.Fatal("inverse IN authorized with decrease")
	}
	cl.Permissions = []string{stock.ReversePermission, stock.IncreasePermission}
	r, err = s.Reverse(ctx, cl, wh, uuid.New(), uuid.New(), target, a, expected, "")
	if err != nil || !r.Replayed || store.command.Direction != stock.In {
		t.Fatal("derived increase failed", err)
	}
	before := reader.reads
	cl.Permissions = []string{stock.DecreasePermission}
	_, err = s.Reverse(ctx, cl, wh, uuid.New(), uuid.New(), target, a, expected, "")
	if !errors.Is(err, stock.ErrForbidden) || reader.reads != before {
		t.Fatal("original disclosed before reverse authorization")
	}
}

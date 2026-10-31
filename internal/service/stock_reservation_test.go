package service

import (
	"context"
	"errors"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"testing"
)

type reservationReplayAccess struct {
	id     uuid.UUID
	denied bool
	checks int
}

func (a *reservationReplayAccess) Owner() uuid.UUID { return a.id }
func (a *reservationReplayAccess) AuthorizeReservation(context.Context, string) error {
	a.checks++
	if a.denied {
		return stock.ErrForbidden
	}
	return nil
}

type reservationReplayStore struct {
	stock.ReservationStore
	calls int
	owner uuid.UUID
}

func (s *reservationReplayStore) Lookup(_ context.Context, owner uuid.UUID, _ stock.ReservationCommand, _ stock.RequestFingerprint) (stock.ReservationResult, bool, error) {
	s.calls++
	s.owner = owner
	return stock.ReservationResult{Replayed: true}, true, nil
}
func TestReservationCompletedReplayPrecedesDependencies(t *testing.T) {
	store := &reservationReplayStore{}
	s, err := NewStockReservationService(store, &openingStore{}, openingRefs{})
	if err != nil {
		t.Fatal(err)
	}
	a := &reservationReplayAccess{id: uuid.New()}
	reserve := stock.ReservationCommand{CommandID: uuid.New(), Key: uuid.New(), ReservationID: uuid.New(), OrderID: uuid.New(), WarehouseID: uuid.New(), Generation: 1, Action: stock.Reserve, Lines: []stock.ReservationLine{{OrderLineID: uuid.New(), VariantID: uuid.New(), Pieces: 1}}}
	for _, action := range []stock.ReservationAction{stock.Reserve, stock.Release, stock.Consume} {
		c := reserve
		c.Action = action
		if action != stock.Reserve {
			c.Lines = nil
			c.ExpectedOrderVersion = 1
			c.ExpectedReservationVersion = 1
		}
		result, err := s.Mutate(context.Background(), a, c)
		if err != nil || !result.Replayed || store.owner != a.id {
			t.Fatal("replay touched reference/physical dependencies", err)
		}
	}
	before := store.calls
	a.denied = true
	_, err = s.Mutate(context.Background(), a, reserve)
	if !errors.Is(err, stock.ErrForbidden) || store.calls != before {
		t.Fatal("unauthorized replay reached store")
	}
}

type reservationCompletionRaceStore struct {
	stock.ReservationStore
	request stock.ReservationCommand
	lookups int
}

func (s *reservationCompletionRaceStore) Lookup(context.Context, uuid.UUID, stock.ReservationCommand, stock.RequestFingerprint) (stock.ReservationResult, bool, error) {
	s.lookups++
	if s.lookups == 1 {
		return stock.ReservationResult{}, false, nil
	}
	return stock.ReservationResult{Replayed: true, State: stock.ReservationConsumed}, true, nil
}
func (s *reservationCompletionRaceStore) Get(context.Context, uuid.UUID, uuid.UUID) (stock.ReservationSnapshot, error) {
	return stock.ReservationSnapshot{OrderID: s.request.OrderID, WarehouseID: s.request.WarehouseID, Generation: s.request.Generation, State: stock.ReservationConsumed}, nil
}
func TestReservationConsumeCompletionBetweenLookupAndStatus(t *testing.T) {
	command := stock.ReservationCommand{CommandID: uuid.New(), Key: uuid.New(), ReservationID: uuid.New(), OrderID: uuid.New(), WarehouseID: uuid.New(), Generation: 1, Action: stock.Consume, ExpectedOrderVersion: 1, ExpectedReservationVersion: 1}
	access := &reservationReplayAccess{id: uuid.New()}
	store := &reservationCompletionRaceStore{request: command}
	s, err := NewStockReservationService(store, &openingStore{}, openingRefs{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Mutate(context.Background(), access, command)
	if err != nil || !result.Replayed || result.State != stock.ReservationConsumed || store.lookups != 2 || access.checks < 2 {
		t.Fatal("concurrent completion was rejected instead of authorized replay", err)
	}
}

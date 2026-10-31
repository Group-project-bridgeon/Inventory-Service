package service

import (
	"context"
	"errors"
	f "github.com/Group-project-bridgeon/Inventory-Service/internal/fulfillment"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"testing"
)

type fulfillmentReplayAccess struct {
	id             uuid.UUID
	checks, denyAt int
}

func (a *fulfillmentReplayAccess) Actor() f.Actor { return f.Actor{Kind: "HUMAN", ID: a.id} }
func (a *fulfillmentReplayAccess) Authorize(context.Context, string) error {
	a.checks++
	if a.denyAt > 0 && a.checks >= a.denyAt {
		return stock.ErrForbidden
	}
	return nil
}

type fulfillmentReplayStore struct {
	f.Store
	reads, lookups int
}

func (s *fulfillmentReplayStore) Get(_ context.Context, _ f.Actor, id uuid.UUID) (f.Snapshot, error) {
	s.reads++
	return f.Snapshot{ID: id, WarehouseID: uuid.MustParse("01935f00-0000-7000-8000-000000000001"), OwnerMachineID: uuid.MustParse("01935f00-0000-7000-8000-000000000002"), ReservationID: uuid.New(), OrderID: uuid.New(), Generation: 1}, nil
}
func (s *fulfillmentReplayStore) Lookup(context.Context, f.Actor, f.Command, stock.RequestFingerprint) (f.Result, bool, error) {
	s.lookups++
	return f.Result{Replayed: true}, true, nil
}
func TestFulfillmentCurrentAuthorityBeforeCompletedReplay(t *testing.T) {
	for _, deny := range []int{0, 1, 2} {
		store := &fulfillmentReplayStore{}
		s, e := NewFulfillmentService(store, openingRefs{})
		if e != nil {
			t.Fatal(e)
		}
		a := &fulfillmentReplayAccess{id: uuid.New(), denyAt: deny}
		c := f.Command{CommandID: uuid.New(), Key: uuid.New(), ID: uuid.New(), WarehouseID: uuid.MustParse("01935f00-0000-7000-8000-000000000001"), Action: f.Dispatch, ExpectedVersion: 1, ExpectedReservationVersion: 1, ExpectedOrderVersion: 1}
		result, e := s.Mutate(context.Background(), a, c)
		if deny == 0 {
			if e != nil || !result.Replayed || store.lookups != 1 {
				t.Fatal("replay touched dependency or mutation", e)
			}
		} else if !errors.Is(e, stock.ErrForbidden) {
			t.Fatal("replay lacked current authority")
		}
		if deny == 1 && (store.reads != 0 || store.lookups != 0) {
			t.Fatal("unauthorized request read store")
		}
	}
}

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	f "github.com/Group-project-bridgeon/Inventory-Service/internal/fulfillment"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/strictjson"
	"github.com/google/uuid"
	"math/rand/v2"
	"time"
)

type FulfillmentService struct {
	store f.Store
	refs  OpeningReferences
}

func NewFulfillmentService(store f.Store, refs OpeningReferences) (*FulfillmentService, error) {
	if store == nil || refs == nil || refs.Freshness() == nil {
		return nil, stock.ErrInvalidArgument
	}
	return &FulfillmentService{store, refs}, nil
}
func authorizeFulfillment(ctx context.Context, a f.Access, permission string) error {
	if a == nil || a.Actor().Validate() != nil {
		return stock.ErrForbidden
	}
	if e := a.Authorize(ctx, permission); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return stock.ErrForbidden
	}
	return nil
}
func (s *FulfillmentService) replay(ctx context.Context, a f.Access, c f.Command, fp stock.RequestFingerprint) (f.Result, bool, error) {
	r, found, e := s.store.Lookup(ctx, a.Actor(), c, fp)
	if e != nil || !found {
		return r, found, e
	}
	if e = authorizeFulfillment(ctx, a, f.Permission(c.Action)); e != nil {
		return f.Result{}, false, e
	}
	return r, true, nil
}
func (s *FulfillmentService) Mutate(ctx context.Context, a f.Access, c f.Command) (f.Result, error) {
	options := stock.DefaultPostingOptions()
	ctx, cancel := context.WithTimeout(ctx, options.TotalTimeout)
	defer cancel()
	if e := authorizeFulfillment(ctx, a, f.Permission(c.Action)); e != nil {
		return f.Result{}, e
	}
	if e := c.Validate(); e != nil {
		return f.Result{}, e
	}
	actor := a.Actor()
	if c.Action == f.Create {
		if actor.Kind != "MACHINE" {
			return f.Result{}, stock.ErrForbidden
		}
		c.OwnerMachineID = actor.ID
	} else {
		d, e := s.store.Get(ctx, actor, c.ID)
		if e != nil {
			return f.Result{}, e
		}
		if d.WarehouseID != c.WarehouseID {
			return f.Result{}, f.ErrNotFound
		}
		c.OwnerMachineID = d.OwnerMachineID
		c.ReservationID = d.ReservationID
		c.OrderID = d.OrderID
		c.Generation = d.Generation
	}
	fp, e := c.Fingerprint(actor)
	if e != nil {
		return f.Result{}, e
	}
	if r, found, e := s.replay(ctx, a, c, fp); e != nil || found {
		return r, e
	}
	reject := func(err error) (f.Result, error) {
		if r, found, e := s.replay(ctx, a, c, fp); e != nil || found {
			return r, e
		}
		return f.Result{}, err
	}
	remaining := options.TransactionBudget
	for attempt := 0; attempt < options.MaxAttempts; attempt++ {
		var prepared []stock.PreparedLine
		if c.Action == f.Dispatch {
			snapshot, err := s.store.Reservation(ctx, c.OwnerMachineID, c.ReservationID)
			if err != nil {
				return reject(err)
			}
			if snapshot.State != stock.ReservationActive {
				return reject(f.ErrState)
			}
			command, err := f.ConsumeCommand(c, actor, snapshot)
			if err != nil {
				return reject(err)
			}
			refctx, stop := context.WithTimeout(ctx, options.ReferenceTimeout)
			observations, err := s.refs.Prepare(refctx, directSelections(command))
			if err == nil {
				prepared, err = stock.Prepare(command, observations)
			}
			if err == nil {
				err = s.refs.Freshness().Check(refctx, observations)
			}
			if refctx.Err() != nil {
				err = refctx.Err()
			}
			stop()
			if err != nil {
				return reject(stock.ReferenceFailure(err))
			}
		}
		if e := authorizeFulfillment(ctx, a, f.Permission(c.Action)); e != nil {
			return f.Result{}, e
		}
		if remaining <= 0 {
			return f.Result{}, context.DeadlineExceeded
		}
		if c.Action == f.Dispatch {
			obs := make([]stock.Observation, len(prepared))
			for i, l := range prepared {
				obs[i] = l.Observation
			}
			if e := s.refs.Freshness().Check(ctx, obs); e != nil {
				return reject(stock.ReferenceFailure(e))
			}
		}
		txctx, stop := context.WithTimeout(ctx, remaining)
		start := time.Now()
		out, err := s.store.Apply(txctx, actor, c, fp, prepared)
		remaining -= time.Since(start)
		stop()
		if err == nil {
			if out.Replayed {
				if e := authorizeFulfillment(ctx, a, f.Permission(c.Action)); e != nil {
					return f.Result{}, e
				}
			}
			return out, nil
		}
		if errors.Is(err, stock.ErrOutcomeUnknown) {
			return f.Result{}, err
		}
		if !errors.Is(err, stock.ErrRetryTransaction) {
			return reject(err)
		}
		if attempt+1 == options.MaxAttempts {
			return f.Result{}, stock.ErrBusy
		}
		timer := time.NewTimer(options.RetryBackoff + time.Duration(rand.Int64N(int64(options.RetryBackoff))))
		select {
		case <-ctx.Done():
			timer.Stop()
			return f.Result{}, ctx.Err()
		case <-timer.C:
		}
	}
	return f.Result{}, stock.ErrBusy
}

type DispatchLine struct {
	Number    int16          `json:"line_number"`
	OrderLine uuid.UUID      `json:"order_line_id"`
	Variant   uuid.UUID      `json:"variant_id"`
	Product   uuid.UUID      `json:"product_id"`
	Unit      uuid.UUID      `json:"base_unit_id"`
	Pieces    stock.Quantity `json:"pieces"`
}
type DispatchDetail struct {
	f.Snapshot
	Lines []DispatchLine `json:"lines"`
}

func (s *FulfillmentService) Get(ctx context.Context, a f.Access, id uuid.UUID) (DispatchDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := authorizeFulfillment(ctx, a, f.ReadPermission); e != nil {
		return DispatchDetail{}, e
	}
	d, e := s.store.Get(ctx, a.Actor(), id)
	if e != nil {
		return DispatchDetail{}, e
	}
	snapshot, e := s.store.Reservation(ctx, d.OwnerMachineID, d.ReservationID)
	if e != nil {
		return DispatchDetail{}, e
	}
	out := DispatchDetail{Snapshot: d, Lines: make([]DispatchLine, 0, len(snapshot.Lines))}
	for _, l := range snapshot.Lines {
		out.Lines = append(out.Lines, DispatchLine{l.LineNumber, l.OrderLineID, l.ReservationLine.VariantID, l.ProductID, l.BaseUnitID, l.Pieces})
	}
	if e := authorizeFulfillment(ctx, a, f.ReadPermission); e != nil {
		return DispatchDetail{}, e
	}
	return out, nil
}

type fulfillmentCursor struct {
	Version     int       `json:"version"`
	Actor       f.Actor   `json:"actor"`
	Kind        string    `json:"kind"`
	Dispatch    uuid.UUID `json:"dispatch"`
	State       string    `json:"state"`
	Warehouse   uuid.UUID `json:"warehouse"`
	Order       uuid.UUID `json:"order"`
	Reservation uuid.UUID `json:"reservation"`
	Owner       uuid.UUID `json:"owner"`
	Before      uuid.UUID `json:"before"`
}

func cursorText(c fulfillmentCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func parseFulfillmentCursor(raw string, want fulfillmentCursor) (uuid.UUID, error) {
	if len(raw) > 1024 {
		return uuid.Nil, stock.ErrInvalidArgument
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(raw)
	var got fulfillmentCursor
	if e != nil || strictjson.Decode(b, &got) != nil || cursorText(got) != raw || got.Before == uuid.Nil {
		return uuid.Nil, stock.ErrInvalidArgument
	}
	after := got.Before
	got.Before = uuid.Nil
	x, _ := json.Marshal(got)
	y, _ := json.Marshal(want)
	if !bytes.Equal(x, y) {
		return uuid.Nil, stock.ErrInvalidArgument
	}
	return after, nil
}

type DispatchPage struct {
	Items []f.Snapshot `json:"items"`
	Next  *string      `json:"next_cursor"`
}

func (s *FulfillmentService) List(ctx context.Context, a f.Access, in f.Filter) (DispatchPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := authorizeFulfillment(ctx, a, f.ReadPermission); e != nil {
		return DispatchPage{}, e
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > 100 || in.After != uuid.Nil {
		return DispatchPage{}, stock.ErrInvalidArgument
	}
	if in.State != "" && in.State != "DRAFT" && in.State != "CANCELLED" && in.State != "DISPATCHED" && in.State != "DELIVERED" {
		return DispatchPage{}, stock.ErrInvalidArgument
	}
	if a.Actor().Kind == "MACHINE" {
		if in.OwnerMachineID != uuid.Nil {
			return DispatchPage{}, stock.ErrInvalidArgument
		}
		in.OwnerMachineID = a.Actor().ID
	}
	cursor := fulfillmentCursor{Version: 1, Actor: a.Actor(), Kind: "dispatches", State: in.State, Warehouse: in.WarehouseID, Order: in.OrderID, Reservation: in.ReservationID, Owner: in.OwnerMachineID}
	if in.Cursor != "" {
		after, e := parseFulfillmentCursor(in.Cursor, cursor)
		if e != nil {
			return DispatchPage{}, e
		}
		in.After = after
	}
	limit := in.Limit
	in.Limit++
	rows, e := s.store.List(ctx, a.Actor(), in)
	if e != nil {
		return DispatchPage{}, e
	}
	if rows == nil {
		rows = []f.Snapshot{}
	}
	page := DispatchPage{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		cursor.Before = page.Items[limit-1].ID
		v := cursorText(cursor)
		page.Next = &v
	}
	if e := authorizeFulfillment(ctx, a, f.ReadPermission); e != nil {
		return DispatchPage{}, e
	}
	return page, nil
}

type DispatchAuditPage struct {
	Items []f.Transition `json:"items"`
	Next  *string        `json:"next_cursor"`
}

func (s *FulfillmentService) Audit(ctx context.Context, a f.Access, id uuid.UUID, in PageInput) (DispatchAuditPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := authorizeFulfillment(ctx, a, f.ReadPermission); e != nil {
		return DispatchAuditPage{}, e
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > 100 {
		return DispatchAuditPage{}, stock.ErrInvalidArgument
	}
	if _, e := s.store.Get(ctx, a.Actor(), id); e != nil {
		return DispatchAuditPage{}, e
	}
	cursor := fulfillmentCursor{Version: 1, Actor: a.Actor(), Kind: "transitions", Dispatch: id}
	var after uuid.UUID
	if in.Cursor != "" {
		var e error
		after, e = parseFulfillmentCursor(in.Cursor, cursor)
		if e != nil {
			return DispatchAuditPage{}, e
		}
	}
	rows, e := s.store.Audit(ctx, a.Actor(), id, after, in.Limit+1)
	if e != nil {
		return DispatchAuditPage{}, e
	}
	if rows == nil {
		rows = []f.Transition{}
	}
	page := DispatchAuditPage{Items: rows}
	if len(rows) > in.Limit {
		page.Items = rows[:in.Limit]
		cursor.Before = page.Items[in.Limit-1].ID
		v := cursorText(cursor)
		page.Next = &v
	}
	if e := authorizeFulfillment(ctx, a, f.ReadPermission); e != nil {
		return DispatchAuditPage{}, e
	}
	return page, nil
}

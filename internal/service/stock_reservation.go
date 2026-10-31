package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/strictjson"
	"github.com/google/uuid"
)

type StockReservationService struct {
	store   stock.ReservationStore
	posting stock.PostingStore
	refs    OpeningReferences
}

func NewStockReservationService(store stock.ReservationStore, posting stock.PostingStore, refs OpeningReferences) (*StockReservationService, error) {
	if store == nil || posting == nil || refs == nil || refs.Freshness() == nil {
		return nil, stock.ErrInvalidArgument
	}
	return &StockReservationService{store, posting, refs}, nil
}
func authorizeReservation(ctx context.Context, a stock.ReservationAccess, permission string) error {
	if a == nil || a.Owner() == uuid.Nil {
		return stock.ErrForbidden
	}
	err := a.AuthorizeReservation(ctx, permission)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return stock.ErrForbidden
	}
	return nil
}
func (s *StockReservationService) replay(ctx context.Context, a stock.ReservationAccess, c stock.ReservationCommand, fp stock.RequestFingerprint) (stock.ReservationResult, bool, error) {
	result, found, err := s.store.Lookup(ctx, a.Owner(), c, fp)
	if err != nil || !found {
		return result, found, err
	}
	if err = authorizeReservation(ctx, a, stock.PermissionFor(c.Action)); err != nil {
		return stock.ReservationResult{}, false, err
	}
	return result, true, nil
}
func (s *StockReservationService) Mutate(ctx context.Context, a stock.ReservationAccess, c stock.ReservationCommand) (stock.ReservationResult, error) {
	options := stock.DefaultPostingOptions()
	ctx, cancel := context.WithTimeout(ctx, options.TotalTimeout)
	defer cancel()
	if err := authorizeReservation(ctx, a, stock.PermissionFor(c.Action)); err != nil {
		return stock.ReservationResult{}, err
	}
	c.Lines = slices.Clone(c.Lines)
	fp, err := c.Fingerprint(a.Owner())
	if err != nil {
		return stock.ReservationResult{}, err
	}
	if r, found, e := s.replay(ctx, a, c, fp); e != nil || found {
		return r, e
	}
	if c.Action == stock.Consume {
		return s.consume(ctx, a, c, fp)
	}
	remaining := options.TransactionBudget
	for attempt := 0; attempt < options.MaxAttempts; attempt++ {
		var observations []stock.Observation
		if c.Action == stock.Reserve {
			selected := make([]stock.Selection, 0, len(c.Lines))
			for i, l := range c.Lines {
				selected = append(selected, stock.Selection{LineNumber: int16(i + 1), VariantID: l.VariantID, InputCount: l.Pieces})
			}
			refctx, stop := context.WithTimeout(ctx, options.ReferenceTimeout)
			observations, err = s.refs.Prepare(refctx, selected)
			if err == nil {
				_, err = stock.PrepareReservation(c, observations)
			}
			if err == nil {
				err = s.refs.Freshness().Check(refctx, observations)
			}
			if refctx.Err() != nil {
				err = refctx.Err()
			}
			stop()
			if err != nil {
				if r, found, e := s.replay(ctx, a, c, fp); e != nil || found {
					return r, e
				}
				return stock.ReservationResult{}, stock.ReferenceFailure(err)
			}
		}
		if err = authorizeReservation(ctx, a, stock.PermissionFor(c.Action)); err != nil {
			return stock.ReservationResult{}, err
		}
		if remaining <= 0 {
			return stock.ReservationResult{}, context.DeadlineExceeded
		}
		if c.Action == stock.Reserve {
			if err = s.refs.Freshness().Check(ctx, observations); err != nil {
				return stock.ReservationResult{}, stock.ReferenceFailure(err)
			}
		}
		txctx, stop := context.WithTimeout(ctx, remaining)
		start := time.Now()
		result, e := s.store.Apply(txctx, a.Owner(), c, fp, observations)
		remaining -= time.Since(start)
		stop()
		if e == nil {
			if result.Replayed {
				if e = authorizeReservation(ctx, a, stock.PermissionFor(c.Action)); e != nil {
					return stock.ReservationResult{}, e
				}
			}
			return result, nil
		}
		if !errors.Is(e, stock.ErrRetryTransaction) {
			return stock.ReservationResult{}, e
		}
		if attempt+1 == options.MaxAttempts {
			return stock.ReservationResult{}, stock.ErrBusy
		}
		timer := time.NewTimer(options.RetryBackoff + time.Duration(rand.Int64N(int64(options.RetryBackoff))))
		select {
		case <-ctx.Done():
			timer.Stop()
			return stock.ReservationResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return stock.ReservationResult{}, stock.ErrBusy
}

type consumeAuthority struct{ access stock.ReservationAccess }

func (a consumeAuthority) Authorize(ctx context.Context, p stock.Principal, c stock.Command) error {
	if p.UserID != uuid.Nil || p.MachineID != a.access.Owner() || c.Producer != stock.ConsumeProducer || c.Direction != stock.Out || c.Consume == nil {
		return stock.ErrForbidden
	}
	return authorizeReservation(ctx, a.access, stock.ConsumePermission)
}

type consumeReferences struct{ OpeningReferences }

func (r consumeReferences) Resolve(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
	if c.Consume == nil || c.Producer != stock.ConsumeProducer {
		return nil, stock.ErrForbidden
	}
	return r.Prepare(ctx, directSelections(c))
}
func (s *StockReservationService) consume(ctx context.Context, a stock.ReservationAccess, c stock.ReservationCommand, fp stock.RequestFingerprint) (stock.ReservationResult, error) {
	// Another request may complete between the initial lookup and this status
	// read. Reconcile the same key before rejecting its now-terminal generation;
	// this is a read, never another mutation attempt.
	reject := func(cause error) (stock.ReservationResult, error) {
		if r, found, err := s.replay(ctx, a, c, fp); err != nil || found {
			return r, err
		}
		return stock.ReservationResult{}, cause
	}
	snapshot, err := s.store.Get(ctx, a.Owner(), c.ReservationID)
	if errors.Is(err, stock.ErrReservationNotFound) {
		return reject(stock.ErrReservationOrder)
	}
	if err != nil {
		return reject(err)
	}
	if snapshot.OrderID != c.OrderID || snapshot.WarehouseID != c.WarehouseID || snapshot.Generation != c.Generation {
		return reject(stock.ErrReservationStale)
	}
	if snapshot.State != stock.ReservationActive {
		return reject(stock.ErrReservationTerminal)
	}
	command, err := stock.NewConsumeCommand(c, snapshot)
	if err != nil {
		return stock.ReservationResult{}, err
	}
	engine, err := stock.NewPostingService(s.posting, consumeAuthority{a}, consumeReferences{s.refs}, s.refs.Freshness(), stock.DefaultPostingOptions())
	if err != nil {
		return stock.ReservationResult{}, err
	}
	physical, err := engine.Post(ctx, stock.Principal{MachineID: a.Owner()}, command)
	if err != nil {
		return stock.ReservationResult{}, err
	}
	result, found, err := s.store.Lookup(ctx, a.Owner(), c, fp)
	if err != nil {
		return stock.ReservationResult{}, errors.Join(stock.ErrOutcomeUnknown, err)
	}
	if !found {
		return stock.ReservationResult{}, stock.ErrStoredResult
	}
	if err = authorizeReservation(ctx, a, stock.ConsumePermission); err != nil {
		return stock.ReservationResult{}, err
	}
	result.Replayed = physical.Replayed
	return result, nil
}
func (s *StockReservationService) Get(ctx context.Context, a stock.ReservationAccess, id uuid.UUID) (stock.ReservationSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := authorizeReservation(ctx, a, stock.ReservationReadPermission); err != nil {
		return stock.ReservationSnapshot{}, err
	}
	result, err := s.store.Get(ctx, a.Owner(), id)
	if err != nil {
		return result, err
	}
	if err = authorizeReservation(ctx, a, stock.ReservationReadPermission); err != nil {
		return stock.ReservationSnapshot{}, err
	}
	return result, nil
}

type ReservationPage struct {
	Items      []stock.ReservationSnapshot
	NextCursor *string
}
type reservationCursor struct {
	Version int       `json:"version"`
	Owner   uuid.UUID `json:"owner"`
	State   string    `json:"state"`
	Key     uuid.UUID `json:"key"`
}

func (s *StockReservationService) List(ctx context.Context, a stock.ReservationAccess, in PageInput) (ReservationPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := authorizeReservation(ctx, a, stock.ReservationReadPermission); err != nil {
		return ReservationPage{}, err
	}
	if in.Limit == 0 {
		in.Limit = DefaultPageLimit
	}
	if in.Limit < 1 || in.Limit > MaxPageLimit || len(in.Cursor) > 1024 {
		return ReservationPage{}, stock.ErrInvalidArgument
	}
	var after *uuid.UUID
	if in.Cursor != "" {
		data, err := base64.RawURLEncoding.Strict().DecodeString(in.Cursor)
		var cursor reservationCursor
		if err != nil || strictjson.Decode(data, &cursor) != nil {
			return ReservationPage{}, stock.ErrInvalidArgument
		}
		canonical, _ := json.Marshal(cursor)
		if !bytes.Equal(canonical, data) || base64.RawURLEncoding.EncodeToString(data) != in.Cursor || cursor.Version != 1 || cursor.Owner != a.Owner() || cursor.State != "ACTIVE" || cursor.Key == uuid.Nil {
			return ReservationPage{}, stock.ErrInvalidArgument
		}
		after = &cursor.Key
	}
	rows, err := s.store.List(ctx, a.Owner(), after, in.Limit+1)
	if err != nil {
		return ReservationPage{}, err
	}
	if err = authorizeReservation(ctx, a, stock.ReservationReadPermission); err != nil {
		return ReservationPage{}, err
	}
	if rows == nil {
		rows = []stock.ReservationSnapshot{}
	}
	page := ReservationPage{Items: rows}
	if len(rows) > in.Limit {
		page.Items = rows[:in.Limit]
		data, _ := json.Marshal(reservationCursor{1, a.Owner(), "ACTIVE", page.Items[in.Limit-1].ID})
		cursor := base64.RawURLEncoding.EncodeToString(data)
		page.NextCursor = &cursor
	}
	return page, nil
}

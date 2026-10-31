package postgres

import (
	"bytes"
	"context"
	"errors"
	f "github.com/Group-project-bridgeon/Inventory-Service/internal/fulfillment"
	db "github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres/sqlcdb"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"math"
	"strconv"
	"time"
)

var ErrFulfillmentClaimed = f.ErrClaimed

type FulfillmentRepository struct{ stock *StockRepository }

func NewFulfillmentRepository(s *StockRepository) (*FulfillmentRepository, error) {
	if s == nil || s.pool == nil {
		return nil, stock.ErrInvalidArgument
	}
	return &FulfillmentRepository{s}, nil
}
func fulfillmentOwner(a f.Actor) pgtype.UUID {
	if a.Kind == "MACHINE" {
		return dbID(a.ID)
	}
	return pgtype.UUID{}
}
func dispatchSnapshot(d db.Dispatch) f.Snapshot {
	var op *uuid.UUID
	if d.OperationID.Valid {
		id := uuid.UUID(d.OperationID.Bytes)
		op = &id
	}
	return f.Snapshot{ID: uuid.UUID(d.ID.Bytes), ReservationID: uuid.UUID(d.ReservationID.Bytes), OrderID: uuid.UUID(d.OrderID.Bytes), WarehouseID: uuid.UUID(d.WarehouseID.Bytes), OwnerMachineID: uuid.UUID(d.OwnerMachineID.Bytes), Generation: stock.Quantity(d.Generation), State: d.State, Version: stock.Quantity(d.Version), OperationID: op, ExternalReference: d.ExternalReference, CreatedAt: d.CreatedAt.Time, UpdatedAt: d.UpdatedAt.Time}
}
func (r *FulfillmentRepository) Get(ctx context.Context, a f.Actor, id uuid.UUID) (f.Snapshot, error) {
	if a.Validate() != nil || id == uuid.Nil {
		return f.Snapshot{}, stock.ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	row, e := db.New(r.stock.pool).GetDispatch(ctx, db.GetDispatchParams{ID: dbID(id), Owner: fulfillmentOwner(a)})
	if errors.Is(e, pgx.ErrNoRows) {
		return f.Snapshot{}, f.ErrNotFound
	}
	if e != nil {
		return f.Snapshot{}, stockFailure(ctx, "dispatch_read", e)
	}
	return dispatchSnapshot(row), nil
}
func (r *FulfillmentRepository) Reservation(ctx context.Context, owner, id uuid.UUID) (stock.ReservationSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	return loadReservation(ctx, db.New(r.stock.pool), owner, id)
}
func lookupFulfillment(ctx context.Context, q *db.Queries, a f.Actor, c f.Command, fp stock.RequestFingerprint) (f.Result, bool, error) {
	row, e := q.GetFulfillmentCommand(ctx, db.GetFulfillmentCommandParams{ActorKind: a.Kind, ActorID: dbID(a.ID), Key: dbID(c.Key)})
	if errors.Is(e, pgx.ErrNoRows) {
		return f.Result{}, false, nil
	}
	if e != nil {
		return f.Result{}, false, stockFailure(ctx, "fulfillment_lookup", e)
	}
	if row.FingerprintVersion != int16(fp.Version) || !bytes.Equal(row.RequestHash, fp.Hash[:]) || uuid.UUID(row.OwnerMachineID.Bytes) != c.OwnerMachineID || uuid.UUID(row.DispatchID.Bytes) != c.ID || uuid.UUID(row.CommandID.Bytes) != c.CommandID {
		return f.Result{}, true, stock.ErrKeyConflict
	}
	var out f.Result
	if row.State != "COMPLETED" || !row.ResultVersion.Valid || row.ResultVersion.Int16 != 1 || !row.CompletedAt.Valid || strictjson.Decode(row.Result, &out) != nil || out.ResultVersion != "1" || out.ID != c.ID || out.ReservationID != c.ReservationID || out.OrderID != c.OrderID || out.WarehouseID != c.WarehouseID || out.Generation != c.Generation || c.ExpectedVersion == math.MaxInt64 || out.Version != c.ExpectedVersion+1 || out.ChangedAt.IsZero() {
		return f.Result{}, true, stock.ErrStoredResult
	}
	switch c.Action {
	case f.Create:
		if out.State != "DRAFT" || out.OperationID != nil || out.Outcome != nil {
			return f.Result{}, true, stock.ErrStoredResult
		}
	case f.Cancel:
		if out.State != "CANCELLED" || out.OperationID != nil || out.Outcome != nil {
			return f.Result{}, true, stock.ErrStoredResult
		}
	case f.Dispatch:
		if out.State != "DISPATCHED" || out.OperationID == nil || *out.OperationID != c.CommandID || out.Outcome != nil {
			return f.Result{}, true, stock.ErrStoredResult
		}
		snapshot, e := loadReservation(ctx, q, c.OwnerMachineID, c.ReservationID)
		if e != nil {
			return f.Result{}, true, e
		}
		command, e := f.ConsumeCommand(c, a, snapshot)
		if e != nil {
			return f.Result{}, true, stock.ErrStoredResult
		}
		if _, e = stock.DecodePostedResult(out.StockResult, 1, command); e != nil {
			return f.Result{}, true, stock.ErrStoredResult
		}
	case f.Delivery:
		if out.OperationID == nil || out.Outcome == nil || *out.Outcome != c.Outcome || (c.Outcome == "FAILED" && out.State != "DISPATCHED") || (c.Outcome == "DELIVERED" && out.State != "DELIVERED") {
			return f.Result{}, true, stock.ErrStoredResult
		}
	}
	if c.Action != f.Dispatch && !bytes.Equal(bytes.TrimSpace(out.StockResult), []byte("null")) {
		return f.Result{}, true, stock.ErrStoredResult
	}
	out.Replayed = true
	return out, true, nil
}
func (r *FulfillmentRepository) Lookup(ctx context.Context, a f.Actor, c f.Command, fp stock.RequestFingerprint) (f.Result, bool, error) {
	expected, e := c.Fingerprint(a)
	if e != nil || expected != fp {
		return f.Result{}, false, stock.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	return lookupFulfillment(ctx, db.New(r.stock.pool), a, c, fp)
}
func lockDispatchForPosting(ctx context.Context, q *db.Queries, p stock.Principal, c stock.Command) error {
	if c.Consume == nil {
		return stock.ErrInvalidArgument
	}
	req := c.Consume.Request
	if req.DispatchID == uuid.Nil {
		live, e := q.HasLiveDispatch(ctx, dbID(req.ReservationID))
		if e != nil {
			return stockFailure(ctx, "dispatch_association", e)
		}
		if live {
			return f.ErrClaimed
		}
		return nil
	}
	d, e := q.LockDispatch(ctx, dbID(req.DispatchID))
	if errors.Is(e, pgx.ErrNoRows) {
		return f.ErrNotFound
	}
	if e != nil {
		return stockFailure(ctx, "dispatch_lock", e)
	}
	if c.CommandVersion != 4 || p.UserID == uuid.Nil || p.UserID != req.ActorUserID || p.MachineID != uuid.Nil || d.State != "DRAFT" {
		return f.ErrState
	}
	if d.Version != int64(req.DispatchVersion) || uuid.UUID(d.ReservationID.Bytes) != req.ReservationID || uuid.UUID(d.OwnerMachineID.Bytes) != c.Consume.Snapshot.OwnerMachineID || uuid.UUID(d.WarehouseID.Bytes) != c.WarehouseID {
		return f.ErrStale
	}
	return nil
}
func (r *FulfillmentRepository) Apply(ctx context.Context, a f.Actor, c f.Command, fp stock.RequestFingerprint, prepared []stock.PreparedLine) (f.Result, error) {
	out, unknown, e := r.attempt(ctx, a, c, fp, prepared)
	if !unknown {
		return out, e
	}
	resolve, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(resolve, r.stock.pool.Config().ConnConfig.Copy())
	if err == nil {
		defer func() {
			closectx, stop := context.WithTimeout(context.Background(), r.stock.options.CleanupTimeout)
			defer stop()
			_ = conn.Close(closectx)
		}()
		if result, found, e := lookupFulfillment(resolve, db.New(conn), a, c, fp); e == nil && found {
			return result, nil
		}
	}
	return f.Result{}, errors.Join(stock.ErrOutcomeUnknown, ctx.Err())
}
func (r *FulfillmentRepository) attempt(ctx context.Context, a f.Actor, c f.Command, fp stock.RequestFingerprint, prepared []stock.PreparedLine) (f.Result, bool, error) {
	expected, e := c.Fingerprint(a)
	if e != nil || fp != expected {
		return f.Result{}, false, stock.ErrInvalidArgument
	}
	if (a.Kind == "MACHINE" && (a.ID != c.OwnerMachineID || (c.Action != f.Create && c.Action != f.Cancel))) || (a.Kind == "HUMAN" && (c.Action != f.Dispatch && c.Action != f.Delivery)) {
		return f.Result{}, false, stock.ErrForbidden
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return f.Result{}, false, stock.ErrInvalidArgument
	}
	var command stock.Command
	var lines []stock.PreparedLine
	if c.Action == f.Dispatch {
		snapshot, err := r.Reservation(ctx, c.OwnerMachineID, c.ReservationID)
		if err != nil {
			return f.Result{}, false, err
		}
		command, e = f.ConsumeCommand(c, a, snapshot)
		if e != nil {
			return f.Result{}, false, e
		}
		observations := make([]stock.Observation, len(prepared))
		for i, l := range prepared {
			observations[i] = l.Observation
		}
		lines, e = stock.Prepare(command, observations)
		if e != nil {
			return f.Result{}, false, e
		}
	}
	begin := r.stock.begin
	if c.Action == f.Dispatch && r.stock.freshness != nil {
		conn, err := r.stock.pool.Acquire(ctx)
		if err != nil {
			return f.Result{}, false, stockFailure(ctx, "fulfillment_acquire", err)
		}
		defer conn.Release()
		obs := make([]stock.Observation, len(lines))
		for i, l := range lines {
			obs[i] = l.Observation
		}
		if e = r.stock.freshness.Check(ctx, obs); e != nil {
			return f.Result{}, false, e
		}
		begin = conn.BeginTx
	}
	tx, e := begin(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return f.Result{}, false, stockFailure(ctx, "fulfillment_begin", e)
	}
	defer r.stock.cleanup(tx)
	fail := func(e error) (f.Result, bool, error) {
		return f.Result{}, false, stockFailure(ctx, "fulfillment_write", e)
	}
	ms := time.Until(deadline).Milliseconds()
	if ms < 1 {
		return fail(context.DeadlineExceeded)
	}
	if _, e = tx.Exec(ctx, "SELECT set_config('lock_timeout',$1,true),set_config('statement_timeout',$2,true)", strconv.FormatInt(min(ms, r.stock.options.LockTimeout.Milliseconds()), 10)+"ms", strconv.FormatInt(ms, 10)+"ms"); e != nil {
		return fail(e)
	}
	q := db.New(tx)
	n, e := q.ClaimFulfillment(ctx, db.ClaimFulfillmentParams{ActorKind: a.Kind, ActorID: dbID(a.ID), Key: dbID(c.Key), CommandID: dbID(c.CommandID), DispatchID: dbID(c.ID), OwnerMachineID: dbID(c.OwnerMachineID), Action: string(c.Action), RequestHash: fp.Hash[:]})
	if e != nil {
		return fail(e)
	}
	if n == 0 {
		out, found, err := lookupFulfillment(ctx, q, a, c, fp)
		if err == nil && !found {
			err = stock.ErrStoredResult
		}
		return out, false, err
	}
	var d db.Dispatch
	if c.Action == f.Dispatch {
		sfp, err := command.Fingerprint(stock.Principal{UserID: a.ID})
		if err != nil {
			return fail(err)
		}
		if _, e = r.stock.writePosting(ctx, tx, stock.Principal{UserID: a.ID}, command, sfp, lines); e != nil {
			return f.Result{}, false, e
		}
		d, e = q.ChangeDispatch(ctx, db.ChangeDispatchParams{State: "DISPATCHED", OperationID: dbID(c.CommandID), ID: dbID(c.ID), ExpectedVersion: int64(c.ExpectedVersion)})
		if e != nil {
			return fail(e)
		}
	} else {
		w, err := q.LockStockWarehouse(ctx, dbID(c.WarehouseID))
		if errors.Is(err, pgx.ErrNoRows) {
			return f.Result{}, false, stock.ErrWarehouseNotFound
		}
		if err != nil {
			return fail(err)
		}
		if c.Action == f.Create && !w.IsActive {
			return f.Result{}, false, stock.ErrWarehouseInactive
		}
		head, err := q.LockReservationOrder(ctx, db.LockReservationOrderParams{OwnerMachineID: dbID(c.OwnerMachineID), OrderID: dbID(c.OrderID)})
		if errors.Is(err, pgx.ErrNoRows) {
			return f.Result{}, false, f.ErrNotFound
		}
		if err != nil {
			return fail(err)
		}
		reservation, err := q.LockReservationOwned(ctx, db.LockReservationOwnedParams{OwnerMachineID: dbID(c.OwnerMachineID), ID: dbID(c.ReservationID)})
		if errors.Is(err, pgx.ErrNoRows) {
			return f.Result{}, false, f.ErrNotFound
		}
		if err != nil {
			return fail(err)
		}
		if uuid.UUID(reservation.WarehouseID.Bytes) != c.WarehouseID || uuid.UUID(reservation.OrderID.Bytes) != c.OrderID || reservation.Generation != int64(c.Generation) {
			return f.Result{}, false, f.ErrStale
		}
		if c.Action == f.Create {
			if reservation.State != "ACTIVE" {
				return f.Result{}, false, f.ErrState
			}
			if reservation.Version != int64(c.ExpectedReservationVersion) || head.Version != int64(c.ExpectedOrderVersion) || uuid.UUID(head.CurrentReservationID.Bytes) != c.ReservationID {
				return f.Result{}, false, f.ErrStale
			}
			d, e = q.InsertDispatch(ctx, db.InsertDispatchParams{ID: dbID(c.ID), OwnerMachineID: dbID(c.OwnerMachineID), ReservationID: dbID(c.ReservationID), OrderID: dbID(c.OrderID), WarehouseID: dbID(c.WarehouseID), Generation: int64(c.Generation), ExternalReference: c.ExternalReference})
			if e != nil {
				return fail(e)
			}
		} else {
			d, e = q.LockDispatch(ctx, dbID(c.ID))
			if errors.Is(e, pgx.ErrNoRows) {
				return f.Result{}, false, f.ErrNotFound
			}
			if e != nil {
				return fail(e)
			}
			if uuid.UUID(d.OwnerMachineID.Bytes) != c.OwnerMachineID || uuid.UUID(d.ReservationID.Bytes) != c.ReservationID {
				return f.Result{}, false, f.ErrStale
			}
			if d.Version == math.MaxInt64 {
				return f.Result{}, false, stock.ErrVersionExhausted
			}
			if d.Version != int64(c.ExpectedVersion) {
				return f.Result{}, false, f.ErrStale
			}
			state := "CANCELLED"
			if c.Action == f.Cancel {
				if d.State != "DRAFT" {
					return f.Result{}, false, f.ErrState
				}
			} else {
				if d.State != "DISPATCHED" {
					return f.Result{}, false, f.ErrState
				}
				state = "DISPATCHED"
				if c.Outcome == "DELIVERED" {
					state = "DELIVERED"
				}
			}
			d, e = q.ChangeDispatch(ctx, db.ChangeDispatchParams{State: state, OperationID: d.OperationID, ID: d.ID, ExpectedVersion: d.Version})
			if e != nil {
				return fail(e)
			}
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fail(stock.ErrInternal)
	}
	e = q.InsertFulfillmentTransition(ctx, db.InsertFulfillmentTransitionParams{ID: dbID(id), DispatchID: d.ID, CommandID: dbID(c.CommandID), ActorKind: a.Kind, ActorID: dbID(a.ID), Action: string(c.Action), State: d.State, Version: d.Version, OperationID: d.OperationID, Outcome: pgtype.Text{String: c.Outcome, Valid: c.Outcome != ""}, ExternalReference: c.ExternalReference, Note: c.Note, FirstRequestID: pgtype.Text{String: c.FirstRequestID, Valid: c.FirstRequestID != ""}})
	if e != nil {
		return fail(e)
	}
	n, e = q.CompleteFulfillment(ctx, db.CompleteFulfillmentParams{ActorKind: a.Kind, ActorID: dbID(a.ID), Key: dbID(c.Key)})
	if e != nil {
		return fail(e)
	}
	if n != 1 {
		return fail(stock.ErrInternal)
	}
	out, found, e := lookupFulfillment(ctx, q, a, c, fp)
	if e != nil {
		return f.Result{}, false, e
	}
	if !found {
		return fail(stock.ErrStoredResult)
	}
	out.Replayed = false
	if e = tx.Commit(ctx); e != nil {
		var pg *pgconn.PgError
		if (errors.As(e, &pg) && pg.Severity != "FATAL" && pg.Severity != "PANIC") || errors.Is(e, pgx.ErrTxCommitRollback) {
			return fail(e)
		}
		return f.Result{}, true, stock.ErrOutcomeUnknown
	}
	return out, false, nil
}
func (r *FulfillmentRepository) List(ctx context.Context, a f.Actor, in f.Filter) ([]f.Snapshot, error) {
	if a.Validate() != nil || in.Limit < 1 || in.Limit > 101 {
		return nil, stock.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	owner := nullableID(in.OwnerMachineID)
	if a.Kind == "MACHINE" {
		owner = dbID(a.ID)
	}
	rows, e := db.New(r.stock.pool).ListDispatches(ctx, db.ListDispatchesParams{Owner: owner, State: pgtype.Text{String: in.State, Valid: in.State != ""}, Warehouse: nullableID(in.WarehouseID), OrderID: nullableID(in.OrderID), Reservation: nullableID(in.ReservationID), BeforeID: nullableID(in.After), PageSize: int32(in.Limit)})
	if e != nil {
		return nil, stockFailure(ctx, "dispatch_list", e)
	}
	out := make([]f.Snapshot, 0, len(rows))
	for _, d := range rows {
		out = append(out, dispatchSnapshot(d))
	}
	return out, nil
}
func (r *FulfillmentRepository) Audit(ctx context.Context, a f.Actor, id, after uuid.UUID, limit int) ([]f.Transition, error) {
	if a.Validate() != nil || id == uuid.Nil || limit < 1 || limit > 101 {
		return nil, stock.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	rows, e := db.New(r.stock.pool).ListDispatchTransitions(ctx, db.ListDispatchTransitionsParams{DispatchID: dbID(id), Owner: fulfillmentOwner(a), BeforeID: nullableID(after), PageSize: int32(limit)})
	if e != nil {
		return nil, stockFailure(ctx, "dispatch_audit", e)
	}
	out := make([]f.Transition, 0, len(rows))
	for _, t := range rows {
		var outcome *string
		if t.Outcome.Valid {
			v := t.Outcome.String
			outcome = &v
		}
		out = append(out, f.Transition{ID: uuid.UUID(t.ID.Bytes), Action: f.Action(t.Action), State: t.State, Version: stock.Quantity(t.Version), Outcome: outcome, ExternalReference: t.ExternalReference, Note: t.Note, ChangedAt: t.ChangedAt.Time})
	}
	return out, nil
}

var _ f.Store = (*FulfillmentRepository)(nil)

package postgres

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	db "github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres/sqlcdb"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *ReservationRepository) attempt(ctx context.Context, owner uuid.UUID, c stock.ReservationCommand, fp stock.RequestFingerprint, observations []stock.Observation) (stock.ReservationResult, bool, error) {
	expected, err := c.Fingerprint(owner)
	if err != nil || expected != fp || c.Action == stock.Consume {
		return stock.ReservationResult{}, false, stock.ErrInvalidArgument
	}
	var lines []stock.ReservationRecordedLine
	if c.Action == stock.Reserve {
		lines, err = stock.PrepareReservation(c, observations)
		if err != nil {
			return stock.ReservationResult{}, false, err
		}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return stock.ReservationResult{}, false, stock.ErrInvalidArgument
	}
	begin := r.stock.begin
	if r.stock.freshness != nil && c.Action == stock.Reserve {
		conn, e := r.stock.pool.Acquire(ctx)
		if e != nil {
			return stock.ReservationResult{}, false, stockFailure(ctx, "reservation_acquire", e)
		}
		defer conn.Release()
		if e = r.stock.freshness.Check(ctx, observations); e != nil {
			return stock.ReservationResult{}, false, e
		}
		begin = conn.BeginTx
	}
	tx, err := begin(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return stock.ReservationResult{}, false, stockFailure(ctx, "reservation_begin", err)
	}
	defer r.stock.cleanup(tx)
	fail := func(stage string, e error) (stock.ReservationResult, bool, error) {
		return stock.ReservationResult{}, false, stockFailure(ctx, stage, e)
	}
	ms := time.Until(deadline).Milliseconds()
	if ms < 1 {
		return fail("reservation_timeout", context.DeadlineExceeded)
	}
	_, err = tx.Exec(ctx, "SELECT set_config('lock_timeout',$1,true),set_config('statement_timeout',$2,true)", strconv.FormatInt(min(ms, r.stock.options.LockTimeout.Milliseconds()), 10)+"ms", strconv.FormatInt(ms, 10)+"ms")
	if err != nil {
		return fail("reservation_timeouts", err)
	}
	q := db.New(tx)
	n, err := claimReservation(ctx, q, owner, c)
	if err != nil {
		return stock.ReservationResult{}, false, err
	}
	if n == 0 {
		result, found, e := lookupReservation(ctx, q, owner, c, fp)
		if e == nil && !found {
			e = stock.ErrInternal
		}
		return result, false, e
	}
	w, err := q.LockStockWarehouse(ctx, dbID(c.WarehouseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return stock.ReservationResult{}, false, stock.ErrWarehouseNotFound
	}
	if err != nil {
		return fail("reservation_warehouse", err)
	}
	if c.Action == stock.Reserve && !w.IsActive {
		return stock.ReservationResult{}, false, stock.ErrWarehouseInactive
	}
	var orderVersion int64
	if c.Action == stock.Reserve {
		if err = q.EnsureReservationOrder(ctx, db.EnsureReservationOrderParams{OwnerMachineID: dbID(owner), OrderID: dbID(c.OrderID)}); err != nil {
			return fail("reservation_order", err)
		}
		h, e := q.LockReservationOrder(ctx, db.LockReservationOrderParams{OwnerMachineID: dbID(owner), OrderID: dbID(c.OrderID)})
		if e != nil {
			return fail("reservation_order", e)
		}
		if h.CurrentState == "CONSUMED" {
			return stock.ReservationResult{}, false, stock.ErrOrderConsumed
		}
		if h.Generation == int64(c.Generation) || h.CurrentState == "ACTIVE" {
			return stock.ReservationResult{}, false, stock.ErrReservationExists
		}
		if h.Generation != int64(c.ExpectedPreviousGeneration) || h.Version != int64(c.ExpectedOrderVersion) || (h.Generation > 0 && h.CurrentState != "RELEASED") {
			return stock.ReservationResult{}, false, stock.ErrReservationStale
		}
		if h.Version == math.MaxInt64 {
			return stock.ReservationResult{}, false, stock.ErrVersionExhausted
		}
		orderVersion = h.Version + 1
	} else {
		row, h, e := lockReservationTerminal(ctx, q, owner, c)
		if e != nil {
			return stock.ReservationResult{}, false, e
		}
		orderVersion = h.Version + 1
		snapshot, e := loadReservation(ctx, q, owner, c.ReservationID)
		if e != nil {
			return stock.ReservationResult{}, false, e
		}
		lines = snapshot.Lines
		if len(lines) != int(row.LineCount) {
			return stock.ReservationResult{}, false, stock.ErrInternal
		}
	}
	// Order/reservation locks precede every balance. Validate the entire batch
	// before changing any reserved quantity; metadata also rolls back on failure.
	sorted := slices.Clone(lines)
	slices.SortFunc(sorted, func(a, b stock.ReservationRecordedLine) int {
		return bytes.Compare(a.ReservationLine.VariantID[:], b.ReservationLine.VariantID[:])
	})
	for _, l := range sorted {
		b, e := q.LockStockBalance(ctx, db.LockStockBalanceParams{WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.ReservationLine.VariantID)})
		if errors.Is(e, pgx.ErrNoRows) {
			return stock.ReservationResult{}, false, stock.ErrInsufficientAvailable
		}
		if e != nil {
			return fail("reservation_balance_lock", e)
		}
		if uuid.UUID(b.BaseUnitID.Bytes) != l.BaseUnitID {
			return stock.ReservationResult{}, false, stock.ErrReferenceChanged
		}
		if b.ReservationVersion == math.MaxInt64 {
			return stock.ReservationResult{}, false, stock.ErrVersionExhausted
		}
		if c.Action == stock.Reserve && int64(l.Pieces) > b.OnHandPieces-b.ReservedPieces {
			return stock.ReservationResult{}, false, stock.ErrInsufficientAvailable
		}
		if c.Action == stock.Release && int64(l.Pieces) > b.ReservedPieces {
			return stock.ReservationResult{}, false, stock.ErrInternal
		}
	}
	if c.Action == stock.Reserve {
		_, err = q.InsertReservation(ctx, db.InsertReservationParams{ID: dbID(c.ReservationID), OwnerMachineID: dbID(owner), OrderID: dbID(c.OrderID), WarehouseID: dbID(c.WarehouseID), Generation: int64(c.Generation), OrderVersion: orderVersion, LineCount: int16(len(lines))})
		if err != nil {
			return fail("reservation_insert", err)
		}
		for _, l := range lines {
			err = q.InsertReservationLine(ctx, db.InsertReservationLineParams{ReservationID: dbID(c.ReservationID), LineNumber: l.LineNumber, OrderLineID: dbID(l.OrderLineID), VariantID: dbID(l.ReservationLine.VariantID), Pieces: int64(l.Pieces), ProductID: dbID(l.ProductID), BaseUnitID: dbID(l.BaseUnitID), ProductCode: l.ProductCode, ProductName: l.ProductName, Sku: l.SKU, BaseUnitCode: l.BaseUnitCode, StockReferenceRevision: l.StockReferenceRevision, ReferenceCheckedAt: pgtype.Timestamptz{Time: l.CheckedAt, Valid: true}})
			if err != nil {
				return fail("reservation_line", err)
			}
		}
	}
	for _, l := range sorted {
		if c.Action == stock.Reserve {
			n, err = q.AddReservedPieces(ctx, db.AddReservedPiecesParams{Pieces: int64(l.Pieces), WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.ReservationLine.VariantID)})
		} else {
			n, err = q.ReleaseReservedPieces(ctx, db.ReleaseReservedPiecesParams{Pieces: int64(l.Pieces), WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.ReservationLine.VariantID)})
		}
		if err != nil {
			return fail("reservation_change", err)
		}
		if n != 1 {
			return stock.ReservationResult{}, false, stock.ErrInternal
		}
	}
	state, version := stock.ReservationActive, int64(1)
	if c.Action == stock.Release {
		state = stock.ReservationReleased
		version = int64(c.ExpectedReservationVersion) + 1
	}
	if err = finishReservation(ctx, q, owner, c, state, version, orderVersion, uuid.Nil); err != nil {
		return stock.ReservationResult{}, false, err
	}
	if err = completeReservation(ctx, q, owner, c); err != nil {
		return stock.ReservationResult{}, false, err
	}
	result, found, err := lookupReservation(ctx, q, owner, c, fp)
	if err != nil || !found {
		if err == nil {
			err = stock.ErrInternal
		}
		return stock.ReservationResult{}, false, err
	}
	result.Replayed = false
	if err = tx.Commit(ctx); err != nil {
		var pg *pgconn.PgError
		if (errors.As(err, &pg) && pg.Severity != "FATAL" && pg.Severity != "PANIC") || errors.Is(err, pgx.ErrTxCommitRollback) {
			return fail("reservation_commit", err)
		}
		return stock.ReservationResult{}, true, stock.ErrOutcomeUnknown
	}
	return result, false, nil
}

package postgres

import (
	"bytes"
	"context"
	"errors"
	"math"

	db "github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres/sqlcdb"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ReservationRepository struct{ stock *StockRepository }

func NewReservationRepository(s *StockRepository) (*ReservationRepository, error) {
	if s == nil || s.pool == nil {
		return nil, stock.ErrInvalidArgument
	}
	return &ReservationRepository{s}, nil
}
func nullableID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: id != uuid.Nil} }
func reservationHeader(row db.StockReservation) stock.ReservationSnapshot {
	return stock.ReservationSnapshot{ID: uuid.UUID(row.ID.Bytes), OwnerMachineID: uuid.UUID(row.OwnerMachineID.Bytes), OrderID: uuid.UUID(row.OrderID.Bytes), WarehouseID: uuid.UUID(row.WarehouseID.Bytes), Generation: stock.Quantity(row.Generation), Version: stock.Quantity(row.Version), OrderVersion: stock.Quantity(row.OrderVersion), State: stock.ReservationState(row.State), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
}
func loadReservation(ctx context.Context, q *db.Queries, owner, id uuid.UUID) (stock.ReservationSnapshot, error) {
	row, err := q.GetReservationOwned(ctx, db.GetReservationOwnedParams{OwnerMachineID: dbID(owner), ID: dbID(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return stock.ReservationSnapshot{}, stock.ErrReservationNotFound
	}
	if err != nil {
		return stock.ReservationSnapshot{}, stockFailure(ctx, "reservation_read", err)
	}
	result := reservationHeader(row)
	lines, err := q.GetReservationLines(ctx, dbID(id))
	if err != nil {
		return result, stockFailure(ctx, "reservation_lines", err)
	}
	if len(lines) != int(row.LineCount) {
		return result, stock.ErrInternal
	}
	for _, l := range lines {
		// These booleans describe historical active-at-reserve evidence only.
		o := stock.Observation{ReferenceSnapshot: stock.ReferenceSnapshot{ProductID: uuid.UUID(l.ProductID.Bytes), VariantID: uuid.UUID(l.VariantID.Bytes), BaseUnitID: uuid.UUID(l.BaseUnitID.Bytes), QuantityKind: "PIECE", ProductActive: true, VariantActive: true, BaseUnitActive: true, StockReferenceRevision: l.StockReferenceRevision, CheckedAt: l.ReferenceCheckedAt.Time}, ProductCode: l.ProductCode, ProductName: l.ProductName, SKU: l.Sku, BaseUnitCode: l.BaseUnitCode}
		result.Lines = append(result.Lines, stock.ReservationRecordedLine{ReservationLine: stock.ReservationLine{OrderLineID: uuid.UUID(l.OrderLineID.Bytes), VariantID: uuid.UUID(l.VariantID.Bytes), Pieces: stock.Quantity(l.Pieces)}, LineNumber: l.LineNumber, Observation: o})
	}
	return result, nil
}
func (r *ReservationRepository) Get(ctx context.Context, owner, id uuid.UUID) (stock.ReservationSnapshot, error) {
	if owner == uuid.Nil || id == uuid.Nil {
		return stock.ReservationSnapshot{}, stock.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	return loadReservation(ctx, db.New(r.stock.pool), owner, id)
}
func (r *ReservationRepository) List(ctx context.Context, owner uuid.UUID, after *uuid.UUID, limit int) ([]stock.ReservationSnapshot, error) {
	if owner == uuid.Nil || limit < 1 || limit > 101 || (after != nil && *after == uuid.Nil) {
		return nil, stock.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	var cursor pgtype.UUID
	if after != nil {
		cursor = dbID(*after)
	}
	rows, err := db.New(r.stock.pool).ListActiveReservations(ctx, db.ListActiveReservationsParams{OwnerMachineID: dbID(owner), AfterID: cursor, PageSize: int32(limit)})
	if err != nil {
		return nil, stockFailure(ctx, "reservation_list", err)
	}
	result := make([]stock.ReservationSnapshot, 0, len(rows))
	for _, row := range rows {
		result = append(result, reservationHeader(row))
	}
	return result, nil
}
func lookupReservation(ctx context.Context, q *db.Queries, owner uuid.UUID, c stock.ReservationCommand, fp stock.RequestFingerprint) (stock.ReservationResult, bool, error) {
	row, err := q.GetReservationCommand(ctx, db.GetReservationCommandParams{OwnerMachineID: dbID(owner), Key: dbID(c.Key)})
	if errors.Is(err, pgx.ErrNoRows) {
		return stock.ReservationResult{}, false, nil
	}
	if err != nil {
		return stock.ReservationResult{}, false, stockFailure(ctx, "reservation_lookup", err)
	}
	if row.FingerprintVersion != int16(fp.Version) || !bytes.Equal(row.RequestHash, fp.Hash[:]) || uuid.UUID(row.CommandID.Bytes) != c.CommandID {
		return stock.ReservationResult{}, true, stock.ErrKeyConflict
	}
	if row.State != "COMPLETED" || !row.ResultVersion.Valid || row.ResultVersion.Int16 != 1 || !row.CompletedAt.Valid {
		return stock.ReservationResult{}, true, stock.ErrStoredResult
	}
	var result stock.ReservationResult
	if strictjson.Decode(row.Result, &result) != nil || result.ResultVersion != "1" || result.ReservationID != c.ReservationID || result.OrderID != c.OrderID || result.WarehouseID != c.WarehouseID || result.Generation != c.Generation || result.ChangedAt.IsZero() || c.ExpectedOrderVersion == math.MaxInt64 || result.OrderVersion != c.ExpectedOrderVersion+1 {
		return result, true, stock.ErrStoredResult
	}
	wantState, wantVersion := stock.ReservationActive, stock.Quantity(1)
	if c.Action == stock.Release {
		wantState = stock.ReservationReleased
		wantVersion = c.ExpectedReservationVersion + 1
	}
	if c.Action == stock.Consume {
		wantState = stock.ReservationConsumed
		wantVersion = c.ExpectedReservationVersion + 1
	}
	if result.State != wantState || result.Version != wantVersion {
		return result, true, stock.ErrStoredResult
	}
	if c.Action == stock.Consume {
		if result.OperationID == nil || *result.OperationID != c.CommandID {
			return result, true, stock.ErrStoredResult
		}
		snapshot, e := loadReservation(ctx, q, owner, c.ReservationID)
		if e != nil {
			return result, true, e
		}
		command, e := stock.NewConsumeCommand(c, snapshot)
		if e != nil {
			return result, true, stock.ErrStoredResult
		}
		if _, e = stock.DecodePostedResult(result.StockResult, 1, command); e != nil {
			return result, true, stock.ErrStoredResult
		}
	} else if result.OperationID != nil || !bytes.Equal(bytes.TrimSpace(result.StockResult), []byte("null")) {
		return result, true, stock.ErrStoredResult
	}
	result.Replayed = true
	return result, true, nil
}
func (r *ReservationRepository) Lookup(ctx context.Context, owner uuid.UUID, c stock.ReservationCommand, fp stock.RequestFingerprint) (stock.ReservationResult, bool, error) {
	expected, err := c.Fingerprint(owner)
	if err != nil || expected != fp {
		return stock.ReservationResult{}, false, stock.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	return lookupReservation(ctx, db.New(r.stock.pool), owner, c, fp)
}
func claimReservation(ctx context.Context, q *db.Queries, owner uuid.UUID, c stock.ReservationCommand) (int64, error) {
	fp, err := c.Fingerprint(owner)
	if err != nil {
		return 0, err
	}
	n, err := q.ClaimReservationCommand(ctx, db.ClaimReservationCommandParams{OwnerMachineID: dbID(owner), Key: dbID(c.Key), CommandID: dbID(c.CommandID), ReservationID: dbID(c.ReservationID), OrderID: dbID(c.OrderID), WarehouseID: dbID(c.WarehouseID), Generation: int64(c.Generation), Action: string(c.Action), FingerprintVersion: int16(fp.Version), RequestHash: fp.Hash[:], ActorUserID: nullableID(c.ActorUserID), DispatchID: nullableID(c.DispatchID)})
	if err != nil {
		return 0, stockFailure(ctx, "claim", err)
	}
	return n, nil
}
func lockReservationTerminal(ctx context.Context, q *db.Queries, owner uuid.UUID, c stock.ReservationCommand) (db.StockReservation, db.LockReservationOrderRow, error) {
	h, err := q.LockReservationOrder(ctx, db.LockReservationOrderParams{OwnerMachineID: dbID(owner), OrderID: dbID(c.OrderID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.StockReservation{}, h, stock.ErrReservationOrder
	}
	if err != nil {
		return db.StockReservation{}, h, stockFailure(ctx, "reservation_order", err)
	}
	row, err := q.LockReservationOwned(ctx, db.LockReservationOwnedParams{OwnerMachineID: dbID(owner), ID: dbID(c.ReservationID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, h, stock.ErrReservationOrder
	}
	if err != nil {
		return row, h, stockFailure(ctx, "reservation_lock", err)
	}
	if uuid.UUID(row.OrderID.Bytes) != c.OrderID || uuid.UUID(row.WarehouseID.Bytes) != c.WarehouseID || row.Generation != int64(c.Generation) {
		return row, h, stock.ErrReservationStale
	}
	if row.State != "ACTIVE" {
		return row, h, stock.ErrReservationTerminal
	}
	if !h.CurrentReservationID.Valid || uuid.UUID(h.CurrentReservationID.Bytes) != c.ReservationID || h.Generation != row.Generation || h.Version != int64(c.ExpectedOrderVersion) || row.Version != int64(c.ExpectedReservationVersion) {
		return row, h, stock.ErrReservationStale
	}
	if h.Version == math.MaxInt64 || row.Version == math.MaxInt64 {
		return row, h, stock.ErrVersionExhausted
	}
	return row, h, nil
}
func finishReservation(ctx context.Context, q *db.Queries, owner uuid.UUID, c stock.ReservationCommand, state stock.ReservationState, version, orderVersion int64, operation uuid.UUID) error {
	if c.Action != stock.Reserve {
		_, err := q.SetReservationTerminal(ctx, db.SetReservationTerminalParams{State: string(state), OrderVersion: orderVersion, OperationID: nullableID(operation), ID: dbID(c.ReservationID), OwnerMachineID: dbID(owner), ExpectedVersion: int64(c.ExpectedReservationVersion)})
		if err != nil {
			return stockFailure(ctx, "reservation_terminal", err)
		}
	}
	_, err := q.AdvanceReservationOrder(ctx, db.AdvanceReservationOrderParams{Generation: int64(c.Generation), ReservationID: dbID(c.ReservationID), OwnerMachineID: dbID(owner), OrderID: dbID(c.OrderID), ExpectedVersion: int64(c.ExpectedOrderVersion)})
	if err != nil {
		return stockFailure(ctx, "reservation_advance", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return stock.ErrInternal
	}
	err = q.InsertReservationTransition(ctx, db.InsertReservationTransitionParams{ID: dbID(id), ReservationID: dbID(c.ReservationID), OwnerMachineID: dbID(owner), OrderID: dbID(c.OrderID), CommandID: dbID(c.CommandID), Action: string(c.Action), State: string(state), ReservationVersion: version, OrderVersion: orderVersion, OperationID: nullableID(operation), FirstRequestID: pgtype.Text{String: c.FirstRequestID, Valid: c.FirstRequestID != ""}, ActorUserID: nullableID(c.ActorUserID), DispatchID: nullableID(c.DispatchID)})
	if err != nil {
		return stockFailure(ctx, "reservation_audit", err)
	}
	return nil
}
func completeReservation(ctx context.Context, q *db.Queries, owner uuid.UUID, c stock.ReservationCommand) error {
	n, err := q.CompleteReservationCommand(ctx, db.CompleteReservationCommandParams{OwnerMachineID: dbID(owner), Key: dbID(c.Key), CommandID: dbID(c.CommandID)})
	if err != nil {
		return stockFailure(ctx, "reservation_complete", err)
	}
	if n != 1 {
		return stock.ErrInternal
	}
	return nil
}
func (r *ReservationRepository) Apply(ctx context.Context, owner uuid.UUID, c stock.ReservationCommand, fp stock.RequestFingerprint, observations []stock.Observation) (stock.ReservationResult, error) {
	result, unknown, err := r.attempt(ctx, owner, c, fp, observations)
	if !unknown {
		return result, err
	}
	// The same remaining-budget, fresh-physical-primary recovery contract as posting.
	resolve, cancel := context.WithTimeout(ctx, r.stock.options.LookupTimeout)
	defer cancel()
	conn, e := pgx.ConnectConfig(resolve, r.stock.pool.Config().ConnConfig.Copy())
	if e == nil {
		defer func() {
			closectx, stop := context.WithTimeout(context.Background(), r.stock.options.CleanupTimeout)
			defer stop()
			_ = conn.Close(closectx)
		}()
		if found, ok, lookupErr := lookupReservation(resolve, db.New(conn), owner, c, fp); lookupErr == nil && ok {
			return found, nil
		}
	}
	return stock.ReservationResult{}, errors.Join(stock.ErrOutcomeUnknown, ctx.Err())
}

var _ stock.ReservationStore = (*ReservationRepository)(nil)

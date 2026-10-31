package postgres

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	db "github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres/sqlcdb"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type StockRepositoryOptions struct{ LockTimeout, LookupTimeout, CleanupTimeout time.Duration }

func DefaultStockRepositoryOptions() StockRepositoryOptions {
	return StockRepositoryOptions{500 * time.Millisecond, 500 * time.Millisecond, 2 * time.Second}
}

// StockRepository must use an authoritative primary. No replica lookup, background
// work, remote reference call or production authorization adapter lives here.
type StockRepository struct {
	pool      *pgxpool.Pool
	options   StockRepositoryOptions
	begin     func(context.Context, pgx.TxOptions) (pgx.Tx, error)
	freshness stock.FreshnessPolicy
}

func NewStockRepository(pool *pgxpool.Pool, options StockRepositoryOptions) (*StockRepository, error) {
	if pool == nil || options.LockTimeout < time.Millisecond || options.LookupTimeout <= 0 || options.CleanupTimeout <= 0 {
		return nil, stock.ErrInvalidArgument
	}
	return &StockRepository{pool: pool, options: options, begin: pool.BeginTx}, nil
}

// NewStockRepositoryWithFreshness checks the approved pure time policy after
// connection acquisition, immediately before BEGIN. Pool waits must not consume
// the observation window unnoticed. Existing isolated repository tests retain
// their explicit synthetic contract through NewStockRepository.
func NewStockRepositoryWithFreshness(pool *pgxpool.Pool, options StockRepositoryOptions, policy stock.FreshnessPolicy) (*StockRepository, error) {
	if policy == nil {
		return nil, stock.ErrInvalidArgument
	}
	r, err := NewStockRepository(pool, options)
	if err != nil {
		return nil, err
	}
	r.freshness = policy
	return r, nil
}

func stockFailure(ctx context.Context, stage string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pg *pgconn.PgError
	tag := func(kind error) error { return errors.Join(kind, logger.Classify("postgres", "stock_"+stage, err)) }
	if errors.As(err, &pg) {
		switch pg.Code {
		case "40001", "40P01":
			return tag(stock.ErrRetryTransaction)
		case "55P03":
			if stage == "claim" {
				return tag(stock.ErrInProgress)
			}
			return tag(stock.ErrBusy)
		case "57014":
			return errors.Join(stock.ErrBusy, logger.Classify("postgres", "stock_"+stage, err))
		case "23505":
			switch pg.ConstraintName {
			case "fulfillment_live_reservation", "dispatches_pkey", "fulfillment_commands_actor_kind_actor_id_command_id_key":
				return tag(ErrFulfillmentClaimed)
			case "reservation_business_unique", "stock_reservations_pkey", "reservation_command_identity_unique":
				return tag(stock.ErrReservationExists)
			case "stock_audit_original_unique":
				return tag(stock.ErrAlreadyReversed)
			case "stock_operations_source_event_unique", "stock_operations_key_unique", "stock_operations_pkey", "stock_idempotency_operation_unique":
				return tag(stock.ErrOperationConflict)
			}
		}
	}
	return errors.Join(stock.ErrInternal, logger.Classify("postgres", "stock_"+stage, err))
}

func stockReplay(row db.StockIdempotency, p stock.Principal, c stock.Command, fp stock.RequestFingerprint) (stock.PostedResult, error) {
	if !stockActorMatches(row, p) {
		return stock.PostedResult{}, stock.ErrForbidden
	}
	if row.FingerprintVersion != int16(fp.Version) || !bytes.Equal(row.RequestHash, fp.Hash[:]) || !row.OperationID.Valid || uuid.UUID(row.OperationID.Bytes) != c.OperationID {
		return stock.PostedResult{}, stock.ErrKeyConflict
	}
	if row.State != "COMPLETED" || !row.ResultVersion.Valid || !row.CompletedAt.Valid {
		return stock.PostedResult{}, stock.ErrStoredResult
	}
	return stock.DecodePostedResult(row.Result, row.ResultVersion.Int16, c)
}

func lookupStock(ctx context.Context, q *db.Queries, p stock.Principal, c stock.Command, fp stock.RequestFingerprint) (stock.PostedResult, bool, error) {
	row, err := q.GetStockIdempotency(ctx, db.GetStockIdempotencyParams{Producer: c.Producer, Key: dbID(c.IdempotencyKey)})
	if errors.Is(err, pgx.ErrNoRows) {
		return stock.PostedResult{}, false, nil
	}
	if err != nil {
		return stock.PostedResult{}, false, stockFailure(ctx, "lookup", err)
	}
	r, err := stockReplay(row, p, c, fp)
	return r, true, err
}

func (r *StockRepository) Lookup(ctx context.Context, p stock.Principal, c stock.Command, fp stock.RequestFingerprint) (stock.PostedResult, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.options.LookupTimeout)
	defer cancel()
	return lookupStock(ctx, db.New(r.pool), p, c, fp)
}

func (r *StockRepository) cleanup(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), r.options.CleanupTimeout)
	defer cancel()
	_ = tx.Rollback(ctx) // pgx closes a failed rollback connection rather than reusing it.
}

func (r *StockRepository) Apply(ctx context.Context, p stock.Principal, c stock.Command, fp stock.RequestFingerprint, lines []stock.PreparedLine) (stock.PostedResult, error) {
	result, uncertain, err := r.attempt(ctx, p, c, fp, lines)
	if !uncertain {
		return result, err
	}
	// attempt has already released/closed the original transaction. Use a new
	// physical primary connection, never the possibly broken session or a replica.
	// Recovery shares the transaction's remaining deadline/cancellation; it is
	// best effort, not detached reconciliation. An expired context cannot recover
	// here and must retain ErrOutcomeUnknown for a later authorized same-key retry.
	resolve, cancel := context.WithTimeout(ctx, r.options.LookupTimeout)
	defer cancel()
	conn, e := pgx.ConnectConfig(resolve, r.pool.Config().ConnConfig.Copy())
	if e == nil {
		defer func() {
			closectx, stop := context.WithTimeout(context.Background(), r.options.CleanupTimeout)
			defer stop()
			_ = conn.Close(closectx)
		}()
		if foundResult, found, lookupErr := lookupStock(resolve, db.New(conn), p, c, fp); lookupErr == nil && found {
			return foundResult, nil
		}
	}
	// Absence is not proof of rollback; no effect retry is permitted here.
	return stock.PostedResult{}, errors.Join(stock.ErrOutcomeUnknown, ctx.Err())
}

func (r *StockRepository) attempt(ctx context.Context, p stock.Principal, c stock.Command, fp stock.RequestFingerprint, prepared []stock.PreparedLine) (stock.PostedResult, bool, error) {
	// Guard the trusted internal boundary against accidental contract misuse.
	expected, err := c.Fingerprint(p)
	if err != nil {
		return stock.PostedResult{}, false, err
	}
	if expected != fp {
		return stock.PostedResult{}, false, stock.ErrInvalidArgument
	}
	observations := make([]stock.Observation, len(prepared))
	for i, l := range prepared {
		observations[i] = l.Observation
	}
	lines, err := stock.Prepare(c, observations)
	if err != nil {
		return stock.PostedResult{}, false, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return stock.PostedResult{}, false, stock.ErrInvalidArgument
	}
	begin := r.begin
	if r.freshness != nil {
		conn, acquireErr := r.pool.Acquire(ctx)
		if acquireErr != nil {
			return stock.PostedResult{}, false, stockFailure(ctx, "acquire", acquireErr)
		}
		defer conn.Release()
		if err := r.freshness.Check(ctx, observations); err != nil {
			return stock.PostedResult{}, false, err
		}
		begin = conn.BeginTx
	}
	tx, err := begin(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return stock.PostedResult{}, false, stockFailure(ctx, "begin", err)
	}
	defer r.cleanup(tx)
	stage := "timeouts"
	fail := func(err error) (stock.PostedResult, bool, error) {
		return stock.PostedResult{}, false, stockFailure(ctx, stage, err)
	}
	statementMS := time.Until(deadline).Milliseconds()
	if statementMS < 1 {
		return fail(context.DeadlineExceeded)
	}
	lockMS := min(statementMS, r.options.LockTimeout.Milliseconds())
	_, err = tx.Exec(ctx, "SELECT set_config('lock_timeout',$1,true),set_config('statement_timeout',$2,true)", strconv.FormatInt(lockMS, 10)+"ms", strconv.FormatInt(statementMS, 10)+"ms")
	if err != nil {
		return fail(err)
	}
	result, err := r.writePosting(ctx, tx, p, c, fp, lines)
	if err != nil {
		return stock.PostedResult{}, false, err
	}
	err = tx.Commit(ctx)
	if err != nil {
		var pg *pgconn.PgError
		if (errors.As(err, &pg) && pg.Severity != "FATAL" && pg.Severity != "PANIC") || errors.Is(err, pgx.ErrTxCommitRollback) {
			stage = "commit"
			return fail(err)
		}
		return stock.PostedResult{}, true, stock.ErrOutcomeUnknown
	}
	return result, false, nil
}

func (r *StockRepository) writePosting(ctx context.Context, tx pgx.Tx, p stock.Principal, c stock.Command, fp stock.RequestFingerprint, lines []stock.PreparedLine) (stock.PostedResult, error) {
	stage := "claim"
	fail := func(err error) (stock.PostedResult, error) {
		return stock.PostedResult{}, stockFailure(ctx, stage, err)
	}
	q := db.New(tx)
	if c.Consume != nil {
		n, e := claimReservation(ctx, q, c.Consume.Snapshot.OwnerMachineID, c.Consume.Request)
		if e != nil {
			return stock.PostedResult{}, e
		}
		if n == 0 {
			requestFP, _ := c.Consume.Request.Fingerprint(c.Consume.Snapshot.OwnerMachineID)
			if _, found, e := lookupReservation(ctx, q, c.Consume.Snapshot.OwnerMachineID, c.Consume.Request, requestFP); e != nil {
				return stock.PostedResult{}, e
			} else if !found {
				return stock.PostedResult{}, stock.ErrInternal
			}
			result, found, e := lookupStock(ctx, q, p, c, fp)
			if e == nil && !found {
				e = stock.ErrInternal
			}
			return result, e
		}
	}
	stage = "claim"
	n, err := q.ClaimStockIdempotency(ctx, db.ClaimStockIdempotencyParams{Producer: c.Producer, Key: dbID(c.IdempotencyKey), FingerprintVersion: int16(fp.Version), RequestHash: fp.Hash[:], ActorUserID: nullableID(p.UserID), ActorKind: stockActorKind(p), ActorMachineID: nullableID(p.MachineID), OperationID: dbID(c.OperationID)})
	if err != nil {
		return fail(err)
	}
	if n == 0 {
		result, found, e := lookupStock(ctx, q, p, c, fp)
		if e == nil && !found {
			e = stock.ErrInternal
		}
		return result, e
	}
	if n != 1 {
		return fail(stock.ErrInternal)
	}
	stage = "warehouse"
	w, err := q.LockStockWarehouse(ctx, dbID(c.WarehouseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return stock.PostedResult{}, stock.ErrWarehouseNotFound
	}
	if err != nil {
		return fail(err)
	}
	if !w.IsActive {
		return stock.PostedResult{}, stock.ErrWarehouseInactive
	}
	if c.Consume != nil {
		_, _, e := lockReservationTerminal(ctx, q, c.Consume.Snapshot.OwnerMachineID, c.Consume.Request)
		if e != nil {
			return stock.PostedResult{}, e
		}
		if e := lockDispatchForPosting(ctx, q, p, c); e != nil {
			return stock.PostedResult{}, e
		}
	}
	stage = "header"
	_, err = q.InsertStockOperation(ctx, db.InsertStockOperationParams{ID: dbID(c.OperationID), Producer: c.Producer, SourceEventID: dbID(c.SourceEventID), WarehouseID: dbID(c.WarehouseID), Direction: string(c.Direction), ActorUserID: nullableID(p.UserID), ActorKind: stockActorKind(p), ActorMachineID: nullableID(p.MachineID), LineCount: int16(len(c.Lines)), CommandVersion: c.CommandVersion, IdempotencyKey: dbID(c.IdempotencyKey), FirstRequestID: pgtype.Text{String: c.FirstRequestID, Valid: c.FirstRequestID != ""}})
	if err != nil {
		return fail(err)
	}
	if c.Audit != nil {
		stage = "audit"
		err = q.InsertStockAudit(ctx, db.InsertStockAuditParams{OperationID: dbID(c.OperationID), ReasonCode: c.Audit.ReasonCode, Explanation: c.Audit.Explanation, OriginalOperationID: pgtype.UUID{Bytes: c.Audit.OriginalOperationID, Valid: c.Audit.OriginalOperationID != uuid.Nil}})
		if err != nil {
			return fail(err)
		}
	}
	slices.SortFunc(lines, func(a, b stock.PreparedLine) int {
		return bytes.Compare(a.CommandLine.VariantID[:], b.CommandLine.VariantID[:])
	})
	locked := make([]db.StockBalance, len(lines))
	for i, l := range lines {
		stage = "initialize"
		if err = q.InitializeStockBalance(ctx, db.InitializeStockBalanceParams{WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.CommandLine.VariantID), BaseUnitID: dbID(l.BaseUnitID)}); err != nil {
			return fail(err)
		}
		stage = "balance_lock"
		b, e := q.LockStockBalance(ctx, db.LockStockBalanceParams{WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.CommandLine.VariantID)})
		if e != nil {
			return fail(e)
		} // Absence after initialization is an invariant failure.
		if !b.BaseUnitID.Valid || uuid.UUID(b.BaseUnitID.Bytes) != l.BaseUnitID {
			return stock.PostedResult{}, stock.ErrReferenceChanged
		}
		if c.Producer == stock.OpeningProducer {
			used, e := q.HasStockMovements(ctx, db.HasStockMovementsParams{WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.CommandLine.VariantID)})
			if e != nil {
				return fail(e)
			}
			if used || b.Version > 0 {
				return stock.PostedResult{}, stock.ErrOpeningNotEligible
			}
			if b.Version != 0 || b.OnHandPieces != 0 {
				return fail(stock.ErrInternal)
			}
		}
		if c.Consume != nil {
			e = validateConsumeStockChange(b, l.Pieces)
		} else {
			e = validateStockChange(b, c.Direction, l.Pieces)
		}
		if e != nil {
			return stock.PostedResult{}, e
		}
		locked[i] = b
	}
	for i, l := range lines {
		stage = "change"
		var b db.StockBalance
		if c.Consume != nil {
			b, err = q.ConsumeReservedStock(ctx, db.ConsumeReservedStockParams{Pieces: int64(l.Pieces), WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.CommandLine.VariantID)})
		} else if c.Direction == stock.In {
			b, err = q.ReceiveStockPieces(ctx, db.ReceiveStockPiecesParams{Pieces: int64(l.Pieces), WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.CommandLine.VariantID)})
		} else {
			b, err = q.IssueStockPieces(ctx, db.IssueStockPiecesParams{Pieces: int64(l.Pieces), WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.CommandLine.VariantID)})
		}
		if err != nil {
			return fail(err)
		} // Locked predicates already checked: no-row is internal.
		if b.Version != locked[i].Version+1 {
			return fail(stock.ErrInternal)
		}
		movement, e := uuid.NewV7()
		if e != nil {
			return fail(e)
		}
		params := db.InsertStockMovementParams{ID: dbID(movement), OperationID: dbID(c.OperationID), LineNumber: l.LineNumber, WarehouseID: dbID(c.WarehouseID), VariantID: dbID(l.CommandLine.VariantID), BalanceVersion: b.Version, Direction: string(c.Direction), InputCount: int64(l.InputCount), PiecesPerInput: int64(l.Factor), Pieces: int64(l.Pieces), BeforePieces: locked[i].OnHandPieces, AfterPieces: b.OnHandPieces, ProductID: dbID(l.ProductID), BaseUnitID: dbID(l.BaseUnitID), ProductCode: l.ProductCode, ProductName: l.ProductName, Sku: l.SKU, BaseUnitCode: l.BaseUnitCode, StockReferenceRevision: l.StockReferenceRevision, ReferenceCheckedAt: pgtype.Timestamptz{Time: l.CheckedAt, Valid: true}}
		if l.Pack != nil {
			params.PackID = dbID(l.Pack.ID)
			params.PackRevision = pgtype.Text{String: l.Pack.Revision, Valid: true}
			if l.PackLabel != nil {
				params.PackLabel = pgtype.Text{String: *l.PackLabel, Valid: true}
			}
		}
		if c.Producer == stock.CorrectionProducer {
			old, _ := c.Original.Line(l.CommandLine.VariantID)
			params.ProductCode, params.ProductName, params.Sku, params.BaseUnitCode = old.ProductCode, old.ProductName, old.SKU, old.BaseUnitCode
			params.PackID = pgtype.UUID{Bytes: old.PackID, Valid: old.PackID != uuid.Nil}
			params.PackRevision = pgtype.Text{String: old.PackRevision, Valid: old.PackID != uuid.Nil}
			if old.PackLabel != nil {
				params.PackLabel = pgtype.Text{String: *old.PackLabel, Valid: true}
			}
		}
		stage = "movement"
		if _, err = q.InsertStockMovement(ctx, params); err != nil {
			return fail(err)
		}
	}
	if c.Consume != nil {
		req := c.Consume.Request
		if e := finishReservation(ctx, q, c.Consume.Snapshot.OwnerMachineID, req, stock.ReservationConsumed, int64(req.ExpectedReservationVersion)+1, int64(req.ExpectedOrderVersion)+1, c.OperationID); e != nil {
			return stock.PostedResult{}, e
		}
	}
	stage = "complete"
	n, err = q.CompleteStockIdempotency(ctx, db.CompleteStockIdempotencyParams{Producer: c.Producer, Key: dbID(c.IdempotencyKey), OperationID: dbID(c.OperationID)})
	if err != nil {
		return fail(err)
	}
	if n != 1 {
		return fail(stock.ErrInternal)
	}
	if c.Consume != nil {
		if e := completeReservation(ctx, q, c.Consume.Snapshot.OwnerMachineID, c.Consume.Request); e != nil {
			return stock.PostedResult{}, e
		}
	}
	result, found, err := lookupStock(ctx, q, p, c, fp)
	if err != nil {
		return stock.PostedResult{}, err
	}
	if !found {
		return fail(stock.ErrInternal)
	}
	result.Replayed = false
	return result, nil
}

func validateStockChange(b db.StockBalance, direction stock.Direction, pieces stock.Quantity) error {
	if b.Version == math.MaxInt64 {
		return stock.ErrVersionExhausted
	}
	if b.Version < 0 || b.OnHandPieces < 0 {
		return stock.ErrInternal
	}
	if direction == stock.In {
		_, err := stock.AddPieces(stock.Quantity(b.OnHandPieces), pieces)
		return err
	}
	if b.ReservedPieces > 0 && int64(pieces) > b.OnHandPieces-b.ReservedPieces {
		return stock.ErrInsufficientAvailable
	}
	_, err := stock.SubtractPieces(stock.Quantity(b.OnHandPieces), pieces)
	return err
}

var _ stock.PostingStore = (*StockRepository)(nil)

func (r *StockRepository) LoadOriginal(ctx context.Context, warehouse, id uuid.UUID) (stock.OriginalOperation, error) {
	ctx, cancel := context.WithTimeout(ctx, r.options.LookupTimeout)
	defer cancel()
	q := db.New(r.pool)
	o, err := q.GetOriginalStockOperation(ctx, db.GetOriginalStockOperationParams{ID: dbID(id), WarehouseID: dbID(warehouse)})
	if errors.Is(err, pgx.ErrNoRows) {
		return stock.OriginalOperation{}, stock.ErrOriginalNotFound
	}
	if err != nil {
		return stock.OriginalOperation{}, stockFailure(ctx, "original", err)
	}
	if o.Producer != stock.OpeningProducer && o.Producer != stock.AdjustmentProducer {
		return stock.OriginalOperation{}, stock.ErrNotReversible
	}
	rows, err := q.GetOriginalStockLines(ctx, dbID(id))
	if err != nil {
		return stock.OriginalOperation{}, stockFailure(ctx, "original_lines", err)
	}
	result := stock.OriginalOperation{ID: id, WarehouseID: warehouse, Producer: o.Producer, Direction: stock.Direction(o.Direction)}
	if len(rows) != int(o.LineCount) {
		return result, stock.ErrInternal
	}
	for _, l := range rows {
		line := stock.RecordedLine{LineNumber: l.LineNumber, VariantID: uuid.UUID(l.VariantID.Bytes), ProductID: uuid.UUID(l.ProductID.Bytes), BaseUnitID: uuid.UUID(l.BaseUnitID.Bytes), InputCount: stock.Quantity(l.InputCount), Factor: stock.Quantity(l.PiecesPerInput), Pieces: stock.Quantity(l.Pieces), ProductCode: l.ProductCode, ProductName: l.ProductName, SKU: l.Sku, BaseUnitCode: l.BaseUnitCode}
		if l.PackID.Valid {
			line.PackID = uuid.UUID(l.PackID.Bytes)
			line.PackRevision = l.PackRevision.String
		}
		if l.PackLabel.Valid {
			v := l.PackLabel.String
			line.PackLabel = &v
		}
		result.Lines = append(result.Lines, line)
	}
	return result, nil
}

func stockActorKind(p stock.Principal) string {
	if p.MachineID != uuid.Nil {
		return "MACHINE"
	}
	return "HUMAN"
}
func stockActorMatches(row db.StockIdempotency, p stock.Principal) bool {
	if p.MachineID != uuid.Nil {
		return p.UserID == uuid.Nil && row.ActorKind == "MACHINE" && !row.ActorUserID.Valid && row.ActorMachineID.Valid && uuid.UUID(row.ActorMachineID.Bytes) == p.MachineID
	}
	return p.UserID != uuid.Nil && row.ActorKind == "HUMAN" && row.ActorUserID.Valid && uuid.UUID(row.ActorUserID.Bytes) == p.UserID && !row.ActorMachineID.Valid
}
func validateConsumeStockChange(b db.StockBalance, pieces stock.Quantity) error {
	if b.Version == math.MaxInt64 || b.ReservationVersion == math.MaxInt64 {
		return stock.ErrVersionExhausted
	}
	if pieces <= 0 || b.ReservedPieces < int64(pieces) || b.OnHandPieces < int64(pieces) || b.ReservedPieces > b.OnHandPieces {
		return stock.ErrInternal
	}
	return nil
}

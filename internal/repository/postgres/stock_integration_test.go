package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	db "github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres/sqlcdb"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// No DATABASE_URL fallback, .env loading, schema resets or normal credentials.
// Tests require the run marker set by scripts/test-stock-postgres.ps1.
type stockFixture struct {
	t                                        *testing.T
	ctx                                      context.Context
	pool                                     *pgxpool.Pool
	warehouse, variant, unit, product, actor pgtype.UUID
}

func stockID() pgtype.UUID { return pgtype.UUID{Bytes: uuid.Must(uuid.NewV7()), Valid: true} }

func stockOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			t.Fatalf("SQL fixture failure: SQLSTATE=%s constraint=%s", pg.Code, pg.ConstraintName)
		}
		t.Fatalf("SQL fixture failure: type=%T (raw cause suppressed)", err)
	}
}

func stockError(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != code || (constraint != "" && pg.ConstraintName != constraint) {
		if pg != nil {
			t.Fatalf("expected SQLSTATE=%s constraint=%s; actual SQLSTATE=%s constraint=%s", code, constraint, pg.Code, pg.ConstraintName)
		}
		t.Fatalf("expected SQLSTATE=%s constraint=%s; actual type=%T", code, constraint, err)
	}
}

func stockDB(t *testing.T) *stockFixture {
	t.Helper()
	url, run := os.Getenv("STOCK_TEST_DATABASE_URL"), os.Getenv("STOCK_TEST_RUN_ID")
	if url == "" && run == "" {
		t.Skip("run scripts/test-stock-postgres.ps1 for disposable stock PostgreSQL evidence")
	}
	port, err := strconv.Atoi(os.Getenv("STOCK_TEST_PORT"))
	if err != nil || port < 1 || port > 65535 || !strings.HasPrefix(run, "inventory-stock-test-") {
		t.Fatal("invalid stock fixture gate")
	}
	if _, err := uuid.Parse(strings.TrimPrefix(run, "inventory-stock-test-")); err != nil {
		t.Fatal("invalid fixture run identity")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" || cfg.ConnConfig.Port != uint16(port) || cfg.ConnConfig.Database != "inventory_stock_test" || cfg.ConnConfig.User != "inventory_stock_fixture" {
		t.Fatal("refusing non-fixture database destination")
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.RuntimeParams["application_name"] = run
	cfg.MaxConns = 8
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	stockOK(t, err)
	t.Cleanup(pool.Close)
	var marker string
	stockOK(t, pool.QueryRow(ctx, "SELECT current_setting('stock.fixture_id',true)").Scan(&marker))
	if marker != run {
		t.Fatal("database does not belong to this fixture run")
	}
	f := &stockFixture{t: t, ctx: ctx, pool: pool, warehouse: stockID(), variant: stockID(), unit: stockID(), product: stockID(), actor: stockID()}
	_, err = pool.Exec(ctx, "INSERT INTO warehouses(id,code,name) VALUES($1,$2,'Synthetic stock test')", f.warehouse, "ST-"+strings.ToUpper(uuid.UUID(f.warehouse.Bytes).String()))
	stockOK(t, err)
	return f
}

// Test-only staged SQL, not an application posting service. Each test explicitly
// decides whether to append a movement, complete the key, commit or roll back.
type stagedStock struct {
	f       *stockFixture
	tx      pgx.Tx
	q       *db.Queries
	command stock.Command
	old     db.StockBalance
	claim   db.ClaimStockIdempotencyParams
}

func (f *stockFixture) stage(t *testing.T, direction string, pieces int64, operationActor ...pgtype.UUID) *stagedStock {
	local := *f
	local.t = t
	f = &local
	f.t.Helper()
	tx, err := f.begin(t)
	stockOK(f.t, err)
	q := db.New(tx)
	c := stock.Command{OperationID: uuid.Must(uuid.NewV7()), SourceEventID: uuid.New(), Producer: "synthetic-stock-sql", WarehouseID: uuid.UUID(f.warehouse.Bytes), Direction: stock.Direction(direction), CommandVersion: stock.CommandVersion, IdempotencyKey: uuid.New(), Lines: []stock.CommandLine{{LineNumber: 1, VariantID: uuid.UUID(f.variant.Bytes), InputCount: stock.Quantity(pieces), ExpectedStockReferenceRevision: "synthetic-v1"}}}
	fp, err := c.Fingerprint(stock.Principal{UserID: uuid.UUID(f.actor.Bytes)})
	stockOK(f.t, err)
	claim := db.ClaimStockIdempotencyParams{Producer: c.Producer, Key: pgtype.UUID{Bytes: c.IdempotencyKey, Valid: true}, FingerprintVersion: int16(fp.Version), RequestHash: fp.Hash[:], ActorUserID: f.actor, OperationID: pgtype.UUID{Bytes: c.OperationID, Valid: true}}
	n, err := q.ClaimStockIdempotency(f.ctx, claim)
	stockOK(f.t, err)
	if n != 1 {
		f.t.Fatal("new claim not inserted")
	}
	w, err := q.LockStockWarehouse(f.ctx, f.warehouse)
	stockOK(f.t, err)
	if !w.IsActive {
		f.t.Fatal("synthetic warehouse inactive")
	}
	headerActor := f.actor
	if len(operationActor) > 0 {
		headerActor = operationActor[0]
	} // Deliberately malformed fixture for deferred checks.
	_, err = q.InsertStockOperation(f.ctx, db.InsertStockOperationParams{ID: claim.OperationID, Producer: c.Producer, SourceEventID: pgtype.UUID{Bytes: c.SourceEventID, Valid: true}, WarehouseID: f.warehouse, Direction: direction, ActorUserID: headerActor, LineCount: 1, CommandVersion: 1, IdempotencyKey: claim.Key})
	stockOK(f.t, err)
	stockOK(f.t, q.InitializeStockBalance(f.ctx, db.InitializeStockBalanceParams{WarehouseID: f.warehouse, VariantID: f.variant, BaseUnitID: f.unit}))
	old, err := q.LockStockBalance(f.ctx, db.LockStockBalanceParams{WarehouseID: f.warehouse, VariantID: f.variant})
	stockOK(f.t, err)
	return &stagedStock{f: f, tx: tx, q: q, command: c, old: old, claim: claim}
}

func (f *stockFixture) begin(t *testing.T) (pgx.Tx, error) {
	tx, err := f.pool.Begin(f.ctx)
	if err == nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = tx.Rollback(ctx)
		})
	}
	return tx, err
}

func (s *stagedStock) change() (db.StockBalance, error) {
	pieces := int64(s.command.Lines[0].InputCount)
	if s.command.Direction == stock.In {
		return s.q.ReceiveStockPieces(s.f.ctx, db.ReceiveStockPiecesParams{Pieces: pieces, WarehouseID: s.f.warehouse, VariantID: s.f.variant})
	}
	return s.q.IssueStockPieces(s.f.ctx, db.IssueStockPiecesParams{Pieces: pieces, WarehouseID: s.f.warehouse, VariantID: s.f.variant})
}

func (s *stagedStock) movement(b db.StockBalance) db.InsertStockMovementParams {
	return db.InsertStockMovementParams{ID: stockID(), OperationID: s.claim.OperationID, LineNumber: 1, WarehouseID: s.f.warehouse, VariantID: s.f.variant, BalanceVersion: b.Version, Direction: string(s.command.Direction), InputCount: int64(s.command.Lines[0].InputCount), PiecesPerInput: 1, Pieces: int64(s.command.Lines[0].InputCount), BeforePieces: s.old.OnHandPieces, AfterPieces: b.OnHandPieces, ProductID: s.f.product, BaseUnitID: s.f.unit, ProductCode: "SYNTHETIC", ProductName: "Synthetic Catalog fixture", Sku: "SYNTHETIC-SKU", BaseUnitCode: "PCS", StockReferenceRevision: "synthetic-v1", ReferenceCheckedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}}
}

func (s *stagedStock) finish(b db.StockBalance) {
	s.f.t.Helper()
	_, err := s.q.InsertStockMovement(s.f.ctx, s.movement(b))
	stockOK(s.f.t, err)
	n, err := s.q.CompleteStockIdempotency(s.f.ctx, db.CompleteStockIdempotencyParams{Producer: s.claim.Producer, Key: s.claim.Key, OperationID: s.claim.OperationID})
	stockOK(s.f.t, err)
	if n != 1 {
		s.f.t.Fatal("completion affected wrong row count")
	}
}

func stockCounts(t *testing.T, f *stockFixture) string {
	t.Helper()
	var result string
	stockOK(t, f.pool.QueryRow(f.ctx, `SELECT concat((SELECT count(*) FROM stock_balances WHERE warehouse_id=$1),':',(SELECT count(*) FROM stock_operations WHERE warehouse_id=$1),':',(SELECT count(*) FROM stock_movements WHERE warehouse_id=$1),':',(SELECT count(*) FROM stock_idempotency WHERE operation_id IN (SELECT id FROM stock_operations WHERE warehouse_id=$1)))`, f.warehouse).Scan(&result))
	return result
}

func TestStockSQLCompleteBoundariesAndReplay(t *testing.T) {
	f := stockDB(t)
	s := f.stage(t, "IN", math.MaxInt64)
	b, err := s.change()
	stockOK(t, err)
	s.finish(b)
	stockOK(t, s.tx.Commit(f.ctx))
	key, err := db.New(f.pool).GetStockIdempotency(f.ctx, db.GetStockIdempotencyParams{Producer: s.claim.Producer, Key: s.claim.Key})
	stockOK(t, err)
	if key.State != "COMPLETED" || !bytes.Equal(key.RequestHash, s.claim.RequestHash) {
		t.Fatal("durable fingerprint lost")
	}
	var replay struct {
		OperationID   string `json:"operation_id"`
		ResultVersion string `json:"result_version"`
		Lines         []struct {
			After   string `json:"after_pieces"`
			Version string `json:"balance_version"`
		} `json:"lines"`
	}
	stockOK(t, json.Unmarshal(key.Result, &replay))
	if replay.OperationID != s.command.OperationID.String() || replay.ResultVersion != "1" || len(replay.Lines) != 1 || replay.Lines[0].After != "9223372036854775807" || replay.Lines[0].Version != "1" {
		t.Fatal("incorrect exact replay result")
	}
	// A completed key is not overwritten, even with a different hash.
	altered := s.claim
	altered.RequestHash = bytes.Repeat([]byte{7}, 32)
	n, err := db.New(f.pool).ClaimStockIdempotency(f.ctx, altered)
	stockOK(t, err)
	if n != 0 {
		t.Fatal("duplicate claim overwrote record")
	}
	after, err := db.New(f.pool).GetStockIdempotency(f.ctx, db.GetStockIdempotencyParams{Producer: s.claim.Producer, Key: s.claim.Key})
	stockOK(t, err)
	if !bytes.Equal(after.Result, key.Result) || !bytes.Equal(after.RequestHash, key.RequestHash) {
		t.Fatal("duplicate claim changed durable record")
	}
	_, err = f.pool.Exec(f.ctx, `INSERT INTO stock_idempotency(producer,key,fingerprint_version,request_hash,actor_user_id,operation_id) VALUES($1,$2,1,$3,$4,$5)`, s.claim.Producer, s.claim.Key, s.claim.RequestHash, f.actor, s.claim.OperationID)
	stockError(t, err, "23505", "stock_idempotency_pkey")
	before := stockCounts(t, f)
	over := f.stage(t, "IN", 1)
	_, err = over.change()
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("receipt overflow was not rejected")
	}
	stockOK(t, over.tx.Rollback(f.ctx))
	if stockCounts(t, f) != before {
		t.Fatal("overflow left partial records")
	}
	issue := f.stage(t, "OUT", math.MaxInt64)
	b, err = issue.change()
	stockOK(t, err)
	issue.finish(b)
	stockOK(t, issue.tx.Commit(f.ctx))
	if b.OnHandPieces != 0 || b.Version != 2 {
		t.Fatal("max deduction did not reach zero")
	}
	for _, table := range []string{"stock_movements", "stock_operations", "stock_idempotency", "stock_balances"} {
		for _, verb := range []string{"DELETE FROM ", "TRUNCATE "} {
			_, err = f.pool.Exec(f.ctx, verb+table)
			if verb == "TRUNCATE " && table != "stock_movements" {
				stockError(t, err, "0A000", "")
			} else {
				stockError(t, err, "23514", "stock_immutable")
			}
		}
	}
	_, err = f.pool.Exec(f.ctx, "UPDATE stock_movements SET pieces=pieces")
	stockError(t, err, "23514", "stock_immutable")
	_, err = f.pool.Exec(f.ctx, "UPDATE stock_operations SET line_count=line_count")
	stockError(t, err, "23514", "stock_immutable")
	_, err = f.pool.Exec(f.ctx, "UPDATE stock_idempotency SET result=result WHERE producer=$1 AND key=$2", s.claim.Producer, s.claim.Key)
	stockError(t, err, "23514", "stock_key_transition")
}

func TestStockSQLInvalidConstraints(t *testing.T) {
	f := stockDB(t)
	for _, tc := range []struct{ name, sql, code, constraint string }{
		{"negative balance", `INSERT INTO stock_balances(warehouse_id,variant_id,base_unit_id,on_hand_pieces) VALUES($1,$2,$3,-1)`, "23514", "stock_balances_nonnegative"},
		{"int64 minimum", `INSERT INTO stock_balances(warehouse_id,variant_id,base_unit_id,on_hand_pieces) VALUES($1,$2,$3,-9223372036854775808)`, "23514", "stock_balances_nonnegative"},
		{"above int64 maximum", `INSERT INTO stock_balances(warehouse_id,variant_id,base_unit_id,on_hand_pieces) VALUES($1,$2,$3,9223372036854775808)`, "22003", ""},
		{"negative version", `INSERT INTO stock_balances(warehouse_id,variant_id,base_unit_id,version) VALUES($1,$2,$3,-1)`, "23514", "stock_balances_version_valid"},
		{"missing warehouse", `INSERT INTO stock_balances(warehouse_id,variant_id,base_unit_id) VALUES($2,$2,$3)`, "23503", ""},
		{"zero variant", `INSERT INTO stock_balances(warehouse_id,variant_id,base_unit_id) VALUES($1,'00000000-0000-0000-0000-000000000000',$3)`, "23514", "stock_balances_ids_nonzero"},
		{"orphan zero", `INSERT INTO stock_balances(warehouse_id,variant_id,base_unit_id) VALUES($1,$2,$3)`, "23514", "stock_balance_ledger_integrity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := f.begin(t)
			stockOK(t, err)
			_, err = tx.Exec(f.ctx, "WITH fixture AS (SELECT $1::uuid AS w,$2::uuid AS v,$3::uuid AS u) "+tc.sql, f.warehouse, f.variant, f.unit)
			if err == nil {
				err = tx.Commit(f.ctx)
			}
			stockError(t, err, tc.code, tc.constraint)
			_ = tx.Rollback(f.ctx)
		})
	}
	if stockCounts(t, f) != "0:0:0:0" {
		t.Fatal("invalid initialization persisted")
	}
	for _, tc := range []struct {
		name       string
		edit       func(*db.InsertStockMovementParams)
		constraint string
	}{
		{"zero count", func(m *db.InsertStockMovementParams) { m.InputCount = 0 }, ""},
		{"negative count", func(m *db.InsertStockMovementParams) { m.InputCount = math.MinInt64 }, ""},
		{"negative factor", func(m *db.InsertStockMovementParams) { m.PiecesPerInput = math.MinInt64 }, ""},
		{"zero factor", func(m *db.InsertStockMovementParams) { m.PiecesPerInput = 0 }, ""},
		{"conversion overflow", func(m *db.InsertStockMovementParams) { m.InputCount = math.MaxInt64; m.PiecesPerInput = math.MaxInt64 }, "stock_movements_conversion_valid"},
		{"wrong delta", func(m *db.InsertStockMovementParams) { m.AfterPieces++ }, "stock_movements_delta_valid"},
		{"negative before", func(m *db.InsertStockMovementParams) { m.BeforePieces = -1; m.AfterPieces = 0 }, "stock_movements_balances_nonnegative"},
		{"invalid direction", func(m *db.InsertStockMovementParams) { m.Direction = "SIDEWAYS" }, "stock_movements_direction_valid"},
		{"missing metadata", func(m *db.InsertStockMovementParams) { m.Sku = "" }, "stock_movements_snapshots_valid"},
		{"pack no revision", func(m *db.InsertStockMovementParams) { m.PackID = stockID() }, "stock_movements_pack_valid"},
		{"zero product", func(m *db.InsertStockMovementParams) { m.ProductID = pgtype.UUID{Valid: true} }, "stock_movements_ids_valid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := f.stage(t, "IN", 1)
			b, err := s.change()
			stockOK(t, err)
			m := s.movement(b)
			tc.edit(&m)
			_, err = s.q.InsertStockMovement(f.ctx, m)
			stockError(t, err, "23514", tc.constraint)
			stockOK(t, s.tx.Rollback(f.ctx))
		})
	}
	if stockCounts(t, f) != "0:0:0:0" {
		t.Fatal("invalid movement left partial state")
	}
}

func TestStockSQLDeferredCorrespondence(t *testing.T) {
	for _, name := range []string{"missing movement", "missing completion", "forged result", "actor mismatch", "base unit mismatch", "missing predecessor", "header direction", "line outside header", "balance without ledger", "movement without balance update"} {
		t.Run(name, func(t *testing.T) {
			f := stockDB(t)
			var s *stagedStock
			if name == "actor mismatch" {
				s = f.stage(t, "IN", 1, stockID())
			} else {
				s = f.stage(t, "IN", 1)
			}
			b := s.old
			var err error
			if name != "movement without balance update" {
				b, err = s.change()
				stockOK(t, err)
			} else {
				b.Version = 1
				b.OnHandPieces = 1
			}
			if name != "missing movement" && name != "balance without ledger" {
				m := s.movement(b)
				switch name {
				case "base unit mismatch":
					m.BaseUnitID = stockID()
				case "missing predecessor":
					m.BalanceVersion = 2
					_, err = s.tx.Exec(f.ctx, "UPDATE stock_balances SET version=2 WHERE warehouse_id=$1 AND variant_id=$2", f.warehouse, f.variant)
					stockOK(t, err)
				case "header direction":
					m.Direction = "OUT"
					m.BeforePieces = 1
					m.AfterPieces = 0
				case "line outside header":
					m.LineNumber = 2
				}
				_, err = s.q.InsertStockMovement(f.ctx, m)
				stockOK(t, err)
			}
			if name == "forged result" {
				_, err = s.tx.Exec(f.ctx, "UPDATE stock_idempotency SET state='COMPLETED',result_version=1,result='{}',completed_at=now() WHERE producer=$1 AND key=$2", s.claim.Producer, s.claim.Key)
				stockOK(t, err)
			} else if name != "missing completion" {
				_, err = s.q.CompleteStockIdempotency(f.ctx, db.CompleteStockIdempotencyParams{Producer: s.claim.Producer, Key: s.claim.Key, OperationID: s.claim.OperationID})
				stockOK(t, err)
			}
			// All constituent statements succeeded: rejection must happen at Commit.
			stockError(t, s.tx.Commit(f.ctx), "23514", "")
			if stockCounts(t, f) != "0:0:0:0" {
				t.Fatal("failed commit left partial records")
			}
			var keyCount int
			stockOK(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM stock_idempotency WHERE key=$1", s.claim.Key).Scan(&keyCount))
			if keyCount != 0 {
				t.Fatal("failed commit retained a claim")
			}
		})
	}
}

func TestStockSQLReferencesUniquenessAndPackSnapshots(t *testing.T) {
	f := stockDB(t)
	s := f.stage(t, "IN", 12)
	b, err := s.change()
	stockOK(t, err)
	m := s.movement(b)
	m.InputCount = 2
	m.PiecesPerInput = 6
	m.PackID = stockID()
	m.PackRevision = pgtype.Text{String: "pack-v1", Valid: true}
	m.PackLabel = pgtype.Text{String: "Synthetic six", Valid: true}
	_, err = s.q.InsertStockMovement(f.ctx, m)
	stockOK(t, err)
	_, err = s.q.CompleteStockIdempotency(f.ctx, db.CompleteStockIdempotencyParams{Producer: s.claim.Producer, Key: s.claim.Key, OperationID: s.claim.OperationID})
	stockOK(t, err)
	stockOK(t, s.tx.Commit(f.ctx))
	_, err = f.pool.Exec(f.ctx, "INSERT INTO stock_balances(warehouse_id,variant_id,base_unit_id) VALUES($1,$2,$3)", f.warehouse, f.variant, f.unit)
	stockError(t, err, "23505", "stock_balances_pkey")
	// ON CONFLICT does not overwrite the established base unit.
	stockOK(t, db.New(f.pool).InitializeStockBalance(f.ctx, db.InitializeStockBalanceParams{WarehouseID: f.warehouse, VariantID: f.variant, BaseUnitID: stockID()}))
	stored, err := db.New(f.pool).GetStockBalance(f.ctx, db.GetStockBalanceParams{WarehouseID: f.warehouse, VariantID: f.variant})
	stockOK(t, err)
	if stored.BaseUnitID != f.unit {
		t.Fatal("initialization overwrote unit binding")
	}
	_, err = f.pool.Exec(f.ctx, "UPDATE stock_balances SET base_unit_id=$3,version=version+1 WHERE warehouse_id=$1 AND variant_id=$2", f.warehouse, f.variant, stockID())
	stockError(t, err, "23514", "stock_balance_transition")
	tx, err := f.begin(t)
	stockOK(t, err)
	other := stockID()
	_, err = db.New(tx).InsertStockOperation(f.ctx, db.InsertStockOperationParams{ID: other, Producer: s.claim.Producer, SourceEventID: pgtype.UUID{Bytes: s.command.SourceEventID, Valid: true}, WarehouseID: f.warehouse, Direction: "IN", ActorUserID: f.actor, LineCount: 1, CommandVersion: 1, IdempotencyKey: stockID()})
	stockError(t, err, "23505", "stock_operations_source_event_unique")
	_ = tx.Rollback(f.ctx)
	tx, err = f.begin(t)
	stockOK(t, err)
	q := db.New(tx)
	newClaim := s.claim
	newClaim.Key = stockID()
	_, err = q.ClaimStockIdempotency(f.ctx, newClaim)
	stockError(t, err, "23505", "stock_idempotency_operation_unique")
	_ = tx.Rollback(f.ctx)
	// A wrong operation FK is rejected immediately, before deferred correspondence.
	tx, err = f.begin(t)
	stockOK(t, err)
	bad := m
	bad.ID = stockID()
	bad.OperationID = stockID()
	bad.BalanceVersion = 2
	bad.BeforePieces = 12
	bad.AfterPieces = 24
	_, err = db.New(tx).InsertStockMovement(f.ctx, bad)
	stockError(t, err, "23503", "stock_movements_operation_fk")
	_ = tx.Rollback(f.ctx)
	var snapshotsOK bool
	stockOK(t, f.pool.QueryRow(f.ctx, "SELECT input_count=2 AND pieces_per_input=6 AND pieces=12 AND pack_revision='pack-v1' AND pack_label='Synthetic six' FROM stock_movements WHERE id=$1", m.ID).Scan(&snapshotsOK))
	if !snapshotsOK {
		t.Fatal("historical conversion snapshot changed")
	}
}

func TestStockSQLConcurrentDeductionAndInitialization(t *testing.T) {
	f := stockDB(t)
	seed := f.stage(t, "IN", 10)
	b, err := seed.change()
	stockOK(t, err)
	seed.finish(b)
	stockOK(t, seed.tx.Commit(f.ctx))
	results, err := runStockWorkers(f.ctx, 2, func(ctx context.Context, id int, ready chan<- stockWorkerReady, release <-chan struct{}) stockWorkerResult {
		return stockSQLWorker(ctx, f, id, "OUT", 7, ready, release, nil)
	})
	stockOK(t, err)
	assertStockWorkers(t, results)
	successes, rejections := 0, 0
	for _, result := range results {
		if result.Committed {
			successes++
		}
		if result.Rejected {
			rejections++
			if !result.RolledBack {
				t.Fatal("rejected worker did not roll back")
			}
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("deductions: successes=%d rejected=%d", successes, rejections)
	}
	b, err = db.New(f.pool).GetStockBalance(f.ctx, db.GetStockBalanceParams{WarehouseID: f.warehouse, VariantID: f.variant})
	stockOK(t, err)
	if b.OnHandPieces != 3 || b.Version != 2 {
		t.Fatal("concurrent deduction invariant")
	}
	f.variant = stockID()
	results, err = runStockWorkers(f.ctx, 2, func(ctx context.Context, id int, ready chan<- stockWorkerReady, release <-chan struct{}) stockWorkerResult {
		return stockSQLWorker(ctx, f, id, "IN", 5, ready, release, nil)
	})
	stockOK(t, err)
	assertStockWorkers(t, results)
	for _, result := range results {
		if !result.Committed {
			t.Fatal("concurrent first receipt did not commit")
		}
	}
	b, err = db.New(f.pool).GetStockBalance(f.ctx, db.GetStockBalanceParams{WarehouseID: f.warehouse, VariantID: f.variant})
	stockOK(t, err)
	if b.OnHandPieces != 10 || b.Version != 2 {
		t.Fatal("concurrent initialization lost a receipt")
	}
}
func TestStockSQLPaginationAndReconciliation(t *testing.T) {
	f := stockDB(t)
	for range 3 {
		s := f.stage(t, "IN", 1)
		b, err := s.change()
		stockOK(t, err)
		s.finish(b)
		stockOK(t, s.tx.Commit(f.ctx))
	}
	q := db.New(f.pool)
	page, err := q.ListStockHistory(f.ctx, db.ListStockHistoryParams{WarehouseID: f.warehouse, VariantID: f.variant, UpperVersion: 3, PageSize: 2})
	stockOK(t, err)
	if len(page) != 2 || page[0].BalanceVersion != 3 || page[1].BalanceVersion != 2 {
		t.Fatal("history ordering")
	}
	s := f.stage(t, "IN", 1)
	b, err := s.change()
	stockOK(t, err)
	s.finish(b)
	stockOK(t, s.tx.Commit(f.ctx))
	next, err := q.ListStockHistory(f.ctx, db.ListStockHistoryParams{WarehouseID: f.warehouse, VariantID: f.variant, UpperVersion: 3, BeforeVersion: pgtype.Int8{Int64: 2, Valid: true}, PageSize: 2})
	stockOK(t, err)
	if len(next) != 1 || next[0].BalanceVersion != 1 {
		t.Fatal("fixed-window history pagination")
	}
	for range 2 {
		f.variant = stockID()
		s := f.stage(t, "IN", 2)
		b, err := s.change()
		stockOK(t, err)
		s.finish(b)
		stockOK(t, s.tx.Commit(f.ctx))
	}
	list, err := q.ListStockBalances(f.ctx, db.ListStockBalancesParams{AfterWarehouseID: pgtype.UUID{Valid: true}, AfterVariantID: pgtype.UUID{Valid: true}, WarehouseFilter: f.warehouse, PageSize: 2})
	stockOK(t, err)
	if len(list) != 2 {
		t.Fatal("balance page size")
	}
	last := list[1]
	rest, err := q.ListStockBalances(f.ctx, db.ListStockBalancesParams{AfterWarehouseID: last.WarehouseID, AfterVariantID: last.VariantID, WarehouseFilter: f.warehouse, PageSize: 2})
	stockOK(t, err)
	if len(rest) != 1 || bytes.Compare(last.VariantID.Bytes[:], rest[0].VariantID.Bytes[:]) >= 0 {
		t.Fatal("balance keyset pagination")
	}
	// One SQL statement supplies one consistent snapshot and widened SUM(bigint).
	var reconciled bool
	stockOK(t, f.pool.QueryRow(f.ctx, `SELECT bool_and(b.on_hand_pieces::numeric=l.total AND b.version=l.n AND l.n=l.max_version) FROM stock_balances b JOIN (SELECT warehouse_id,variant_id,SUM(CASE direction WHEN 'IN' THEN pieces::numeric ELSE -pieces::numeric END) total,count(*) n,max(balance_version) max_version FROM stock_movements GROUP BY warehouse_id,variant_id) l USING(warehouse_id,variant_id) WHERE b.warehouse_id=$1`, f.warehouse).Scan(&reconciled))
	if !reconciled {
		t.Fatal("ledger/balance reconciliation failed")
	}
}

func TestStockSQLMultiLineTransaction(t *testing.T) {
	f := stockDB(t)
	tx, err := f.begin(t)
	stockOK(t, err)
	q := db.New(tx)
	c := stock.Command{OperationID: uuid.Must(uuid.NewV7()), SourceEventID: uuid.New(), Producer: "synthetic-multiline", WarehouseID: uuid.UUID(f.warehouse.Bytes), Direction: stock.In, CommandVersion: 1, IdempotencyKey: uuid.New(), Lines: []stock.CommandLine{
		{LineNumber: 1, VariantID: uuid.UUID(f.variant.Bytes), InputCount: 2, ExpectedStockReferenceRevision: "synthetic-v1"},
		{LineNumber: 2, VariantID: uuid.UUID(stockID().Bytes), InputCount: 3, ExpectedStockReferenceRevision: "synthetic-v1"},
	}}
	fp, err := c.Fingerprint(stock.Principal{UserID: uuid.UUID(f.actor.Bytes)})
	stockOK(t, err)
	claim := db.ClaimStockIdempotencyParams{Producer: c.Producer, Key: pgtype.UUID{Bytes: c.IdempotencyKey, Valid: true}, OperationID: pgtype.UUID{Bytes: c.OperationID, Valid: true}, ActorUserID: f.actor, FingerprintVersion: 1, RequestHash: fp.Hash[:]}
	n, err := q.ClaimStockIdempotency(f.ctx, claim)
	stockOK(t, err)
	if n != 1 {
		t.Fatal("multiline claim")
	}
	_, err = q.LockStockWarehouse(f.ctx, f.warehouse)
	stockOK(t, err)
	_, err = q.InsertStockOperation(f.ctx, db.InsertStockOperationParams{ID: claim.OperationID, Producer: claim.Producer, SourceEventID: pgtype.UUID{Bytes: c.SourceEventID, Valid: true}, WarehouseID: f.warehouse, Direction: "IN", ActorUserID: f.actor, LineCount: 2, CommandVersion: 1, IdempotencyKey: claim.Key})
	stockOK(t, err)
	lines := slices.Clone(c.Lines)
	slices.SortFunc(lines, func(a, b stock.CommandLine) int { return bytes.Compare(a.VariantID[:], b.VariantID[:]) })
	for _, line := range lines {
		variant := pgtype.UUID{Bytes: line.VariantID, Valid: true}
		stockOK(t, q.InitializeStockBalance(f.ctx, db.InitializeStockBalanceParams{WarehouseID: f.warehouse, VariantID: variant, BaseUnitID: f.unit}))
		old, err := q.LockStockBalance(f.ctx, db.LockStockBalanceParams{WarehouseID: f.warehouse, VariantID: variant})
		stockOK(t, err)
		balance, err := q.ReceiveStockPieces(f.ctx, db.ReceiveStockPiecesParams{WarehouseID: f.warehouse, VariantID: variant, Pieces: int64(line.InputCount)})
		stockOK(t, err)
		local := *f
		local.variant = variant
		lineCommand := c
		lineCommand.Lines = []stock.CommandLine{line}
		s := stagedStock{f: &local, tx: tx, q: q, old: old, command: lineCommand, claim: claim}
		m := s.movement(balance)
		m.LineNumber = line.LineNumber
		_, err = q.InsertStockMovement(f.ctx, m)
		stockOK(t, err)
	}
	n, err = q.CompleteStockIdempotency(f.ctx, db.CompleteStockIdempotencyParams{Producer: claim.Producer, Key: claim.Key, OperationID: claim.OperationID})
	stockOK(t, err)
	if n != 1 {
		t.Fatal("multiline completion")
	}
	_, err = tx.Exec(f.ctx, "SET LOCAL TIME ZONE 'Asia/Kolkata'")
	stockOK(t, err)
	_, err = tx.Exec(f.ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	stockOK(t, err)
	stockOK(t, tx.Commit(f.ctx))
	if stockCounts(t, f) != "2:1:2:1" {
		t.Fatal("multiline complete transaction did not persist atomically")
	}
}

type stockWorkerReady struct {
	ID         int
	BackendPID uint32
}
type stockWorkerResult struct {
	ID                                                 int
	BackendPID                                         uint32
	Step                                               string
	Committed, Rejected, RollbackAttempted, RolledBack bool
	Err, CleanupErr                                    error
}
type stockWorkerFunc func(context.Context, int, chan<- stockWorkerReady, <-chan struct{}) stockWorkerResult

// No worker uses testing.T. One buffered terminal result per worker cannot block
// if its coordinator has already returned. Readiness and collection are bounded.
func runStockWorkers(parent context.Context, count int, work stockWorkerFunc) ([]stockWorkerResult, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	ready := make(chan stockWorkerReady, count)
	release := make(chan struct{})
	done := make(chan stockWorkerResult, count)
	for id := range count {
		go func() {
			result := stockWorkerResult{ID: id}
			defer func() {
				if p := recover(); p != nil {
					result.Err = fmt.Errorf("worker panic type %T", p)
				}
				result.ID = id
				done <- result
			}()
			result = work(ctx, id, ready, release)
		}()
	}
	results := make([]stockWorkerResult, 0, count)
	collectCanceled := func(cause error) ([]stockWorkerResult, error) {
		cancel()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for len(results) < count {
			select {
			case r := <-done:
				results = append(results, r)
			case <-timer.C:
				return results, errors.New("worker cleanup collection timed out")
			}
		}
		return results, cause
	}
	seen := make(map[int]bool, count)
	for len(seen) < count {
		select {
		case r := <-ready:
			if r.ID < 0 || r.ID >= count || seen[r.ID] {
				return collectCanceled(errors.New("invalid worker readiness"))
			}
			seen[r.ID] = true
		case r := <-done:
			results = append(results, r)
			return collectCanceled(errors.New("worker exited before barrier"))
		case <-ctx.Done():
			return collectCanceled(ctx.Err())
		}
	}
	close(release)
	for len(results) < count {
		select {
		case r := <-done:
			results = append(results, r)
			if r.Err != nil || r.CleanupErr != nil {
				return collectCanceled(errors.New("worker execution failed"))
			}
		case <-ctx.Done():
			return collectCanceled(ctx.Err())
		}
	}
	return results, nil
}

// This is test-only SQL composition, never a production posting repository.
// Rollback has its own context and completes before the terminal result is sent.
func stockSQLWorker(ctx context.Context, f *stockFixture, id int, direction string, pieces int64, ready chan<- stockWorkerReady, release <-chan struct{}, beforeBarrier func(context.Context, pgx.Tx) error) (result stockWorkerResult) {
	result.ID = id
	result.Step = "begin"
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		result.Err = err
		return
	}
	result.BackendPID = tx.Conn().PgConn().PID()
	defer func() {
		if !result.Committed {
			result.RollbackAttempted = true
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := tx.Rollback(cleanup)
			result.RolledBack = err == nil
			if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				result.CleanupErr = err
			}
		}
	}()
	local := *f
	local.ctx = ctx
	q := db.New(tx)
	op, err := uuid.NewV7()
	if err != nil {
		result.Err = err
		return
	}
	source, err := uuid.NewRandom()
	if err != nil {
		result.Err = err
		return
	}
	key, err := uuid.NewRandom()
	if err != nil {
		result.Err = err
		return
	}
	c := stock.Command{OperationID: op, SourceEventID: source, Producer: "synthetic-worker", WarehouseID: uuid.UUID(f.warehouse.Bytes), Direction: stock.Direction(direction), CommandVersion: 1, IdempotencyKey: key, Lines: []stock.CommandLine{{LineNumber: 1, VariantID: uuid.UUID(f.variant.Bytes), InputCount: stock.Quantity(pieces), ExpectedStockReferenceRevision: "synthetic-v1"}}}
	fp, err := c.Fingerprint(stock.Principal{UserID: uuid.UUID(f.actor.Bytes)})
	if err != nil {
		result.Err = err
		return
	}
	claim := db.ClaimStockIdempotencyParams{Producer: c.Producer, Key: pgtype.UUID{Bytes: key, Valid: true}, OperationID: pgtype.UUID{Bytes: op, Valid: true}, ActorUserID: f.actor, FingerprintVersion: 1, RequestHash: fp.Hash[:]}
	result.Step = "claim"
	n, err := q.ClaimStockIdempotency(ctx, claim)
	if err != nil {
		result.Err = err
		return
	}
	if n != 1 {
		result.Err = errors.New("worker claim count")
		return
	}
	result.Step = "warehouse"
	w, err := q.LockStockWarehouse(ctx, f.warehouse)
	if err != nil {
		result.Err = err
		return
	}
	if !w.IsActive {
		result.Err = stock.ErrReferenceInactive
		return
	}
	result.Step = "header"
	_, err = q.InsertStockOperation(ctx, db.InsertStockOperationParams{ID: claim.OperationID, Producer: claim.Producer, SourceEventID: pgtype.UUID{Bytes: source, Valid: true}, WarehouseID: f.warehouse, Direction: direction, ActorUserID: f.actor, LineCount: 1, CommandVersion: 1, IdempotencyKey: claim.Key})
	if err != nil {
		result.Err = err
		return
	}
	result.Step = "before barrier"
	if beforeBarrier != nil {
		if err := beforeBarrier(ctx, tx); err != nil {
			result.Err = err
			return
		}
	}
	select {
	case ready <- stockWorkerReady{ID: id, BackendPID: result.BackendPID}:
	case <-ctx.Done():
		result.Err = ctx.Err()
		return
	}
	result.Step = "barrier"
	select {
	case <-release:
	case <-ctx.Done():
		result.Err = ctx.Err()
		return
	}
	result.Step = "balance"
	err = q.InitializeStockBalance(ctx, db.InitializeStockBalanceParams{WarehouseID: f.warehouse, VariantID: f.variant, BaseUnitID: f.unit})
	if err != nil {
		result.Err = err
		return
	}
	old, err := q.LockStockBalance(ctx, db.LockStockBalanceParams{WarehouseID: f.warehouse, VariantID: f.variant})
	if err != nil {
		result.Err = err
		return
	}
	if old.BaseUnitID != f.unit {
		result.Err = stock.ErrReferenceChanged
		return
	}
	s := stagedStock{f: &local, tx: tx, q: q, command: c, old: old, claim: claim}
	result.Step = "conditional change"
	balance, err := s.change()
	if errors.Is(err, pgx.ErrNoRows) {
		result.Rejected = true
		return
	}
	if err != nil {
		result.Err = err
		return
	}
	result.Step = "movement"
	_, err = q.InsertStockMovement(ctx, s.movement(balance))
	if err != nil {
		result.Err = err
		return
	}
	result.Step = "complete"
	n, err = q.CompleteStockIdempotency(ctx, db.CompleteStockIdempotencyParams{Producer: claim.Producer, Key: claim.Key, OperationID: claim.OperationID})
	if err != nil {
		result.Err = err
		return
	}
	if n != 1 {
		result.Err = errors.New("worker completion count")
		return
	}
	result.Step = "commit"
	result.Err = tx.Commit(ctx)
	result.Committed = result.Err == nil
	return
}

func assertStockWorkers(t *testing.T, results []stockWorkerResult) {
	t.Helper()
	if len(results) != 2 {
		t.Fatal("expected two terminal worker results")
	}
	if results[0].BackendPID == 0 || results[0].BackendPID == results[1].BackendPID {
		t.Fatal("workers did not hold independent PostgreSQL transactions")
	}
	for _, r := range results {
		if r.Err != nil || r.CleanupErr != nil {
			t.Logf("worker=%d step=%s rollback_attempted=%t", r.ID, r.Step, r.RollbackAttempted)
		}
		stockOK(t, r.Err)
		stockOK(t, r.CleanupErr)
	}
}

func TestStockWorkerFailureBeforeBarrier(t *testing.T) {
	injected := errors.New("synthetic pre-barrier failure")
	results, err := runStockWorkers(context.Background(), 2, func(ctx context.Context, id int, ready chan<- stockWorkerReady, release <-chan struct{}) stockWorkerResult {
		if id == 0 {
			return stockWorkerResult{Err: injected}
		}
		select {
		case ready <- stockWorkerReady{ID: id}:
		case <-ctx.Done():
			return stockWorkerResult{Err: ctx.Err()}
		}
		select {
		case <-ctx.Done():
			return stockWorkerResult{Err: ctx.Err()}
		case <-release:
			return stockWorkerResult{Err: errors.New("barrier unexpectedly released")}
		}
	})
	if err == nil || len(results) != 2 {
		t.Fatal("pre-barrier failure did not collect both terminal results")
	}
	for _, r := range results {
		if r.ID == 0 && !errors.Is(r.Err, injected) {
			t.Fatal("lost injected worker failure")
		}
		if r.ID == 1 && !errors.Is(r.Err, context.Canceled) {
			t.Fatal("waiting worker did not observe cancellation")
		}
	}
}

func TestStockSQLWorkerFailureBeforeBarrier(t *testing.T) {
	f := stockDB(t)
	otherPrepared := make(chan struct{})
	injected := errors.New("synthetic SQL worker failure")
	results, err := runStockWorkers(f.ctx, 2, func(ctx context.Context, id int, ready chan<- stockWorkerReady, release <-chan struct{}) stockWorkerResult {
		return stockSQLWorker(ctx, f, id, "IN", 1, ready, release, func(ctx context.Context, _ pgx.Tx) error {
			if id == 1 {
				close(otherPrepared)
				return nil
			}
			select {
			case <-otherPrepared:
				return injected
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	})
	if err == nil || len(results) != 2 {
		t.Fatal("SQL pre-barrier failure did not terminate both workers")
	}
	if results[0].BackendPID == 0 || results[0].BackendPID == results[1].BackendPID {
		t.Fatal("failure fixture did not open two independent transactions")
	}
	for _, r := range results {
		stockOK(t, r.CleanupErr)
		if !r.RollbackAttempted || !r.RolledBack || r.Committed {
			t.Fatal("failed worker transaction not rolled back")
		}
		if r.ID == 0 && !errors.Is(r.Err, injected) {
			t.Fatal("lost synthetic failure")
		}
		if r.ID == 1 && !errors.Is(r.Err, context.Canceled) {
			t.Fatal("peer did not cancel")
		}
	}
	if stockCounts(t, f) != "0:0:0:0" {
		t.Fatal("worker failure left stock facts")
	}
	var claims int
	stockOK(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM stock_idempotency WHERE producer='synthetic-worker' AND operation_id NOT IN (SELECT id FROM stock_operations)").Scan(&claims))
	if claims != 0 {
		t.Fatal("worker failure left an orphan claim")
	}
}

type stockCostSample struct{ SetupMS, StatementsMS, CommitMS float64 }

// Timings are observations, not pass/fail latency limits. Fixture creation is
// outside the sample; setup covers IDs/command/hash, statements include Begin and
// completion, and Commit includes deferred checks plus the client round trip.
func measureStockOperation(t *testing.T, f *stockFixture, lineCount int) (sample stockCostSample, operation, variant pgtype.UUID) {
	t.Helper()
	setupStart := time.Now()
	c := stock.Command{OperationID: uuid.Must(uuid.NewV7()), SourceEventID: uuid.New(), Producer: "synthetic-cost", WarehouseID: uuid.UUID(f.warehouse.Bytes), Direction: stock.In, CommandVersion: 1, IdempotencyKey: uuid.New(), Lines: make([]stock.CommandLine, lineCount)}
	movementIDs := make(map[uuid.UUID]pgtype.UUID, lineCount)
	for i := range c.Lines {
		id := uuid.UUID(stockID().Bytes)
		c.Lines[i] = stock.CommandLine{LineNumber: int16(i + 1), VariantID: id, InputCount: stock.Quantity(i + 1), ExpectedStockReferenceRevision: "synthetic-v1"}
		movementIDs[id] = stockID()
	}
	fp, err := c.Fingerprint(stock.Principal{UserID: uuid.UUID(f.actor.Bytes)})
	stockOK(t, err)
	claim := db.ClaimStockIdempotencyParams{Producer: c.Producer, Key: pgtype.UUID{Bytes: c.IdempotencyKey, Valid: true}, OperationID: pgtype.UUID{Bytes: c.OperationID, Valid: true}, ActorUserID: f.actor, FingerprintVersion: 1, RequestHash: fp.Hash[:]}
	lines := slices.Clone(c.Lines)
	slices.SortFunc(lines, func(a, b stock.CommandLine) int { return bytes.Compare(a.VariantID[:], b.VariantID[:]) })
	checkedAt := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	sample.SetupMS = float64(time.Since(setupStart)) / float64(time.Millisecond)
	statementStart := time.Now()
	tx, err := f.begin(t)
	stockOK(t, err)
	q := db.New(tx)
	n, err := q.ClaimStockIdempotency(f.ctx, claim)
	stockOK(t, err)
	if n != 1 {
		t.Fatal("measurement claim count")
	}
	w, err := q.LockStockWarehouse(f.ctx, f.warehouse)
	stockOK(t, err)
	if !w.IsActive {
		t.Fatal("measurement warehouse inactive")
	}
	_, err = q.InsertStockOperation(f.ctx, db.InsertStockOperationParams{ID: claim.OperationID, Producer: claim.Producer, SourceEventID: pgtype.UUID{Bytes: c.SourceEventID, Valid: true}, WarehouseID: f.warehouse, Direction: "IN", ActorUserID: f.actor, LineCount: int16(lineCount), CommandVersion: 1, IdempotencyKey: claim.Key})
	stockOK(t, err)
	for _, line := range lines {
		v := pgtype.UUID{Bytes: line.VariantID, Valid: true}
		stockOK(t, q.InitializeStockBalance(f.ctx, db.InitializeStockBalanceParams{WarehouseID: f.warehouse, VariantID: v, BaseUnitID: f.unit}))
		old, err := q.LockStockBalance(f.ctx, db.LockStockBalanceParams{WarehouseID: f.warehouse, VariantID: v})
		stockOK(t, err)
		if old.BaseUnitID != f.unit {
			t.Fatal("measurement unit mismatch")
		}
		balance, err := q.ReceiveStockPieces(f.ctx, db.ReceiveStockPiecesParams{WarehouseID: f.warehouse, VariantID: v, Pieces: int64(line.InputCount)})
		stockOK(t, err)
		_, err = q.InsertStockMovement(f.ctx, db.InsertStockMovementParams{ID: movementIDs[line.VariantID], OperationID: claim.OperationID, LineNumber: line.LineNumber, WarehouseID: f.warehouse, VariantID: v, BalanceVersion: balance.Version, Direction: "IN", InputCount: int64(line.InputCount), PiecesPerInput: 1, Pieces: int64(line.InputCount), BeforePieces: old.OnHandPieces, AfterPieces: balance.OnHandPieces, ProductID: f.product, BaseUnitID: f.unit, ProductCode: "SYNTHETIC", ProductName: "Synthetic cost fixture", Sku: "SYNTHETIC-SKU", BaseUnitCode: "PCS", StockReferenceRevision: "synthetic-v1", ReferenceCheckedAt: checkedAt})
		stockOK(t, err)
	}
	n, err = q.CompleteStockIdempotency(f.ctx, db.CompleteStockIdempotencyParams{Producer: claim.Producer, Key: claim.Key, OperationID: claim.OperationID})
	stockOK(t, err)
	if n != 1 {
		t.Fatal("measurement completion count")
	}
	sample.StatementsMS = float64(time.Since(statementStart)) / float64(time.Millisecond)
	commitStart := time.Now()
	stockOK(t, tx.Commit(f.ctx))
	sample.CommitMS = float64(time.Since(commitStart)) / float64(time.Millisecond)
	return sample, claim.OperationID, pgtype.UUID{Bytes: lines[0].VariantID, Valid: true}
}

type stockPlanNode struct {
	NodeType   string          `json:"Node Type"`
	IndexName  string          `json:"Index Name"`
	ActualRows float64         `json:"Actual Rows"`
	Plans      []stockPlanNode `json:"Plans"`
}

func logStockPlan(t *testing.T, f *stockFixture, label, query string, args ...any) {
	t.Helper()
	var encoded []byte
	stockOK(t, f.pool.QueryRow(f.ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query, args...).Scan(&encoded))
	var plans []struct {
		Plan        stockPlanNode `json:"Plan"`
		PlanningMS  float64       `json:"Planning Time"`
		ExecutionMS float64       `json:"Execution Time"`
	}
	stockOK(t, json.Unmarshal(encoded, &plans))
	if len(plans) != 1 {
		t.Fatal("unexpected EXPLAIN plan count")
	}
	nodes := []string{}
	var visit func(stockPlanNode)
	visit = func(n stockPlanNode) {
		nodes = append(nodes, fmt.Sprintf("%s[index=%s rows=%.0f]", n.NodeType, n.IndexName, n.ActualRows))
		for _, child := range n.Plans {
			visit(child)
		}
	}
	visit(plans[0].Plan)
	t.Logf("cost_plan label=%s planning_ms=%.3f execution_ms=%.3f nodes=%s", label, plans[0].PlanningMS, plans[0].ExecutionMS, strings.Join(nodes, "; "))
}

func TestStockSQLDeferredCost(t *testing.T) {
	f := stockDB(t)
	var server, sharedBuffers, jit string
	stockOK(t, f.pool.QueryRow(f.ctx, "SELECT version(),current_setting('shared_buffers'),current_setting('jit')").Scan(&server, &sharedBuffers, &jit))
	t.Logf("cost_environment server=%s shared_buffers=%s jit=%s client=%s/%s %s logical_cpus=%d", server, sharedBuffers, jit, runtime.GOOS, runtime.GOARCH, runtime.Version(), runtime.NumCPU())
	for _, count := range []int{1, 10, 100} {
		// One warmup per size, then seven measured complete, valid transactions.
		measureStockOperation(t, f, count)
		setup, statements, commits, totals := []float64{}, []float64{}, []float64{}, []float64{}
		var operation, variant pgtype.UUID
		for range 7 {
			sample, op, v := measureStockOperation(t, f, count)
			operation, variant = op, v
			setup = append(setup, sample.SetupMS)
			statements = append(statements, sample.StatementsMS)
			commits = append(commits, sample.CommitMS)
			totals = append(totals, sample.SetupMS+sample.StatementsMS+sample.CommitMS)
		}
		t.Logf("cost_samples lines=%d repetitions=7 warmups=1 setup_ms=%v statements_ms=%v commit_ms=%v total_ms=%v", count, setup, statements, commits, totals)
		for name, values := range map[string][]float64{"setup": setup, "statements": statements, "commit": commits, "total": totals} {
			sorted := slices.Clone(values)
			slices.Sort(sorted)
			t.Logf("cost_summary lines=%d phase=%s min_ms=%.3f median_ms=%.3f max_ms=%.3f", count, name, sorted[0], sorted[len(sorted)/2], sorted[len(sorted)-1])
		}
		_, err := f.pool.Exec(f.ctx, "ANALYZE stock_movements,stock_balances,stock_operations,stock_idempotency")
		stockOK(t, err)
		label := fmt.Sprintf("%d_lines", count)
		logStockPlan(t, f, label+"_count", "SELECT count(*) FROM stock_movements WHERE operation_id=$1", operation)
		logStockPlan(t, f, label+"_result_lines", `SELECT jsonb_agg(jsonb_build_object('line_number',line_number,'variant_id',variant_id::text,'movement_id',id::text,'after_pieces',after_pieces::text,'balance_version',balance_version::text) ORDER BY line_number) FROM stock_movements WHERE operation_id=$1`, operation)
		logStockPlan(t, f, label+"_balance_tail", "SELECT id,after_pieces,base_unit_id,balance_version FROM stock_movements WHERE warehouse_id=$1 AND variant_id=$2 ORDER BY balance_version DESC LIMIT 1", f.warehouse, variant)
		logStockPlan(t, f, label+"_predecessor", `SELECT m.line_number,prev.after_pieces,b.base_unit_id FROM stock_movements m JOIN stock_balances b ON (b.warehouse_id,b.variant_id)=(m.warehouse_id,m.variant_id) LEFT JOIN stock_movements prev ON (prev.warehouse_id,prev.variant_id,prev.balance_version)=(m.warehouse_id,m.variant_id,m.balance_version-1) WHERE m.operation_id=$1`, operation)
	}
	t.Log("EXPLAIN covers representative inner lookups only, not all deferred-trigger execution; timings are local observations, not a production SLA")
}

package postgres

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
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

type postingAuth struct{ actor uuid.UUID }

func (a postingAuth) Authorize(ctx context.Context, p stock.Principal, c stock.Command) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.UserID != a.actor || c.Producer != "synthetic-posting" {
		return stock.ErrForbidden
	}
	return nil
}

type postingRefs func(context.Context, stock.Command) ([]stock.Observation, error)

func (f postingRefs) Resolve(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
	return f(ctx, c)
}

// Explicitly synthetic freshness policy; never wired into startup.
type syntheticFreshness struct{}

func (syntheticFreshness) Check(ctx context.Context, _ []stock.Observation) error { return ctx.Err() }

type postingFixture struct {
	ctx                      context.Context
	pool                     *pgxpool.Pool
	repo                     *StockRepository
	service                  *stock.PostingService
	p                        stock.Principal
	warehouse, unit, variant uuid.UUID
	refs                     postingRefs
}

func postingDB(t *testing.T, configure ...func(*pgxpool.Config)) *postingFixture {
	t.Helper()
	url, run := os.Getenv("STOCK_TEST_DATABASE_URL"), os.Getenv("STOCK_TEST_RUN_ID")
	if url == "" && run == "" {
		t.Skip("requires disposable stock harness")
	}
	port, e := strconv.Atoi(os.Getenv("STOCK_TEST_PORT"))
	if e != nil || port < 1 || port > 65535 || !strings.HasPrefix(run, "inventory-stock-test-") {
		t.Fatal("invalid posting fixture gate")
	}
	if _, e = uuid.Parse(strings.TrimPrefix(run, "inventory-stock-test-")); e != nil {
		t.Fatal("invalid posting fixture identity")
	}
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil || cfg.ConnConfig.Host != "127.0.0.1" || int(cfg.ConnConfig.Port) != port || cfg.ConnConfig.Database != "inventory_stock_test" || cfg.ConnConfig.User != "inventory_stock_fixture" {
		t.Fatal("refusing non-fixture posting destination")
	}
	cfg.MaxConns = 8
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	// Test-only driver hooks permit deterministic recovery transport faults while
	// retaining the same fixture host/database/role/marker gates.
	for _, hook := range configure {
		hook(cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	postingOK(t, e)
	t.Cleanup(pool.Close)
	var marker string
	postingOK(t, pool.QueryRow(ctx, "SELECT current_setting('stock.fixture_id',true)").Scan(&marker))
	if marker != run {
		t.Fatal("posting fixture ownership mismatch")
	}
	f := &postingFixture{ctx: ctx, pool: pool, p: stock.Principal{UserID: uuid.New()}, warehouse: uuid.New(), unit: uuid.New(), variant: uuid.New()}
	_, e = pool.Exec(ctx, "INSERT INTO warehouses(id,code,name) VALUES($1,$2,'Synthetic posting')", f.warehouse, "POST-"+strings.ToUpper(f.warehouse.String()))
	postingOK(t, e)
	f.repo, e = NewStockRepository(pool, DefaultStockRepositoryOptions())
	postingOK(t, e)
	f.refs = func(_ context.Context, c stock.Command) ([]stock.Observation, error) {
		out := make([]stock.Observation, 0, len(c.Lines))
		for _, l := range c.Lines {
			o := stock.Observation{ReferenceSnapshot: stock.ReferenceSnapshot{ProductID: f.variant, VariantID: l.VariantID, BaseUnitID: f.unit, QuantityKind: "PIECE", ProductActive: true, VariantActive: true, BaseUnitActive: true, StockReferenceRevision: l.ExpectedStockReferenceRevision, CheckedAt: time.Now().UTC()}, ProductCode: "SYN", ProductName: "Synthetic", SKU: "SYN-SKU", BaseUnitCode: "PC"}
			if l.PackID != uuid.Nil {
				o.Pack = &stock.PackSnapshot{ID: l.PackID, VariantID: l.VariantID, Revision: l.PackRevision, PiecesPerPack: 6, Active: true}
				label := "Synthetic pack"
				o.PackLabel = &label
			}
			out = append(out, o)
		}
		return out, nil
	}
	f.service = f.newService(t, f.refs)
	return f
}
func (f *postingFixture) newService(t *testing.T, refs postingRefs) *stock.PostingService {
	t.Helper()
	s, e := stock.NewPostingService(f.repo, postingAuth{f.p.UserID}, refs, syntheticFreshness{}, stock.DefaultPostingOptions())
	postingOK(t, e)
	return s
}
func postingOK(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		failure := logger.Classify("test", "posting_fixture", e)
		t.Fatalf("posting fixture failed: reason=%s sqlstate=%s type=%T", failure.Reason, failure.SQLState, e)
	}
}
func (f *postingFixture) command(d stock.Direction, n stock.Quantity) stock.Command {
	return stock.Command{OperationID: uuid.New(), SourceEventID: uuid.New(), Producer: "synthetic-posting", WarehouseID: f.warehouse, Direction: d, CommandVersion: 1, IdempotencyKey: uuid.New(), Lines: []stock.CommandLine{{LineNumber: 1, VariantID: f.variant, InputCount: n, ExpectedStockReferenceRevision: "synthetic-v1"}}}
}
func (f *postingFixture) post(t *testing.T, c stock.Command) stock.PostedResult {
	t.Helper()
	r, e := f.service.Post(f.ctx, f.p, c)
	postingOK(t, e)
	return r
}
func (f *postingFixture) balance(t *testing.T, id uuid.UUID) db.StockBalance {
	t.Helper()
	b, e := db.New(f.pool).GetStockBalance(f.ctx, db.GetStockBalanceParams{WarehouseID: dbID(f.warehouse), VariantID: dbID(id)})
	postingOK(t, e)
	return b
}
func (f *postingFixture) absent(t *testing.T, c stock.Command) {
	t.Helper()
	var n int
	postingOK(t, f.pool.QueryRow(f.ctx, "SELECT (SELECT count(*) FROM stock_operations WHERE id=$1)+(SELECT count(*) FROM stock_idempotency WHERE producer=$2 AND key=$3)+(SELECT count(*) FROM stock_movements WHERE operation_id=$1)", c.OperationID, c.Producer, c.IdempotencyKey).Scan(&n))
	if n != 0 {
		t.Fatal("partial posting facts persisted")
	}
}

func TestStockPostingReceiptIssuePackReplay(t *testing.T) {
	f := postingDB(t)
	receipt := f.command(stock.In, 100)
	first := f.post(t, receipt)
	if first.Replayed || first.Lines[0].AfterPieces != 100 {
		t.Fatal("receipt result")
	}
	issue := f.command(stock.Out, 10)
	original := f.post(t, issue)
	if original.Lines[0].AfterPieces != 90 {
		t.Fatal("issue result")
	}
	replay := f.post(t, issue)
	if !replay.Replayed || replay.Lines[0] != original.Lines[0] || !replay.PostedAt.Equal(original.PostedAt) {
		t.Fatal("unstable durable replay")
	}
	pack := f.command(stock.In, 2)
	pack.Lines[0].PackID = uuid.New()
	pack.Lines[0].PackRevision = "pack-v1"
	if r := f.post(t, pack); r.Lines[0].AfterPieces != 102 {
		t.Fatal("pack conversion")
	}
	if f.balance(t, f.variant).OnHandPieces != 102 {
		t.Fatal("duplicate effect")
	}
	broken := f.newService(t, func(context.Context, stock.Command) ([]stock.Observation, error) {
		return nil, stock.ErrReferenceInactive
	})
	r, e := broken.Post(f.ctx, f.p, issue)
	postingOK(t, e)
	if !r.Replayed || r.Lines[0].AfterPieces != 90 {
		t.Fatal("replay contacted Catalog or read current balance")
	}
	var n int
	postingOK(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM stock_balances b WHERE warehouse_id=$1 AND (b.on_hand_pieces<>(SELECT sum(CASE direction WHEN 'IN' THEN pieces::numeric ELSE -pieces::numeric END) FROM stock_movements m WHERE (m.warehouse_id,m.variant_id)=(b.warehouse_id,b.variant_id)) OR b.version<>(SELECT count(*) FROM stock_movements m WHERE (m.warehouse_id,m.variant_id)=(b.warehouse_id,b.variant_id)))`, f.warehouse).Scan(&n))
	if n != 0 {
		t.Fatal("ledger reconciliation mismatch")
	}
}

func TestStockPostingAtomicityAndReferences(t *testing.T) {
	f := postingDB(t)
	seed := f.command(stock.In, 20)
	other := uuid.New()
	seed.Lines = append(seed.Lines, stock.CommandLine{LineNumber: 2, VariantID: other, InputCount: 3, ExpectedStockReferenceRevision: "synthetic-v1"})
	f.post(t, seed)
	for _, mode := range []string{"insufficient later", "unit mismatch", "missing references", "inactive references", "wrong references", "warehouse missing", "warehouse inactive"} {
		t.Run(mode, func(t *testing.T) {
			c := f.command(stock.Out, 2)
			c.Lines = append(c.Lines, stock.CommandLine{LineNumber: 2, VariantID: other, InputCount: 2, ExpectedStockReferenceRevision: "synthetic-v1"})
			service := f.service
			want := stock.ErrReferenceChanged
			switch mode {
			case "insufficient later":
				c.Lines[1].InputCount = 4
				want = stock.ErrInsufficientStock
			case "unit mismatch", "missing references", "inactive references", "wrong references":
				service = f.newService(t, func(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
					o, e := f.refs(ctx, c)
					switch mode {
					case "unit mismatch":
						o[1].BaseUnitID = uuid.New()
					case "missing references":
						return o[:1], e
					case "inactive references":
						o[1].ProductActive = false
					case "wrong references":
						o[1].VariantID = uuid.New()
					}
					return o, e
				})
				if mode == "inactive references" {
					want = stock.ErrReferenceInactive
				}
			case "warehouse missing":
				c.WarehouseID = uuid.New()
				want = stock.ErrWarehouseNotFound
			case "warehouse inactive":
				_, e := f.pool.Exec(f.ctx, "UPDATE warehouses SET is_active=false WHERE id=$1", f.warehouse)
				postingOK(t, e)
				defer func() {
					_, e := f.pool.Exec(f.ctx, "UPDATE warehouses SET is_active=true WHERE id=$1", f.warehouse)
					postingOK(t, e)
				}()
				want = stock.ErrWarehouseInactive
			}
			_, e := service.Post(f.ctx, f.p, c)
			if !errors.Is(e, want) {
				t.Fatalf("got %v want %v", e, want)
			}
			f.absent(t, c)
			if f.balance(t, f.variant).OnHandPieces != 20 || f.balance(t, other).OnHandPieces != 3 {
				t.Fatal("multi-line rollback failed")
			}
		})
	}
	fresh := f.command(stock.Out, 1)
	fresh.Lines[0].VariantID = uuid.New()
	_, e := f.service.Post(f.ctx, f.p, fresh)
	if !errors.Is(e, stock.ErrInsufficientStock) {
		t.Fatal(e)
	}
	f.absent(t, fresh)
	_, e = db.New(f.pool).GetStockBalance(f.ctx, db.GetStockBalanceParams{WarehouseID: dbID(f.warehouse), VariantID: dbID(fresh.Lines[0].VariantID)})
	if !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("orphan balance")
	}
	maxc := f.command(stock.In, math.MaxInt64)
	maxc.Lines[0].VariantID = uuid.New()
	f.post(t, maxc)
	over := f.command(stock.In, 1)
	over.Lines[0].VariantID = maxc.Lines[0].VariantID
	_, e = f.service.Post(f.ctx, f.p, over)
	if !errors.Is(e, stock.ErrQuantityOverflow) {
		t.Fatal(e)
	}
	f.absent(t, over)
}

func TestStockPostingIdentityConflicts(t *testing.T) {
	f := postingDB(t)
	c := f.command(stock.In, 10)
	f.post(t, c)
	for _, mode := range []string{"payload", "operation", "source", "new key same operation", "owner", "command version"} {
		t.Run(mode, func(t *testing.T) {
			d := c
			d.Lines = slices.Clone(c.Lines)
			p := f.p
			want := stock.ErrOperationConflict
			switch mode {
			case "payload":
				d.Lines[0].InputCount++
				want = stock.ErrKeyConflict
			case "operation":
				d.OperationID = uuid.New()
				want = stock.ErrKeyConflict
			case "source":
				d.IdempotencyKey = uuid.New()
				d.OperationID = uuid.New()
			case "new key same operation":
				d.IdempotencyKey = uuid.New()
			case "owner":
				p.UserID = uuid.New()
				want = stock.ErrForbidden
			case "command version":
				d.CommandVersion = 2
				want = stock.ErrInvalidArgument
			}
			_, e := f.service.Post(f.ctx, p, d)
			if !errors.Is(e, want) {
				t.Fatalf("got %v want %v", e, want)
			}
		})
	}
	if f.balance(t, f.variant).OnHandPieces != 10 {
		t.Fatal("conflict changed stock")
	}
}

type postingOutcome struct {
	r stock.PostedResult
	e error
}

func postingConcurrent(t *testing.T, f *postingFixture, commands []stock.Command) []postingOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	defer cancel()
	ready := make(chan struct{}, len(commands))
	release := make(chan struct{})
	done := make(chan postingOutcome, len(commands))
	for _, c := range commands {
		go func(c stock.Command) {
			ready <- struct{}{}
			select {
			case <-ctx.Done():
				done <- postingOutcome{e: ctx.Err()}
				return
			case <-release:
			}
			r, e := f.service.Post(ctx, f.p, c)
			done <- postingOutcome{r, e}
		}(c)
	}
	for range commands {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("posting start barrier timed out")
		}
	}
	close(release)
	out := make([]postingOutcome, 0, len(commands))
	for range commands {
		select {
		case r := <-done:
			out = append(out, r)
		case <-ctx.Done():
			t.Fatal("posting collection timed out")
		}
	}
	return out
}

func TestStockPostingConcurrency(t *testing.T) {
	for _, mode := range []string{"same key", "deductions", "first receipts", "reversed lines"} {
		t.Run(mode, func(t *testing.T) {
			f := postingDB(t)
			a := f.command(stock.In, 10)
			b := a
			switch mode {
			case "deductions":
				f.post(t, a)
				a = f.command(stock.Out, 7)
				b = f.command(stock.Out, 7)
			case "first receipts":
				b = f.command(stock.In, 10)
			case "reversed lines":
				a.Lines = append(a.Lines, stock.CommandLine{LineNumber: 2, VariantID: uuid.New(), InputCount: 10, ExpectedStockReferenceRevision: "synthetic-v1"})
				b = f.command(stock.In, 10)
				b.Lines = slices.Clone(a.Lines)
				slices.Reverse(b.Lines)
			}
			out := postingConcurrent(t, f, []stock.Command{a, b})
			success, replays, insufficient := 0, 0, 0
			for _, o := range out {
				if o.e == nil {
					success++
					if o.r.Replayed {
						replays++
					}
				} else if errors.Is(o.e, stock.ErrInsufficientStock) {
					insufficient++
				} else {
					t.Fatal(o.e)
				}
			}
			want := int64(20)
			switch mode {
			case "same key":
				want = 10
				if success != 2 || replays != 1 {
					t.Fatal("same key concurrency")
				}
			case "deductions":
				want = 3
				if success != 1 || insufficient != 1 {
					t.Fatal("deduction concurrency")
				}
			default:
				if success != 2 {
					t.Fatal("receipt concurrency")
				}
			}
			if f.balance(t, f.variant).OnHandPieces != want {
				t.Fatal("concurrent effects incorrect")
			}
		})
	}
}

// Narrow transport/transaction fault seams. Actual SQL still runs on PostgreSQL;
// errors below are simulations, not real network fault injection.
type postingFaultTx struct {
	pgx.Tx
	mode string
	hits *atomic.Int32
}
type postingErrorRow struct{ err error }

func (r postingErrorRow) Scan(...any) error { return r.err }
func (tx postingFaultTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if (tx.mode == "claim" && strings.Contains(sql, "ClaimStockIdempotency")) || (tx.mode == "initialize" && strings.Contains(sql, "InitializeStockBalance")) || (tx.mode == "complete" && strings.Contains(sql, "CompleteStockIdempotency")) {
		return pgconn.CommandTag{}, errors.New("synthetic stage failure")
	}
	return tx.Tx.Exec(ctx, sql, args...)
}
func (tx postingFaultTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	for mode, query := range map[string]string{"header": "InsertStockOperation", "change": "ReceiveStockPieces", "movement": "InsertStockMovement"} {
		if tx.mode == mode && strings.Contains(sql, query) {
			return postingErrorRow{errors.New("synthetic stage failure")}
		}
	}
	if tx.mode == "second movement" && strings.Contains(sql, "InsertStockMovement") && tx.hits.Add(1) == 2 {
		return postingErrorRow{errors.New("synthetic second movement failure")}
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}
func (tx postingFaultTx) Commit(ctx context.Context) error {
	switch tx.mode {
	case "deferred":
		// Inject a ledger mismatch only after the repository decoded its result.
		// xmin confines this fixture fault to rows written by this transaction.
		if _, e := tx.Tx.Exec(ctx, "UPDATE stock_balances SET version=version+1 WHERE xmin::text=pg_current_xact_id()::text"); e != nil {
			return e
		}
	case "lost acknowledgement":
		if e := tx.Tx.Commit(ctx); e != nil {
			return e
		}
		return errors.New("synthetic acknowledgement lost")
	case "definite rollback":
		if e := tx.Tx.Rollback(ctx); e != nil {
			return e
		}
		return pgx.ErrTxCommitRollback
	case "unresolved":
		_ = tx.Tx.Rollback(ctx)
		return errors.New("synthetic transport outcome unknown")
	case "40001", "40P01":
		if tx.hits.Add(1) < 3 {
			_ = tx.Tx.Rollback(ctx)
			return &pgconn.PgError{Code: tx.mode}
		}
	}
	return tx.Tx.Commit(ctx)
}

func TestStockPostingFailureStagesAndCommit(t *testing.T) {
	for _, mode := range []string{"claim", "initialize", "header", "change", "movement", "second movement", "complete", "deferred", "lost acknowledgement", "definite rollback", "unresolved", "40001", "40P01"} {
		t.Run(mode, func(t *testing.T) {
			f := postingDB(t)
			var hits atomic.Int32
			var begins atomic.Int32
			f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
				begins.Add(1)
				tx, e := f.pool.BeginTx(ctx, o)
				if e != nil {
					return nil, e
				}
				return postingFaultTx{tx, mode, &hits}, nil
			}
			c := f.command(stock.In, 10)
			c.Lines = append(c.Lines, stock.CommandLine{LineNumber: 2, VariantID: uuid.New(), InputCount: 1, ExpectedStockReferenceRevision: "synthetic-v1"})
			r, e := f.service.Post(f.ctx, f.p, c)
			switch mode {
			case "lost acknowledgement":
				postingOK(t, e)
				if !r.Replayed || begins.Load() != 1 || f.balance(t, f.variant).OnHandPieces != 10 {
					t.Fatal("commit recovery")
				}
			case "40001", "40P01":
				postingOK(t, e)
				if begins.Load() != 3 || f.balance(t, f.variant).OnHandPieces != 10 {
					t.Fatal("retry boundary")
				}
			default:
				if e == nil {
					t.Fatal("injected failure accepted")
				}
				if mode == "unresolved" && !errors.Is(e, stock.ErrOutcomeUnknown) {
					t.Fatal("unknown outcome misclassified")
				}
				if mode == "deferred" && !errors.Is(e, stock.ErrInternal) {
					t.Fatal("deferred constraint misclassified")
				}
				if mode != "40001" && mode != "40P01" && begins.Load() != 1 {
					t.Fatal("unexpected effect retry")
				}
				f.absent(t, c)
				var n int
				postingOK(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM stock_balances WHERE warehouse_id=$1", f.warehouse).Scan(&n))
				if n != 0 {
					t.Fatal("partial balance persisted")
				}
			}
			if f.pool.Stat().AcquiredConns() != 0 {
				t.Fatal("connection leaked")
			}
		})
	}
}

func TestStockPostingLockTimeoutCancellation(t *testing.T) {
	f := postingDB(t)
	f.post(t, f.command(stock.In, 10))
	tx, e := f.pool.Begin(f.ctx)
	postingOK(t, e)
	defer f.repo.cleanup(tx)
	_, e = db.New(tx).LockStockBalance(f.ctx, db.LockStockBalanceParams{WarehouseID: dbID(f.warehouse), VariantID: dbID(f.variant)})
	postingOK(t, e)
	c := f.command(stock.Out, 1)
	_, e = f.service.Post(f.ctx, f.p, c)
	if !errors.Is(e, stock.ErrBusy) {
		t.Fatalf("lock timeout: %v", e)
	}
	f.absent(t, c)
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Millisecond)
	defer cancel()
	_, e = f.service.Post(ctx, f.p, c)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", e)
	}
	f.absent(t, c)
	f.repo.cleanup(tx)
	f.post(t, c)
	if f.balance(t, f.variant).OnHandPieces != 9 {
		t.Fatal("lock cleanup")
	}
}

func TestStockPostingReferenceFailureRaceReplay(t *testing.T) {
	f := postingDB(t)
	c := f.command(stock.In, 10)
	service := f.newService(t, func(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
		_, e := f.service.Post(ctx, f.p, c)
		if e != nil {
			return nil, e
		}
		return nil, stock.ErrReferenceInactive
	})
	r, e := service.Post(f.ctx, f.p, c)
	postingOK(t, e)
	if !r.Replayed || f.balance(t, f.variant).OnHandPieces != 10 {
		t.Fatal("redundant reference failure lost completed result")
	}
}

func TestStockPostingLockedStateClassification(t *testing.T) {
	for _, tc := range []struct {
		b    db.StockBalance
		d    stock.Direction
		p    stock.Quantity
		want error
	}{
		{db.StockBalance{Version: math.MaxInt64}, stock.In, 1, stock.ErrVersionExhausted},
		{db.StockBalance{Version: 1, OnHandPieces: math.MaxInt64}, stock.In, 1, stock.ErrQuantityOverflow},
		{db.StockBalance{Version: 1}, stock.Out, 1, stock.ErrInsufficientStock},
		{db.StockBalance{Version: 1, OnHandPieces: 1}, stock.Out, 1, nil},
	} {
		if e := validateStockChange(tc.b, tc.d, tc.p); !errors.Is(e, tc.want) {
			t.Fatal("locked state classification")
		}
	}
	if e := stockFailure(context.Background(), "change", pgx.ErrNoRows); !errors.Is(e, stock.ErrInternal) {
		t.Fatal("blanket no-row mapping")
	}
	for _, stage := range []string{"claim", "balance_lock"} {
		want := stock.ErrBusy
		if stage == "claim" {
			want = stock.ErrInProgress
		}
		if e := stockFailure(context.Background(), stage, &pgconn.PgError{Code: "55P03"}); !errors.Is(e, want) {
			t.Fatal("timeout mapping")
		}
	}
}

func TestStockPostingClaimWaitAndDurableVersions(t *testing.T) {
	f := postingDB(t)
	c := f.command(stock.In, 10)
	fp, e := c.Fingerprint(f.p)
	postingOK(t, e)
	tx, e := f.pool.Begin(f.ctx)
	postingOK(t, e)
	defer f.repo.cleanup(tx)
	_, e = db.New(tx).ClaimStockIdempotency(f.ctx, db.ClaimStockIdempotencyParams{Producer: c.Producer, Key: dbID(c.IdempotencyKey), FingerprintVersion: 1, RequestHash: fp.Hash[:], ActorUserID: dbID(f.p.UserID), OperationID: dbID(c.OperationID)})
	postingOK(t, e)
	_, e = f.service.Post(f.ctx, f.p, c)
	if !errors.Is(e, stock.ErrInProgress) {
		t.Fatalf("claim timeout: %v", e)
	}
	f.repo.cleanup(tx)
	f.absent(t, c)
	f.post(t, c)
	row, e := db.New(f.pool).GetStockIdempotency(f.ctx, db.GetStockIdempotencyParams{Producer: c.Producer, Key: dbID(c.IdempotencyKey)})
	postingOK(t, e)
	for _, mode := range []string{"owner", "fingerprint version", "result version", "operation", "hash"} {
		t.Run(mode, func(t *testing.T) {
			copy := row
			want := stock.ErrKeyConflict
			switch mode {
			case "owner":
				copy.ActorUserID = dbID(uuid.New())
				want = stock.ErrForbidden
			case "fingerprint version":
				copy.FingerprintVersion = 2
			case "result version":
				copy.ResultVersion.Int16 = 2
				want = stock.ErrStoredResult
			case "operation":
				copy.OperationID = dbID(uuid.New())
			case "hash":
				copy.RequestHash = make([]byte, 32)
			}
			_, e := stockReplay(copy, f.p, c, fp)
			if !errors.Is(e, want) {
				t.Fatalf("version/identity replay mismatch: %v", e)
			}
		})
	}
}

func TestStockPostingHundredLines(t *testing.T) {
	f := postingDB(t)
	// This is a functional maximum-line test, not a default-budget SLA. Compilation
	// contention can exceed the 3s operational default; cancellation is tested above.
	options := stock.DefaultPostingOptions()
	options.TotalTimeout = 15 * time.Second
	options.TransactionBudget = 10 * time.Second
	service, e := stock.NewPostingService(f.repo, postingAuth{f.p.UserID}, f.refs, syntheticFreshness{}, options)
	postingOK(t, e)
	c := f.command(stock.In, 1)
	for i := 2; i <= stock.MaxLines; i++ {
		c.Lines = append(c.Lines, stock.CommandLine{LineNumber: int16(i), VariantID: uuid.New(), InputCount: 1, ExpectedStockReferenceRevision: "synthetic-v1"})
	}
	slices.Reverse(c.Lines)
	start := time.Now()
	r, e := service.Post(f.ctx, f.p, c)
	postingOK(t, e)
	t.Logf("100-line internal post elapsed_ms=%.3f; local observation, not SLA", float64(time.Since(start).Microseconds())/1000)
	if len(r.Lines) != 100 || r.Lines[0].LineNumber != 1 || r.Lines[99].LineNumber != 100 {
		t.Fatal("semantic result line order")
	}
	for _, l := range r.Lines {
		if l.AfterPieces != 1 || l.BalanceVersion != 1 {
			t.Fatal("100-line quantity/version")
		}
	}
}

type postingLockTraceTx struct {
	pgx.Tx
	order *[]uuid.UUID
}

func (tx postingLockTraceTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "LockStockBalance") {
		*tx.order = append(*tx.order, uuid.UUID(args[1].(pgtype.UUID).Bytes))
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func TestStockPostingStableLockOrderAndR1(t *testing.T) {
	f := postingDB(t)
	var order []uuid.UUID
	f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
		tx, e := f.pool.BeginTx(ctx, o)
		if e != nil {
			return nil, e
		}
		return postingLockTraceTx{tx, &order}, nil
	}
	c := f.command(stock.In, 2)
	c.Lines = append(c.Lines, stock.CommandLine{LineNumber: 2, VariantID: uuid.New(), InputCount: 2, ExpectedStockReferenceRevision: "synthetic-v1"})
	slices.SortFunc(c.Lines, func(a, b stock.CommandLine) int { return -bytes.Compare(a.VariantID[:], b.VariantID[:]) })
	remoteActive := true
	service := f.newService(t, func(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
		o, e := f.refs(ctx, c)
		if !remoteActive {
			o[0].VariantActive = false
		}
		remoteActive = false
		return o, e
	})
	r, e := service.Post(f.ctx, f.p, c)
	postingOK(t, e)
	if len(order) != 2 || bytes.Compare(order[0][:], order[1][:]) >= 0 || r.Lines[0].LineNumber != 1 {
		t.Fatal("lock order or semantic lines")
	}
	// The synthetic remote state deactivated after observation; R1 permits the
	// already validated post, while the next new command must be rejected.
	next := f.command(stock.In, 1)
	_, e = service.Post(f.ctx, f.p, next)
	if !errors.Is(e, stock.ErrReferenceInactive) {
		t.Fatal("R1 check-time status semantics")
	}
	f.absent(t, next)
}

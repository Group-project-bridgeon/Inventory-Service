package postgres

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	db "github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres/sqlcdb"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recoveryValueKey struct{}

// These driver/transaction seams simulate acknowledgement/connection faults;
// the stock transaction, unique-key waits and query-lock timeout use real PG.
// No production connection factory, context policy or timeout is replaced.
type recoveryProbe struct {
	mode                                              string
	active, cleaned, badCleanup, badOrder, badContext atomic.Bool
	begins, queries, dials, connects                  atomic.Int32
	originalPID                                       atomic.Uint32
	mu                                                sync.Mutex
	connections                                       []*pgx.Conn
}

func (p *recoveryProbe) configure(cfg *pgxpool.Config) {
	cfg.ConnConfig.Tracer = p
	dial := cfg.ConnConfig.DialFunc
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		if p.active.Load() {
			p.dials.Add(1)
			switch p.mode {
			case "connection failure":
				return nil, errors.New("synthetic private fault detail")
			case "connection timeout":
				<-ctx.Done()
				return nil, ctx.Err()
			}
		}
		return dial(ctx, network, address)
	}
}

func (p *recoveryProbe) TraceConnectStart(ctx context.Context, _ pgx.TraceConnectStartData) context.Context {
	if p.active.Load() {
		if !p.cleaned.Load() {
			p.badOrder.Store(true)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 500*time.Millisecond || ctx.Value(recoveryValueKey{}) != "retained" {
			p.badContext.Store(true)
		}
	}
	return ctx
}
func (p *recoveryProbe) TraceConnectEnd(_ context.Context, data pgx.TraceConnectEndData) {
	if p.active.Load() && data.Err == nil && data.Conn != nil {
		p.connects.Add(1)
		if data.Conn.PgConn().PID() == p.originalPID.Load() {
			p.badOrder.Store(true)
		}
		p.mu.Lock()
		p.connections = append(p.connections, data.Conn)
		p.mu.Unlock()
	}
}
func (p *recoveryProbe) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if p.active.Load() && strings.Contains(data.SQL, "GetStockIdempotency") {
		p.queries.Add(1)
		if p.mode == "query failure" {
			_ = conn.Close(ctx)
		}
	}
	return ctx
}
func (*recoveryProbe) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type recoveryTx struct {
	pgx.Tx
	probe  *recoveryProbe
	commit func(context.Context, pgx.Tx) error
}

func (tx recoveryTx) Commit(ctx context.Context) error { return tx.commit(ctx, tx.Tx) }
func (tx recoveryTx) Rollback(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok || ctx.Err() != nil || time.Until(deadline) > 2*time.Second {
		tx.probe.badCleanup.Store(true)
	}
	err := tx.Tx.Rollback(ctx)
	tx.probe.cleaned.Store(true)
	return err
}
func (p *recoveryProbe) install(f *postingFixture, commit func(context.Context, pgx.Tx) error) {
	f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
		n := p.begins.Add(1)
		tx, err := f.pool.BeginTx(ctx, o)
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return tx, nil
		} // Concurrent retry uses ordinary real PG semantics.
		p.originalPID.Store(tx.Conn().PgConn().PID())
		return recoveryTx{tx, p, commit}, nil
	}
}
func (p *recoveryProbe) resources(t *testing.T, f *postingFixture) {
	t.Helper()
	if !p.cleaned.Load() || p.badCleanup.Load() || p.badOrder.Load() || p.badContext.Load() {
		t.Fatal("recovery cleanup/order/context contract violated")
	}
	if f.pool.Stat().AcquiredConns() != 0 {
		t.Fatal("recovery retained a pooled connection")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.connections {
		if !conn.IsClosed() {
			t.Fatal("fresh recovery connection leaked")
		}
	}
}

func recoveryService(t *testing.T, f *postingFixture, auth stock.ProducerAuthorizer, txBudget time.Duration) *stock.PostingService {
	t.Helper()
	options := stock.DefaultPostingOptions()
	// Functional fault tests have headroom for SQL setup and the intentional wait;
	// the real production defaults remain unchanged and are documented separately.
	options.TotalTimeout = 10 * time.Second
	options.TransactionBudget = txBudget
	s, err := stock.NewPostingService(f.repo, auth, f.refs, syntheticFreshness{}, options)
	postingOK(t, err)
	return s
}
func recoveryUnknown(t *testing.T, err, cause error) {
	t.Helper()
	if !errors.Is(err, stock.ErrOutcomeUnknown) || errors.Is(err, stock.ErrRetryTransaction) || strings.Contains(err.Error(), "private fault") {
		t.Fatal("uncertainty was masked, retried or exposed raw cause")
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatal("uncertainty lost caller cancellation/deadline cause")
	}
}
func recoveryEffect(t *testing.T, f *postingFixture, c stock.Command) stock.PostedResult {
	t.Helper()
	var operations, movements, keys int
	postingOK(t, f.pool.QueryRow(f.ctx, `SELECT
 (SELECT count(*) FROM stock_operations WHERE id=$1),
 (SELECT count(*) FROM stock_movements WHERE operation_id=$1),
 (SELECT count(*) FROM stock_idempotency WHERE producer=$2 AND key=$3 AND state='COMPLETED')`, c.OperationID, c.Producer, c.IdempotencyKey).Scan(&operations, &movements, &keys))
	b := f.balance(t, f.variant)
	if operations != 1 || movements != 1 || keys != 1 || b.OnHandPieces != 90 || b.Version != 2 {
		t.Fatal("expected exactly one issue effect from 100 to 90")
	}
	row, err := db.New(f.pool).GetStockIdempotency(f.ctx, db.GetStockIdempotencyParams{Producer: c.Producer, Key: dbID(c.IdempotencyKey)})
	postingOK(t, err)
	r, err := stock.DecodePostedResult(row.Result, row.ResultVersion.Int16, c)
	postingOK(t, err)
	t.Log("verified one operation, one movement, one completed key; balance=90 version=2")
	return r
}
func recoveryLaterReplay(t *testing.T, f *postingFixture, p *recoveryProbe, c stock.Command) {
	t.Helper()
	p.active.Store(false)
	durable := recoveryEffect(t, f, c)
	before := p.begins.Load()
	r, err := f.service.Post(f.ctx, f.p, c)
	postingOK(t, err)
	if !r.Replayed || r.OperationID != durable.OperationID || !r.PostedAt.Equal(durable.PostedAt) || len(r.Lines) != 1 || r.Lines[0] != durable.Lines[0] || p.begins.Load() != before {
		t.Fatal("later authorized replay changed result or began mutation")
	}
	recoveryEffect(t, f, c)
}

func TestStockPostingRecoveryTimeoutContract(t *testing.T) {
	for _, mode := range []string{"A live acknowledgement loss", "B transaction deadline", "D definite rollback", "E connection failure", "E connection timeout", "E query failure", "E query timeout", "E absent result"} {
		t.Run(mode, func(t *testing.T) {
			p := &recoveryProbe{mode: strings.TrimPrefix(mode, "E ")}
			f := postingDB(t, p.configure)
			f.post(t, f.command(stock.In, 100))
			c := f.command(stock.Out, 10)
			var blocker pgx.Tx
			t.Cleanup(func() {
				if blocker != nil {
					f.repo.cleanup(blocker)
				}
			})
			p.install(f, func(ctx context.Context, tx pgx.Tx) error {
				if mode == "D definite rollback" || mode == "E absent result" {
					if err := tx.Rollback(ctx); err != nil {
						return err
					}
					if mode == "D definite rollback" {
						return pgx.ErrTxCommitRollback
					}
				} else {
					if err := tx.Commit(ctx); err != nil {
						return err
					}
				}
				if mode == "B transaction deadline" {
					<-ctx.Done()
				}
				if mode == "E query timeout" {
					var err error
					blocker, err = f.pool.Begin(f.ctx)
					if err != nil {
						return err
					}
					if _, err = blocker.Exec(f.ctx, "LOCK TABLE stock_idempotency IN ACCESS EXCLUSIVE MODE"); err != nil {
						return err
					}
				}
				p.active.Store(true)
				return errors.New("synthetic private fault detail")
			})
			budget := 8 * time.Second
			if mode == "B transaction deadline" {
				budget = 2 * time.Second
			}
			s := recoveryService(t, f, postingAuth{f.p.UserID}, budget)
			ctx := context.WithValue(f.ctx, recoveryValueKey{}, "retained")
			r, err := s.Post(ctx, f.p, c)
			if blocker != nil {
				f.repo.cleanup(blocker)
				blocker = nil
			}
			if mode == "D definite rollback" {
				if err == nil || errors.Is(err, stock.ErrOutcomeUnknown) || !errors.Is(err, pgx.ErrTxCommitRollback) || p.connects.Load() != 0 || p.queries.Load() != 0 {
					t.Fatal("definite rollback misclassified or reconciled")
				}
				f.absent(t, c)
				if b := f.balance(t, f.variant); b.OnHandPieces != 100 || b.Version != 1 {
					t.Fatal("rollback changed stock")
				}
			} else if mode == "A live acknowledgement loss" {
				postingOK(t, err)
				if !r.Replayed || p.connects.Load() != 1 || p.queries.Load() != 1 {
					t.Fatal("live recovery did not use one fresh physical primary lookup")
				}
			} else {
				var cause error
				if mode == "B transaction deadline" {
					cause = context.DeadlineExceeded
				}
				recoveryUnknown(t, err, cause)
				if ctx.Err() != nil || r.OperationID != uuid.Nil || len(r.Lines) != 0 {
					t.Fatal("test request expired or uncertain result leaked")
				}
				if mode == "B transaction deadline" && (p.connects.Load() != 0 || p.queries.Load() != 0) {
					t.Fatal("expired transaction context performed recovery I/O")
				}
				if strings.HasPrefix(mode, "E ") && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
					t.Fatal("child recovery timeout replaced outcome classification")
				}
				if strings.HasPrefix(mode, "E connection") && p.dials.Load() < 1 {
					t.Fatal("connection fault seam not reached")
				}
				if (mode == "E query timeout" || mode == "E query failure") && p.queries.Load() != 1 {
					t.Fatal("query fault seam not reached")
				}
			}
			if p.begins.Load() != 1 {
				t.Fatal("automatic mutation retry after uncertain Commit")
			}
			p.resources(t, f)
			if mode == "E absent result" {
				p.active.Store(false)
				f.absent(t, c)
				if b := f.balance(t, f.variant); b.OnHandPieces != 100 || b.Version != 1 {
					t.Fatal("absent outcome changed stock")
				}
				f.repo.begin = f.pool.BeginTx
				f.post(t, c) // Same identities/key can safely claim after the real rollback.
				recoveryEffect(t, f, c)
			} else if mode != "D definite rollback" {
				recoveryLaterReplay(t, f, p, c)
			}
		})
	}
}

func recoveryWorker(t *testing.T, ctx context.Context, f *postingFixture, s *stock.PostingService, c stock.Command) func() postingOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	done := make(chan postingOutcome, 1)
	go func() { r, err := s.Post(ctx, f.p, c); done <- postingOutcome{r, err} }()
	collected := false
	t.Cleanup(func() {
		cancel()
		if !collected {
			cleanup, stop := context.WithTimeout(context.Background(), 6*time.Second)
			defer stop()
			select {
			case <-done:
				collected = true
			case <-cleanup.Done():
				t.Error("recovery worker did not terminate after cancellation")
			}
		}
	})
	return func() postingOutcome {
		// The caller may already be canceled. Collection, like cleanup, needs its
		// own finite bound so cancellation cannot race the worker's terminal send.
		collection, stop := context.WithTimeout(context.Background(), 6*time.Second)
		defer stop()
		select {
		case out := <-done:
			collected = true
			return out
		case <-collection.Done():
			t.Fatal("recovery worker collection deadline")
			return postingOutcome{}
		}
	}
}
func recoverySignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal("recovery barrier not reached")
	}
}

func TestStockPostingRecoveryCallerCancellation(t *testing.T) {
	p := &recoveryProbe{}
	f := postingDB(t, p.configure)
	f.post(t, f.command(stock.In, 100))
	c := f.command(stock.Out, 10)
	committed := make(chan struct{})
	p.install(f, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		close(committed)
		<-ctx.Done() // Explicit caller cancellation, not a coordination sleep.
		p.active.Store(true)
		return errors.New("synthetic private fault detail")
	})
	ctx, cancel := context.WithCancel(context.WithValue(f.ctx, recoveryValueKey{}, "retained"))
	defer cancel()
	s := recoveryService(t, f, postingAuth{f.p.UserID}, 8*time.Second)
	collect := recoveryWorker(t, ctx, f, s, c)
	recoverySignal(t, committed)
	cancel()
	out := collect()
	recoveryUnknown(t, out.e, context.Canceled)
	if out.r.OperationID != uuid.Nil || p.begins.Load() != 1 || p.connects.Load() != 0 || p.queries.Load() != 0 {
		t.Fatal("canceled uncertain Commit retried, disclosed result or recovered in background")
	}
	p.resources(t, f)
	recoveryLaterReplay(t, f, p, c)
}

func TestStockPostingRecoveryConcurrentUnresolvedRetry(t *testing.T) {
	p := &recoveryProbe{}
	f := postingDB(t, p.configure)
	f.post(t, f.command(stock.In, 100))
	c := f.command(stock.Out, 10)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	p.install(f, func(ctx context.Context, tx pgx.Tx) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		p.active.Store(true)
		return errors.New("synthetic private fault detail")
	})
	s := recoveryService(t, f, postingAuth{f.p.UserID}, 8*time.Second)
	ctx := context.WithValue(f.ctx, recoveryValueKey{}, "retained")
	collect := recoveryWorker(t, ctx, f, s, c)
	// Release first in test cleanup, then the registered worker collector can drain.
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	recoverySignal(t, entered)
	r, err := s.Post(ctx, f.p, c)
	if !errors.Is(err, stock.ErrInProgress) || r.OperationID != uuid.Nil {
		t.Fatal("unresolved same-key retry bypassed the bounded unique-key gate")
	}
	once.Do(func() { close(release) })
	out := collect()
	postingOK(t, out.e)
	if !out.r.Replayed || p.begins.Load() != 2 {
		t.Fatal("pending original did not recover or retry changed stock")
	}
	p.resources(t, f)
	recoveryLaterReplay(t, f, p, c)
}

type recoveryAuthorizer func(context.Context, stock.Principal, stock.Command) error

func (a recoveryAuthorizer) Authorize(ctx context.Context, p stock.Principal, c stock.Command) error {
	return a(ctx, p, c)
}

func TestStockPostingRecoveryReplayAuthorization(t *testing.T) {
	p := &recoveryProbe{}
	f := postingDB(t, p.configure)
	f.post(t, f.command(stock.In, 100))
	c := f.command(stock.Out, 10)
	var allowed atomic.Bool
	allowed.Store(true)
	auth := recoveryAuthorizer(func(ctx context.Context, principal stock.Principal, c stock.Command) error {
		if !allowed.Load() {
			return stock.ErrForbidden
		}
		return (postingAuth{f.p.UserID}).Authorize(ctx, principal, c)
	})
	p.install(f, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		allowed.Store(false) // Authority revoked before recovery would disclose replay.
		p.active.Store(true)
		return errors.New("synthetic private fault detail")
	})
	s := recoveryService(t, f, auth, 8*time.Second)
	ctx := context.WithValue(f.ctx, recoveryValueKey{}, "retained")
	r, err := s.Post(ctx, f.p, c)
	if !errors.Is(err, stock.ErrForbidden) || r.OperationID != uuid.Nil || p.connects.Load() != 1 || p.queries.Load() != 1 {
		t.Fatal("unauthorized recovered result was exposed")
	}
	p.resources(t, f)
	p.active.Store(false)
	r, err = s.Post(ctx, f.p, c)
	if !errors.Is(err, stock.ErrForbidden) || r.OperationID != uuid.Nil || p.begins.Load() != 1 {
		t.Fatal("later unauthorized retry exposed result or mutated")
	}
	allowed.Store(true)
	r, err = s.Post(ctx, f.p, c)
	postingOK(t, err)
	if !r.Replayed || p.begins.Load() != 1 {
		t.Fatal("authorized replay failed after grant restored")
	}
	recoveryEffect(t, f, c)
}

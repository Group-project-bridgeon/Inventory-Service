package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Explicit synthetic access/references for database tests, never production wiring.
type reservationTestAccess struct {
	id   uuid.UUID
	deny bool
}

func (a *reservationTestAccess) Owner() uuid.UUID { return a.id }
func (a *reservationTestAccess) AuthorizeReservation(ctx context.Context, _ string) error {
	if a.deny {
		return stock.ErrForbidden
	}
	return ctx.Err()
}

type reservationTestReferences struct {
	f              *postingFixture
	inactive, fail bool
	calls          atomic.Int32
}

func (r *reservationTestReferences) Freshness() stock.FreshnessPolicy { return syntheticFreshness{} }
func (r *reservationTestReferences) Resolve(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
	return r.f.refs.Resolve(ctx, c)
}
func (r *reservationTestReferences) Prepare(ctx context.Context, selected []stock.Selection) ([]stock.Observation, error) {
	r.calls.Add(1)
	if r.fail {
		return nil, stock.ErrReferenceUnavailable
	}
	var c stock.Command
	for _, l := range selected {
		c.Lines = append(c.Lines, stock.CommandLine{LineNumber: l.LineNumber, VariantID: l.VariantID, InputCount: l.InputCount, ExpectedStockReferenceRevision: "synthetic-current"})
	}
	observations, err := r.f.refs.Resolve(ctx, c)
	for i := range observations {
		observations[i].VariantActive = !r.inactive
	}
	return observations, err
}

type reservationFixture struct {
	f      *postingFixture
	repo   *ReservationRepository
	svc    *service.StockReservationService
	access *reservationTestAccess
	refs   *reservationTestReferences
}

func reservationDB(t *testing.T) *reservationFixture {
	if os.Getenv("RESERVATION_TEST_PHASE") != "verify" {
		t.Skip("requires disposable reservation harness")
	}
	f := adjustmentFixture(t)
	f.post(t, adjustmentCommand(f, stock.In, 100))
	repo, err := NewReservationRepository(f.repo)
	postingOK(t, err)
	refs := &reservationTestReferences{f: f}
	svc, err := service.NewStockReservationService(repo, f.repo, refs)
	postingOK(t, err)
	return &reservationFixture{f, repo, svc, &reservationTestAccess{id: uuid.New()}, refs}
}
func (r *reservationFixture) command(pieces stock.Quantity) stock.ReservationCommand {
	return stock.ReservationCommand{CommandID: uuid.New(), Key: uuid.New(), ReservationID: uuid.New(), OrderID: uuid.New(), WarehouseID: r.f.warehouse, Generation: 1, Action: stock.Reserve, Lines: []stock.ReservationLine{{OrderLineID: uuid.New(), VariantID: r.f.variant, Pieces: pieces}}}
}
func terminalReservation(c stock.ReservationCommand, action stock.ReservationAction, orderVersion stock.Quantity) stock.ReservationCommand {
	c.CommandID, c.Key = uuid.New(), uuid.New()
	c.Action = action
	c.Lines = nil
	c.ExpectedPreviousGeneration = 0
	c.ExpectedReservationVersion = 1
	c.ExpectedOrderVersion = orderVersion
	return c
}
func (r *reservationFixture) mutate(t *testing.T, c stock.ReservationCommand) stock.ReservationResult {
	t.Helper()
	result, err := r.svc.Mutate(r.f.ctx, r.access, c)
	postingOK(t, err)
	return result
}
func (r *reservationFixture) absent(t *testing.T, c stock.ReservationCommand) {
	t.Helper()
	var n int
	postingOK(t, r.f.pool.QueryRow(r.f.ctx, "SELECT count(*) FROM reservation_commands WHERE owner_machine_id=$1 AND key=$2", r.access.id, c.Key).Scan(&n))
	if n != 0 {
		t.Fatal("failed reservation command persisted")
	}
}

func TestReservationLifecycleReplayAndGenerations(t *testing.T) {
	r := reservationDB(t)
	c := r.command(70)
	first := r.mutate(t, c)
	if first.State != stock.ReservationActive || first.Replayed {
		t.Fatal("reserve result")
	}
	b := r.f.balance(t, r.f.variant)
	if b.OnHandPieces != 100 || b.ReservedPieces != 70 || b.Version != 1 || b.ReservationVersion != 1 {
		t.Fatal("reserve changed physical stock")
	}
	if !r.mutate(t, c).Replayed {
		t.Fatal("reserve replay")
	}
	changed := c
	changed.Lines = append([]stock.ReservationLine(nil), c.Lines...)
	changed.Lines[0].Pieces++
	_, err := r.svc.Mutate(r.f.ctx, r.access, changed)
	if !errors.Is(err, stock.ErrKeyConflict) {
		t.Fatal("changed intent key", err)
	}
	duplicate := c
	duplicate.CommandID, duplicate.Key = uuid.New(), uuid.New()
	_, err = r.svc.Mutate(r.f.ctx, r.access, duplicate)
	if !errors.Is(err, stock.ErrReservationExists) {
		t.Fatal("business duplicate", err)
	}
	r.absent(t, duplicate)
	release := terminalReservation(c, stock.Release, 1)
	r.mutate(t, release)
	if !r.mutate(t, release).Replayed {
		t.Fatal("release replay")
	}
	if !r.mutate(t, c).Replayed {
		t.Fatal("historical reserve result lost")
	}
	b = r.f.balance(t, r.f.variant)
	if b.ReservedPieces != 0 || b.OnHandPieces != 100 || b.Version != 1 {
		t.Fatal("release issued stock")
	}
	next := r.command(50)
	next.OrderID = c.OrderID
	next.Generation = 2
	next.ExpectedPreviousGeneration = 1
	next.ExpectedOrderVersion = 2
	r.mutate(t, next)
	consume := terminalReservation(next, stock.Consume, 3)
	result := r.mutate(t, consume)
	if result.State != stock.ReservationConsumed || result.OperationID == nil || *result.OperationID != consume.CommandID {
		t.Fatal("consume result")
	}
	if !r.mutate(t, consume).Replayed {
		t.Fatal("consume replay")
	}
	b = r.f.balance(t, r.f.variant)
	if b.OnHandPieces != 50 || b.ReservedPieces != 0 || b.Version != 2 || b.ReservationVersion != 4 {
		t.Fatal("consume effect/version")
	}
	var kind string
	var userNull bool
	var machine uuid.UUID
	postingOK(t, r.f.pool.QueryRow(r.f.ctx, "SELECT actor_kind,actor_user_id IS NULL,actor_machine_id FROM stock_operations WHERE id=$1", consume.CommandID).Scan(&kind, &userNull, &machine))
	if kind != "MACHINE" || !userNull || machine != r.access.id {
		t.Fatal("machine stored as human")
	}
	next.CommandID, next.Key, next.ReservationID = uuid.New(), uuid.New(), uuid.New()
	next.Generation = 3
	next.ExpectedPreviousGeneration = 2
	next.ExpectedOrderVersion = 4
	_, err = r.svc.Mutate(r.f.ctx, r.access, next)
	if !errors.Is(err, stock.ErrOrderConsumed) {
		t.Fatal("consumed order reopened", err)
	}
	_, err = r.f.repo.LoadOriginal(r.f.ctx, c.WarehouseID, consume.CommandID)
	if !errors.Is(err, stock.ErrNotReversible) {
		t.Fatal("consume entered adjustment correction producer")
	}
}
func TestReservationBeforeCancelAndAtomicFailure(t *testing.T) {
	r := reservationDB(t)
	c := r.command(10)
	early := terminalReservation(c, stock.Release, 1)
	_, err := r.svc.Mutate(r.f.ctx, r.access, early)
	if !errors.Is(err, stock.ErrReservationOrder) {
		t.Fatal("early release acknowledged", err)
	}
	r.absent(t, early)
	r.mutate(t, c)
	r.mutate(t, early)
	delayed := c
	delayed.CommandID, delayed.Key = uuid.New(), uuid.New()
	_, err = r.svc.Mutate(r.f.ctx, r.access, delayed)
	if !errors.Is(err, stock.ErrReservationExists) {
		t.Fatal("terminal generation recreated", err)
	}
	bad := r.command(20)
	bad.Lines = append(bad.Lines, stock.ReservationLine{OrderLineID: uuid.New(), VariantID: uuid.New(), Pieces: 1})
	_, err = r.svc.Mutate(r.f.ctx, r.access, bad)
	if !errors.Is(err, stock.ErrInsufficientAvailable) {
		t.Fatal("partial hold", err)
	}
	r.absent(t, bad)
	var n int
	postingOK(t, r.f.pool.QueryRow(r.f.ctx, "SELECT count(*) FROM stock_reservations WHERE id=$1", bad.ReservationID).Scan(&n))
	if n != 0 || r.f.balance(t, r.f.variant).ReservedPieces != 0 {
		t.Fatal("failed batch persisted")
	}
}
func TestReservationConcurrentNoOversellAndTerminalRace(t *testing.T) {
	for _, mode := range []string{"reserve", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			r := reservationDB(t)
			a := r.command(70)
			b := r.command(70)
			if mode == "terminal" {
				r.mutate(t, a)
				b = terminalReservation(a, stock.Release, 1)
				a = terminalReservation(a, stock.Consume, 1)
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make(chan error, 2)
			for _, c := range []stock.ReservationCommand{a, b} {
				wg.Add(1)
				go func(c stock.ReservationCommand) {
					defer wg.Done()
					<-start
					_, err := r.svc.Mutate(r.f.ctx, r.access, c)
					errs <- err
				}(c)
			}
			close(start)
			wg.Wait()
			close(errs)
			success := 0
			for err := range errs {
				if err == nil {
					success++
				} else if mode == "reserve" && !errors.Is(err, stock.ErrInsufficientAvailable) {
					t.Fatal("reserve race", err)
				} else if mode == "terminal" && !errors.Is(err, stock.ErrReservationTerminal) {
					t.Fatal("terminal race", err)
				}
			}
			stockBalance := r.f.balance(t, r.f.variant)
			if success != 1 || stockBalance.ReservedPieces > stockBalance.OnHandPieces {
				t.Fatal("race duplicated effects")
			}
			if mode == "reserve" && stockBalance.ReservedPieces != 70 {
				t.Fatal("oversell")
			}
			if mode == "terminal" && (stockBalance.ReservedPieces != 0 || (stockBalance.OnHandPieces != 30 && stockBalance.OnHandPieces != 100)) {
				t.Fatal("invalid terminal outcome")
			}
		})
	}
}
func TestReservationProtectsAdjustmentsReversalsAndOwnership(t *testing.T) {
	r := reservationDB(t)
	c := r.command(70)
	r.mutate(t, c)
	_, err := r.f.service.Post(r.f.ctx, r.f.p, adjustmentCommand(r.f, stock.Out, 40))
	if !errors.Is(err, stock.ErrInsufficientAvailable) {
		t.Fatal("adjustment spent held stock", err)
	}
	var originalID uuid.UUID
	postingOK(t, r.f.pool.QueryRow(r.f.ctx, "SELECT id FROM stock_operations WHERE warehouse_id=$1 ORDER BY posted_at LIMIT 1", r.f.warehouse).Scan(&originalID))
	original := adjustmentCommand(r.f, stock.In, 100)
	original.OperationID = originalID
	reverse := reversalCommand(t, r.f, original)
	_, err = r.f.service.Post(r.f.ctx, r.f.p, reverse)
	if !errors.Is(err, stock.ErrInsufficientAvailable) {
		t.Fatal("reversal spent hold", err)
	}
	_, err = NewWarehouseRepository(r.f.pool).SetActive(r.f.ctx, r.f.warehouse, false)
	if err == nil {
		t.Fatal("warehouse deactivated with hold")
	}
	stranger := &reservationTestAccess{id: uuid.New()}
	_, err = r.svc.Get(r.f.ctx, stranger, c.ReservationID)
	if !errors.Is(err, stock.ErrReservationNotFound) {
		t.Fatal("other owner read hold")
	}
	_, err = r.svc.Mutate(r.f.ctx, stranger, terminalReservation(c, stock.Consume, 1))
	if !errors.Is(err, stock.ErrReservationOrder) {
		t.Fatal("other owner consumed hold", err)
	}
	r.access.deny = true
	calls := r.refs.calls.Load()
	_, err = r.svc.Mutate(r.f.ctx, r.access, c)
	if !errors.Is(err, stock.ErrForbidden) || r.refs.calls.Load() != calls {
		t.Fatal("unauthorized replay")
	}
}
func TestReservationInactiveCleanupAndReplay(t *testing.T) {
	r := reservationDB(t)
	c := r.command(70)
	r.mutate(t, c)
	r.refs.inactive = true
	_, err := r.svc.Mutate(r.f.ctx, r.access, terminalReservation(c, stock.Consume, 1))
	if !errors.Is(err, stock.ErrReferenceInactive) {
		t.Fatal("inactive consume", err)
	}
	calls := r.refs.calls.Load()
	if !r.mutate(t, c).Replayed || r.refs.calls.Load() != calls {
		t.Fatal("inactive replay called Catalog")
	}
	r.refs.inactive = false
	_, err = r.f.pool.Exec(r.f.ctx, "UPDATE warehouses SET is_active=false WHERE id=$1", r.f.warehouse)
	postingOK(t, err)
	_, err = r.svc.Mutate(r.f.ctx, r.access, terminalReservation(c, stock.Consume, 1))
	if !errors.Is(err, stock.ErrWarehouseInactive) {
		t.Fatal("inactive warehouse consume", err)
	}
	r.refs.fail = true
	calls = r.refs.calls.Load()
	r.mutate(t, terminalReservation(c, stock.Release, 1))
	if r.refs.calls.Load() != calls || r.f.balance(t, r.f.variant).ReservedPieces != 0 {
		t.Fatal("cleanup depended on Catalog")
	}
}
func TestReservationCommitRecoveryAndRollback(t *testing.T) {
	for _, action := range []stock.ReservationAction{stock.Reserve, stock.Release, stock.Consume} {
		for _, mode := range []string{"lost acknowledgement", "unresolved"} {
			t.Run(string(action)+"/"+mode, func(t *testing.T) {
				r := reservationDB(t)
				c := r.command(40)
				if action != stock.Reserve {
					r.mutate(t, c)
					c = terminalReservation(c, action, 1)
				}
				var begins, hits atomic.Int32
				r.f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
					begins.Add(1)
					tx, err := r.f.pool.BeginTx(ctx, o)
					if err != nil {
						return nil, err
					}
					return postingFaultTx{tx, mode, &hits}, nil
				}
				result, err := r.svc.Mutate(r.f.ctx, r.access, c)
				if begins.Load() != 1 {
					t.Fatal("uncertain effect automatically retried")
				}
				if mode == "lost acknowledgement" {
					postingOK(t, err)
					if !result.Replayed {
						t.Fatal("durable recovery not replay")
					}
				} else {
					if !errors.Is(err, stock.ErrOutcomeUnknown) {
						t.Fatal("unknown outcome lost", err)
					}
					r.absent(t, c)
					r.f.repo.begin = r.f.pool.BeginTx
					r.mutate(t, c)
				}
				if !r.mutate(t, c).Replayed {
					t.Fatal("later reconciliation")
				}
			})
		}
	}
}
func TestReservationSQLBackstops(t *testing.T) {
	r := reservationDB(t)
	c := r.command(20)
	r.mutate(t, c)
	for _, sql := range []string{
		"UPDATE stock_balances SET reserved_pieces=reserved_pieces+1,reservation_version=reservation_version+1 WHERE warehouse_id=$1",
		"UPDATE stock_balances SET on_hand_pieces=reserved_pieces-1,version=version+1 WHERE warehouse_id=$1",
	} {
		_, err := r.f.pool.Exec(r.f.ctx, sql, r.f.warehouse)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" {
			t.Fatal("SQL quantity backstop")
		}
	}
	for _, sql := range []string{"UPDATE reservation_lines SET pieces=1 WHERE reservation_id=$1", "DELETE FROM reservation_transitions WHERE reservation_id=$1"} {
		_, err := r.f.pool.Exec(r.f.ctx, sql, c.ReservationID)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.ConstraintName != "stock_immutable" {
			t.Fatal("mutable reservation facts")
		}
	}
	_, err := r.f.pool.Exec(r.f.ctx, "UPDATE stock_reservations SET state='RELEASED',version=2,order_version=2 WHERE id=$1", c.ReservationID)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" {
		t.Fatal("projection changed without corresponding transition")
	}
}

func TestReservationConsumeAndAvailableOUTTogether(t *testing.T) {
	r := reservationDB(t)
	hold := r.command(70)
	r.mutate(t, hold)
	consume := terminalReservation(hold, stock.Consume, 1)
	out := adjustmentCommand(r.f, stock.Out, 30)
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() { <-start; _, err := r.svc.Mutate(r.f.ctx, r.access, consume); done <- err }()
	go func() { <-start; _, err := r.f.service.Post(r.f.ctx, r.f.p, out); done <- err }()
	close(start)
	postingOK(t, <-done)
	postingOK(t, <-done)
	b := r.f.balance(t, r.f.variant)
	if b.OnHandPieces != 0 || b.ReservedPieces != 0 || b.Version != 3 {
		t.Fatal("consume used another allocation or OUT overspent")
	}
}

func TestReservationConcurrentSameKeyActions(t *testing.T) {
	for _, action := range []stock.ReservationAction{stock.Reserve, stock.Release, stock.Consume} {
		t.Run(string(action), func(t *testing.T) {
			r := reservationDB(t)
			command := r.command(10)
			if action != stock.Reserve {
				r.mutate(t, command)
				command = terminalReservation(command, action, 1)
			}
			start := make(chan struct{})
			type outcome struct {
				result stock.ReservationResult
				err    error
			}
			done := make(chan outcome, 2)
			for i := 0; i < 2; i++ {
				go func() {
					<-start
					result, err := r.svc.Mutate(r.f.ctx, r.access, command)
					done <- outcome{result, err}
				}()
			}
			close(start)
			a, b := <-done, <-done
			postingOK(t, a.err)
			postingOK(t, b.err)
			if !a.result.Replayed && !b.result.Replayed {
				t.Fatal("duplicate concurrent action not replayed")
			}
			var n int
			postingOK(t, r.f.pool.QueryRow(r.f.ctx, "SELECT count(*) FROM reservation_transitions WHERE owner_machine_id=$1 AND command_id=$2", r.access.id, command.CommandID).Scan(&n))
			if n != 1 {
				t.Fatal("concurrent same key duplicated transition")
			}
			balance := r.f.balance(t, r.f.variant)
			wantOnHand, wantReserved := int64(100), int64(0)
			if action == stock.Reserve {
				wantReserved = 10
			}
			if action == stock.Consume {
				wantOnHand = 90
			}
			if balance.OnHandPieces != wantOnHand || balance.ReservedPieces != wantReserved {
				t.Fatal("concurrent duplicate quantity effects")
			}
		})
	}
}
func TestReservationConsumeMultilineRollback(t *testing.T) {
	r := reservationDB(t)
	other := uuid.New()
	receipt := adjustmentCommand(r.f, stock.In, 10)
	receipt.Lines[0].VariantID = other
	r.f.post(t, receipt)
	hold := r.command(20)
	hold.Lines = append(hold.Lines, stock.ReservationLine{OrderLineID: uuid.New(), VariantID: other, Pieces: 5})
	r.mutate(t, hold)
	var hits atomic.Int32
	r.f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
		tx, err := r.f.pool.BeginTx(ctx, o)
		if err != nil {
			return nil, err
		}
		return postingFaultTx{tx, "second movement", &hits}, nil
	}
	consume := terminalReservation(hold, stock.Consume, 1)
	_, err := r.svc.Mutate(r.f.ctx, r.access, consume)
	if err == nil {
		t.Fatal("injected second movement failure passed")
	}
	r.absent(t, consume)
	a, b := r.f.balance(t, r.f.variant), r.f.balance(t, other)
	if a.OnHandPieces != 100 || a.ReservedPieces != 20 || b.OnHandPieces != 10 || b.ReservedPieces != 5 {
		t.Fatal("partial consume release or issue persisted")
	}
	snapshot, err := r.repo.Get(r.f.ctx, r.access.id, hold.ReservationID)
	postingOK(t, err)
	if snapshot.State != stock.ReservationActive {
		t.Fatal("failed consume made terminal hold")
	}
}

type reservationCancelAckTx struct {
	pgx.Tx
	cancel context.CancelFunc
}

func (t reservationCancelAckTx) Commit(ctx context.Context) error {
	if err := t.Tx.Commit(ctx); err != nil {
		return err
	}
	t.cancel()
	return errors.New("synthetic lost acknowledgement after cancellation")
}
func TestReservationCanceledRecoveryStillUnknown(t *testing.T) {
	for _, action := range []stock.ReservationAction{stock.Reserve, stock.Release, stock.Consume} {
		t.Run(string(action), func(t *testing.T) {
			r := reservationDB(t)
			c := r.command(10)
			if action != stock.Reserve {
				r.mutate(t, c)
				c = terminalReservation(c, action, 1)
			}
			ctx, cancel := context.WithCancel(r.f.ctx)
			defer cancel()
			var begins atomic.Int32
			r.f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
				begins.Add(1)
				tx, err := r.f.pool.BeginTx(ctx, o)
				if err != nil {
					return nil, err
				}
				return reservationCancelAckTx{tx, cancel}, nil
			}
			_, err := r.svc.Mutate(ctx, r.access, c)
			if !errors.Is(err, stock.ErrOutcomeUnknown) || begins.Load() != 1 {
				t.Fatal("canceled recovery classified rollback or retried", err)
			}
			r.f.repo.begin = r.f.pool.BeginTx
			if !r.mutate(t, c).Replayed {
				t.Fatal("later same-key failed durable reconciliation")
			}
		})
	}
}

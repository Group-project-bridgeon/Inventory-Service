package postgres

import (
	"context"
	"errors"
	f "github.com/Group-project-bridgeon/Inventory-Service/internal/fulfillment"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"sync/atomic"
	"testing"
)

type fulfillmentAccessTest struct {
	actor       f.Actor
	permissions map[string]bool
}

func (a fulfillmentAccessTest) Actor() f.Actor { return a.actor }
func (a fulfillmentAccessTest) Authorize(ctx context.Context, p string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if !a.permissions[p] {
		return stock.ErrForbidden
	}
	return nil
}

type fulfillmentFixture struct {
	r            *reservationFixture
	repo         *FulfillmentRepository
	svc          *service.FulfillmentService
	sales, human fulfillmentAccessTest
	hold         stock.ReservationCommand
}

func fulfillmentDB(t *testing.T) *fulfillmentFixture {
	r := reservationDB(t)
	repo, e := NewFulfillmentRepository(r.f.repo)
	postingOK(t, e)
	svc, e := service.NewFulfillmentService(repo, r.refs)
	postingOK(t, e)
	hold := r.command(70)
	r.mutate(t, hold)
	return &fulfillmentFixture{r, repo, svc, fulfillmentAccessTest{f.Actor{Kind: "MACHINE", ID: r.access.id}, map[string]bool{f.CreatePermission: true, f.CancelPermission: true, f.ReadPermission: true}}, fulfillmentAccessTest{f.Actor{Kind: "HUMAN", ID: uuid.New()}, map[string]bool{f.DispatchPermission: true, f.DeliveryPermission: true, f.ReadPermission: true}}, hold}
}
func (x *fulfillmentFixture) draft() f.Command {
	return f.Command{CommandID: uuid.New(), Key: uuid.New(), ID: uuid.New(), ReservationID: x.hold.ReservationID, OrderID: x.hold.OrderID, WarehouseID: x.hold.WarehouseID, Generation: 1, ExpectedReservationVersion: 1, ExpectedOrderVersion: 1, Action: f.Create, ExternalReference: "FIXTURE"}
}
func (x *fulfillmentFixture) mutate(t *testing.T, a fulfillmentAccessTest, c f.Command) f.Result {
	t.Helper()
	r, e := x.svc.Mutate(x.r.f.ctx, a, c)
	postingOK(t, e)
	return r
}
func dispatchCommand(d f.Command) f.Command {
	return f.Command{CommandID: uuid.New(), Key: uuid.New(), ID: d.ID, WarehouseID: d.WarehouseID, ExpectedVersion: 1, ExpectedReservationVersion: 1, ExpectedOrderVersion: 1, Action: f.Dispatch}
}
func metadataCommand(d f.Command, action f.Action, version stock.Quantity) f.Command {
	return f.Command{CommandID: uuid.New(), Key: uuid.New(), ID: d.ID, WarehouseID: d.WarehouseID, ExpectedVersion: version, Action: action}
}
func (x *fulfillmentFixture) physical(t *testing.T) string {
	t.Helper()
	var s string
	postingOK(t, x.r.f.pool.QueryRow(x.r.f.ctx, `SELECT concat_ws(':',b.on_hand_pieces,b.reserved_pieces,b.version,b.reservation_version,(SELECT count(*) FROM stock_movements WHERE warehouse_id=$1),(SELECT count(*) FROM stock_operations WHERE warehouse_id=$1)) FROM stock_balances b WHERE warehouse_id=$1 AND variant_id=$2`, x.hold.WarehouseID, x.r.f.variant).Scan(&s))
	return s
}
func TestFulfillmentLifecycleAndExactActor(t *testing.T) {
	x := fulfillmentDB(t)
	d := x.draft()
	before := x.physical(t)
	created := x.mutate(t, x.sales, d)
	if created.State != "DRAFT" || x.physical(t) != before {
		t.Fatal("draft changed stock")
	}
	if !x.mutate(t, x.sales, d).Replayed {
		t.Fatal("create replay")
	}
	raw := terminalReservation(x.hold, stock.Consume, 1)
	_, e := x.r.svc.Mutate(x.r.f.ctx, x.r.access, raw)
	if !errors.Is(e, f.ErrClaimed) {
		t.Fatal("standalone consume bypassed live draft", e)
	}
	dispatch := dispatchCommand(d)
	out := x.mutate(t, x.human, dispatch)
	if out.State != "DISPATCHED" || out.OperationID == nil || x.physical(t) != "30:0:2:2:2:2" {
		t.Fatal("wrong dispatch effect")
	}
	after := x.physical(t)
	if !x.mutate(t, x.human, dispatch).Replayed || after != x.physical(t) {
		t.Fatal("dispatch replay effect")
	}
	var kind string
	var actor uuid.UUID
	var machineNull bool
	postingOK(t, x.r.f.pool.QueryRow(x.r.f.ctx, "SELECT actor_kind,actor_user_id,actor_machine_id IS NULL FROM stock_operations WHERE id=$1", dispatch.CommandID).Scan(&kind, &actor, &machineNull))
	if kind != "HUMAN" || actor != x.human.actor.ID || !machineNull {
		t.Fatal("dispatch impersonated owner")
	}
	failed := metadataCommand(d, f.Delivery, 2)
	failed.Outcome = "FAILED"
	failed.Note = "Fixture unsuccessful delivery"
	r := x.mutate(t, x.human, failed)
	if r.State != "DISPATCHED" || r.Version != 3 || x.physical(t) != after {
		t.Fatal("failed outcome restocked")
	}
	x.r.refs.fail = true
	success := metadataCommand(d, f.Delivery, 3)
	success.Outcome = "DELIVERED"
	r = x.mutate(t, x.human, success)
	if r.State != "DELIVERED" || r.Version != 4 || x.physical(t) != after {
		t.Fatal("delivery affected stock or depended on Catalog")
	}
	if r = x.mutate(t, x.human, failed); !r.Replayed || r.State != "DISPATCHED" || r.Version != 3 {
		t.Fatal("historical outcome replay lost")
	}
	changed := failed
	changed.Note = "changed"
	if _, e = x.svc.Mutate(x.r.f.ctx, x.human, changed); !errors.Is(e, stock.ErrKeyConflict) {
		t.Fatal("changed evidence key conflict", e)
	}
	page, e := x.svc.Audit(x.r.f.ctx, x.sales, d.ID, service.PageInput{Limit: 2})
	postingOK(t, e)
	if len(page.Items) != 2 || page.Next == nil {
		t.Fatal("bounded audit")
	}
	next, e := x.svc.Audit(x.r.f.ctx, x.sales, d.ID, service.PageInput{Limit: 2, Cursor: *page.Next})
	postingOK(t, e)
	if len(next.Items) != 2 || next.Next != nil || page.Items[1].ID == next.Items[0].ID {
		t.Fatal("audit keyset")
	}
	t.Log("PASS: draft no stock; human-confirmed consume 70 once; on-hand30/reserved0; FAILED then DELIVERED no physical effect; actual human actor and historical replay")
}
func TestFulfillmentCancelReplacementAndOwnership(t *testing.T) {
	x := fulfillmentDB(t)
	d := x.draft()
	x.mutate(t, x.sales, d)
	before := x.physical(t)
	other := x.sales
	other.actor.ID = uuid.New()
	if _, e := x.svc.Get(x.r.f.ctx, other, d.ID); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("owner disclosure", e)
	}
	cancel := metadataCommand(d, f.Cancel, 1)
	if _, e := x.svc.Mutate(x.r.f.ctx, other, cancel); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("owner cancellation")
	}
	denied := x.human
	denied.permissions = map[string]bool{f.ReadPermission: true}
	if _, e := x.svc.Mutate(x.r.f.ctx, denied, dispatchCommand(d)); !errors.Is(e, stock.ErrForbidden) {
		t.Fatal("implicit permission")
	}
	_, e := x.r.f.pool.Exec(x.r.f.ctx, "UPDATE warehouses SET is_active=false WHERE id=$1", d.WarehouseID)
	postingOK(t, e)
	x.r.refs.fail = true
	x.mutate(t, x.sales, cancel)
	if before != x.physical(t) {
		t.Fatal("draft cancel released reservation")
	}
	_, e = x.r.f.pool.Exec(x.r.f.ctx, "UPDATE warehouses SET is_active=true WHERE id=$1", d.WarehouseID)
	postingOK(t, e)
	replacement := x.draft()
	x.mutate(t, x.sales, replacement)
	x.mutate(t, x.sales, metadataCommand(replacement, f.Cancel, 1))
	x.r.mutate(t, terminalReservation(x.hold, stock.Release, 1))
	if x.r.f.balance(t, x.r.f.variant).OnHandPieces != 100 {
		t.Fatal("explicit release issued stock")
	}
}
func TestFulfillmentPriorConsumeAndConcurrentDraft(t *testing.T) {
	x := fulfillmentDB(t)
	x.r.mutate(t, terminalReservation(x.hold, stock.Consume, 1))
	if _, e := x.svc.Mutate(x.r.f.ctx, x.sales, x.draft()); !errors.Is(e, f.ErrState) {
		t.Fatal("historical consume attached", e)
	}
	y := fulfillmentDB(t)
	a, b := y.draft(), y.draft()
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, c := range []f.Command{a, b} {
		go func(c f.Command) { <-start; _, e := y.svc.Mutate(y.r.f.ctx, y.sales, c); done <- e }(c)
	}
	close(start)
	e1, e2 := <-done, <-done
	if (e1 == nil) == (e2 == nil) {
		t.Fatal("multiple live drafts", e1, e2)
	}
}
func TestFulfillmentDispatchRaces(t *testing.T) {
	for _, mode := range []string{"same-key", "different-key", "release", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			x := fulfillmentDB(t)
			d := x.draft()
			x.mutate(t, x.sales, d)
			dispatch := dispatchCommand(d)
			start := make(chan struct{})
			done := make(chan error, 2)
			go func() { <-start; _, e := x.svc.Mutate(x.r.f.ctx, x.human, dispatch); done <- e }()
			go func() {
				<-start
				var e error
				switch mode {
				case "release":
					_, e = x.r.svc.Mutate(x.r.f.ctx, x.r.access, terminalReservation(x.hold, stock.Release, 1))
				case "cancel":
					_, e = x.svc.Mutate(x.r.f.ctx, x.sales, metadataCommand(d, f.Cancel, 1))
				default:
					other := dispatch
					if mode == "different-key" {
						other.CommandID, other.Key = uuid.New(), uuid.New()
					}
					_, e = x.svc.Mutate(x.r.f.ctx, x.human, other)
				}
				done <- e
			}()
			close(start)
			e1, e2 := <-done, <-done
			if mode == "same-key" {
				postingOK(t, e1)
				postingOK(t, e2)
			} else if (e1 == nil) == (e2 == nil) {
				t.Fatal("race must have one valid winner", e1, e2)
			}
			b := x.r.f.balance(t, x.r.f.variant)
			if b.OnHandPieces != 30 && b.OnHandPieces != 100 {
				t.Fatal("double issue")
			}
			var n int
			postingOK(t, x.r.f.pool.QueryRow(x.r.f.ctx, "SELECT count(*) FROM stock_operations WHERE warehouse_id=$1 AND producer=$2", d.WarehouseID, stock.DispatchProducer).Scan(&n))
			if n > 1 {
				t.Fatal("duplicate dispatch operation")
			}
		})
	}
}

type fulfillmentFaultTx struct {
	pgx.Tx
	mode   string
	cancel context.CancelFunc
}

func (tx fulfillmentFaultTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx.mode == "audit" && strings.Contains(sql, "INSERT INTO fulfillment_transitions") {
		return pgconn.CommandTag{}, &pgconn.PgError{Code: "23514", ConstraintName: "synthetic_fulfillment_fault"}
	}
	return tx.Tx.Exec(ctx, sql, args...)
}
func (tx fulfillmentFaultTx) Commit(ctx context.Context) error {
	switch tx.mode {
	case "unresolved":
		return errors.New("synthetic unconfirmed commit")
	case "ack", "cancel":
		if e := tx.Tx.Commit(ctx); e != nil {
			return e
		}
		if tx.cancel != nil {
			tx.cancel()
		}
		return errors.New("synthetic lost acknowledgement")
	}
	return tx.Tx.Commit(ctx)
}
func TestFulfillmentAtomicFaultAndRecovery(t *testing.T) {
	for _, action := range []f.Action{f.Create, f.Dispatch, f.Delivery, f.Cancel} {
		for _, mode := range []string{"audit", "ack", "unresolved", "cancel"} {
			t.Run(string(action)+"/"+mode, func(t *testing.T) {
				x := fulfillmentDB(t)
				d := x.draft()
				command := d
				actor := x.sales
				if action != f.Create {
					x.mutate(t, x.sales, d)
					command = metadataCommand(d, action, 1)
					if action == f.Dispatch {
						command = dispatchCommand(d)
						actor = x.human
					}
					if action == f.Delivery {
						x.mutate(t, x.human, dispatchCommand(d))
						command = metadataCommand(d, f.Delivery, 2)
						command.Outcome = "FAILED"
						actor = x.human
					}
				}
				before := x.physical(t)
				ctx, cancel := context.WithCancel(x.r.f.ctx)
				defer cancel()
				var calls atomic.Int32
				x.r.f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
					calls.Add(1)
					tx, e := x.r.f.pool.BeginTx(ctx, o)
					if e != nil {
						return nil, e
					}
					var stop context.CancelFunc
					if mode == "cancel" {
						stop = cancel
					}
					return fulfillmentFaultTx{tx, mode, stop}, nil
				}
				out, e := x.svc.Mutate(ctx, actor, command)
				if calls.Load() != 1 {
					t.Fatal("fault caused automatic mutation retry")
				}
				if mode == "ack" {
					postingOK(t, e)
					if !out.Replayed {
						t.Fatal("primary recovery not durable replay")
					}
				} else if mode == "cancel" || mode == "unresolved" {
					if !errors.Is(e, stock.ErrOutcomeUnknown) {
						t.Fatal("unknown outcome classification lost", e)
					}
				} else if e == nil {
					t.Fatal("partial SQL fault passed")
				}
				x.r.f.repo.begin = x.r.f.pool.BeginTx
				if mode == "audit" || mode == "unresolved" {
					if x.physical(t) != before {
						t.Fatal("partial stock effects")
					}
					var n int
					postingOK(t, x.r.f.pool.QueryRow(x.r.f.ctx, "SELECT count(*) FROM fulfillment_commands WHERE actor_id=$1 AND key=$2", actor.actor.ID, command.Key).Scan(&n))
					if n != 0 {
						t.Fatal("failed command persisted")
					}
					x.mutate(t, actor, command)
				}
				if !x.mutate(t, actor, command).Replayed {
					t.Fatal("same-key reconciliation failed")
				}
			})
		}
	}
}
func TestFulfillmentSQLGuardsAndInactive(t *testing.T) {
	x := fulfillmentDB(t)
	d := x.draft()
	x.mutate(t, x.sales, d)
	for _, sql := range []string{"UPDATE dispatches SET order_id='01935f00-0000-7000-8000-000000000099' WHERE id=$1", "DELETE FROM fulfillment_transitions WHERE dispatch_id=$1", "UPDATE dispatches SET state='CANCELLED',version=2 WHERE id=$1"} {
		_, e := x.r.f.pool.Exec(x.r.f.ctx, sql, d.ID)
		var pg *pgconn.PgError
		if !errors.As(e, &pg) || pg.Code != "23514" {
			t.Fatal("unguarded dispatch mutation")
		}
	}
	x.r.refs.inactive = true
	before := x.physical(t)
	if _, e := x.svc.Mutate(x.r.f.ctx, x.human, dispatchCommand(d)); !errors.Is(e, stock.ErrReferenceInactive) {
		t.Fatal("inactive dispatch allowed", e)
	}
	if before != x.physical(t) {
		t.Fatal("inactive partial effect")
	}
	x.r.refs.inactive = false
	x.r.refs.fail = true
	if _, e := x.svc.Mutate(x.r.f.ctx, x.human, dispatchCommand(d)); !errors.Is(e, stock.ErrReferenceUnavailable) {
		t.Fatal("dependency failure ignored", e)
	}
	x.mutate(t, x.sales, metadataCommand(d, f.Cancel, 1))
}

func TestFulfillmentMultilineConsumeRollback(t *testing.T) {
	x := fulfillmentDB(t)
	x.r.mutate(t, terminalReservation(x.hold, stock.Release, 1))
	other := uuid.New()
	receipt := adjustmentCommand(x.r.f, stock.In, 10)
	receipt.Lines[0].VariantID = other
	x.r.f.post(t, receipt)
	hold := x.r.command(20)
	hold.Lines = append(hold.Lines, stock.ReservationLine{OrderLineID: uuid.New(), VariantID: other, Pieces: 5})
	x.r.mutate(t, hold)
	x.hold = hold
	d := x.draft()
	x.mutate(t, x.sales, d)
	command := dispatchCommand(d)
	before := x.physical(t)
	var hits atomic.Int32
	x.r.f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
		tx, e := x.r.f.pool.BeginTx(ctx, o)
		if e != nil {
			return nil, e
		}
		return postingFaultTx{tx, "second movement", &hits}, nil
	}
	_, e := x.svc.Mutate(x.r.f.ctx, x.human, command)
	if e == nil {
		t.Fatal("injected multiline fault passed")
	}
	if before != x.physical(t) {
		t.Fatal("partial first-line issue")
	}
	b := x.r.f.balance(t, other)
	if b.OnHandPieces != 10 || b.ReservedPieces != 5 {
		t.Fatal("partial second-line effect")
	}
	detail, e := x.repo.Get(x.r.f.ctx, x.human.actor, d.ID)
	postingOK(t, e)
	if detail.State != "DRAFT" {
		t.Fatal("failed consume marked dispatched")
	}
	var n int
	postingOK(t, x.r.f.pool.QueryRow(x.r.f.ctx, "SELECT count(*) FROM fulfillment_commands WHERE actor_id=$1 AND key=$2", x.human.actor.ID, command.Key).Scan(&n))
	if n != 0 {
		t.Fatal("failed batch left key")
	}
	x.r.f.repo.begin = x.r.f.pool.BeginTx
	x.mutate(t, x.human, command)
	if x.r.f.balance(t, other).OnHandPieces != 5 || x.r.f.balance(t, x.r.f.variant).OnHandPieces != 80 {
		t.Fatal("whole batch dispatch did not issue exactly once")
	}
}

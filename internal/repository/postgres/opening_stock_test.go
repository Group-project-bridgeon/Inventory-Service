package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type openingTestAuth struct{}

func (openingTestAuth) Authorize(context.Context, stock.Principal, stock.Command) error { return nil } // Synthetic fixture only.
func openingCommand(f *postingFixture) stock.Command {
	c := f.command(stock.In, 100)
	c.Producer = stock.OpeningProducer
	c.SourceEventID = c.OperationID
	return c
}
func openingFixture(t *testing.T) *postingFixture {
	f := postingDB(t)
	var e error
	f.service, e = stock.NewPostingService(f.repo, openingTestAuth{}, f.refs, syntheticFreshness{}, stock.DefaultPostingOptions())
	postingOK(t, e)
	return f
}
func TestOpeningPersistenceAndReplay(t *testing.T) {
	f := openingFixture(t)
	c := openingCommand(f)
	c.FirstRequestID = "opening-first"
	r := f.post(t, c)
	if r.Replayed || r.Lines[0].AfterPieces != 100 {
		t.Fatal("opening result")
	}
	c.FirstRequestID = "retry"
	r = f.post(t, c)
	if !r.Replayed {
		t.Fatal("not replay")
	}
	var request string
	postingOK(t, f.pool.QueryRow(f.ctx, "SELECT first_request_id FROM stock_operations WHERE id=$1", c.OperationID).Scan(&request))
	if request != "opening-first" {
		t.Fatal("audit overwritten")
	}
	second := openingCommand(f)
	_, e := f.service.Post(f.ctx, f.p, second)
	if !errors.Is(e, stock.ErrOpeningNotEligible) {
		t.Fatal("second opening allowed")
	}
	f.absent(t, second)
	f.post(t, f.command(stock.Out, 100))
	_, e = f.service.Post(f.ctx, f.p, openingCommand(f))
	if !errors.Is(e, stock.ErrOpeningNotEligible) {
		t.Fatal("zero balance reopened")
	}
	// Completed replay remains historical when both warehouse and Catalog are inactive.
	_, e = f.pool.Exec(f.ctx, "UPDATE warehouses SET is_active=false WHERE id=$1", f.warehouse)
	postingOK(t, e)
	failing := postingRefs(func(context.Context, stock.Command) ([]stock.Observation, error) {
		t.Fatal("completed replay called Catalog")
		return nil, nil
	})
	svc, e := stock.NewPostingService(f.repo, openingTestAuth{}, failing, syntheticFreshness{}, stock.DefaultPostingOptions())
	postingOK(t, e)
	r, e = svc.Post(f.ctx, f.p, c)
	postingOK(t, e)
	if !r.Replayed {
		t.Fatal("lost replay")
	}
}
func TestOpeningPriorReceiptAndAtomicity(t *testing.T) {
	f := openingFixture(t)
	f.post(t, f.command(stock.In, 1))
	c := openingCommand(f)
	c.Lines = append(c.Lines, stock.CommandLine{LineNumber: 2, VariantID: uuid.New(), InputCount: 2, ExpectedStockReferenceRevision: "synthetic-v1"})
	_, e := f.service.Post(f.ctx, f.p, c)
	if !errors.Is(e, stock.ErrOpeningNotEligible) {
		t.Fatal("prior receipt accepted")
	}
	f.absent(t, c)
	var count int
	postingOK(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM stock_balances WHERE warehouse_id=$1", f.warehouse).Scan(&count))
	if count != 1 {
		t.Fatal("initialized row leaked")
	}
}
func TestOpeningConcurrentKeysAndOverlap(t *testing.T) {
	for _, mode := range []string{"same", "different", "overlap", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			f := openingFixture(t)
			a := openingCommand(f)
			b := openingCommand(f)
			if mode == "same" {
				b = a
			}
			if mode == "ordinary" {
				b = f.command(stock.In, 100)
			}
			if mode == "overlap" {
				other := stock.CommandLine{LineNumber: 2, VariantID: uuid.New(), InputCount: 10, ExpectedStockReferenceRevision: "synthetic-v1"}
				a.Lines = append(a.Lines, other)
				b.Lines = []stock.CommandLine{other, b.Lines[0]}
			}
			out := postingConcurrent(t, f, []stock.Command{a, b})
			success, replay := 0, 0
			for _, o := range out {
				if o.e == nil {
					success++
					if o.r.Replayed {
						replay++
					}
				} else if !errors.Is(o.e, stock.ErrOpeningNotEligible) {
					t.Fatalf("unexpected %v", o.e)
				}
			}
			if mode == "same" {
				if success != 2 || replay != 1 {
					t.Fatal("same-key effects")
				}
			} else if mode != "ordinary" && success != 1 {
				t.Fatal("different-key duplicate")
			}
			bala := f.balance(t, f.variant)
			if mode != "ordinary" && bala.OnHandPieces != 100 {
				t.Fatal("duplicate quantity")
			}
			if mode == "ordinary" && (bala.OnHandPieces != 100 && bala.OnHandPieces != 200) {
				t.Fatal("ordinary race")
			}
		})
	}
}
func TestOpeningWarehouseDeactivation(t *testing.T) {
	f := openingFixture(t)
	f.post(t, openingCommand(f))
	_, e := NewWarehouseRepository(f.pool).SetActive(f.ctx, f.warehouse, false)
	if !errors.Is(e, domain.ErrWarehouseHasStock) {
		t.Fatal("warehouse with stock deactivated")
	}
}
func TestOpeningSQLBackstop(t *testing.T) {
	for _, mode := range []string{"direction", "source", "version"} {
		t.Run(mode, func(t *testing.T) {
			f := openingFixture(t)
			f.post(t, f.command(stock.In, 1))
			tx, e := f.pool.Begin(f.ctx)
			postingOK(t, e)
			defer tx.Rollback(f.ctx)
			id, key, source := uuid.New(), uuid.New(), uuid.New()
			direction := "IN"
			if mode != "source" {
				source = id
			}
			if mode == "direction" {
				direction = "OUT"
			}
			_, e = tx.Exec(f.ctx, "INSERT INTO stock_operations(id,producer,source_event_id,warehouse_id,direction,actor_user_id,line_count,command_version,idempotency_key) VALUES($1,$2,$3,$4,$5,$6,1,1,$7)", id, stock.OpeningProducer, source, f.warehouse, direction, f.p.UserID, key)
			postingOK(t, e)
			if mode == "version" {
				_, e = tx.Exec(f.ctx, `INSERT INTO stock_movements SELECT $1,$2,1,warehouse_id,variant_id,2,'IN',1,1,1,1,2,product_id,base_unit_id,NULL,NULL,product_code,product_name,sku,base_unit_code,NULL,stock_reference_revision,reference_checked_at FROM stock_movements WHERE warehouse_id=$3 LIMIT 1`, uuid.Must(uuid.NewV7()), id, f.warehouse)
				postingOK(t, e)
			}
			_, e = tx.Exec(f.ctx, "SELECT stock_assert_operation($1)", id)
			var pg *pgconn.PgError
			if !errors.As(e, &pg) || pg.ConstraintName != "stock_opening_integrity" {
				t.Fatal("opening SQL guard not enforced")
			}
		})
	}
}

// Hold the real transaction at Commit to observe lock contention deterministically.
type openingBarrierTx struct {
	pgx.Tx
	ready, release chan struct{}
}

func (t openingBarrierTx) Commit(ctx context.Context) error {
	close(t.ready)
	select {
	case <-t.release:
		return t.Tx.Commit(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestOpeningWarehousePostingRace(t *testing.T) {
	f := openingFixture(t)
	ready, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
		tx, e := f.pool.BeginTx(ctx, o)
		return openingBarrierTx{tx, ready, release}, e
	}
	posted := make(chan error, 1)
	go func() { _, e := f.service.Post(f.ctx, f.p, openingCommand(f)); posted <- e }()
	select {
	case <-ready:
	case <-f.ctx.Done():
		t.Fatal("posting barrier timeout")
	}
	deactivated := make(chan error, 1)
	go func() { _, e := NewWarehouseRepository(f.pool).SetActive(f.ctx, f.warehouse, false); deactivated <- e }()
	ticker := time.NewTicker(time.Millisecond * 5)
	defer ticker.Stop()
	for {
		var blocked bool
		postingOK(t, f.pool.QueryRow(f.ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockWarehouseStatus%')").Scan(&blocked))
		if blocked {
			break
		}
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
			t.Fatal("deactivation never waited")
		}
	}
	close(release)
	postingOK(t, <-posted)
	if e := <-deactivated; !errors.Is(e, domain.ErrWarehouseHasStock) {
		t.Fatal("deactivation failed to recheck posted stock")
	}
}

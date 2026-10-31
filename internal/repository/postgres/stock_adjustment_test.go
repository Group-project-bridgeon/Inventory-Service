package postgres

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func adjustmentFixture(t *testing.T) *postingFixture {
	f := postingDB(t)
	refs := postingRefs(func(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
		if c.Producer == stock.CorrectionProducer {
			copy := c
			copy.Lines = append([]stock.CommandLine(nil), c.Lines...)
			for i := range copy.Lines {
				copy.Lines[i].PackID = uuid.Nil
				copy.Lines[i].PackRevision = ""
			}
			return f.refs.Resolve(ctx, copy)
		}
		return f.refs.Resolve(ctx, c)
	})
	var err error
	f.service, err = stock.NewPostingService(f.repo, openingTestAuth{}, refs, syntheticFreshness{}, stock.DefaultPostingOptions())
	postingOK(t, err)
	return f
}
func adjustmentCommand(f *postingFixture, dir stock.Direction, n stock.Quantity) stock.Command {
	c := f.command(dir, n)
	c.Producer = stock.AdjustmentProducer
	c.SourceEventID = c.OperationID
	c.CommandVersion = 2
	c.Audit = &stock.AdjustmentAudit{ReasonCode: "DATA_CORRECTION", Explanation: "Synthetic correction evidence"}
	return c
}
func reversalCommand(t *testing.T, f *postingFixture, original stock.Command) stock.Command {
	t.Helper()
	old, err := f.repo.LoadOriginal(f.ctx, f.warehouse, original.OperationID)
	postingOK(t, err)
	c := adjustmentCommand(f, old.ReverseDirection(), 1)
	c.Producer = stock.CorrectionProducer
	c.Original = &old
	c.Audit.OriginalOperationID = old.ID
	c.Lines = nil
	for _, l := range old.Lines {
		c.Lines = append(c.Lines, stock.CommandLine{LineNumber: l.LineNumber, VariantID: l.VariantID, InputCount: l.InputCount, PackID: l.PackID, PackRevision: l.PackRevision, ExpectedStockReferenceRevision: "synthetic-current-direct"})
	}
	return c
}
func TestAdjustmentPersistencePackReversalAndReplay(t *testing.T) {
	f := adjustmentFixture(t)
	c := adjustmentCommand(f, stock.In, 10)
	c.Lines[0].PackID = uuid.New()
	c.Lines[0].PackRevision = "1"
	first := f.post(t, c)
	if first.Lines[0].AfterPieces != 60 {
		t.Fatal("pack increase")
	}
	if !f.post(t, c).Replayed {
		t.Fatal("not replay")
	}
	changed := c
	a := *c.Audit
	changed.Audit = &a
	changed.Audit.ReasonCode = "OTHER"
	_, err := f.service.Post(f.ctx, f.p, changed)
	if !errors.Is(err, stock.ErrKeyConflict) {
		t.Fatal("changed reason reused key")
	}
	reverse := reversalCommand(t, f, c)
	r := f.post(t, reverse)
	if r.Lines[0].AfterPieces != 0 {
		t.Fatal("historical factor not reversed")
	}
	var factor, pieces int64
	var revision, reason string
	postingOK(t, f.pool.QueryRow(f.ctx, "SELECT m.pieces_per_input,m.pieces,m.pack_revision,a.reason_code FROM stock_movements m JOIN stock_operation_audit a USING(operation_id) WHERE m.operation_id=$1", reverse.OperationID).Scan(&factor, &pieces, &revision, &reason))
	if factor != 6 || pieces != 60 || revision != "1" || reason != "DATA_CORRECTION" {
		t.Fatal("audit/history changed")
	}
	if !f.post(t, reverse).Replayed {
		t.Fatal("reverse replay")
	}
	duplicate := reversalCommand(t, f, c)
	_, err = f.service.Post(f.ctx, f.p, duplicate)
	if !errors.Is(err, stock.ErrAlreadyReversed) {
		t.Fatal("duplicate different-key reverse", err)
	}
	f.absent(t, duplicate)
	_, err = f.repo.LoadOriginal(f.ctx, f.warehouse, reverse.OperationID)
	if !errors.Is(err, stock.ErrNotReversible) {
		t.Fatal("reversal of reversal")
	}
	// Opening cannot be reopened by returning to zero.
	_, err = f.service.Post(f.ctx, f.p, openingCommand(f))
	if !errors.Is(err, stock.ErrOpeningNotEligible) {
		t.Fatal("opening reopened")
	}
	// Immutable audit and safe downgrade refusal preserve all facts.
	_, err = f.pool.Exec(f.ctx, "UPDATE stock_operation_audit SET reason_code='OTHER' WHERE operation_id=$1", c.OperationID)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "stock_immutable" {
		t.Fatal("mutable audit")
	}
}
func TestAdjustmentAtomicRollbackAndConcurrentDecrease(t *testing.T) {
	f := adjustmentFixture(t)
	f.post(t, adjustmentCommand(f, stock.In, 100))
	bad := adjustmentCommand(f, stock.Out, 30)
	bad.Lines = append(bad.Lines, stock.CommandLine{LineNumber: 2, VariantID: uuid.New(), InputCount: 1, ExpectedStockReferenceRevision: "synthetic-v1"})
	_, err := f.service.Post(f.ctx, f.p, bad)
	if !errors.Is(err, stock.ErrInsufficientStock) {
		t.Fatal("partial decrease", err)
	}
	f.absent(t, bad)
	var n int
	postingOK(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM stock_operation_audit WHERE operation_id=$1", bad.OperationID).Scan(&n))
	if n != 0 || f.balance(t, f.variant).OnHandPieces != 100 {
		t.Fatal("partial audit/balance")
	}
	out := postingConcurrent(t, f, []stock.Command{adjustmentCommand(f, stock.Out, 70), adjustmentCommand(f, stock.Out, 70)})
	success := 0
	for _, v := range out {
		if v.e == nil {
			success++
		} else if !errors.Is(v.e, stock.ErrInsufficientStock) {
			t.Fatal("unexpected concurrent error", v.e)
		}
	}
	if success != 1 || f.balance(t, f.variant).OnHandPieces != 30 {
		t.Fatal("overdraw")
	}
}
func TestAdjustmentConcurrentWholeReversal(t *testing.T) {
	for _, same := range []bool{false, true} {
		f := adjustmentFixture(t)
		original := openingCommand(f)
		f.post(t, original)
		a := reversalCommand(t, f, original)
		b := reversalCommand(t, f, original)
		if same {
			b = a
		}
		out := postingConcurrent(t, f, []stock.Command{a, b})
		success, replays := 0, 0
		for _, v := range out {
			if v.e == nil {
				success++
				if v.r.Replayed {
					replays++
				}
			} else if !errors.Is(v.e, stock.ErrAlreadyReversed) {
				t.Fatal("concurrent reversal", v.e)
			}
		}
		if (!same && success != 1) || (same && (success != 2 || replays != 1)) || f.balance(t, f.variant).OnHandPieces != 0 {
			t.Fatal("duplicate reversal effect")
		}
	}
}
func TestAdjustmentSQLAuditBackstop(t *testing.T) {
	f := adjustmentFixture(t)
	original := adjustmentCommand(f, stock.In, 1)
	f.post(t, original)
	// SQL guard is exercised directly inside a disposable transaction, not weakened
	// by application validation. A legacy operation cannot acquire new audit.
	legacy := f.command(stock.In, 1)
	f.post(t, legacy)
	tx, err := f.pool.Begin(f.ctx)
	postingOK(t, err)
	defer tx.Rollback(f.ctx)
	_, err = tx.Exec(f.ctx, "INSERT INTO stock_operation_audit(operation_id,reason_code,explanation) VALUES($1,'OTHER','Invalid historical decoration')", legacy.OperationID)
	postingOK(t, err)
	err = tx.Commit(f.ctx)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "stock_adjustment_integrity" {
		t.Fatal("legacy audit accepted")
	}
	// New audit cannot be inserted without the matching completed posting facts.
	_, err = f.pool.Exec(f.ctx, "SELECT stock_assert_adjustment($1)", uuid.New())
	if err == nil {
		t.Fatal("missing header accepted")
	}
}

func TestAdjustmentReversalSQLCorrespondence(t *testing.T) {
	f := adjustmentFixture(t)
	original := adjustmentCommand(f, stock.In, 10)
	f.post(t, original)
	c := reversalCommand(t, f, original)
	// Simulated trusted-boundary bug: the deferred SQL guard must reject changed
	// historical conversion/label facts even when domain validation passes.
	c.Original.Lines[0].ProductName = "Fabricated historical label"
	_, err := f.service.Post(f.ctx, f.p, c)
	if !errors.Is(err, stock.ErrInternal) {
		t.Fatal("database accepted nonhistorical reversal", err)
	}
	f.absent(t, c)
	if f.balance(t, f.variant).OnHandPieces != 10 {
		t.Fatal("constraint rollback lost quantity")
	}
}
func TestAdjustmentCommitRecovery(t *testing.T) {
	for _, mode := range []string{"lost acknowledgement", "unresolved"} {
		t.Run(mode, func(t *testing.T) {
			f := adjustmentFixture(t)
			original := adjustmentCommand(f, stock.In, 10)
			f.post(t, original)
			c := reversalCommand(t, f, original)
			var begins, hits atomic.Int32
			f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
				begins.Add(1)
				tx, err := f.pool.BeginTx(ctx, o)
				if err != nil {
					return nil, err
				}
				return postingFaultTx{tx, mode, &hits}, nil
			}
			result, err := f.service.Post(f.ctx, f.p, c)
			if begins.Load() != 1 {
				t.Fatal("uncertain mutation retried")
			}
			if mode == "lost acknowledgement" {
				postingOK(t, err)
				if !result.Replayed || f.balance(t, f.variant).OnHandPieces != 0 {
					t.Fatal("v2 durable recovery")
				}
			} else {
				if !errors.Is(err, stock.ErrOutcomeUnknown) {
					t.Fatal("unknown lost", err)
				}
				f.absent(t, c)
				f.repo.begin = f.pool.BeginTx
				result = f.post(t, c)
				if result.Replayed || f.balance(t, f.variant).OnHandPieces != 0 {
					t.Fatal("later same-key reconciliation")
				}
			}
			if !f.post(t, c).Replayed {
				t.Fatal("later replay")
			}
		})
	}
}
func TestAdjustmentInsufficientWholeReversal(t *testing.T) {
	f := adjustmentFixture(t)
	original := adjustmentCommand(f, stock.In, 100)
	original.Lines = append(original.Lines, stock.CommandLine{LineNumber: 2, VariantID: uuid.New(), InputCount: 10, ExpectedStockReferenceRevision: "synthetic-v1"})
	f.post(t, original)
	f.post(t, adjustmentCommand(f, stock.Out, 1))
	c := reversalCommand(t, f, original)
	_, err := f.service.Post(f.ctx, f.p, c)
	if !errors.Is(err, stock.ErrInsufficientStock) {
		t.Fatal("insufficient inverse", err)
	}
	f.absent(t, c)
	if f.balance(t, original.Lines[1].VariantID).OnHandPieces != 10 {
		t.Fatal("second inverse persisted")
	}
}
func TestAdjustmentSQLMissingAudit(t *testing.T) {
	f := adjustmentFixture(t)
	tx, err := f.pool.Begin(f.ctx)
	postingOK(t, err)
	defer tx.Rollback(f.ctx)
	c := adjustmentCommand(f, stock.In, 1)
	_, err = tx.Exec(f.ctx, `INSERT INTO stock_operations(id,producer,source_event_id,warehouse_id,direction,actor_user_id,line_count,command_version,idempotency_key) VALUES($1,$2,$1,$3,'IN',$4,1,2,$5)`, c.OperationID, c.Producer, c.WarehouseID, f.p.UserID, c.IdempotencyKey)
	postingOK(t, err)
	_, err = tx.Exec(f.ctx, "SELECT stock_assert_adjustment($1)", c.OperationID)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "stock_adjustment_integrity" {
		t.Fatal("missing audit allowed")
	}
}

func TestAdjustmentReverseDecrease(t *testing.T) {
	f := adjustmentFixture(t)
	f.post(t, adjustmentCommand(f, stock.In, 100))
	original := adjustmentCommand(f, stock.Out, 30)
	f.post(t, original)
	reverse := reversalCommand(t, f, original)
	if reverse.Direction != stock.In {
		t.Fatal("decrease inverse direction")
	}
	r := f.post(t, reverse)
	if r.Lines[0].AfterPieces != 100 {
		t.Fatal("decrease inverse quantity")
	}
	if !f.post(t, reverse).Replayed || f.balance(t, f.variant).OnHandPieces != 100 {
		t.Fatal("decrease inverse replay")
	}
}

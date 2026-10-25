package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func stockReadFixture(t *testing.T) *postingFixture {
	t.Helper()
	if os.Getenv("STOCK_READ_ISOLATED") != "1" {
		t.Skip("requires scripts/test-stock-read-postgres.ps1")
	}
	return postingDB(t)
}
func readOnlyStockPool(t *testing.T, f *postingFixture) *pgxpool.Pool {
	t.Helper()
	cfg := f.pool.Config()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
	postingOK(t, err)
	t.Cleanup(pool.Close)
	return pool
}
func TestStockReadPersistencePaginationAndIsolation(t *testing.T) {
	f := stockReadFixture(t)
	command := f.command(stock.In, math.MaxInt64)
	f.post(t, command)
	other := uuid.New()
	zero := uuid.New()
	multi := f.command(stock.In, 6)
	multi.Lines = []stock.CommandLine{{LineNumber: 1, VariantID: other, InputCount: 6, ExpectedStockReferenceRevision: "synthetic-v1"}, {LineNumber: 2, VariantID: zero, InputCount: 1, ExpectedStockReferenceRevision: "synthetic-v1"}}
	f.post(t, multi)
	issue := f.command(stock.Out, 1)
	issue.Lines[0].VariantID = zero
	f.post(t, issue)
	pack := f.command(stock.In, 2)
	pack.Lines[0].VariantID = other
	pack.Lines[0].PackID = uuid.New()
	pack.Lines[0].PackRevision = "1"
	f.post(t, pack)
	// Change the synthetic current metadata source; reads must use stored labels.
	original := f.refs
	f.refs = func(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
		o, e := original(ctx, c)
		for i := range o {
			o[i].ProductName = "Changed synthetic current label"
		}
		return o, e
	}
	f.service = f.newService(t, f.refs)
	newCommand := f.command(stock.In, 1)
	newCommand.Lines[0].VariantID = other
	f.post(t, newCommand)
	foreign := postingDB(t)
	foreign.post(t, foreign.command(stock.In, 99))
	_, err := f.pool.Exec(f.ctx, "UPDATE warehouses SET is_active=false WHERE id=$1", f.warehouse)
	postingOK(t, err)
	pool := readOnlyStockPool(t, f)
	reader := NewStockReadRepository(pool)
	s := service.NewStockReadService(reader, NewWarehouseRepository(pool), 5*time.Second)
	page, err := s.ListBalances(f.ctx, service.StockReadInput{WarehouseID: f.warehouse})
	postingOK(t, err)
	if len(page.Items) != 3 {
		t.Fatal("warehouse balance isolation")
	}
	foundMax, foundZero := false, false
	for _, b := range page.Items {
		if b.WarehouseID != f.warehouse {
			t.Fatal("foreign balance")
		}
		foundMax = foundMax || b.OnHandPieces == math.MaxInt64
		foundZero = foundZero || b.OnHandPieces == 0
	}
	if !foundMax || !foundZero {
		t.Fatal("integer bounds missing")
	}
	filtered, err := s.ListBalances(f.ctx, service.StockReadInput{WarehouseID: f.warehouse, NonzeroOnly: true})
	postingOK(t, err)
	if len(filtered.Items) != 2 {
		t.Fatal("nonzero filter")
	}
	filtered, err = s.ListBalances(f.ctx, service.StockReadInput{WarehouseID: f.warehouse, VariantID: &zero})
	postingOK(t, err)
	if len(filtered.Items) != 1 || filtered.Items[0].OnHandPieces != 0 {
		t.Fatal("variant/zero balance")
	}
	before := readFacts(t, f)
	var ids []uuid.UUID
	in := service.StockReadInput{WarehouseID: f.warehouse, PageInput: service.PageInput{Limit: 1}}
	for {
		p, e := s.ListBalances(f.ctx, in)
		postingOK(t, e)
		for _, b := range p.Items {
			ids = append(ids, b.VariantID)
		}
		if p.NextCursor == nil {
			break
		}
		in.Cursor = *p.NextCursor
	}
	if len(ids) != 3 || !slices.IsSortedFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) }) {
		t.Fatal("balance keyset traversal")
	}
	var history []domain.StockMovementView
	in.Cursor = ""
	for {
		p, e := s.ListMovements(f.ctx, in)
		postingOK(t, e)
		history = append(history, p.Items...)
		if p.NextCursor == nil {
			break
		}
		in.Cursor = *p.NextCursor
	}
	if len(history) != 6 {
		t.Fatalf("movement traversal length %d", len(history))
	}
	seen := map[uuid.UUID]bool{}
	var sameTime []domain.StockMovementView
	oldLabel, newLabel, packSeen := false, false, false
	for i, m := range history {
		if seen[m.ID] || m.WarehouseID != f.warehouse || (i > 0 && strings.Compare(history[i-1].ID.String(), m.ID.String()) <= 0) {
			t.Fatal("history gaps/order/isolation")
		}
		seen[m.ID] = true
		if m.OperationID == multi.OperationID {
			sameTime = append(sameTime, m)
		}
		oldLabel = oldLabel || m.ProductName == "Synthetic"
		newLabel = newLabel || m.ProductName == "Changed synthetic current label"
		if m.PackID != nil {
			packSeen = m.PackRevision != nil && *m.PackRevision == "1" && m.PiecesPerInput == 6 && m.PackLabel != nil
		}
	}
	if len(sameTime) != 2 || !sameTime[0].PostedAt.Equal(sameTime[1].PostedAt) || !oldLabel || !newLabel || !packSeen {
		t.Fatal("equal timestamp or durable snapshot contract")
	}
	p, err := s.ListMovements(f.ctx, service.StockReadInput{WarehouseID: f.warehouse, VariantID: &zero})
	postingOK(t, err)
	if len(p.Items) != 2 {
		t.Fatal("movement variant filter")
	}
	empty := uuid.New()
	p, err = s.ListMovements(f.ctx, service.StockReadInput{WarehouseID: f.warehouse, VariantID: &empty})
	postingOK(t, err)
	if p.Items == nil || len(p.Items) != 0 {
		t.Fatal("empty unknown variant")
	}
	if _, err = s.ListBalances(f.ctx, service.StockReadInput{WarehouseID: uuid.New()}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("missing warehouse")
	}
	if readFacts(t, f) != before {
		t.Fatal("reads changed durable rows")
	}
	// The pool rejects any attempted write, including hidden initialization.
	if _, err = pool.Exec(f.ctx, "SELECT 1"); err != nil {
		t.Fatal("read-only pool failed")
	}
	t.Log("PASS: read-only pool; max int64/zero, warehouse isolation, six movements with equal timestamps, static keyset traversal and immutable synthetic snapshots")
}
func readFacts(t *testing.T, f *postingFixture) string {
	t.Helper()
	var value string
	err := f.pool.QueryRow(f.ctx, `SELECT md5(jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY warehouse_id,variant_id) FROM stock_balances x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM stock_operations x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM stock_movements x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY producer,key) FROM stock_idempotency x))::text)`).Scan(&value)
	postingOK(t, err)
	return value
}

type heldReadCommit struct {
	pgx.Tx
	ready     chan uuid.UUID
	release   <-chan struct{}
	operation uuid.UUID
}

func (h *heldReadCommit) Commit(ctx context.Context) error {
	var id uuid.UUID
	if err := h.Tx.QueryRow(ctx, "SELECT id FROM stock_movements WHERE operation_id=$1", h.operation).Scan(&id); err != nil {
		return err
	}
	select {
	case h.ready <- id:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-h.release:
		return h.Tx.Commit(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestStockReadConcurrentCommitIsLiveKeyset(t *testing.T) {
	f := stockReadFixture(t)
	for i := 0; i < 3; i++ {
		f.post(t, f.command(stock.In, 1))
	}
	ready := make(chan uuid.UUID, 1)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	pending := f.command(stock.In, 1)
	f.repo.begin = func(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
		tx, e := f.pool.BeginTx(ctx, o)
		if e != nil {
			return nil, e
		}
		return &heldReadCommit{tx, ready, release, pending.OperationID}, nil
	}
	result := make(chan error, 1)
	go func() { _, e := f.service.Post(f.ctx, f.p, pending); result <- e }()
	var late uuid.UUID
	select {
	case late = <-ready:
	case <-f.ctx.Done():
		t.Fatal("commit barrier deadline")
	}
	s := service.NewStockReadService(NewStockReadRepository(f.pool), NewWarehouseRepository(f.pool), time.Second)
	in := service.StockReadInput{WarehouseID: f.warehouse, PageInput: service.PageInput{Limit: 1}}
	page, err := s.ListMovements(f.ctx, in)
	postingOK(t, err)
	if len(page.Items) != 1 || page.NextCursor == nil || strings.Compare(late.String(), page.Items[0].ID.String()) <= 0 {
		t.Fatal("late insert not above cursor")
	}
	close(release)
	select {
	case err = <-result:
		postingOK(t, err)
	case <-f.ctx.Done():
		t.Fatal("commit worker leaked")
	}
	ids := []uuid.UUID{page.Items[0].ID}
	in.Cursor = *page.NextCursor
	for {
		p, e := s.ListMovements(f.ctx, in)
		postingOK(t, e)
		for _, m := range p.Items {
			ids = append(ids, m.ID)
		}
		if p.NextCursor == nil {
			break
		}
		in.Cursor = *p.NextCursor
	}
	if len(ids) != 3 || slices.Contains(ids, late) {
		t.Fatal("live cursor commit limitation changed")
	}
	in.Cursor = ""
	p, err := s.ListMovements(f.ctx, in)
	postingOK(t, err)
	if p.Items[0].ID != late {
		t.Fatal("refresh omitted late committed row")
	}
	t.Log("PASS: ID allocated before page but committed after it is skipped above cursor; refresh sees it (no snapshot/commit-order claim)")
}

func TestStockReadQueryPlans(t *testing.T) {
	f := stockReadFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	f.ctx = ctx
	var selected uuid.UUID
	for batch := 0; batch < 8; batch++ {
		c := f.command(stock.In, 1)
		if batch > 0 {
			warehouse := uuid.New()
			_, err := f.pool.Exec(f.ctx, "INSERT INTO warehouses(id,code,name) VALUES($1,$2,'Synthetic read-plan warehouse')", warehouse, "PLAN-"+strings.ToUpper(warehouse.String()))
			postingOK(t, err)
			c.WarehouseID = warehouse
		}
		c.Lines = nil
		lines := 100
		if batch == 0 {
			lines = 5
		}
		for i := 1; i <= lines; i++ {
			id := uuid.New()
			if batch == 0 {
				selected = id
			}
			c.Lines = append(c.Lines, stock.CommandLine{LineNumber: int16(i), VariantID: id, InputCount: 1, ExpectedStockReferenceRevision: "synthetic-v1"})
		}
		f.post(t, c)
	}
	_, err := f.pool.Exec(f.ctx, "ANALYZE stock_movements; ANALYZE stock_operations")
	postingOK(t, err)
	for _, tc := range []struct {
		variant any
		indexes []string
	}{{nil, []string{"stock_movements_warehouse_id_desc", "stock_movements_warehouse_variant_id_desc"}}, {selected, []string{"stock_movements_warehouse_variant_id_desc"}}} {
		var plan string
		err = f.pool.QueryRow(f.ctx, `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) SELECT m.*,o.posted_at FROM stock_movements m JOIN stock_operations o ON o.id=m.operation_id WHERE m.warehouse_id=$1 AND ($2::uuid IS NULL OR m.variant_id=$2::uuid) AND ($3::uuid IS NULL OR m.id<$3::uuid) ORDER BY m.id DESC LIMIT 11`, f.warehouse, tc.variant, nil).Scan(&plan)
		postingOK(t, err)
		names := regexp.MustCompile(`"Index Name": "([a-zA-Z0-9_]+)"`).FindAllStringSubmatch(plan, -1)
		for _, name := range names {
			t.Log("EXPLAIN index:", name[1])
		}
		matched := false
		for _, index := range tc.indexes {
			matched = matched || strings.Contains(plan, index)
		}
		if !matched {
			t.Fatal("warehouse-prefix index absent from selective query plan")
		}
		var measured []struct {
			Plan struct {
				ActualRows int `json:"Actual Rows"`
			} `json:"Plan"`
		}
		postingOK(t, json.Unmarshal([]byte(plan), &measured))
		if len(measured) != 1 || measured[0].Plan.ActualRows > 11 {
			t.Fatal("plan returned unbounded rows")
		}
		t.Log("PASS: EXPLAIN ANALYZE used a warehouse-prefix index on 705 movements across eight warehouses")
	}
}

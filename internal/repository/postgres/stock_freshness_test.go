package postgres

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/catalogclient"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type freshnessAcquireTrace struct {
	enabled atomic.Bool
	once    sync.Once
	started chan struct{}
}

func (t *freshnessAcquireTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (*freshnessAcquireTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (t *freshnessAcquireTrace) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	if t.enabled.Load() {
		t.once.Do(func() { close(t.started) })
	}
	return ctx
}
func (*freshnessAcquireTrace) TraceAcquireEnd(context.Context, *pgxpool.Pool, pgxpool.TraceAcquireEndData) {
}
func TestStockFreshnessAfterPoolWait(t *testing.T) {
	trace := &freshnessAcquireTrace{started: make(chan struct{})}
	f := postingDB(t, func(c *pgxpool.Config) { c.MaxConns = 1; c.ConnConfig.Tracer = trace })
	command := f.command(stock.In, 1)
	observations, err := f.refs(f.ctx, command)
	postingOK(t, err)
	prepared, err := stock.Prepare(command, observations)
	postingOK(t, err)
	fp, err := command.Fingerprint(f.p)
	postingOK(t, err)
	var clock atomic.Int64
	clock.Store(observations[0].CheckedAt.UnixNano())
	policy := catalogclient.Freshness{Now: func() time.Time { return time.Unix(0, clock.Load()) }}
	repo, err := NewStockRepositoryWithFreshness(f.pool, DefaultStockRepositoryOptions(), policy)
	postingOK(t, err)
	held, err := f.pool.Acquire(f.ctx)
	postingOK(t, err)
	defer held.Release()
	trace.enabled.Store(true)
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	go func() { _, err := repo.Apply(ctx, f.p, command, fp, prepared); result <- err }()
	select {
	case <-trace.started:
	case <-ctx.Done():
		t.Fatal("acquisition did not start")
	}
	clock.Add(int64(6 * time.Second))
	held.Release()
	select {
	case err := <-result:
		if !errors.Is(err, stock.ErrReferenceStale) {
			t.Fatal("pool wait bypassed freshness")
		}
	case <-ctx.Done():
		t.Fatal("pool acquisition leaked")
	}
	f.absent(t, command)
}

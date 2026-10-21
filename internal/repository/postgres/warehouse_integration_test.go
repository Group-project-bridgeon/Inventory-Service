package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationWarehousePersistenceConstraintsAndDefaultSerialization(t *testing.T) {
	url := os.Getenv("INVENTORY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set INVENTORY_TEST_DATABASE_URL to an isolated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil || cfg.ConnConfig.Database != "inventory_warehouse_test" {
		t.Fatal("requires isolated inventory_warehouse_test database")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("connect isolated PostgreSQL")
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		t.Fatal("ping isolated PostgreSQL")
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM warehouses").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("integration database must be empty, warehouses=%d", count)
	}
	r := NewWarehouseRepository(pool)
	code := "IT-" + strings.ToUpper(uuid.NewString()[:8])
	w, err := r.Create(ctx, repository.CreateWarehouseParams{ID: uuid.Must(uuid.NewV7()), Code: code, Name: "Integration", Address: "Disposable"})
	if err != nil || !w.IsDefault || !w.IsActive {
		t.Fatalf("first create: %+v %v", w, err)
	}
	if _, err = r.GetByID(ctx, w.ID); err != nil {
		t.Fatalf("persisted detail: %v", err)
	}
	if _, err = r.Create(ctx, repository.CreateWarehouseParams{ID: uuid.Must(uuid.NewV7()), Code: code, Name: "Duplicate"}); !errors.Is(err, domain.ErrDuplicateCode) {
		t.Fatalf("duplicate unique constraint: %v", err)
	}
	var directErr error
	directErr = pool.QueryRow(ctx, "INSERT INTO warehouses(id,code,name) VALUES($1,$2,$3) RETURNING id", uuid.New(), code, "Direct duplicate").Scan(new(uuid.UUID))
	if directErr == nil {
		t.Fatal("database unique constraint accepted duplicate")
	}
	if _, err = pool.Exec(ctx, "UPDATE warehouses SET is_default=false WHERE id=$1", w.ID); err == nil {
		t.Fatal("database accepted a state with no active default")
	}
	// The deferred trigger permits the statement but rejects Commit. Map that
	// error through the same boundary used by every repository mutation.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal("begin deferred-constraint test")
	}
	if _, err = tx.Exec(ctx, "UPDATE warehouses SET is_default=false WHERE id=$1", w.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal("deferred constraint fired before commit")
	}
	if err = tx.Commit(ctx); !errors.Is(mapErr("commit_warehouse", err), domain.ErrDefaultConflict) {
		t.Fatal("commit-time default violation did not map to conflict")
	}
	w2, err := r.Create(ctx, repository.CreateWarehouseParams{ID: uuid.Must(uuid.NewV7()), Code: "IT-" + strings.ToUpper(uuid.NewString()[:8]), Name: "Second"})
	if err != nil || w2.IsDefault {
		t.Fatalf("second create: %+v %v", w2, err)
	}
	promoted, err := r.Update(ctx, repository.UpdateWarehouseParams{ID: w2.ID, Name: "Second edited", Address: "", IsDefault: ptr(true)})
	if err != nil || !promoted.IsDefault {
		t.Fatalf("promote: %+v %v", promoted, err)
	}
	if _, err = r.SetActive(ctx, w2.ID, false); !errors.Is(err, domain.ErrDefaultConflict) {
		t.Fatalf("deactivated default: %v", err)
	}
	if _, err = r.SetActive(ctx, w.ID, false); err != nil {
		t.Fatalf("deactivate prior default: %v", err)
	}
	// Concurrent later creations and default switches must preserve one default.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, e := r.Create(ctx, repository.CreateWarehouseParams{ID: uuid.Must(uuid.NewV7()), Code: fmt.Sprintf("CON-%02d-%s", i, strings.ToUpper(uuid.NewString()[:6])), Name: "Concurrent"})
			if e != nil {
				errs <- e
				return
			}
			if i%2 == 0 {
				yes := true
				_, e = r.Update(ctx, repository.UpdateWarehouseParams{ID: created.ID, Name: created.Name, IsDefault: &yes})
				if e != nil {
					errs <- e
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM warehouses WHERE is_default").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one active default, got %d", count)
	}
	page, err := r.List(ctx, repository.ListWarehousesParams{AfterCode: "", AfterID: uuid.Nil, PageSize: 2})
	if err != nil || len(page) != 2 {
		t.Fatalf("list persistence: %d %v", len(page), err)
	}
}

func ptr[T any](v T) *T { return &v }

package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"sync"
	"testing"
	"time"
)

func TestIntegrationConcurrentFirstCreation(t *testing.T)  { concurrentCreate(t, false) }
func TestIntegrationConcurrentNormalizedCode(t *testing.T) { concurrentCreate(t, true) }

func concurrentCreate(t *testing.T, same bool) {
	t.Helper()
	url := os.Getenv("INVENTORY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("disposable PostgreSQL fixture required")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil || cfg.ConnConfig.Database != "inventory_warehouse_test" {
		t.Fatal("requires isolated inventory_warehouse_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("open fixture")
	}
	defer admin.Close()
	schema := "concurrent_first"
	if same {
		schema = "concurrent_code"
	}
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create isolated case schema")
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("case cleanup failed")
		}
	}()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("open case pool")
	}
	defer pool.Close()
	sql, err := os.ReadFile("../../../db/migrations/000001_create_warehouses.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal("create case schema objects")
	}
	s := service.NewWarehouseService(NewWarehouseRepository(pool))
	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	var ready, done sync.WaitGroup
	ready.Add(workers)
	done.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			code := fmt.Sprintf("CASE-%d", i)
			if same {
				code = []string{" same-code ", "SAME-CODE", "same-code", "Same-Code"}[i%4]
			}
			_, err := s.Create(ctx, service.CreateWarehouseInput{Code: code, Name: "Concurrent"})
			results <- err
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(results)
	successes, duplicates := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrDuplicateCode):
			duplicates++
		default:
			t.Fatalf("unexpected result type %T", err)
		}
	}
	want := workers
	if same {
		want = 1
	}
	if successes != want || duplicates != workers-want {
		t.Fatalf("success=%d duplicate=%d", successes, duplicates)
	}
	var total, defaults int
	if err = pool.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE is_default AND is_active) FROM warehouses").Scan(&total, &defaults); err != nil || total != want || defaults != 1 {
		t.Fatalf("persisted=%d active defaults=%d", total, defaults)
	}
	if same {
		var code string
		if err = pool.QueryRow(ctx, "SELECT code FROM warehouses").Scan(&code); err != nil || code != "SAME-CODE" {
			t.Fatal("normalization not persisted")
		}
	}
	t.Logf("barrier workers=%d success=%d duplicate=%d persisted=%d active_default=%d", workers, successes, duplicates, total, defaults)
}

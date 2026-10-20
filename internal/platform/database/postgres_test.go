package database

import (
	"context"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/config"
	"strings"
	"testing"
	"time"
)

func settings() config.DatabaseConfig {
	return config.DatabaseConfig{MaxConns: 10, MinConns: 0, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, StartupTimeout: time.Second, StatementTimeout: 1500 * time.Millisecond}
}
func TestPoolConfig(t *testing.T) {
	cfg, err := poolConfig("postgres://inventory_service:private-secret@localhost/inventory_db?statement_timeout=0", settings())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 10 || cfg.MinConns != 0 || cfg.ConnConfig.RuntimeParams["statement_timeout"] != "1500" || cfg.ConnConfig.ConnectTimeout != time.Second {
		t.Fatal("pool settings not applied")
	}
	for _, url := range []string{"postgres://catalog_service:private-secret@localhost/inventory_db", "postgres://inventory_service:private-secret@localhost/catalog_db", "postgres://%private-secret"} {
		_, err := poolConfig(url, settings())
		if err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("unsafe or missing validation")
		}
	}
}
func TestCanceledStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool, err := NewPool(ctx, "postgres://inventory_service:private-secret@127.0.0.1:1/inventory_db", settings())
	if pool != nil || err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("canceled startup not handled safely")
	}
}

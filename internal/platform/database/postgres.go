package database

import (
	"context"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/config"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/jackc/pgx/v5/pgxpool"
	"strconv"
)

func poolConfig(url string, settings config.DatabaseConfig) (*pgxpool.Config, error) {
	c, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, diagnose("parse_config", err)
	}
	if c.ConnConfig.Database != "inventory_db" || c.ConnConfig.User != "inventory_service" {
		return nil, &logger.Failure{Component: "database", Operation: "validate_identity", Reason: "invalid_connection_configuration", Summary: "DATABASE_URL must select inventory_db as inventory_service"}
	}
	c.MaxConns = settings.MaxConns
	c.MinConns = settings.MinConns
	c.MaxConnLifetime = settings.MaxConnLifetime
	c.MaxConnIdleTime = settings.MaxConnIdleTime
	c.ConnConfig.ConnectTimeout = settings.StartupTimeout
	// A decimal millisecond value is valid PostgreSQL syntax for every duration.
	c.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(settings.StatementTimeout.Milliseconds(), 10)
	return c, nil
}

func NewPool(ctx context.Context, url string, settings config.DatabaseConfig) (*pgxpool.Pool, error) {
	c, err := poolConfig(url, settings)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, c)
	if err != nil {
		return nil, diagnose("create_pool", err)
	}
	startup, cancel := context.WithTimeout(ctx, settings.StartupTimeout)
	defer cancel()
	if err = pool.Ping(startup); err != nil {
		pool.Close()
		return nil, diagnose("startup_ping", err)
	}
	return pool, nil
}

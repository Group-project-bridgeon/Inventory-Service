package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type DatabaseConfig struct {
	MaxConns, MinConns                                                 int32
	MaxConnLifetime, MaxConnIdleTime, StartupTimeout, StatementTimeout time.Duration
}
type HTTPConfig struct {
	MaxBodyBytes                                                                                                       int64
	SlowRequestThreshold, ReadinessTimeout, ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout, ShutdownTimeout time.Duration
}
type Config struct {
	Port        string
	DatabaseURL string
	Database    DatabaseConfig
	HTTP        HTTPConfig
}

// Load reads the environment once. Errors identify keys, never their values.
func Load() (*Config, error) {
	c := &Config{DatabaseURL: os.Getenv("DATABASE_URL")}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	number := func(key string, fallback, min, max int64) (int64, error) {
		value, ok := os.LookupEnv(key)
		if !ok {
			return fallback, nil
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < min || n > max {
			return 0, fmt.Errorf("%s must be an integer between %d and %d", key, min, max)
		}
		return n, nil
	}
	port, err := number("PORT", 8082, 1, 65535)
	if err != nil {
		return nil, err
	}
	c.Port = strconv.FormatInt(port, 10)
	max, err := number("DB_MAX_CONNS", 10, 1, 2147483647)
	if err != nil {
		return nil, err
	}
	min, err := number("DB_MIN_CONNS", 2, 0, max)
	if err != nil {
		return nil, err
	}
	c.Database.MaxConns = int32(max)
	c.Database.MinConns = int32(min)
	c.HTTP.MaxBodyBytes, err = number("HTTP_MAX_BODY_BYTES", 1<<20, 1, 9223372036854775807)
	if err != nil {
		return nil, err
	}
	durations := []struct {
		key      string
		target   *time.Duration
		fallback time.Duration
	}{
		{"DB_MAX_CONN_LIFETIME", &c.Database.MaxConnLifetime, 30 * time.Minute},
		{"DB_MAX_CONN_IDLE_TIME", &c.Database.MaxConnIdleTime, 5 * time.Minute},
		{"DB_STARTUP_TIMEOUT", &c.Database.StartupTimeout, 5 * time.Second},
		{"DB_STATEMENT_TIMEOUT", &c.Database.StatementTimeout, 5 * time.Second},
		{"READINESS_TIMEOUT", &c.HTTP.ReadinessTimeout, 2 * time.Second},
		{"HTTP_SLOW_REQUEST_THRESHOLD", &c.HTTP.SlowRequestThreshold, 500 * time.Millisecond},
		{"HTTP_READ_HEADER_TIMEOUT", &c.HTTP.ReadHeaderTimeout, 5 * time.Second},
		{"HTTP_READ_TIMEOUT", &c.HTTP.ReadTimeout, 15 * time.Second},
		{"HTTP_WRITE_TIMEOUT", &c.HTTP.WriteTimeout, 15 * time.Second},
		{"HTTP_IDLE_TIMEOUT", &c.HTTP.IdleTimeout, 60 * time.Second},
		{"SHUTDOWN_TIMEOUT", &c.HTTP.ShutdownTimeout, 10 * time.Second},
	}
	for _, d := range durations {
		value, ok := os.LookupEnv(d.key)
		parsed := d.fallback
		if ok {
			parsed, err = time.ParseDuration(value)
			if err != nil || parsed <= 0 {
				return nil, fmt.Errorf("%s must be a positive duration", d.key)
			}
		}
		*d.target = parsed
	}
	// PostgreSQL uses integer milliseconds; never silently turn a positive timeout into zero.
	if c.Database.StatementTimeout < time.Millisecond || c.Database.StatementTimeout/time.Millisecond > 2147483647 {
		return nil, fmt.Errorf("DB_STATEMENT_TIMEOUT must be between 1ms and 2147483647ms")
	}
	return c, nil
}

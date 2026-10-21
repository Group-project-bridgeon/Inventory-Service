package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func isolated(t *testing.T) {
	t.Helper()
	keys := []string{"DATABASE_URL", "PORT", "DB_MAX_CONNS", "DB_MIN_CONNS", "HTTP_MAX_BODY_BYTES", "DB_MAX_CONN_LIFETIME", "DB_MAX_CONN_IDLE_TIME", "DB_STARTUP_TIMEOUT", "DB_STATEMENT_TIMEOUT", "READINESS_TIMEOUT", "HTTP_SLOW_REQUEST_THRESHOLD", "HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "SHUTDOWN_TIMEOUT"}
	for _, key := range keys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DATABASE_URL", "postgres://inventory_service:example@localhost/inventory_db")
	t.Setenv("JWT_PUBLIC_KEY_PATH", "keys/auth-public.pem")
	t.Setenv("JWT_ISSUER", "erp-auth")
	t.Setenv("JWT_AUDIENCE", "erp-api")
}
func TestDefaults(t *testing.T) {
	isolated(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != "8082" || c.Database.MaxConns != 10 || c.Database.MinConns != 2 || c.HTTP.MaxBodyBytes != 1<<20 || c.Database.StatementTimeout != 5*time.Second || c.HTTP.ReadinessTimeout != 2*time.Second {
		t.Fatalf("unexpected defaults")
	}
}
func TestValidation(t *testing.T) {
	cases := map[string][]string{
		"DATABASE_URL": {"", " "}, "PORT": {"0", "65536", "abc", ""}, "DB_MAX_CONNS": {"0", "2147483648"}, "DB_MIN_CONNS": {"-1", "11"}, "HTTP_MAX_BODY_BYTES": {"0", "-1"},
		"DB_MAX_CONN_LIFETIME": {"0s", "bad"}, "DB_MAX_CONN_IDLE_TIME": {"-1s"}, "DB_STARTUP_TIMEOUT": {"0s"}, "DB_STATEMENT_TIMEOUT": {"1ns", "2147483648ms"}, "READINESS_TIMEOUT": {"0s"}, "HTTP_SLOW_REQUEST_THRESHOLD": {"-1s"}, "HTTP_READ_HEADER_TIMEOUT": {"0s"}, "HTTP_READ_TIMEOUT": {"0s"}, "HTTP_WRITE_TIMEOUT": {"0s"}, "HTTP_IDLE_TIMEOUT": {"0s"}, "SHUTDOWN_TIMEOUT": {"0s"},
	}
	for key, values := range cases {
		for _, value := range values {
			t.Run(key+"/"+value, func(t *testing.T) {
				isolated(t)
				t.Setenv(key, value)
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("expected %s validation error, got %v", key, err)
				}
			})
		}
	}
}
func TestOverrides(t *testing.T) {
	isolated(t)
	t.Setenv("PORT", "9092")
	t.Setenv("DB_MIN_CONNS", "0")
	t.Setenv("READINESS_TIMEOUT", "250ms")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != "9092" || c.Database.MinConns != 0 || c.HTTP.ReadinessTimeout != 250*time.Millisecond {
		t.Fatal("overrides ignored")
	}
	t.Setenv("PORT", "secret-value")
	_, err = Load()
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatal("value leaked")
	}
}

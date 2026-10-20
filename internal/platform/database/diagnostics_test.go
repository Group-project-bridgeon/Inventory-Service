package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
)

const secret = "FAKE_PASSWORD_71"
const privateURL = "postgres://inventory_service:FAKE_PASSWORD_71@secret-host/inventory_db"
const detail = "SENSITIVE_SERVER_DETAIL_92"

func TestDiagnostics(t *testing.T) {
	pg := func(code string) error {
		return &pgconn.PgError{Code: code, Message: privateURL, Detail: detail, Hint: secret}
	}
	cases := []struct {
		name          string
		err           error
		reason, state string
	}{
		{"auth", pg("28P01"), "authentication_failed", "28P01"},
		{"authorization", pg("28000"), "authentication_failed", "28000"},
		{"missing", pg("3D000"), "database_missing", "3D000"},
		{"privileges", pg("42501"), "insufficient_privileges", "42501"},
		{"other server", pg("53300"), "postgres_server_error", "53300"},
		{"invalid SQLSTATE", pg(secret), "postgres_server_error", ""},
		{"parse", pgconn.NewParseConfigError(privateURL, detail, errors.New(secret)), "invalid_connection_configuration", ""},
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}, "connection_refused", ""},
		{"dns", &net.DNSError{Name: privateURL, Err: detail}, "dns_lookup_failed", ""},
		{"DNS timeout", &net.DNSError{Name: privateURL, Err: detail, IsTimeout: true}, "timeout", ""},
		{"deadline", context.DeadlineExceeded, "timeout", ""},
		{"cancel", context.Canceled, "context_canceled", ""},
		{"unknown", errors.New(privateURL + detail), "unknown_connection_failure", ""},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, struct {
			name          string
			err           error
			reason, state string
		}{"winsock refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.Errno(10061)}, "connection_refused", ""})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := diagnose("startup_ping", fmt.Errorf("%s: %w", privateURL, tc.err))
			if f.Reason != tc.reason || f.SQLState != tc.state || f.Component != "database" || f.Operation != "startup_ping" {
				t.Fatalf("wrong metadata: %+v", f)
			}
			if errors.Unwrap(f) != nil {
				t.Fatal("retains unsafe cause")
			}
			var b bytes.Buffer
			logger.LogFailure(zerolog.New(&b).With().Str("service", "inventory").Logger(), fmt.Errorf("%s: %w", detail, f))
			for _, marker := range []string{secret, privateURL, detail} {
				if strings.Contains(b.String()+f.Error(), marker) {
					t.Fatalf("secret leaked: %s", marker)
				}
			}
			var event map[string]any
			decoder := json.NewDecoder(&b)
			if err := decoder.Decode(&event); err != nil {
				t.Fatal(err)
			}
			if event["reason"] != tc.reason || event["component"] != "database" || event["operation"] != "startup_ping" || event["service"] != "inventory" || event["error"] != f.Error() || event["message"] != "inventory service failed" || event["level"] != "error" {
				t.Fatalf("invalid structured log: %v", event)
			}
			if tc.state != "" && event["sqlstate"] != tc.state {
				t.Fatal("missing SQLSTATE")
			}
			if tc.state == "" {
				if _, ok := event["sqlstate"]; ok {
					t.Fatal("unexpected SQLSTATE")
				}
			}
			if decoder.Decode(&event) == nil {
				t.Fatal("duplicate log")
			}
		})
	}
}
func TestActualPGXConnectWrapping(t *testing.T) {
	// Inject a failing dialer: exercise pgx's private wrappers without any network I/O.
	cfg, err := pgconn.ParseConfig("postgres://inventory_service:FAKE_PASSWORD_71@127.0.0.1/inventory_db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}
	conn, err := pgconn.ConnectConfig(context.Background(), cfg)
	if conn != nil {
		conn.Close(context.Background())
		t.Fatal("unexpected connection")
	}
	var connect *pgconn.ConnectError
	if !errors.As(err, &connect) {
		t.Fatal("expected pgx ConnectError")
	}
	if f := diagnose("startup_ping", err); f.Reason != "connection_refused" {
		t.Fatalf("wrong reason %s", f.Reason)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = pgconn.ConnectConfig(ctx, cfg)
	if f := diagnose("startup_ping", err); f.Reason != "context_canceled" {
		t.Fatalf("pgx canceled context classified as %s", f.Reason)
	}
}

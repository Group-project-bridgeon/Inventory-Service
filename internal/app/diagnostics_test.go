package app

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/rs/zerolog"
)

func TestApplicationDiagnostics(t *testing.T) {
	cases := []struct {
		op     string
		err    error
		reason string
	}{
		{"load_dotenv", errors.New("password=SECRET_MARKER"), "invalid_dotenv"},
		{"load_dotenv", &os.PathError{Op: "open", Path: "SECRET_MARKER", Err: os.ErrPermission}, "permission_denied"},
		{"listen", &net.OpError{Op: "listen", Err: syscall.EADDRINUSE}, "address_in_use"},
		{"listen", errors.New("SECRET_MARKER"), "listen_failed"},
		{"serve", errors.New("SECRET_MARKER"), "serve_failed"},
		{"shutdown", context.DeadlineExceeded, "timeout"},
		{"shutdown", context.Canceled, "context_canceled"},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, struct {
			op     string
			err    error
			reason string
		}{"listen", syscall.Errno(10048), "address_in_use"})
	}
	for _, tc := range cases {
		f := diagnose("http", tc.op, tc.err)
		if f.Reason != tc.reason {
			t.Fatalf("%s: %s", tc.op, f.Reason)
		}
		var b bytes.Buffer
		logger.LogFailure(zerolog.New(&b), f)
		if strings.Contains(f.Error()+b.String(), "SECRET_MARKER") {
			t.Fatal("secret leaked")
		}
	}
}
func TestRunConfigurationDiagnostics(t *testing.T) {
	// Isolate from the developer's .env and fail before database initialization.
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "")
	err := Run()
	var f *logger.Failure
	if !errors.As(err, &f) || f.Component != "configuration" || f.Operation != "load_config" || f.Reason != "invalid_configuration" || !strings.Contains(f.Error(), "DATABASE_URL") {
		t.Fatalf("unexpected safe diagnostic %v", err)
	}
	if err := os.WriteFile(".env", []byte("INVALID!KEY=SECRET_MARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	err = Run()
	if !errors.As(err, &f) || f.Operation != "load_dotenv" || strings.Contains(err.Error(), "SECRET_MARKER") {
		t.Fatal("unsafe dotenv diagnostic")
	}
}

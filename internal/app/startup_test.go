package app

import (
	"errors"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInvalidJWTConfigurationPrecedesDatabaseConnection(t *testing.T) {
	for _, issuer := range []string{"erp-auth", " invalid "} {
		t.Run(issuer, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			dir := t.TempDir()
			t.Chdir(dir) // Prevent Run from loading a developer .env.
			key := filepath.Join(dir, "invalid.pem")
			if err = os.WriteFile(key, []byte("invalid-public-key"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DATABASE_URL", "postgres://inventory_service@"+listener.Addr().String()+"/inventory_db?sslmode=disable")
			t.Setenv("JWT_PUBLIC_KEY_PATH", key)
			t.Setenv("JWT_ISSUER", issuer)
			t.Setenv("JWT_AUDIENCE", "erp-api")
			t.Setenv("DB_STARTUP_TIMEOUT", "100ms")
			err = Run()
			var f *logger.Failure
			wantReason := "invalid_jwt_configuration"
			if issuer != "erp-auth" {
				wantReason = "invalid_configuration"
			}
			if !errors.As(err, &f) || f.Reason != wantReason {
				t.Fatal("expected JWT/config failure before database initialization")
			}
			if err = listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			conn, err := listener.Accept()
			if err == nil {
				conn.Close()
				t.Fatal("database connection attempted with invalid JWT configuration")
			}
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatal("unexpected listener failure")
			}
		})
	}
}

package app

import (
	"context"
	"errors"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/config"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/database"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/router"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func Run() error {
	log := logger.New()
	// Load preserves values already supplied by the process environment.
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return diagnose("configuration", "load_dotenv", err)
	}
	cfg, err := config.Load()
	if err != nil {
		// config.Load returns key names and static constraints, never environment values.
		return &logger.Failure{Component: "configuration", Operation: "load_config", Reason: "invalid_configuration", Summary: err.Error()}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := database.NewPool(ctx, cfg.DatabaseURL, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	gin.SetMode(gin.ReleaseMode)
	server := &http.Server{Addr: ":" + cfg.Port, Handler: router.New(db, log, cfg.HTTP), ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout, ReadTimeout: cfg.HTTP.ReadTimeout, WriteTimeout: cfg.HTTP.WriteTimeout, IdleTimeout: cfg.HTTP.IdleTimeout}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return diagnose("http", "listen", err)
	}
	log.Info().Str("port", cfg.Port).Msg("inventory service starting")
	err = serve(ctx, server, listener, cfg.HTTP.ShutdownTimeout)
	log.Info().Msg("inventory HTTP server stopped")
	return err
}

// serve drains HTTP before Run's deferred database close. Forced close cancels
// active request contexts when the grace period expires.
func serve(ctx context.Context, server *http.Server, listener net.Listener, timeout time.Duration) error {
	defer listener.Close()
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	var serveErr error
	select {
	case serveErr = <-result:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return diagnose("http", "shutdown", err)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return diagnose("http", "serve", serveErr)
	}
	return nil
}

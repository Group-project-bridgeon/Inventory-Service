package app

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"syscall"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
)

func diagnose(component, operation string, err error) *logger.Failure {
	f := &logger.Failure{Component: component, Operation: operation, Reason: "operation_failed", Summary: "Application operation failed"}
	switch operation {
	case "load_dotenv":
		f.Reason = "invalid_dotenv"
		f.Summary = "Could not read or parse .env; check local file syntax and access"
	case "listen":
		f.Reason = "listen_failed"
		f.Summary = "HTTP listener could not start; check PORT and local network configuration"
	case "serve":
		f.Reason = "serve_failed"
		f.Summary = "HTTP server failed"
	case "shutdown":
		f.Reason = "shutdown_failed"
		f.Summary = "HTTP graceful shutdown failed; remaining connections closed"
	}
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		f.Reason = "context_canceled"
		f.Summary = "Application operation canceled"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()):
		f.Reason = "timeout"
		f.Summary = "Application operation timed out"
	case errors.Is(err, syscall.EADDRINUSE) || (runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10048))): // WSAEADDRINUSE
		f.Reason = "address_in_use"
		f.Summary = "HTTP port is already in use; select an available PORT"
	case errors.Is(err, os.ErrPermission) || (runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10013))): // WSAEACCES
		f.Reason = "permission_denied"
		f.Summary = "Operating system denied access; check local permissions"
	}
	return f
}

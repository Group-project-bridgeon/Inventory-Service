package database

import (
	"context"
	"errors"
	"net"
	"runtime"
	"syscall"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/jackc/pgx/v5/pgconn"
)

// Classify before discarding the cause: raw pgx errors may contain credentials,
// server details or connection strings. Do not retain them in the returned value.
func diagnose(operation string, err error) *logger.Failure {
	f := &logger.Failure{Component: "database", Operation: operation, Reason: "unknown_connection_failure", Summary: "PostgreSQL connection failed; cause could not be classified"}
	var parse *pgconn.ParseConfigError
	var pg *pgconn.PgError
	var dns *net.DNSError
	var network net.Error
	switch {
	case errors.As(err, &parse):
		f.Reason = "invalid_connection_configuration"
		f.Summary = "Invalid PostgreSQL connection configuration; check DATABASE_URL"
	// pgx can wrap an already-canceled context in its timeout type. Check cancellation first.
	case errors.Is(err, context.Canceled):
		f.Reason = "context_canceled"
		f.Summary = "PostgreSQL operation canceled"
	case errors.Is(err, context.DeadlineExceeded):
		f.Reason = "timeout"
		f.Summary = "PostgreSQL operation timed out"
	case errors.As(err, &pg):
		f.Reason = "postgres_server_error"
		f.Summary = "PostgreSQL server rejected the operation"
		// SQLSTATE is server-controlled. Only retain the protocol's five-character code.
		if validSQLState(pg.Code) {
			f.SQLState = pg.Code
		}
		switch pg.Code {
		case "28P01", "28000":
			f.Reason = "authentication_failed"
			f.Summary = "PostgreSQL authentication failed"
		case "3D000":
			f.Reason = "database_missing"
			f.Summary = "PostgreSQL database does not exist"
		case "42501":
			f.Reason = "insufficient_privileges"
			f.Summary = "PostgreSQL privileges are insufficient"
		}
	case pgconn.Timeout(err) || (errors.As(err, &network) && network.Timeout()):
		f.Reason = "timeout"
		f.Summary = "PostgreSQL operation timed out"
	case errors.As(err, &dns):
		f.Reason = "dns_lookup_failed"
		f.Summary = "PostgreSQL hostname lookup failed"
	case errors.Is(err, syscall.ECONNREFUSED) || (runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10061))): // WSAECONNREFUSED
		f.Reason = "connection_refused"
		f.Summary = "PostgreSQL connection refused; check listener and host port"
	}
	return f
}
func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if !(c >= '0' && c <= '9') && !(c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

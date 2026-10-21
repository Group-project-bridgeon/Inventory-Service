package logger

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"net"
)

const RequestLoggerKey = "inventory.requestLogger"
const RequestIDKey = "inventory.requestID"

func Request(c *gin.Context) zerolog.Logger {
	if value, ok := c.Get(RequestLoggerKey); ok {
		if log, ok := value.(zerolog.Logger); ok {
			return log
		}
	}
	return zerolog.Nop()
}
func Route(c *gin.Context) string {
	if c.FullPath() == "" {
		return "unmatched"
	}
	return c.FullPath()
}
func Respond(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"code": code, "error": message, "request_id": c.GetString(RequestIDKey)})
}

// Classify preserves typed causes but never renders them.
func Classify(component, operation string, err error) *Failure {
	f := &Failure{Component: component, Operation: operation, Reason: "unknown_failure", Summary: "Unexpected operation failure", cause: err}
	var pg *pgconn.PgError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		f.Reason = "context_canceled"
		f.Summary = "Operation canceled"
	case errors.Is(err, context.DeadlineExceeded):
		f.Reason = "timeout"
		f.Summary = "Operation timed out"
	case errors.As(err, &pg):
		f.Reason = "postgres_server_error"
		f.Summary = "Database operation failed"
		if len(pg.Code) == 5 {
			valid := true
			for _, r := range pg.Code {
				if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
					valid = false
				}
			}
			if valid {
				f.SQLState = pg.Code
			}
		}
		if pg.Code == "57014" {
			f.Reason = "query_canceled"
			f.Summary = "Database query canceled"
		}
	case pgconn.Timeout(err) || (errors.As(err, &network) && network.Timeout()):
		f.Reason = "timeout"
		f.Summary = "Operation timed out"
	}
	return f
}
func DiagnoseHTTP(c *gin.Context, operation string, err error) *Failure {
	f := Classify("handler", operation, err)
	var known *Failure
	if errors.As(err, &known) && known != nil {
		f = known
	}
	if c.GetBool("inventory.diagnosed") {
		return f
	}
	c.Set("inventory.diagnosed", true)
	log := Request(c)
	event := log.Error()
	if f.Reason == "context_canceled" {
		event = log.Debug()
	}
	event.Str("component", f.Component).Str("operation", f.Operation).Str("reason", f.Reason)
	if f.SQLState != "" {
		event.Str("sqlstate", f.SQLState)
	}
	event.Msg(f.Summary)
	return f
}

package logger

import (
	"errors"
	"github.com/rs/zerolog"
)

// Failure contains only safe metadata and a safe summary, never a raw cause.
// Construct it with fixed strings or validated, value-free configuration errors.
type Failure struct {
	Component string
	Operation string
	Reason    string
	SQLState  string
	Summary   string
}

func (f *Failure) Error() string { return f.Summary }

// LogFailure is called once at the process boundary, after application cleanup.
// Even a wrapper around Failure may contain secrets: log only the safe payload.
func LogFailure(log zerolog.Logger, err error) {
	f := &Failure{Component: "application", Operation: "run", Reason: "unknown_failure", Summary: "Application failed; cause could not be classified"}
	var known *Failure
	if errors.As(err, &known) && known != nil {
		f = known
	}
	event := log.Error().Str("component", f.Component).Str("operation", f.Operation).Str("reason", f.Reason).Str("error", f.Summary)
	if f.SQLState != "" {
		event.Str("sqlstate", f.SQLState)
	}
	event.Msg("inventory service failed")
}

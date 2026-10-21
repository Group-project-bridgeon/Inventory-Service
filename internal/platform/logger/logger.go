package logger

import (
	"github.com/rs/zerolog"
	"os"
)

func New() zerolog.Logger {
	return zerolog.New(os.Stdout).Level(zerolog.InfoLevel).With().Timestamp().Str("service", "inventory").Logger()
}

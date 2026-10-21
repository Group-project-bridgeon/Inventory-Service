package logger

import (
	"bytes"
	"errors"
	"github.com/rs/zerolog"
	"strings"
	"testing"
)

func TestUnknownFailureDoesNotSerializeRawError(t *testing.T) {
	var b bytes.Buffer
	LogFailure(zerolog.New(&b), errors.New("postgres://user:FAKE_PASSWORD@host/db SENSITIVE_DETAIL"))
	if strings.Contains(b.String(), "FAKE_PASSWORD") || strings.Contains(b.String(), "SENSITIVE_DETAIL") || !strings.Contains(b.String(), "unknown_failure") {
		t.Fatal("unsafe fallback")
	}
	if strings.Count(b.String(), "\n") != 1 {
		t.Fatal("expected exactly one event")
	}
}

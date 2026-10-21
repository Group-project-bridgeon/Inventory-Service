package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvalidWarehouseRequestsStopBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewWarehouseHandler(nil) // Any service call panics and fails the test.
	for _, tc := range []struct {
		name, id, body string
		handler        gin.HandlerFunc
	}{
		{"missing id", "", "", h.GetByID}, {"invalid id", "bad", "", h.GetByID},
		{"zero id", "00000000-0000-0000-0000-000000000000", "", h.GetByID},
		{"immutable code", "11111111-1111-4111-8111-111111111111", `{"name":"N","code":"NEW"}`, h.Update},
		{"missing status", "11111111-1111-4111-8111-111111111111", `{}`, h.SetActive},
		{"null status", "11111111-1111-4111-8111-111111111111", `{"is_active":null}`, h.SetActive},
		{"unknown field", "", `{"code":"A","name":"N","unknown":true}`, h.Create},
		{"trailing JSON", "", `{"code":"A","name":"N"} {}`, h.Create},
		{"trailing junk", "", `{"code":"A","name":"N"} junk`, h.Create},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			c.Params = gin.Params{{Key: "id", Value: tc.id}}
			tc.handler(c)
			if w.Code != 400 {
				t.Fatalf("HTTP %d", w.Code)
			}
		})
	}
}

func TestWarehouseErrorResponsesAndSafeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrInvalidArgument, 400, "invalid_input"}, {domain.ErrNotFound, 404, "warehouse_not_found"},
		{domain.ErrDuplicateCode, 409, "warehouse_code_exists"}, {domain.ErrDefaultConflict, 409, "default_warehouse_conflict"},
		{domain.ErrInactiveDefault, 409, "inactive_default"}, {context.Canceled, 408, "request_canceled"},
		{context.DeadlineExceeded, 504, "request_timeout"}, {errors.New("SECRET-CANARY"), 500, "internal_error"},
		{&pgconn.PgError{Code: "57014", Message: "SECRET-CANARY"}, 504, "request_timeout"},
		{&pgconn.PgError{Code: "23514", ConstraintName: "warehouses_unknown", Message: "SECRET-CANARY", Detail: "SECRET-CANARY", Hint: "SECRET-CANARY"}, 500, "internal_error"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			var logs bytes.Buffer
			log := zerolog.New(&logs).With().Str("service", "inventory").Str("request_id", "test-id").Logger()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(logger.RequestLoggerKey, log)
			c.Set(logger.RequestIDKey, "test-id")
			writeError(c, "test_operation", fmt.Errorf("wrapped: %w", tc.err))
			var body map[string]string
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != tc.status || body["code"] != tc.code || body["request_id"] != "test-id" {
				t.Fatal("incorrect public error contract")
			}
			if strings.Contains(logs.String()+w.Body.String(), "SECRET-CANARY") {
				t.Fatal("secret escaped")
			}
			if tc.status >= 500 || tc.status == 408 {
				logger.DiagnoseHTTP(c, "test_operation", tc.err)
				if bytes.Count(logs.Bytes(), []byte("\n")) != 1 {
					t.Fatal("diagnostic was not emitted exactly once")
				}
				var event map[string]any
				if json.Unmarshal(logs.Bytes(), &event) != nil {
					t.Fatal("invalid log JSON")
				}
				for _, key := range []string{"service", "component", "operation", "reason", "request_id", "message"} {
					if event[key] == nil {
						t.Fatalf("missing %s", key)
					}
				}
				var pg *pgconn.PgError
				if errors.As(tc.err, &pg) && event["sqlstate"] != pg.Code {
					t.Fatal("missing safe SQLSTATE")
				}
			}
		})
	}
}

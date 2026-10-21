package router

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"net/http/httptest"
	"testing"
	"time"
)

type checker func(context.Context) error

func (f checker) Ping(c context.Context) error { return f(c) }
func TestProbes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		path   string
		fail   bool
		code   int
		status string
	}{{"/health", true, 200, "ok"}, {"/ready", false, 200, "ready"}, {"/ready", true, 503, "not_ready"}} {
		t.Run(tc.path+tc.status, func(t *testing.T) {
			calls := 0
			db := checker(func(ctx context.Context) error {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > time.Second {
					t.Error("missing bounded context")
				}
				if tc.fail {
					return errors.New("private database details")
				}
				return nil
			})
			r := New(db, zerolog.Nop(), config.HTTPConfig{MaxBodyBytes: 1024, ReadinessTimeout: time.Second})
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("status=%d", rec.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 2 || body["status"] != tc.status || body["service"] != "inventory" {
				t.Fatalf("unexpected response %s", rec.Body.String())
			}
			if tc.path == "/health" && calls != 0 {
				t.Fatal("health queried DB")
			}
		})
	}
}
func TestReadinessCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "parent"}[cancelParent], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelParent {
				cancel()
			}
			var observed error
			r := New(checker(func(ctx context.Context) error { <-ctx.Done(); observed = ctx.Err(); return observed }), zerolog.Nop(), config.HTTPConfig{MaxBodyBytes: 1024, ReadinessTimeout: 10 * time.Millisecond})
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest("GET", "/ready", nil).WithContext(ctx))
			expected := context.DeadlineExceeded
			if cancelParent {
				expected = context.Canceled
			}
			if !errors.Is(observed, expected) || rec.Code != 503 {
				t.Fatalf("cancellation: %v status %d", observed, rec.Code)
			}
		})
	}
}

package middleware

import (
	"bytes"
	"errors"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRecoveryDoesNotLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	r := gin.New()
	r.Use(Recovery(zerolog.New(&logs)))
	r.GET("/panic", func(c *gin.Context) { panic("secret-token") })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/panic?password=secret-token", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "internal server error") || strings.Contains(rec.Body.String()+logs.String(), "secret-token") {
		t.Fatalf("unsafe recovery response/log")
	}
	if !strings.Contains(logs.String(), "stack") {
		t.Fatal("missing diagnostics")
	}
}

func TestRequestIDCorrelationAndRejection(t *testing.T) {
	for _, tc := range []struct {
		name, supplied string
		accepted       bool
	}{{"generated", "", false}, {"valid", "req-abc_123", true}, {"rejected", "bad id?", false}, {"too long", strings.Repeat("a", 65), false}} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			r := gin.New()
			r.Use(RequestLogger(zerolog.New(&logs), time.Hour))
			r.GET("/items/:id", func(c *gin.Context) {
				err := errors.New("postgres://user:FAKE_PASSWORD database detail SECRET_DETAIL token SECRET_TOKEN")
				logger.DiagnoseHTTP(c, "get_item", err)
				logger.Respond(c, 500, "internal_error", "internal server error")
			})
			req := httptest.NewRequest("GET", "/items/1", nil)
			if tc.supplied != "" {
				req.Header.Set("X-Request-ID", tc.supplied)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			id := rec.Header().Get("X-Request-ID")
			if id == "" || (tc.accepted && id != tc.supplied) || (!tc.accepted && id == tc.supplied) {
				t.Fatalf("unexpected request id %q", id)
			}
			if !strings.Contains(rec.Body.String(), `"request_id":"`+id+`"`) || !strings.Contains(logs.String(), `"request_id":"`+id+`"`) {
				t.Fatal("request ID missing from response or diagnostic")
			}
			for _, secret := range []string{"FAKE_PASSWORD", "SECRET_DETAIL", "SECRET_TOKEN", "postgres://"} {
				if strings.Contains(rec.Body.String()+logs.String(), secret) {
					t.Fatalf("secret leaked: %s", secret)
				}
			}
			if !strings.Contains(logs.String(), `"reason":"unknown_failure"`) {
				t.Fatal("unknown safe classification missing")
			}
			if strings.Count(logs.String(), `"reason":"unknown_failure"`) != 1 {
				t.Fatal("unexpected failure was diagnostically logged more than once")
			}
		})
	}
}
func TestBodyLimits(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		unknown, read bool
		code          int
	}{{"known oversized", "12345", false, false, 413}, {"stream oversized", "12345", true, true, 413}, {"exact limit", "1234", true, true, 204}, {"unread stream", "12345", true, false, 204}} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(MaxBodyBytes(4))
			r.POST("/read", func(c *gin.Context) {
				if tc.read {
					_, err := io.ReadAll(c.Request.Body)
					var tooLarge *http.MaxBytesError
					if errors.As(err, &tooLarge) {
						c.Status(413)
						return
					}
					if err != nil {
						t.Error(err)
					}
				}
				c.Status(204)
			})
			req := httptest.NewRequest("POST", "/read", strings.NewReader(tc.body))
			if tc.unknown {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.code {
				t.Fatalf("got %d want %d", rec.Code, tc.code)
			}
		})
	}
}
func TestRequestLogging(t *testing.T) {
	for _, tc := range []struct {
		path   string
		status int
		slow   time.Duration
		logged bool
	}{{"/health", 200, 0, false}, {"/ready", 503, 0, true}, {"/test", 200, time.Hour, false}, {"/test", 400, time.Hour, false}, {"/test", 500, time.Hour, true}, {"/test", 200, 0, true}} {
		var logs bytes.Buffer
		r := gin.New()
		r.Use(RequestLogger(zerolog.New(&logs), tc.slow))
		r.GET(tc.path, func(c *gin.Context) { c.Status(tc.status) })
		req := httptest.NewRequest("GET", tc.path+"?token=private-token", nil)
		req.Header.Set("Authorization", "Bearer private-token")
		r.ServeHTTP(httptest.NewRecorder(), req)
		if (logs.Len() > 0) != tc.logged || strings.Contains(logs.String(), "private-token") {
			t.Fatalf("unexpected logs %s", logs.String())
		}
	}
}

func TestProbeFailureLoggingIsRateBounded(t *testing.T) {
	var logs bytes.Buffer
	r := gin.New()
	r.Use(RequestLogger(zerolog.New(&logs), time.Hour))
	r.GET("/ready", func(c *gin.Context) { c.Status(http.StatusServiceUnavailable) })
	for i := 0; i < 3; i++ {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ready", nil))
	}
	if lines := strings.Count(strings.TrimSpace(logs.String()), "\n") + 1; lines != 1 || !strings.Contains(logs.String(), `"reason":"probe_failed"`) {
		t.Fatalf("probe failures not bounded: %s", logs.String())
	}
}

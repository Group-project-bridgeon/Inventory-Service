package middleware

import (
	"bytes"
	"errors"
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
	}{{"/health", 200, 0, false}, {"/ready", 503, 0, false}, {"/test", 200, time.Hour, false}, {"/test", 400, time.Hour, true}, {"/test", 500, time.Hour, true}, {"/test", 200, 0, true}} {
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

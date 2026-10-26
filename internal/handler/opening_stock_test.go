package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/gin-gonic/gin"
)

func TestOpeningStrictInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	line := `{"line_number":1,"variant_id":"01935f00-0000-7000-8000-000000000002","input_count":"100","expected_stock_reference_revision":"cat-stock-v1:` + strings.Repeat("a", 64) + `"}`
	body := `{"operation_id":"01935f00-0000-7000-8000-000000000011","lines":[` + line + `]}`
	tests := map[string]struct {
		body   string
		status int
	}{"valid": {body, 200}, "fraction": {strings.Replace(body, `"100"`, `"1.5"`, 1), 400}, "zero": {strings.Replace(body, `"100"`, `"0"`, 1), 400}, "negative": {strings.Replace(body, `"100"`, `"-1"`, 1), 400}, "max": {strings.Replace(body, `"100"`, `"9223372036854775807"`, 1), 200}, "overflow": {strings.Replace(body, `"100"`, `"9223372036854775808"`, 1), 400}, "number": {strings.Replace(body, `"100"`, `100`, 1), 400}, "unknown": {strings.Replace(body, `"lines":`, `"actor":"spoof","lines":`, 1), 400}, "duplicate": {strings.Replace(body, `"input_count":"100"`, `"input_count":"100","input_count":"1"`, 1), 400}, "null": {strings.Replace(body, `"100"`, `null`, 1), 400}, "null optional": {strings.Replace(body, `"input_count":`, `"pack_id":null,"input_count":`, 1), 400}, "case": {strings.Replace(body, "input_count", "Input_Count", 1), 400}, "trailing": {body + `{}`, 400}, "oversized": {body + strings.Repeat(" ", 65536), 413}, "duplicate variant": {strings.Replace(body, line, line+","+strings.Replace(line, `"line_number":1`, `"line_number":2`, 1), 1), 400}}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = gin.Params{{Key: "id", Value: "01935f00-0000-7000-8000-000000000001"}}
			c.Request = httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("Idempotency-Key", "01935f00-0000-7000-8000-000000000010")
			_, _, _, _, ok := openingInput(c, false)
			if ok != (tt.status == 200) || w.Code != tt.status {
				t.Fatalf("status=%d ok=%v", w.Code, ok)
			}
		})
	}
	for _, n := range []int{0, 1, 100, 101} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ls := make([]string, n)
			for i := range ls {
				ls[i] = fmt.Sprintf(`{"line_number":%d,"variant_id":"01935f00-0000-7000-8000-%012d","input_count":"1"}`, i+1, i+1)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = gin.Params{{Key: "id", Value: "01935f00-0000-7000-8000-000000000001"}}
			c.Request = httptest.NewRequest("POST", "/", strings.NewReader(`{"lines":[`+strings.Join(ls, ",")+`]}`))
			c.Request.Header.Set("Content-Type", "application/json")
			_, _, _, _, ok := openingInput(c, true)
			if ok != (n >= 1 && n <= 100) {
				t.Fatal("line bound")
			}
		})
	}
}
func TestOpeningUnknownPrecedesTimeout(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	openingError(c, errors.Join(stock.ErrOutcomeUnknown, context.DeadlineExceeded))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "outcome_unknown") {
		t.Fatal("unknown outcome lost")
	}
}

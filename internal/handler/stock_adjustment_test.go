package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAdjustmentStrictEnvelope(t *testing.T) {
	valid := `{"direction":"IN","reason_code":"FOUND","explanation":"Found pieces","lines":[{"line_number":1,"variant_id":"01935f00-0000-7000-8000-000000000002","input_count":"5"}]}`
	for _, body := range []string{strings.Replace(valid, `"direction":`, `"actor":"spoof","direction":`, 1), strings.Replace(valid, `"direction":"IN"`, `"direction":"IN","direction":"OUT"`, 1), strings.Replace(valid, `"input_count":"5"`, `"input_count":null`, 1), strings.Replace(valid, `"input_count":"5"`, `"pieces_per_input":"6","input_count":"5"`, 1), valid + `{}`, `null`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "01935f00-0000-7000-8000-000000000001"}}
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		var dst adjustmentRequest
		_, ok := adjustmentInput(c, &dst)
		if ok || w.Code != 400 {
			t.Fatal("untrusted/ambiguous input accepted")
		}
	}
	for _, body := range []string{`{"original_operation_id":"x","reason_code":"OTHER","explanation":"Reason","lines":[]}`, `{"original_operation_id":"x","reason_code":"OTHER","explanation":"Reason","direction":"IN"}`, `{"original_operation_id":"x","reason_code":"OTHER","explanation":"Reason","effective_at":"2020-01-01"}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "01935f00-0000-7000-8000-000000000001"}}
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		var dst correctionRequest
		_, ok := adjustmentInput(c, &dst)
		if ok {
			t.Fatal("caller reversal facts accepted")
		}
	}
}

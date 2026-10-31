package handler

import (
	f "github.com/Group-project-bridgeon/Inventory-Service/internal/fulfillment"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrictFulfillmentRequest(t *testing.T) {
	good := `{"command_id":"01935f00-0000-7000-8000-000000000031","dispatch_id":"01935f00-0000-7000-8000-000000000032","reservation_id":"01935f00-0000-7000-8000-000000000012","order_id":"01935f00-0000-7000-8000-000000000013","generation":"1","expected_reservation_version":"1","expected_order_version":"1"}`
	for i, body := range []string{good, strings.Replace(good, `"generation":"1"`, `"generation":1`, 1), strings.Replace(good, `"generation":"1"`, `"generation":"1.5"`, 1), strings.Replace(good, `"generation":"1"`, `"generation":null`, 1), strings.Replace(good, `"generation":"1"`, `"generation":"1","generation":"2"`, 1), strings.Replace(good, `"generation":"1"`, `"generation":"1","actor_user_id":"spoof"`, 1), strings.Replace(good, `"generation":"1"`, `"generation":"1","expected_dispatch_version":"0"`, 1), good + `{}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "01935f00-0000-7000-8000-000000000001"}}
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("Idempotency-Key", "01935f00-0000-7000-8000-000000000035")
		_, ok := parseFulfillment(c, f.Create)
		if ok != (i == 0) {
			t.Fatal("strict create shape", i)
		}
	}
}

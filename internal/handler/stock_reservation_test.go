package handler

import (
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReservationStrictHTTPIntent(t *testing.T) {
	body := `{"command_id":"01935f00-0000-7000-8000-000000000011","reservation_id":"01935f00-0000-7000-8000-000000000012","order_id":"01935f00-0000-7000-8000-000000000013","generation":"1","expected_previous_generation":"0","expected_order_version":"0","lines":[{"order_line_id":"01935f00-0000-7000-8000-000000000014","variant_id":"01935f00-0000-7000-8000-000000000002","pieces":"10"}]}`
	cases := []struct {
		body string
		ok   bool
	}{{body, true}, {strings.Replace(body, `"pieces":"10"`, `"pieces":"0"`, 1), false}, {strings.Replace(body, `"pieces":"10"`, `"pieces":"-1"`, 1), false}, {strings.Replace(body, `"pieces":"10"`, `"pieces":"1.5"`, 1), false}, {strings.Replace(body, `"pieces":"10"`, `"pieces":"9223372036854775808"`, 1), false}, {strings.Replace(body, `"pieces":"10"`, `"pieces":10`, 1), false}, {strings.Replace(body, `"pieces":"10"`, `"pieces":"10","pieces":"20"`, 1), false}, {strings.Replace(body, `"pieces":"10"`, `"pack_id":"x","pieces":"10"`, 1), false}, {strings.Replace(body, `"command_id":`, `"owner_machine_id":"spoof","command_id":`, 1), false}, {strings.Replace(body, `"generation":"1"`, `"generation":null`, 1), false}, {body + `{}`, false}}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "01935f00-0000-7000-8000-000000000001"}}
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("Idempotency-Key", "01935f00-0000-7000-8000-000000000015")
		_, ok := parseReservation(c, stock.Reserve)
		if ok != tc.ok {
			t.Fatal("strict reservation intent")
		}
	}
	terminal := `{"command_id":"01935f00-0000-7000-8000-000000000011","order_id":"01935f00-0000-7000-8000-000000000013","generation":"1","expected_order_version":"1","expected_reservation_version":"1","lines":[]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "01935f00-0000-7000-8000-000000000001"}, {Key: "reservation_id", Value: "01935f00-0000-7000-8000-000000000012"}}
	c.Request = httptest.NewRequest("POST", "/", strings.NewReader(terminal))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Idempotency-Key", "01935f00-0000-7000-8000-000000000015")
	if _, ok := parseReservation(c, stock.Consume); ok {
		t.Fatal("terminal accepted caller lines")
	}
}

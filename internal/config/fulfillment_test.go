package config

import "testing"

func TestFulfillmentGate(t *testing.T) {
	isolated(t)
	t.Setenv("STOCK_REFERENCE_ENABLED", "false")
	t.Setenv("STOCK_RESERVATIONS_ENABLED", "false")
	t.Setenv("STOCK_FULFILLMENT_ENABLED", "")
	c, e := Load()
	if e != nil || c.StockFulfillmentEnabled {
		t.Fatal("default off", e)
	}
	for _, v := range []string{"true", "TRUE", "1"} {
		t.Setenv("STOCK_FULFILLMENT_ENABLED", v)
		if _, e := Load(); e == nil {
			t.Fatal("invalid gate/dependencies")
		}
	}
}

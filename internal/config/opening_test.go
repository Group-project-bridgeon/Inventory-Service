package config

import "testing"

func TestOpeningEnableGate(t *testing.T) {
	isolated(t)
	t.Setenv("STOCK_REFERENCE_ENABLED", "false")
	t.Setenv("OPENING_STOCK_ENABLED", "false")
	c, e := Load()
	if e != nil || c.OpeningStockEnabled {
		t.Fatal("disabled default")
	}
	for _, v := range []string{"true", "yes"} {
		t.Setenv("OPENING_STOCK_ENABLED", v)
		if _, e := Load(); e == nil {
			t.Fatal("missing dependencies or invalid flag accepted")
		}
	}
}

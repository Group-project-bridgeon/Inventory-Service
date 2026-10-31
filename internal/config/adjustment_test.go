package config

import "testing"

func TestAdjustmentEnableGate(t *testing.T) {
	isolated(t)
	t.Setenv("STOCK_ADJUSTMENTS_ENABLED", "false")
	t.Setenv("OPENING_STOCK_ENABLED", "false")
	t.Setenv("STOCK_REFERENCE_ENABLED", "false")
	cfg, err := Load()
	if err != nil || cfg.StockAdjustmentsEnabled {
		t.Fatal("default gate", err)
	}
	for _, v := range []string{"true", "TRUE", "1"} {
		t.Setenv("STOCK_ADJUSTMENTS_ENABLED", v)
		if _, err = Load(); err == nil {
			t.Fatal("invalid/missing reference configuration accepted")
		}
	}
}

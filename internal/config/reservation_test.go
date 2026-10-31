package config

import "testing"

func TestReservationEnableGate(t *testing.T) {
	isolated(t)
	t.Setenv("STOCK_REFERENCE_ENABLED", "false")
	t.Setenv("STOCK_RESERVATIONS_ENABLED", "")
	cfg, err := Load()
	if err != nil || cfg.StockReservationsEnabled {
		t.Fatal("default off", err)
	}
	for _, v := range []string{"true", "TRUE", "1"} {
		t.Setenv("STOCK_RESERVATIONS_ENABLED", v)
		if _, err = Load(); err == nil {
			t.Fatal("missing dependencies or invalid flag accepted")
		}
	}
}

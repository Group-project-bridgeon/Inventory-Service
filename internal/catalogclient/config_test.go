package catalogclient

import (
	"errors"
	"net/http"
	"os"
	"testing"
)

func TestOptionalConfiguration(t *testing.T) {
	keys := []string{"STOCK_REFERENCE_ENABLED", "STOCK_AUTH_TOKEN_URL", "STOCK_CATALOG_BATCH_URL", "STOCK_MACHINE_ID", "STOCK_CREDENTIAL_SECRET_FILE", "STOCK_CA_FILE", "STOCK_AUTH_ISSUER", "STOCK_TOKEN_TIMEOUT", "STOCK_REFERENCE_TIMEOUT", "STOCK_TOKEN_MAX_BYTES", "STOCK_REFERENCE_MAX_BYTES"}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	t.Setenv("STOCK_AUTH_TOKEN_URL", "invalid ignored while disabled")
	if c, err := LoadConfig(); err != nil || c.Enabled {
		t.Fatal("disabled integration changed startup")
	}
	t.Setenv("STOCK_REFERENCE_ENABLED", "true")
	if _, err := LoadConfig(); !errors.Is(err, ErrConfiguration) {
		t.Fatal("incomplete enabled integration accepted")
	}
	cfg, _ := fixtureConfig(t, func(http.ResponseWriter, *http.Request) {})
	values := map[string]string{"STOCK_AUTH_TOKEN_URL": cfg.TokenURL, "STOCK_CATALOG_BATCH_URL": cfg.BatchURL, "STOCK_MACHINE_ID": cfg.ClientID, "STOCK_CREDENTIAL_SECRET_FILE": cfg.SecretFile, "STOCK_CA_FILE": cfg.CAFile, "STOCK_AUTH_ISSUER": cfg.Issuer}
	for key, value := range values {
		t.Setenv(key, value)
	}
	if _, err := LoadConfig(); err != nil {
		t.Fatal("valid configuration rejected")
	}
	for key, value := range map[string]string{"STOCK_REFERENCE_ENABLED": "yes", "STOCK_TOKEN_TIMEOUT": "6s", "STOCK_REFERENCE_TIMEOUT": "0s", "STOCK_TOKEN_MAX_BYTES": "999999", "STOCK_REFERENCE_MAX_BYTES": "0", "STOCK_AUTH_TOKEN_URL": cfg.TokenURL + "?secret=synthetic", "STOCK_CATALOG_BATCH_URL": "http://localhost/api/v1/catalog/stock-references/batch"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := LoadConfig(); !errors.Is(err, ErrConfiguration) {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	bad := cfg
	bad.SecretFile = cfg.SecretFile + ".missing"
	if _, err := New(bad); !errors.Is(err, ErrConfiguration) {
		t.Fatal("missing secret file accepted")
	}
	if err := os.WriteFile(cfg.CAFile, []byte("invalid synthetic CA"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg); !errors.Is(err, ErrConfiguration) {
		t.Fatal("invalid trust bundle accepted")
	}
}

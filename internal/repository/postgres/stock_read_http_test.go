package postgres

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/config"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/router"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func TestStockReadRealHTTPAndPostgres(t *testing.T) {
	f := stockReadFixture(t)
	f.post(t, f.command(stock.In, math.MaxInt64))
	pool := readOnlyStockPool(t, f)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	postingOK(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	postingOK(t, err)
	path := filepath.Join(t.TempDir(), "public.pem")
	postingOK(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600))
	v, err := security.Load(path, "erp-auth", "erp-api")
	postingOK(t, err)
	warehouses := NewWarehouseRepository(pool)
	server := httptest.NewServer(router.NewWithBusiness(pool, zerolog.Nop(), config.HTTPConfig{MaxBodyBytes: 1 << 20, ReadinessTimeout: time.Second, SlowRequestThreshold: time.Hour}, v, warehouses, service.NewStockReadService(NewStockReadRepository(pool), warehouses, time.Second)))
	defer server.Close()
	now := time.Now().Unix()
	id := uuid.NewString()
	raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"user_id": id, "sub": id, "email": "fixture@example.invalid", "token_version": 0, "permissions": []string{"inventory.stock-balances:read", "inventory.stock-movements:read"}, "iss": "erp-auth", "aud": []string{"erp-api"}, "iat": now, "nbf": now, "exp": now + 300}).SignedString(key)
	postingOK(t, err)
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	before := readFacts(t, f)
	for _, endpoint := range []string{"stock-balances", "stock-movements"} {
		req, err := http.NewRequestWithContext(f.ctx, "GET", server.URL+"/api/v1/inventory/warehouses/"+f.warehouse.String()+"/"+endpoint, nil)
		postingOK(t, err)
		req.Header.Set("Authorization", "Bearer "+raw)
		response, err := client.Do(req)
		postingOK(t, err)
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		postingOK(t, readErr)
		if response.StatusCode != 200 || response.Header.Get("X-Request-ID") == "" || !strings.Contains(string(data), `"9223372036854775807"`) {
			t.Fatal("actual HTTP precision/status contract")
		}
		var decoded struct {
			Items []map[string]any `json:"items"`
		}
		postingOK(t, json.Unmarshal(data, &decoded))
		if len(decoded.Items) != 1 {
			t.Fatal("HTTP history/balance count")
		}
	}
	if readFacts(t, f) != before {
		t.Fatal("HTTP read changed rows")
	}
	t.Log("PASS: actual loopback HTTP -> human JWT middleware -> handler/service -> read-only PostgreSQL -> exact decimal-string response (fixture-signed human token)")
}

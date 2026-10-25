package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/config"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type httpStockReader struct {
	calls int
	empty bool
	err   error
}

func (s *httpStockReader) ListBalances(ctx context.Context, p repository.ListStockBalancesParams) ([]domain.StockBalanceView, error) {
	s.calls++
	if s.empty || s.err != nil {
		return nil, s.err
	}
	return []domain.StockBalanceView{{WarehouseID: p.WarehouseID, VariantID: uuid.New(), BaseUnitID: uuid.New(), OnHandPieces: math.MaxInt64, Version: math.MaxInt64}}, nil
}
func (s *httpStockReader) ListMovements(ctx context.Context, p repository.ListStockMovementsParams) ([]domain.StockMovementView, error) {
	s.calls++
	if s.empty || s.err != nil {
		return nil, s.err
	}
	return []domain.StockMovementView{{ID: uuid.New(), WarehouseID: p.WarehouseID, InputCount: math.MaxInt64, PiecesPerInput: 1, Pieces: math.MaxInt64, BeforePieces: 0, AfterPieces: math.MaxInt64, BalanceVersion: math.MaxInt64, ProductName: "Recorded product"}}, nil
}

type httpReadWarehouse struct {
	warehouseStub
	missing bool
}

func (s *httpReadWarehouse) GetByID(ctx context.Context, id uuid.UUID) (domain.Warehouse, error) {
	if s.missing {
		return domain.Warehouse{}, domain.ErrNotFound
	}
	return domain.Warehouse{ID: id, IsActive: false}, nil
}

func TestStockReadHTTPProtectionAndContract(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "public.pem")
	if os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600) != nil {
		t.Fatal("fixture key")
	}
	v, err := security.Load(path, "erp-auth", "erp-api")
	if err != nil {
		t.Fatal(err)
	}
	token := func(permission string, machine, expired bool) string {
		now := time.Now().Unix()
		id := uuid.NewString()
		claims := jwt.MapClaims{"user_id": id, "sub": id, "email": "fixture@example.invalid", "token_version": 0, "permissions": []string{permission}, "iss": "erp-auth", "aud": []string{"erp-api"}, "iat": now, "nbf": now, "exp": now + 300}
		if expired {
			claims["iat"] = now - 600
			claims["nbf"] = now - 600
			claims["exp"] = now - 300
		}
		if machine {
			delete(claims, "user_id")
			delete(claims, "token_version")
			claims["sub"] = "machine:" + id
			claims["client_id"] = id
			claims["token_kind"] = "machine"
			claims["aud"] = []string{"catalog-stock-references"}
		}
		raw, e := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	w := &httpReadWarehouse{}
	reader := &httpStockReader{}
	var logs bytes.Buffer
	r := NewWithBusiness(readyDB{}, zerolog.New(&logs), config.HTTPConfig{MaxBodyBytes: 1 << 20, ReadinessTimeout: time.Second, SlowRequestThreshold: time.Hour}, v, w, service.NewStockReadService(reader, w, time.Second))
	request := func(path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		res := httptest.NewRecorder()
		r.ServeHTTP(res, req)
		if res.Header().Get("X-Request-ID") == "" {
			t.Fatal("request id missing")
		}
		return res
	}
	for _, route := range []struct{ path, permission string }{{"stock-balances", "inventory.stock-balances:read"}, {"stock-movements", "inventory.stock-movements:read"}} {
		base := "/api/v1/inventory/warehouses/" + uuid.NewString() + "/" + route.path
		good := token(route.permission, false, false)
		for _, tc := range []struct {
			token  string
			status int
		}{{"", 401}, {"malformed", 401}, {token(route.permission, false, true), 401}, {token("inventory.warehouses:read", false, false), 403}, {token(route.permission, true, false), 401}} {
			before := reader.calls
			if res := request(base, tc.token); res.Code != tc.status || reader.calls != before {
				t.Fatal("stock authentication/permission boundary")
			}
		}
		res := request(base, good)
		if res.Code != 200 || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("authorized read failed")
		}
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if json.Unmarshal(res.Body.Bytes(), &body) != nil || len(body.Items) != 1 {
			t.Fatal("response shape")
		}
		field := "on_hand_pieces"
		if route.path == "stock-movements" {
			field = "pieces"
		}
		if body.Items[0][field] != "9223372036854775807" {
			t.Fatal("BIGINT precision lost")
		}
		for _, sensitive := range []string{"request_hash", "actor_user_id", "first_request_id", "result_version", "idempotency_key"} {
			if _, ok := body.Items[0][sensitive]; ok {
				t.Fatal("internal metadata exposed")
			}
		}
		reader.empty = true
		res = request(base, good)
		if res.Code != 200 || !strings.Contains(res.Body.String(), `"items":[]`) {
			t.Fatal("empty array contract")
		}
		reader.empty = false
		w.missing = true
		if request(base, good).Code != 404 {
			t.Fatal("missing warehouse")
		}
		w.missing = false
		for _, query := range []string{"limit=0", "limit=101", "limit=-1", "limit=1.5", "limit=", "limit=1&limit=2", "cursor=bad", "cursor=", "cursor=" + strings.Repeat("a", 1025), "variant_id=bad", "variant_id=00000000-0000-0000-0000-000000000000", "unknown=value", "variant_id=%zz", "cursor=a;limit=2"} {
			before := reader.calls
			if request(base+"?"+query, good).Code != 400 || reader.calls != before {
				t.Fatalf("invalid query reached reader: %s", query)
			}
		}
		for _, query := range []string{"nonzero_only=1", "nonzero_only=TRUE", "nonzero_only=false&nonzero_only=true"} {
			if request(base+"?"+query, good).Code != 400 {
				t.Fatal("invalid boolean")
			}
		}
		if route.path == "stock-movements" && request(base+"?nonzero_only=false", good).Code != 400 {
			t.Fatal("unsupported filter accepted")
		}
		reader.err = context.DeadlineExceeded
		if request(base, good).Code != 504 {
			t.Fatal("timeout convention")
		}
		reader.err = nil
		if strings.Contains(logs.String(), good) {
			t.Fatal("token logged")
		}
	}
	for _, path := range []string{"/health", "/ready"} {
		if request(path, "").Code != 200 {
			t.Fatal("probe regression")
		}
	}
	if request("/api/v1/inventory/warehouses", token("inventory.warehouses:read", false, false)).Code != 200 {
		t.Fatal("warehouse route regression")
	}
}

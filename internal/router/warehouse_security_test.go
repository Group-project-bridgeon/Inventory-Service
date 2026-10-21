package router

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
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
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type warehouseStub struct {
	calls int
	row   domain.Warehouse
}

func (s *warehouseStub) Create(_ context.Context, p repository.CreateWarehouseParams) (domain.Warehouse, error) {
	s.calls++
	s.row = domain.Warehouse{ID: p.ID, Code: p.Code, Name: p.Name, IsActive: true}
	return s.row, nil
}
func (s *warehouseStub) GetByID(context.Context, uuid.UUID) (domain.Warehouse, error) {
	s.calls++
	return s.row, nil
}
func (s *warehouseStub) List(context.Context, repository.ListWarehousesParams) ([]domain.Warehouse, error) {
	s.calls++
	return []domain.Warehouse{}, nil
}
func (s *warehouseStub) Update(context.Context, repository.UpdateWarehouseParams) (domain.Warehouse, error) {
	s.calls++
	return s.row, nil
}
func (s *warehouseStub) SetActive(context.Context, uuid.UUID, bool) (domain.Warehouse, error) {
	s.calls++
	return s.row, nil
}

type readyDB struct{}

func (readyDB) Ping(context.Context) error { return nil }

func TestWarehouseRoutesRequireExactPermissionBeforeHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "public.pem")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := security.Load(path, "erp-auth", "erp-api")
	if err != nil {
		t.Fatal(err)
	}
	token := func(perms []string) string {
		now := time.Now().Unix()
		claims := jwt.MapClaims{"user_id": "e28b7175-11a0-4ef7-af75-6b3b868a8838", "sub": "e28b7175-11a0-4ef7-af75-6b3b868a8838", "email": "fake@example.invalid", "token_version": 0, "permissions": perms, "iss": "erp-auth", "aud": []string{"erp-api"}, "iat": now, "nbf": now, "exp": now + 900}
		raw, e := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	stub := &warehouseStub{}
	r := NewWithBusiness(readyDB{}, zerolog.Nop(), config.HTTPConfig{MaxBodyBytes: 1 << 20}, v, stub)
	routes := []struct{ method, path, body, permission string }{{"POST", "/api/v1/inventory/warehouses", `{"code":"A","name":"Alpha"}`, "inventory.warehouses:create"}, {"GET", "/api/v1/inventory/warehouses", "", "inventory.warehouses:read"}, {"GET", "/api/v1/inventory/warehouses/11111111-1111-4111-8111-111111111111", "", "inventory.warehouses:read"}, {"PUT", "/api/v1/inventory/warehouses/11111111-1111-4111-8111-111111111111", `{"name":"Alpha","address":""}`, "inventory.warehouses:update"}, {"PATCH", "/api/v1/inventory/warehouses/11111111-1111-4111-8111-111111111111/status", `{"is_active":true}`, "inventory.warehouses:update"}}
	for _, rt := range routes {
		for _, tc := range []struct {
			name, authorization string
			status              int
		}{{"missing", "", 401}, {"wrong permission", "Bearer " + token([]string{"inventory.items:read"}), 403}} {
			t.Run(rt.method+"/"+tc.name, func(t *testing.T) {
				before := stub.calls
				req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
				if tc.authorization != "" {
					req.Header.Set("Authorization", tc.authorization)
				}
				res := httptest.NewRecorder()
				r.ServeHTTP(res, req)
				if res.Code != tc.status || stub.calls != before {
					t.Fatalf("status=%d calls=%d expected=%d", res.Code, stub.calls, before)
				}
				if res.Header().Get("X-Request-ID") == "" {
					t.Fatal("request ID missing")
				}
			})
		}
		res := httptest.NewRecorder()
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
		req.Header.Set("Authorization", "Bearer "+token([]string{rt.permission}))
		r.ServeHTTP(res, req)
		if res.Code >= 400 {
			t.Fatalf("authorized %s %s: %d %s", rt.method, rt.path, res.Code, res.Body.String())
		}
	}
	for _, path := range []string{"/health", "/ready"} {
		res := httptest.NewRecorder()
		r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != 200 {
			t.Fatalf("public probe %s: %d", path, res.Code)
		}
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/unmapped", nil)
	req.Header.Set("Authorization", "Bearer "+token([]string{"inventory.warehouses:read"}))
	r.ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatalf("unmapped route status %d", res.Code)
	}
}

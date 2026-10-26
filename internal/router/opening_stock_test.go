package router

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/config"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type openingUnused struct{}

func (openingUnused) Lookup(context.Context, stock.Principal, stock.Command, stock.RequestFingerprint) (stock.PostedResult, bool, error) {
	panic("unauthorized store")
}
func (openingUnused) Apply(context.Context, stock.Principal, stock.Command, stock.RequestFingerprint, []stock.PreparedLine) (stock.PostedResult, error) {
	panic("unauthorized effect")
}
func (openingUnused) Resolve(context.Context, stock.Command) ([]stock.Observation, error) {
	panic("unauthorized reference")
}
func (openingUnused) Prepare(context.Context, []stock.Selection) ([]stock.Observation, error) {
	panic("unauthorized preparation")
}
func (openingUnused) Freshness() stock.FreshnessPolicy                 { return openingUnused{} }
func (openingUnused) Check(context.Context, []stock.Observation) error { return nil }
func TestOpeningRouteAuthority(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	der, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "public.pem")
	if os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600) != nil {
		t.Fatal("key")
	}
	v, e := security.Load(file, "erp-auth", "erp-api")
	if e != nil {
		t.Fatal(e)
	}
	s, e := service.NewOpeningStockService(openingUnused{}, openingUnused{}, &warehouseStub{})
	if e != nil {
		t.Fatal(e)
	}
	r := NewWithOpening(readyDB{}, zerolog.Nop(), config.HTTPConfig{MaxBodyBytes: 1 << 20, ReadinessTimeout: time.Second, SlowRequestThreshold: time.Hour}, v, &warehouseStub{}, nil, s)
	token := func(permission, kind string) string {
		id := uuid.NewString()
		now := time.Now().Unix()
		c := jwt.MapClaims{"iss": "erp-auth", "aud": []string{"erp-api"}, "sub": id, "user_id": id, "email": "fixture@example.invalid", "token_version": 0, "permissions": []string{permission}, "iat": now, "nbf": now, "exp": now + 300}
		if kind == "expired" {
			c["iat"] = now - 600
			c["nbf"] = now - 600
			c["exp"] = now - 300
		}
		if kind == "machine" {
			delete(c, "user_id")
			delete(c, "token_version")
			c["sub"] = "machine:" + id
			c["token_kind"] = "machine"
			c["client_id"] = id
			c["aud"] = []string{"catalog-stock-references"}
		}
		s, e := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	for _, suffix := range []string{"", "/prepare"} {
		for _, tc := range []struct {
			token  string
			status int
		}{{"", 401}, {"invalid", 401}, {token(stock.OpeningPermission, "expired"), 401}, {token(stock.OpeningPermission, "machine"), 401}, {token("inventory.stock-balances:read", ""), 403}, {token(stock.OpeningPermission, ""), 400}} {
			req := httptest.NewRequest("POST", "/api/v1/inventory/warehouses/"+uuid.NewString()+"/opening-stock"+suffix, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status %d expected %d", w.Code, tc.status)
			}
		}
	}
}

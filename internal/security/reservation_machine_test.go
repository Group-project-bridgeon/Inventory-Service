package security

import (
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReservationMachineAuthorityAndSeparation(t *testing.T) {
	human, key := testVerifier(t)
	verifier := &ReservationMachineVerifier{key: &key.PublicKey, issuer: human.issuer}
	id := uuid.NewString()
	now := time.Now().Unix()
	base := func() jwt.MapClaims {
		return jwt.MapClaims{"iss": human.issuer, "aud": []string{stock.ReservationAudience}, "sub": "machine:" + id, "client_id": id, "token_kind": "machine", "permissions": []string{stock.ReservePermission}, "jti": uuid.NewString(), "iat": now, "nbf": now, "exp": now + 300}
	}
	for name, change := range map[string]func(jwt.MapClaims){"valid": func(jwt.MapClaims) {}, "catalog audience": func(c jwt.MapClaims) { c["aud"] = []string{"catalog-stock-references"} }, "other audience": func(c jwt.MapClaims) { c["aud"] = []string{"erp-api"} }, "extra audience": func(c jwt.MapClaims) { c["aud"] = []string{stock.ReservationAudience, "erp-api"} }, "human kind": func(c jwt.MapClaims) { c["token_kind"] = "human" }, "human fields": func(c jwt.MapClaims) { c["user_id"] = uuid.NewString() }, "overlong within tolerance": func(c jwt.MapClaims) { c["exp"] = now + 301 }, "unknown scope": func(c jwt.MapClaims) { c["permissions"] = []string{"inventory.reservations:*"} }, "duplicate scope": func(c jwt.MapClaims) { c["permissions"] = []string{stock.ReservePermission, stock.ReservePermission} }, "future": func(c jwt.MapClaims) { c["iat"] = now + 60; c["nbf"] = now + 60; c["exp"] = now + 360 }} {
		t.Run(name, func(t *testing.T) {
			c := base()
			change(c)
			token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			_, err = verifier.Verify(token)
			if (err == nil) != (name == "valid") {
				t.Fatal("machine contract")
			}
			if name == "valid" {
				if _, err = human.Verify(token); err == nil {
					t.Fatal("machine accepted as human")
				}
			}
		})
	}
	claims := base()
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range []string{stock.ReservePermission, stock.ConsumePermission} {
		r := gin.New()
		r.Use(ProtectReservations(verifier, permission))
		r.POST("/hold", func(c *gin.Context) { c.Status(204) })
		req := httptest.NewRequest("POST", "/hold", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		want := 403
		if permission == stock.ReservePermission {
			want = 204
		}
		if w.Code != want {
			t.Fatal("exact route authority")
		}
	}
}

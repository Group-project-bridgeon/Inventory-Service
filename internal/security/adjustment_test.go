package security

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestAdjustmentRouteAlternativesPreserveAllOf(t *testing.T) {
	v, key := testVerifier(t)
	for _, tc := range []struct {
		route   string
		perms   []string
		machine bool
		status  int
	}{
		{"/delta", []string{"inventory.stock-adjustments:increase"}, false, 204},
		{"/delta", []string{"inventory.stock-adjustments:decrease"}, false, 204},
		{"/delta", []string{"inventory.opening-stock:post"}, false, 403},
		{"/delta", []string{"inventory.stock-adjustments:*"}, false, 403},
		{"/both", []string{"inventory.stock-corrections:reverse"}, false, 403},
		{"/both", []string{"inventory.stock-corrections:reverse", "inventory.stock-adjustments:decrease"}, false, 204},
		{"/delta", []string{"inventory.stock-adjustments:increase"}, true, 401},
	} {
		claims := validClaims()
		claims["permissions"] = tc.perms
		if tc.machine {
			delete(claims, "user_id")
			claims["sub"] = "machine:01935f00-0000-7000-8000-000000000001"
			claims["token_kind"] = "machine"
			claims["aud"] = []string{"catalog-stock-references"}
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		r := gin.New()
		r.Use(Protect(v, map[string][]string{"POST /delta": {"inventory.stock-adjustments:increase", "inventory.stock-adjustments:decrease"}, "POST /both": {"inventory.stock-corrections:reverse", "inventory.stock-adjustments:decrease"}}, map[string]bool{"POST /delta": true}))
		r.POST("/delta", func(c *gin.Context) { c.Status(204) })
		r.POST("/both", func(c *gin.Context) { c.Status(204) })
		req := httptest.NewRequest("POST", tc.route, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("route=%s expected=%d got=%d", tc.route, tc.status, w.Code)
		}
	}
}

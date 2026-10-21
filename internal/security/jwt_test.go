package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func testVerifier(t *testing.T) (*Verifier, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: data}), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := Load(path, "erp-auth", "erp-api")
	if err != nil {
		t.Fatal(err)
	}
	return v, key
}

func validClaims() jwt.MapClaims {
	now := time.Now().Unix()
	return jwt.MapClaims{"user_id": "e28b7175-11a0-4ef7-af75-6b3b868a8838", "sub": "e28b7175-11a0-4ef7-af75-6b3b868a8838", "email": "fixture@example.invalid", "token_version": 0, "permissions": []string{"catalog.products:read"}, "iss": "erp-auth", "aud": []string{"erp-api"}, "iat": now, "nbf": now, "exp": now + 900}
}

func TestJWTContract(t *testing.T) {
	v, key := testVerifier(t)
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(jwt.MapClaims)
		method jwt.SigningMethod
		key    any
		valid  bool
	}{
		{name: "valid", valid: true},
		{name: "empty permissions authenticated", change: func(c jwt.MapClaims) { c["permissions"] = []string{} }, valid: true},
		{name: "wrong key", key: wrongKey},
		{name: "HS256", method: jwt.SigningMethodHS256, key: []byte("test-only-secret")},
		{name: "none", method: jwt.SigningMethodNone, key: jwt.UnsafeAllowNoneSignatureType},
		{name: "RS512", method: jwt.SigningMethodRS512},
		{name: "PS256", method: jwt.SigningMethodPS256},
		{name: "expired", change: func(c jwt.MapClaims) {
			c["iat"] = time.Now().Unix() - 1000
			c["nbf"] = c["iat"]
			c["exp"] = time.Now().Unix() - 1
		}},
		{name: "future nbf", change: func(c jwt.MapClaims) { c["nbf"] = time.Now().Unix() + 60 }},
		{name: "future iat", change: func(c jwt.MapClaims) { c["iat"] = time.Now().Unix() + 60; c["nbf"] = c["iat"] }},
		{name: "string timestamp", change: func(c jwt.MapClaims) { c["iat"] = "123" }},
		{name: "wrong issuer", change: func(c jwt.MapClaims) { c["iss"] = "other" }},
		{name: "wrong audience", change: func(c jwt.MapClaims) { c["aud"] = "other" }},
		{name: "long lifetime", change: func(c jwt.MapClaims) { c["exp"] = time.Now().Unix() + 901 }},
		{name: "nbf before iat", change: func(c jwt.MapClaims) { c["nbf"] = time.Now().Unix() - 1 }},
		{name: "exp equals iat", change: func(c jwt.MapClaims) { c["exp"] = c["iat"] }},
		{name: "nbf equals exp", change: func(c jwt.MapClaims) { c["nbf"] = c["exp"] }},
		{name: "subject mismatch", change: func(c jwt.MapClaims) { c["sub"] = "other" }},
		{name: "invalid UUID", change: func(c jwt.MapClaims) { c["user_id"] = "bad" }},
		{name: "nil UUID", change: func(c jwt.MapClaims) { c["user_id"] = "00000000-0000-0000-0000-000000000000"; c["sub"] = c["user_id"] }},
		{name: "negative version", change: func(c jwt.MapClaims) { c["token_version"] = -1 }},
		{name: "string version", change: func(c jwt.MapClaims) { c["token_version"] = "0" }},
		{name: "fractional version", change: func(c jwt.MapClaims) { c["token_version"] = 0.5 }},
		{name: "overflow version", change: func(c jwt.MapClaims) { c["token_version"] = int64(1) << 32 }},
		{name: "null permissions", change: func(c jwt.MapClaims) { c["permissions"] = nil }},
		{name: "string permissions", change: func(c jwt.MapClaims) { c["permissions"] = "catalog.products:read" }},
		{name: "numeric permission", change: func(c jwt.MapClaims) { c["permissions"] = []any{1} }},
		{name: "malformed permission", change: func(c jwt.MapClaims) { c["permissions"] = []string{"catalog:products:read"} }},
		{name: "padded permission", change: func(c jwt.MapClaims) { c["permissions"] = []string{" catalog.products:read"} }},
	}
	for _, field := range []string{"iss", "aud", "sub", "user_id", "token_version", "permissions", "iat", "nbf", "exp"} {
		field := field
		cases = append(cases, struct {
			name   string
			change func(jwt.MapClaims)
			method jwt.SigningMethod
			key    any
			valid  bool
		}{name: "missing " + field, change: func(c jwt.MapClaims) { delete(c, field) }})
		cases = append(cases, struct {
			name   string
			change func(jwt.MapClaims)
			method jwt.SigningMethod
			key    any
			valid  bool
		}{name: "null " + field, change: func(c jwt.MapClaims) { c[field] = nil }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validClaims()
			if tc.change != nil {
				tc.change(c)
			}
			method := tc.method
			if method == nil {
				method = jwt.SigningMethodRS256
			}
			var signingKey any = key
			if tc.key != nil {
				signingKey = tc.key
			}
			raw, err := jwt.NewWithClaims(method, c).SignedString(signingKey)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Verify(raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	for _, raw := range []string{"", "not.jwt", "a.b.c"} {
		if _, err := v.Verify(raw); err == nil {
			t.Fatal("accepted malformed JWT")
		}
	}
}

func TestPublicKeyConfiguration(t *testing.T) {
	_, key := testVerifier(t)
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	valid := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	for _, tc := range []struct {
		name  string
		data  []byte
		valid bool
	}{
		{"PKCS1", valid, true},
		{"private", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), false},
		{"weak", pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&weak.PublicKey)}), false},
		{"EC", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: ecDER}), false},
		{"garbage", []byte("bad"), false}, {"trailing", append(append([]byte{}, valid...), []byte("bad")...), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key.pem")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path, "erp-auth", "erp-api")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][3]string{{"", "erp-auth", "erp-api"}, {path, "", "erp-api"}, {path, "erp-auth", ""}, {path, " erp-auth", "erp-api"}, {path, "erp-auth", "erp-api "}, {t.TempDir(), "erp-auth", "erp-api"}, {path + "missing", "erp-auth", "erp-api"}} {
		if _, err := Load(args[0], args[1], args[2]); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
}

func TestMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	v, key := testVerifier(t)
	sign := func(perms []string) string {
		c := validClaims()
		c["permissions"] = perms
		raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	valid := sign([]string{"catalog.products:read"})
	for _, tc := range []struct {
		name    string
		headers []string
		code    int
	}{
		{"missing", nil, 401}, {"empty", []string{""}, 401}, {"scheme", []string{"Basic " + valid}, 401}, {"no token", []string{"Bearer"}, 401}, {"extra", []string{"Bearer " + valid + " extra"}, 401}, {"duplicate", []string{"Bearer " + valid, "Bearer " + valid}, 401},
		{"valid", []string{"Bearer " + valid}, 204}, {"case insensitive scheme", []string{"bearer " + valid}, 204},
		{"missing permission", []string{"Bearer " + sign([]string{"catalog.products:create"})}, 403},
		{"empty permissions", []string{"Bearer " + sign([]string{})}, 403},
		{"wildcard", []string{"Bearer " + sign([]string{"catalog.products:*"})}, 403},
		{"admin", []string{"Bearer " + sign([]string{"admin:all"})}, 403},
		{"case sensitive grant", []string{"Bearer " + sign([]string{"catalog.products:Read"})}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			r := gin.New()
			r.Use(Protect(v, map[string][]string{"GET /items": {"catalog.products:read"}}))
			r.GET("/items", func(c *gin.Context) {
				called = true
				if claims, ok := Identity(c); !ok || claims.UserID.String() != validClaims()["user_id"] {
					t.Error("verified identity missing")
				}
				c.Status(204)
			})
			req := httptest.NewRequest("GET", "/items", nil)
			for _, h := range tc.headers {
				req.Header.Add("Authorization", h)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.code || called != (tc.code == 204) {
				t.Fatalf("status=%d handler=%v", w.Code, called)
			}
		})
	}
}

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Run only with scripts/test-warehouse-postgres.ps1 -IncludeHTTP. The harness
// owns the entire disposable databases and removes all fixture data on exit.
func TestIsolatedAuthInventoryHTTP(t *testing.T) {
	if os.Getenv("WAREHOUSE_HTTP_ISOLATED") != "1" {
		t.Skip("disposable direct HTTP harness required")
	}
	runWarehouseHTTP(t, false)
}

// TestIsolatedAuthInventoryGateway uses real login tokens through disposable Kong.
func TestIsolatedAuthInventoryGateway(t *testing.T) {
	if os.Getenv("WAREHOUSE_GATEWAY_ISOLATED") != "1" {
		t.Skip("disposable Kong fixture required")
	}
	runWarehouseHTTP(t, true)
}

func runWarehouseHTTP(t *testing.T, gateway bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	connect := func(env, name string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(os.Getenv(env))
		if err != nil || cfg.ConnConfig.Database != name || cfg.ConnConfig.Host != "127.0.0.1" {
			t.Fatal("invalid isolated database destination")
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal("open fixture database")
		}
		t.Cleanup(pool.Close)
		return pool
	}
	authName := "auth_warehouse_test"
	if gateway {
		authName = "auth_security_test"
	}
	auth := connect("WAREHOUSE_AUTH_DATABASE_URL", authName)
	inventory := connect("WAREHOUSE_INVENTORY_DATABASE_URL", "inventory_db")
	bases := map[string]string{"auth": os.Getenv("WAREHOUSE_AUTH_URL"), "inventory": os.Getenv("WAREHOUSE_INVENTORY_URL")}
	for _, base := range bases {
		if !strings.HasPrefix(base, "http://127.0.0.1:") {
			t.Fatal("loopback HTTP required")
		}
	}
	verifier, err := security.Load(os.Getenv("WAREHOUSE_PUBLIC_KEY"), "erp-auth", "erp-api")
	if err != nil {
		t.Fatal("fixture public key unavailable")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	request := func(target, method, path, token string, body any, want int) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal("encode request")
		}
		req, err := http.NewRequestWithContext(ctx, method, bases[target]+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal("build request")
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal("HTTP request failed; details redacted")
		}
		defer res.Body.Close()
		data, err = io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			t.Fatal("read response")
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s %s: HTTP %d want %d; body redacted", target, method, path, res.StatusCode, want)
		}
		t.Logf("%s %s %s: HTTP %d", target, method, path, res.StatusCode)
		return data
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := auth.Exec(ctx, sql, args...); err != nil {
			t.Fatal("fixture SQL failed; details redacted")
		}
	}
	fixture := func(actions []string, scope string) string {
		id, role := uuid.New(), uuid.New()
		email := "warehouse-" + id.String() + "@example.invalid"
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			t.Fatal("random fixture credential")
		}
		password := hex.EncodeToString(secret)
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			t.Fatal("hash fixture credential")
		}
		exec(`INSERT INTO identity_users(id,email,full_name,email_verified_at) VALUES($1,$2,'Disposable warehouse test',now())`, id, email)
		exec(`INSERT INTO identity_credentials(user_id,password_hash) VALUES($1,$2)`, id, string(hash))
		if len(actions) > 0 {
			exec(`INSERT INTO identity_roles(id,name) VALUES($1,$2)`, role, "test-"+role.String())
			for _, action := range actions {
				tag, err := auth.Exec(ctx, `INSERT INTO identity_role_permissions(role_id,permission_id) SELECT $1,id FROM identity_permissions WHERE resource='inventory.warehouses' AND action=$2`, role, action)
				if err != nil || tag.RowsAffected() != 1 {
					t.Fatal("missing permission definition")
				}
			}
			var scopeID any
			if scope == "WAREHOUSE" {
				scopeID = uuid.New()
			}
			exec(`INSERT INTO identity_user_roles(user_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,$4)`, id, role, scope, scopeID)
		}
		payload := request("auth", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": password}, 200)
		var login struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			ExpiresIn   int64  `json:"expires_in"`
		}
		if json.Unmarshal(payload, &login) != nil || login.TokenType != "Bearer" || login.ExpiresIn < 1 || login.ExpiresIn > 900 {
			t.Fatal("login contract")
		}
		claims, err := verifier.Verify(login.AccessToken)
		if err != nil || claims.UserID != id || claims.TokenVersion == nil || *claims.TokenVersion != 0 || int64(claims.ExpiresAt.Sub(claims.IssuedAt.Time).Seconds()) != login.ExpiresIn {
			t.Fatal("runtime signature/claim contract")
		}
		expected := map[string]bool{}
		if scope == "GLOBAL" {
			for _, action := range actions {
				expected["inventory.warehouses:"+action] = true
			}
		}
		if len(claims.Permissions) != len(expected) {
			t.Fatal("incorrect permission scope")
		}
		for _, p := range claims.Permissions {
			if !expected[p] {
				t.Fatal("unexpected permission")
			}
		}
		var me struct {
			ID uuid.UUID `json:"id"`
		}
		if json.Unmarshal(request("auth", "GET", "/api/v1/auth/me", login.AccessToken, nil, 200), &me) != nil || me.ID != id {
			t.Fatal("me identity mismatch")
		}
		return login.AccessToken
	}
	// Establish that migrations did not create identities or grants.
	var users, grants, assignments int
	if err = auth.QueryRow(ctx, `SELECT (SELECT count(*) FROM identity_users),(SELECT count(*) FROM identity_role_permissions),(SELECT count(*) FROM identity_user_roles)`).Scan(&users, &grants, &assignments); err != nil || users+grants+assignments != 0 {
		t.Fatal("fixture database is not empty of users/grants")
	}
	editor := fixture([]string{"read", "create", "update"}, "GLOBAL")
	reader := fixture([]string{"read"}, "GLOBAL")
	empty := fixture(nil, "GLOBAL")
	scoped := fixture([]string{"read", "create", "update"}, "WAREHOUSE")
	if !gateway {
		for _, target := range []string{"auth", "inventory"} {
			for _, path := range []string{"/health", "/ready"} {
				request(target, "GET", path, "", nil, 200)
			}
		}
	}
	const path = "/api/v1/inventory/warehouses"
	snapshot := func() string {
		t.Helper()
		var s string
		if err := inventory.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(w) ORDER BY code)::text,'[]') FROM warehouses w`).Scan(&s); err != nil {
			t.Fatal("snapshot failed")
		}
		return s
	}
	if snapshot() != "[]" {
		t.Fatal("HTTP database must start empty")
	}
	body := map[string]string{"code": "HTTP-A", "name": "First"}
	request("inventory", "GET", path, "", nil, 401)
	for _, token := range []string{empty, scoped} {
		request("inventory", "GET", path, token, nil, 403)
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {reader, 403}, {empty, 403}, {scoped, 403}} {
		request("inventory", "POST", path, tc.token, body, tc.status)
	}
	if snapshot() != "[]" {
		t.Fatal("denied create modified database")
	}
	type warehouseResponse struct {
		ID        uuid.UUID `json:"id"`
		IsDefault bool      `json:"is_default"`
		IsActive  bool      `json:"is_active"`
	}
	create := func(code string) warehouseResponse {
		var w warehouseResponse
		payload := request("inventory", "POST", path, editor, map[string]string{"code": code, "name": code}, 201)
		if json.Unmarshal(payload, &w) != nil || w.ID == uuid.Nil {
			t.Fatal("create response")
		}
		return w
	}
	first := create("HTTP-A")
	second := create("HTTP-B")
	request("inventory", "POST", path, editor, map[string]string{"code": " http-a ", "name": "Duplicate"}, 409)
	if !first.IsDefault || !first.IsActive || second.IsDefault {
		t.Fatal("initial default contract")
	}
	item := path + "/" + first.ID.String()
	other := path + "/" + second.ID.String()
	request("inventory", "GET", path, reader, nil, 200)
	request("inventory", "GET", item, reader, nil, 200)
	before := snapshot()
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {reader, 403}, {empty, 403}, {scoped, 403}} {
		request("inventory", "PUT", item, tc.token, map[string]string{"name": "Denied"}, tc.status)
		request("inventory", "PATCH", other+"/status", tc.token, map[string]bool{"is_active": false}, tc.status)
	}
	if before != snapshot() {
		t.Fatal("denied writes modified persisted state")
	}
	request("inventory", "PATCH", item+"/status", editor, map[string]bool{"is_active": false}, 409)
	request("inventory", "PUT", other, editor, map[string]any{"name": "Promoted", "is_default": true}, 200)
	request("inventory", "PATCH", item+"/status", editor, map[string]bool{"is_active": false}, 200)
	request("inventory", "PUT", item, editor, map[string]any{"name": "Inactive", "is_default": true}, 409)
	request("inventory", "PATCH", other+"/status", editor, map[string]bool{"is_active": false}, 409)
	var total, defaults int
	var name string
	var active bool
	if err = inventory.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE is_default AND is_active) FROM warehouses`).Scan(&total, &defaults); err != nil || total != 2 || defaults != 1 {
		t.Fatal("persisted default invariant")
	}
	if err = inventory.QueryRow(ctx, `SELECT name,is_active FROM warehouses WHERE id=$1`, first.ID).Scan(&name, &active); err != nil || name != "HTTP-A" || active {
		t.Fatal("status persistence or rejected promotion changed data")
	}
	if err = inventory.QueryRow(ctx, `SELECT name,is_active FROM warehouses WHERE id=$1 AND is_default`, second.ID).Scan(&name, &active); err != nil || name != "Promoted" || !active {
		t.Fatal("promotion persistence")
	}
	if gateway {
		request("inventory", "GET", "/health", "", nil, 404)
		request("inventory", "GET", "/ready", "", nil, 404)
		request("inventory", "GET", path+"/"+uuid.NewString(), reader, nil, 404)
		request("inventory", "GET", "/api/v1/catalog/brands", "", nil, 401)
		request("inventory", "GET", "/api/v1/catalog/brands", reader, nil, 403)
		request("inventory", "GET", path, "invalid-token", nil, 401)
		// Only the negative expiry case is locally signed; every successful request
		// above uses actual Auth login. The fixture key is never mounted into Inventory.
		data, err := os.ReadFile(os.Getenv("WAREHOUSE_NEGATIVE_PRIVATE_KEY"))
		if err != nil {
			t.Fatal("negative-test fixture key unavailable")
		}
		key, err := jwt.ParseRSAPrivateKeyFromPEM(data)
		if err != nil {
			t.Fatal("negative-test fixture key invalid")
		}
		claims, err := verifier.Verify(editor)
		if err != nil {
			t.Fatal("real login token invalid")
		}
		claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(-901 * time.Second))
		claims.NotBefore = claims.IssuedAt
		claims.ExpiresAt = jwt.NewNumericDate(claims.IssuedAt.Add(900 * time.Second))
		expired, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
		if err != nil {
			t.Fatal("build negative expired-token fixture")
		}
		request("inventory", "GET", path, expired, nil, 401)
		t.Log("PASS: Kong routing, real login JWTs, invalid/expired rejection, preserved Auth/Catalog routes, persisted warehouse rules")
	} else {
		t.Log("PASS: real Auth login, local signature/contract, GLOBAL scope, unchanged denied writes, persisted default transitions; direct HTTP, no Kong")
	}
}

package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const balanceRead = "inventory.stock-balances:read"
const movementRead = "inventory.stock-movements:read"

type stockGateway struct {
	t               *testing.T
	ctx             context.Context
	auth, inventory *pgxpool.Pool
	base            string
	client          *http.Client
	verifier        *security.Verifier
}

func (g *stockGateway) ok(err error) {
	g.t.Helper()
	if err != nil {
		f := logger.Classify("fixture", "gateway", err)
		g.t.Fatalf("fixture failed: reason=%s sqlstate=%s type=%T", f.Reason, f.SQLState, err)
	}
}
func (g *stockGateway) require(ok bool, reason string) {
	g.t.Helper()
	if !ok {
		g.t.Fatal(reason)
	}
}
func (g *stockGateway) request(path, token, label string, status int, body any) []byte {
	g.t.Helper()
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		g.ok(err)
		input = strings.NewReader(string(data))
	}
	method := "GET"
	if body != nil {
		method = "POST"
	}
	req, err := http.NewRequestWithContext(g.ctx, method, g.base+path, input)
	g.ok(err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := g.client.Do(req)
	g.ok(err)
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	g.ok(err)
	g.require(len(data) <= 1<<20, "fixture response exceeded limit")
	g.require(strings.Contains(strings.ToLower(res.Header.Get("Via")), "kong"), "request did not traverse real Kong")
	g.require(res.StatusCode == status, fmt.Sprintf("%s: HTTP %d expected %d; body/URL/token withheld", label, res.StatusCode, status))
	if strings.Contains(path, "/inventory/") {
		g.require(res.Header.Get("X-Request-ID") != "", "request ID missing")
	}
	g.t.Logf("%s HTTP %d", label, res.StatusCode)
	return data
}
func (g *stockGateway) login(permissions []string, scope string) string {
	g.t.Helper()
	id, role := uuid.New(), uuid.New()
	email := "stock-gateway-" + id.String() + "@example.invalid"
	random := make([]byte, 32)
	_, err := rand.Read(random)
	g.ok(err)
	password := hex.EncodeToString(random)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	g.ok(err)
	_, err = g.auth.Exec(g.ctx, "INSERT INTO identity_users(id,email,full_name,email_verified_at) VALUES($1,$2,'Synthetic stock read user',now())", id, email)
	g.ok(err)
	_, err = g.auth.Exec(g.ctx, "INSERT INTO identity_credentials(user_id,password_hash) VALUES($1,$2)", id, string(hash))
	g.ok(err)
	if len(permissions) > 0 {
		_, err = g.auth.Exec(g.ctx, "INSERT INTO identity_roles(id,name) VALUES($1,$2)", role, "stock-gateway-"+role.String())
		g.ok(err)
		for _, permission := range permissions {
			parts := strings.Split(permission, ":")
			g.require(len(parts) == 2, "invalid fixture permission")
			tag, e := g.auth.Exec(g.ctx, "INSERT INTO identity_role_permissions(role_id,permission_id) SELECT $1,id FROM identity_permissions WHERE resource=$2 AND action=$3", role, parts[0], parts[1])
			g.ok(e)
			g.require(tag.RowsAffected() == 1, "approved permission definition missing")
		}
		var scopeID any
		if scope == "WAREHOUSE" {
			scopeID = uuid.New()
		}
		_, err = g.auth.Exec(g.ctx, "INSERT INTO identity_user_roles(user_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,$4)", id, role, scope, scopeID)
		g.ok(err)
	}
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	g.ok(json.Unmarshal(g.request("/api/v1/auth/login", "", "actual-login", 200, map[string]string{"email": email, "password": password}), &result))
	g.require(result.TokenType == "Bearer", "login token type")
	claims, err := g.verifier.Verify(result.AccessToken)
	if err != nil {
		// Diagnose clock drift without printing the token or identity claims.
		var timing jwt.RegisteredClaims
		_, _, parseErr := jwt.NewParser().ParseUnverified(result.AccessToken, &timing)
		if parseErr == nil && timing.IssuedAt != nil && timing.NotBefore != nil && timing.ExpiresAt != nil {
			g.t.Logf("login verification timing: issued_offset_ms=%d not_before_offset_ms=%d remaining_ms=%d", time.Until(timing.IssuedAt.Time).Milliseconds(), time.Until(timing.NotBefore.Time).Milliseconds(), time.Until(timing.ExpiresAt.Time).Milliseconds())
		}
	}
	g.ok(err)
	g.require(claims.UserID == id, "login identity mismatch")
	expected := map[string]bool{}
	if scope == "GLOBAL" {
		for _, p := range permissions {
			expected[p] = true
		}
	}
	g.require(len(claims.Permissions) == len(expected), "incorrect GLOBAL-only claims")
	for _, p := range claims.Permissions {
		g.require(expected[p], "unexpected login grant")
	}
	var me struct {
		ID uuid.UUID `json:"id"`
	}
	g.ok(json.Unmarshal(g.request("/api/v1/auth/me", result.AccessToken, "actual-me", 200, nil), &me))
	g.require(me.ID == id, "me mismatch")
	return result.AccessToken
}

// Explicitly synthetic setup authority/references, never connected to a router.
type gatewayPosting struct{ actor, product, unit uuid.UUID }

func (p gatewayPosting) Authorize(ctx context.Context, actor stock.Principal, c stock.Command) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if actor.UserID != p.actor || c.Producer != "gateway-fixture" {
		return stock.ErrForbidden
	}
	return nil
}
func (p gatewayPosting) Check(ctx context.Context, _ []stock.Observation) error { return ctx.Err() }
func (p gatewayPosting) Resolve(ctx context.Context, c stock.Command) ([]stock.Observation, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := make([]stock.Observation, 0, len(c.Lines))
	for _, line := range c.Lines {
		o := stock.Observation{ReferenceSnapshot: stock.ReferenceSnapshot{ProductID: p.product, VariantID: line.VariantID, BaseUnitID: p.unit, QuantityKind: "PIECE", ProductActive: true, VariantActive: true, BaseUnitActive: true, StockReferenceRevision: line.ExpectedStockReferenceRevision, CheckedAt: time.Now().UTC()}, ProductCode: "GATEWAY-HISTORY", ProductName: "Historical synthetic product", SKU: "GW-" + line.VariantID.String(), BaseUnitCode: "PC"}
		if line.PackID != uuid.Nil {
			label := "Historical box of six"
			o.PackLabel = &label
			o.Pack = &stock.PackSnapshot{ID: line.PackID, VariantID: line.VariantID, Revision: line.PackRevision, PiecesPerPack: 6, Active: true}
		}
		out = append(out, o)
	}
	return out, nil
}

type gatewayPage struct {
	Items []map[string]json.RawMessage `json:"items"`
	Next  *string                      `json:"next_cursor"`
}

func (g *stockGateway) page(path, token, label string) gatewayPage {
	g.t.Helper()
	var p gatewayPage
	g.ok(json.Unmarshal(g.request(path, token, label, 200, nil), &p))
	g.require(p.Items != nil, "items was null")
	return p
}
func (g *stockGateway) text(row map[string]json.RawMessage, key string) string {
	g.t.Helper()
	var value string
	g.ok(json.Unmarshal(row[key], &value))
	return value
}
func (g *stockGateway) snapshot() [32]byte {
	g.t.Helper()
	var value string
	g.ok(g.inventory.QueryRow(g.ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY warehouse_id,variant_id) FROM stock_balances x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM stock_operations x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM stock_movements x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM stock_operation_audit x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY producer,key) FROM stock_idempotency x))::text`).Scan(&value))
	return sha256.Sum256([]byte(value))
}

func TestStockReadGateway(t *testing.T) {
	if os.Getenv("STOCK_GATEWAY_ISOLATED") != "1" {
		t.Skip("requires ERP-Platform/scripts/test-inventory-gateway.ps1")
	}
	run := os.Getenv("STOCK_GATEWAY_RUN_ID")
	if !regexp.MustCompile(`^inventory-gateway-[a-f0-9]{12}$`).MatchString(run) {
		t.Fatal("invalid gateway fixture gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	g := &stockGateway{t: t, ctx: ctx, base: os.Getenv("WAREHOUSE_INVENTORY_URL"), client: &http.Client{Timeout: 10 * time.Second}}
	defer g.client.CloseIdleConnections()
	g.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	u, e := url.Parse(g.base)
	g.ok(e)
	g.require(u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.Path == "" && u.User == nil && u.RawQuery == "" && os.Getenv("WAREHOUSE_AUTH_URL") == g.base, "non-fixture gateway destination")
	connect := func(env, db, user string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(os.Getenv(env))
		g.ok(err)
		g.require(cfg.ConnConfig.Host == "127.0.0.1" && cfg.ConnConfig.Database == db && cfg.ConnConfig.User == user, "non-fixture database destination")
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		g.ok(err)
		t.Cleanup(p.Close)
		var marker string
		g.ok(p.QueryRow(ctx, "SELECT current_setting('stock.gateway_fixture',true)").Scan(&marker))
		g.require(marker == run, "database fixture ownership mismatch")
		return p
	}
	g.auth = connect("WAREHOUSE_AUTH_DATABASE_URL", "auth_security_test", "security_test")
	g.inventory = connect("WAREHOUSE_INVENTORY_DATABASE_URL", "inventory_db", "inventory_service")
	pub := os.Getenv("WAREHOUSE_PUBLIC_KEY")
	g.require(filepath.Base(filepath.Dir(pub)) == run, "non-fixture key path")
	g.verifier, e = security.Load(pub, "erp-auth", "erp-api")
	g.ok(e)
	warehouses := postgres.NewWarehouseRepository(g.inventory)
	ids := make([]uuid.UUID, 3)
	for i := range ids {
		w, err := warehouses.Create(ctx, repository.CreateWarehouseParams{ID: uuid.New(), Code: fmt.Sprintf("STOCK-GW-%d", i), Name: "Synthetic stock gateway"})
		g.ok(err)
		ids[i] = w.ID
	}
	actor := gatewayPosting{uuid.New(), uuid.New(), uuid.New()}
	repo, e := postgres.NewStockRepository(g.inventory, postgres.DefaultStockRepositoryOptions())
	g.ok(e)
	posting, e := stock.NewPostingService(repo, actor, actor, actor, stock.DefaultPostingOptions())
	g.ok(e)
	main, zero, packed, foreign := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	packID := uuid.New()
	post := func(warehouse uuid.UUID, d stock.Direction, lines ...stock.CommandLine) {
		t.Helper()
		for i := range lines {
			lines[i].LineNumber = int16(i + 1)
			lines[i].ExpectedStockReferenceRevision = "synthetic-gateway-v1"
		}
		_, err := posting.Post(ctx, stock.Principal{UserID: actor.actor}, stock.Command{OperationID: uuid.New(), SourceEventID: uuid.New(), Producer: "gateway-fixture", WarehouseID: warehouse, Direction: d, CommandVersion: 1, IdempotencyKey: uuid.New(), Lines: lines})
		g.ok(err)
	}
	const large int64 = 9007199254741093
	post(ids[0], stock.In, stock.CommandLine{VariantID: main, InputCount: stock.Quantity(large)})
	post(ids[0], stock.Out, stock.CommandLine{VariantID: main, InputCount: 3})
	post(ids[0], stock.In, stock.CommandLine{VariantID: zero, InputCount: 5})
	post(ids[0], stock.Out, stock.CommandLine{VariantID: zero, InputCount: 5})
	post(ids[0], stock.In, stock.CommandLine{VariantID: packed, InputCount: 2, PackID: packID, PackRevision: "1"})
	fill := make([]stock.CommandLine, 100)
	for i := range fill {
		fill[i] = stock.CommandLine{VariantID: uuid.New(), InputCount: 1}
	}
	post(ids[0], stock.In, fill...)
	post(ids[0], stock.In, stock.CommandLine{VariantID: uuid.New(), InputCount: 1})
	post(ids[1], stock.In, stock.CommandLine{VariantID: foreign, InputCount: 7})
	_, e = warehouses.SetActive(ctx, ids[0], false)
	g.require(errors.Is(e, domain.ErrWarehouseHasStock), "positive-stock deactivation must reject")
	// Owned fixture only: model historical/legacy inactive rows without weakening
	// the normal repository policy. Read APIs must still serve such history.
	_, e = g.inventory.Exec(ctx, "UPDATE warehouses SET is_active=false WHERE id=$1", ids[0])
	g.ok(e)
	var balances, movements, operations, keys int
	g.ok(g.inventory.QueryRow(ctx, "SELECT (SELECT count(*) FROM stock_balances),(SELECT count(*) FROM stock_movements),(SELECT count(*) FROM stock_operations),(SELECT count(*) FROM stock_idempotency WHERE state='COMPLETED')").Scan(&balances, &movements, &operations, &keys))
	g.require(balances == 105 && movements == 107 && operations == 8 && keys == 8, "fixture reconciliation counts")
	var actual int64
	g.ok(g.inventory.QueryRow(ctx, "SELECT on_hand_pieces FROM stock_balances WHERE warehouse_id=$1 AND variant_id=$2", ids[0], main).Scan(&actual))
	g.require(actual == large-3, "fixture BIGINT reconciliation")
	none := g.login(nil, "GLOBAL")
	balance := g.login([]string{balanceRead}, "GLOBAL")
	movement := g.login([]string{movementRead}, "GLOBAL")
	both := g.login([]string{balanceRead, movementRead}, "GLOBAL")
	scoped := g.login([]string{balanceRead, movementRead}, "WAREHOUSE")
	warehouseOnly := g.login([]string{"inventory.warehouses:read"}, "GLOBAL")
	// Locally signed material is used ONLY for this explicit negative expiry case.
	privatePath := os.Getenv("WAREHOUSE_NEGATIVE_PRIVATE_KEY")
	g.require(filepath.Dir(privatePath) == filepath.Dir(pub), "non-fixture negative key")
	private, e := os.ReadFile(privatePath)
	g.ok(e)
	block, _ := pem.Decode(private)
	g.require(block != nil, "invalid fixture key PEM")
	key, e := x509.ParsePKCS1PrivateKey(block.Bytes)
	g.ok(e)
	now := time.Now().Unix()
	id := uuid.NewString()
	expired, e := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"user_id": id, "sub": id, "email": "expired@example.invalid", "token_version": 0, "permissions": []string{balanceRead, movementRead}, "iss": "erp-auth", "aud": []string{"erp-api"}, "iat": now - 600, "nbf": now - 600, "exp": now - 1}).SignedString(key)
	g.ok(e)
	before := g.snapshot()
	for _, endpoint := range []struct {
		name, token, other string
		count              int
	}{{"stock-balances", balance, movement, 104}, {"stock-movements", movement, balance, 106}} {
		path := "/api/v1/inventory/warehouses/" + ids[0].String() + "/" + endpoint.name
		for _, c := range []struct {
			label, token string
			status       int
		}{{"missing", "", 401}, {"malformed", "invalid", 401}, {"tampered", endpoint.token + "tampered", 401}, {"expired-negative-signed", expired, 401}, {"no-permission", none, 403}, {"other-endpoint-permission", endpoint.other, 403}, {"warehouse-management-only", warehouseOnly, 403}, {"WAREHOUSE-scope-only", scoped, 403}} {
			g.request(path, c.token, endpoint.name+"/"+c.label, c.status, nil)
		}
		first := g.page(path, endpoint.token, endpoint.name+"/exact-GLOBAL-default-50")
		g.require(len(first.Items) == 50 && first.Next != nil, "default pagination contract")
		g.require(len(g.page(path+"?limit=100", both, endpoint.name+"/max-100").Items) == 100, "maximum page contract")
		kind := "balances"
		column := "variant_id"
		if endpoint.name == "stock-movements" {
			kind = "movements"
			column = "id"
		}
		expected := map[string]bool{}
		rows, e := g.inventory.Query(ctx, "SELECT "+column+" FROM stock_"+kind+" WHERE warehouse_id=$1", ids[0])
		g.ok(e)
		for rows.Next() {
			var identifier uuid.UUID
			g.ok(rows.Scan(&identifier))
			expected[identifier.String()] = true
		}
		g.ok(rows.Err())
		rows.Close()
		next := ""
		seen := 0
		for pageNumber := 0; pageNumber < 20; pageNumber++ {
			query := "?limit=17"
			if next != "" {
				query += "&cursor=" + url.QueryEscape(next)
			}
			p := g.page(path+query, both, endpoint.name+"/static-page")
			g.require(len(p.Items) <= 17, "page exceeds limit")
			for _, row := range p.Items {
				key := g.text(row, column)
				g.require(expected[key], "duplicate/foreign/unexpected traversal key")
				delete(expected, key)
				g.require(g.text(row, "warehouse_id") == ids[0].String(), "warehouse leak")
				seen++
				fields := []string{"on_hand_pieces", "version"}
				if kind == "movements" {
					fields = []string{"input_count", "pieces_per_input", "pieces", "before_pieces", "after_pieces", "balance_version"}
					g.require(g.text(row, "product_name") == "Historical synthetic product", "historical snapshot lost")
					if g.text(row, "variant_id") == packed.String() {
						g.require(g.text(row, "pack_label") == "Historical box of six" && g.text(row, "pieces_per_input") == "6", "recorded pack lost")
					}
				}
				for _, field := range fields {
					number, err := strconv.ParseInt(g.text(row, field), 10, 64)
					g.ok(err)
					g.require(number >= 0, "negative/invalid quantity version")
				}
			}
			if p.Next == nil {
				break
			}
			next = *p.Next
		}
		g.require(seen == endpoint.count && len(expected) == 0, "static traversal missing rows")
		filter := main.String()
		filtered := g.page(path+"?variant_id="+filter, both, endpoint.name+"/variant-filter")
		want := 1
		if kind == "movements" {
			want = 2
		}
		g.require(len(filtered.Items) == want, "variant count")
		for _, row := range filtered.Items {
			g.require(g.text(row, "variant_id") == filter, "variant filter ignored")
		}
		if kind == "balances" {
			g.require(g.text(filtered.Items[0], "on_hand_pieces") == strconv.FormatInt(large-3, 10), "BIGINT response precision")
			zeroPage := g.page(path+"?variant_id="+zero.String(), both, "balances/zero-stored")
			g.require(len(zeroPage.Items) == 1 && g.text(zeroPage.Items[0], "on_hand_pieces") == "0", "stored zero missing")
			g.require(len(g.page(path+"?variant_id="+zero.String()+"&nonzero_only=true", both, "balances/nonzero-excludes-zero").Items) == 0, "nonzero filter ignored")
		}
		cursor := url.QueryEscape(*first.Next)
		for _, suffix := range []string{"?variant_id=" + main.String() + "&cursor=" + cursor, "?variant_id=" + zero.String() + "&cursor=" + cursor} {
			g.request(path+suffix, both, endpoint.name+"/cursor-filter-reuse", 400, nil)
		}
		g.request("/api/v1/inventory/warehouses/"+ids[1].String()+"/"+endpoint.name+"?cursor="+cursor, both, endpoint.name+"/cursor-warehouse-reuse", 400, nil)
		if kind == "balances" {
			g.request(path+"?nonzero_only=true&cursor="+cursor, both, "balances/cursor-nonzero-reuse", 400, nil)
		}
		for _, query := range []string{"limit=0", "limit=101", "limit=bad", "limit=1&limit=2", "cursor=bad", "cursor=", "variant_id=bad", "variant_id=00000000-0000-0000-0000-000000000000", "unsupported=1", "nonzero_only=1"} {
			g.request(path+"?"+query, both, endpoint.name+"/invalid-input", 400, nil)
		}
		g.request("/api/v1/inventory/warehouses/not-a-uuid/"+endpoint.name, both, endpoint.name+"/invalid-warehouse", 400, nil)
		g.request("/api/v1/inventory/warehouses/"+uuid.NewString()+"/"+endpoint.name, both, endpoint.name+"/missing-warehouse", 404, nil)
		emptyPage := g.page("/api/v1/inventory/warehouses/"+ids[2].String()+"/"+endpoint.name, both, endpoint.name+"/existing-empty")
		g.require(len(emptyPage.Items) == 0 && emptyPage.Next == nil, "existing empty contract")
		foreignPage := g.page("/api/v1/inventory/warehouses/"+ids[1].String()+"/"+endpoint.name, both, endpoint.name+"/second-warehouse")
		g.require(len(foreignPage.Items) == 1 && g.text(foreignPage.Items[0], "variant_id") == foreign.String(), "second warehouse isolation")
	}
	after := g.snapshot()
	g.require(before == after, "stock tables changed during read requests")
	t.Logf("stock table SHA256 before=%x after=%x unchanged=true", before, after)
	t.Log("reconciled: balances105 movements107 operations8 completed-keys8; inactive/history reads passed with Catalog stopped; successful tokens from real Auth login; gateway machine-token case NOT TESTED")
}

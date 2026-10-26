package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/security"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOpeningStockGateway(t *testing.T) {
	if os.Getenv("OPENING_GATEWAY_ISOLATED") != "1" {
		t.Skip("requires disposable opening gateway fixture")
	}
	run := os.Getenv("STOCK_GATEWAY_RUN_ID")
	if !regexp.MustCompile(`^inventory-gateway-[a-f0-9]{12}$`).MatchString(run) {
		t.Fatal("invalid fixture identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	g := &stockGateway{t: t, ctx: ctx, base: os.Getenv("WAREHOUSE_INVENTORY_URL"), client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	defer g.client.CloseIdleConnections()
	u, e := url.Parse(g.base)
	g.ok(e)
	g.require(u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.Path == "" && u.User == nil && u.RawQuery == "" && g.base == os.Getenv("WAREHOUSE_AUTH_URL"), "non-fixture gateway")
	connect := func(env, db, user string) *pgxpool.Pool {
		cfg, e := pgxpool.ParseConfig(os.Getenv(env))
		g.ok(e)
		g.require(cfg.ConnConfig.Host == "127.0.0.1" && cfg.ConnConfig.Database == db && cfg.ConnConfig.User == user, "non-fixture database")
		p, e := pgxpool.NewWithConfig(ctx, cfg)
		g.ok(e)
		t.Cleanup(p.Close)
		var marker string
		g.ok(p.QueryRow(ctx, "SELECT current_setting('stock.gateway_fixture',true)").Scan(&marker))
		g.require(marker == run, "fixture marker mismatch")
		return p
	}
	g.auth = connect("WAREHOUSE_AUTH_DATABASE_URL", "auth_security_test", "security_test")
	g.inventory = connect("WAREHOUSE_INVENTORY_DATABASE_URL", "inventory_db", "inventory_service")
	catalog := connect("OPENING_CATALOG_DATABASE_URL", "catalog_security_test", "security_test")
	pub := os.Getenv("WAREHOUSE_PUBLIC_KEY")
	g.require(filepath.Base(filepath.Dir(pub)) == run, "non-fixture public key")
	g.verifier, e = security.Load(pub, "erp-auth", "erp-api")
	g.ok(e)
	var granted int
	g.ok(g.auth.QueryRow(ctx, "SELECT count(*) FROM identity_role_permissions rp JOIN identity_permissions p ON p.id=rp.permission_id WHERE p.resource='inventory.opening-stock' AND p.action='post'").Scan(&granted))
	g.require(granted == 0, "opening migration must not assign grants")
	actor := g.login([]string{stock.OpeningPermission, balanceRead, movementRead}, "GLOBAL")
	reader := g.login([]string{balanceRead, movementRead}, "GLOBAL")
	scoped := g.login([]string{stock.OpeningPermission}, "WAREHOUSE")
	category, unit, product, variant, packed, pack := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx, e := catalog.Begin(ctx)
	g.ok(e)
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "INSERT INTO catalog_categories(id,name,code) VALUES($1,'Opening fixture',$2)", category, category.String())
	g.ok(e)
	_, e = tx.Exec(ctx, "INSERT INTO catalog_units(id,name,code) VALUES($1,$2,$3)", unit, "Piece-"+unit.String(), "PC-"+unit.String()[:20])
	g.ok(e)
	_, e = tx.Exec(ctx, "INSERT INTO catalog_products(id,category_id,name,product_code) VALUES($1,$2,'Opening plate',$3)", product, category, product.String())
	g.ok(e)
	for _, id := range []uuid.UUID{variant, packed} {
		_, e = tx.Exec(ctx, "INSERT INTO catalog_product_variants(id,product_id,sku,unit_id) VALUES($1,$2,$3,$4)", id, product, id.String(), unit)
		g.ok(e)
	}
	_, e = tx.Exec(ctx, "INSERT INTO catalog_piece_units(unit_id) VALUES($1)", unit)
	g.ok(e)
	for _, id := range []uuid.UUID{variant, packed} {
		_, e = tx.Exec(ctx, "INSERT INTO catalog_stock_eligibility(variant_id,base_unit_id) VALUES($1,$2)", id, unit)
		g.ok(e)
	}
	_, e = tx.Exec(ctx, "INSERT INTO catalog_variant_packs(id,variant_id,code,is_active) VALUES($1,$2,'BOX6',true)", pack, packed)
	g.ok(e)
	_, e = tx.Exec(ctx, "INSERT INTO catalog_pack_revisions(pack_id,revision,pieces_per_pack,label) VALUES($1,1,6,'Box of six')", pack)
	g.ok(e)
	g.ok(tx.Commit(ctx))
	wh, e := postgres.NewWarehouseRepository(g.inventory).Create(ctx, repository.CreateWarehouseParams{ID: uuid.New(), Code: "OPEN-" + strings.ToUpper(uuid.NewString()[:20]), Name: "Opening fixture"})
	g.ok(e)
	path := "/api/v1/inventory/warehouses/" + wh.ID.String() + "/opening-stock"
	request := func(token, key string, body any, status int) []byte {
		data, e := json.Marshal(body)
		g.ok(e)
		req, e := http.NewRequestWithContext(ctx, "POST", g.base+path, strings.NewReader(string(data)))
		g.ok(e)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, e := g.client.Do(req)
		g.ok(e)
		defer res.Body.Close()
		data, e = io.ReadAll(io.LimitReader(res.Body, 1<<20))
		g.ok(e)
		g.require(res.StatusCode == status, fmt.Sprintf("opening HTTP %d expected %d (body withheld)", res.StatusCode, status))
		g.require(strings.Contains(strings.ToLower(res.Header.Get("Via")), "kong"), "not Kong")
		g.require(res.Header.Get("Cache-Control") == "no-store", "missing no-store")
		return data
	}
	selections := map[string]any{"lines": []any{map[string]any{"line_number": 1, "variant_id": variant, "input_count": "100"}, map[string]any{"line_number": 2, "variant_id": packed, "input_count": "10", "pack_id": pack, "pack_revision": "1"}}}
	before := g.snapshot()
	path += "/prepare"
	request("", "", selections, 401)
	request("invalid", "", selections, 401)
	request(reader, "", selections, 403)
	request(scoped, "", selections, 403)
	var prepared struct {
		Lines []map[string]json.RawMessage `json:"lines"`
	}
	g.ok(json.Unmarshal(request(actor, "", selections, 200), &prepared))
	g.require(before == g.snapshot(), "preparation wrote stock")
	g.require(g.text(prepared.Lines[0], "pieces") == "100" && g.text(prepared.Lines[1], "pieces") == "60", "piece/pack conversion")
	op, key := uuid.New(), uuid.New()
	lines := []map[string]any{}
	for _, l := range prepared.Lines {
		var n int
		g.ok(json.Unmarshal(l["line_number"], &n))
		x := map[string]any{"line_number": n, "variant_id": g.text(l, "variant_id"), "input_count": g.text(l, "input_count"), "expected_stock_reference_revision": g.text(l, "expected_stock_reference_revision")}
		if _, ok := l["pack_id"]; ok {
			x["pack_id"] = g.text(l, "pack_id")
			x["pack_revision"] = g.text(l, "pack_revision")
		}
		lines = append(lines, x)
	}
	body := map[string]any{"operation_id": op, "lines": lines}
	path = strings.TrimSuffix(path, "/prepare")
	request(reader, key.String(), body, 403)
	request(scoped, key.String(), body, 403)
	// A reviewed revision must not silently become a different operation intent.
	_, e = catalog.Exec(ctx, "UPDATE catalog_products SET name='Changed opening plate' WHERE id=$1", product)
	g.ok(e)
	request(actor, key.String(), body, 409)
	g.require(before == g.snapshot(), "changed reference wrote stock")
	path += "/prepare"
	g.ok(json.Unmarshal(request(actor, "", selections, 200), &prepared))
	for i := range lines {
		lines[i]["expected_stock_reference_revision"] = g.text(prepared.Lines[i], "expected_stock_reference_revision")
	}
	path = strings.TrimSuffix(path, "/prepare")
	first := request(actor, key.String(), body, 201)
	var posted map[string]json.RawMessage
	g.ok(json.Unmarshal(first, &posted))
	g.require(string(posted["replayed"]) == "false", "new result replayed")
	after := g.snapshot()
	replay := request(actor, key.String(), body, 200)
	g.ok(json.Unmarshal(replay, &posted))
	g.require(string(posted["replayed"]) == "true" && after == g.snapshot(), "replay changed stock")
	body["operation_id"] = uuid.New()
	request(actor, uuid.NewString(), body, 409)
	g.require(after == g.snapshot(), "second opening wrote stock")
	balances := g.page("/api/v1/inventory/warehouses/"+wh.ID.String()+"/stock-balances", actor, "opening balances")
	g.require(len(balances.Items) == 2, "missing balances")
	movements := g.page("/api/v1/inventory/warehouses/"+wh.ID.String()+"/stock-movements", actor, "opening movements")
	g.require(len(movements.Items) == 2, "duplicate movements")
	var count int
	g.ok(g.inventory.QueryRow(ctx, "SELECT count(*) FROM stock_operations WHERE warehouse_id=$1", wh.ID).Scan(&count))
	g.require(count == 1, "more than one opening operation")
	path += "/prepare"
	request(actor, "", map[string]any{"lines": []any{map[string]any{"line_number": 1, "variant_id": uuid.New(), "input_count": "1"}}}, 404)
	ineligible := uuid.New()
	_, e = catalog.Exec(ctx, "INSERT INTO catalog_product_variants(id,product_id,sku,unit_id) VALUES($1,$2,$3,$4)", ineligible, product, ineligible.String(), unit)
	g.ok(e)
	request(actor, "", map[string]any{"lines": []any{map[string]any{"line_number": 1, "variant_id": ineligible, "input_count": "1"}}}, 409)
	tx, e = catalog.Begin(ctx)
	g.ok(e)
	_, e = tx.Exec(ctx, "INSERT INTO catalog_pack_revisions(pack_id,revision,pieces_per_pack,label) VALUES($1,2,12,'Box of twelve')", pack)
	g.ok(e)
	_, e = tx.Exec(ctx, "UPDATE catalog_variant_packs SET current_revision=2 WHERE id=$1", pack)
	g.ok(e)
	g.ok(tx.Commit(ctx))
	request(actor, "", selections, 409) // Explicit revision 1 is now obsolete.
	_, e = catalog.Exec(ctx, "UPDATE catalog_products SET is_active=false WHERE id=$1", product)
	g.ok(e)
	request(actor, "", selections, 409)
	path = strings.TrimSuffix(path, "/prepare")
	body["operation_id"] = op
	request(actor, key.String(), body, 200)
	g.require(after == g.snapshot(), "inactive replay changed stock")
	t.Log("PASS: real human login through Kong; HTTPS machine issuance and Catalog preparation; 100 pieces + 10 packs x 6; one operation/two movements; replay and second-key rejection unchanged")
}

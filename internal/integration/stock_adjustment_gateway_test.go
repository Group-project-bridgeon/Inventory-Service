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

func TestStockAdjustmentGateway(t *testing.T) {
	if os.Getenv("ADJUSTMENT_GATEWAY_ISOLATED") != "1" {
		t.Skip("requires disposable adjustment HTTPS gateway fixture")
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
	g.ok(g.auth.QueryRow(ctx, "SELECT count(*) FROM identity_role_permissions rp JOIN identity_permissions p ON p.id=rp.permission_id WHERE p.resource IN ('inventory.stock-adjustments','inventory.stock-corrections')").Scan(&granted))
	g.require(granted == 0, "adjustment definitions must not assign grants")
	actor := g.login([]string{stock.IncreasePermission, stock.DecreasePermission, stock.ReversePermission, balanceRead, movementRead}, "GLOBAL")
	reader := g.login([]string{balanceRead, movementRead}, "GLOBAL")
	scoped := g.login([]string{stock.IncreasePermission, stock.DecreasePermission, stock.ReversePermission}, "WAREHOUSE")
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
	path := "/api/v1/inventory/warehouses/" + wh.ID.String() + "/stock-adjustments"
	step := 0
	request := func(token, key string, body any, status int) []byte {
		step++
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
		var response struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &response)
		t.Logf("stock step=%d path=%s status=%d expected=%d code=%s", step, strings.TrimPrefix(path, "/api/v1/inventory/warehouses/"+wh.ID.String()), res.StatusCode, status, response.Code)
		g.require(res.StatusCode == status, fmt.Sprintf("stock step=%d HTTP %d expected %d (body withheld)", step, res.StatusCode, status))
		g.require(strings.Contains(strings.ToLower(res.Header.Get("Via")), "kong"), "not Kong")
		g.require(res.Header.Get("Cache-Control") == "no-store", "missing no-store")
		return data
	}

	increaseOnly := g.login([]string{stock.IncreasePermission}, "GLOBAL")
	reverseOnly := g.login([]string{stock.ReversePermission}, "GLOBAL")
	otherActor := g.login([]string{stock.DecreasePermission, stock.ReversePermission}, "GLOBAL")
	selections := map[string]any{"direction": "IN", "reason_code": "FOUND", "explanation": "Found physical stock", "lines": []any{map[string]any{"line_number": 1, "variant_id": variant, "input_count": "100"}, map[string]any{"line_number": 2, "variant_id": packed, "input_count": "10", "pack_id": pack, "pack_revision": "1"}}}
	before := g.snapshot()
	path += "/prepare"
	request("", "", selections, 401)
	request(reader, "", selections, 403)
	request(scoped, "", selections, 403)
	var preview struct {
		Lines []map[string]json.RawMessage `json:"lines"`
	}
	g.ok(json.Unmarshal(request(increaseOnly, "", selections, 200), &preview))
	g.require(before == g.snapshot(), "adjustment preparation wrote stock")
	g.require(g.text(preview.Lines[0], "pieces") == "100" && g.text(preview.Lines[1], "pieces") == "60", "adjustment conversion")
	lines := []map[string]any{}
	for _, l := range preview.Lines {
		var number int
		g.ok(json.Unmarshal(l["line_number"], &number))
		x := map[string]any{"line_number": number, "variant_id": g.text(l, "variant_id"), "input_count": g.text(l, "input_count"), "expected_stock_reference_revision": g.text(l, "expected_stock_reference_revision")}
		if _, ok := l["pack_id"]; ok {
			x["pack_id"] = g.text(l, "pack_id")
			x["pack_revision"] = g.text(l, "pack_revision")
		}
		lines = append(lines, x)
	}
	op, key := uuid.New(), uuid.NewString()
	body := map[string]any{"operation_id": op, "direction": "IN", "reason_code": "FOUND", "explanation": "  Found physical stock  ", "lines": lines}
	path = strings.TrimSuffix(path, "/prepare")
	request(actor, key, body, 201)
	after := g.snapshot()
	request(actor, key, body, 200)
	g.require(after == g.snapshot(), "increase replay changed stock")
	body["reason_code"] = "OTHER"
	request(actor, key, body, 409)
	body["reason_code"] = "FOUND"
	request(otherActor, key, body, 403) // Derived/direction permission is enforced before replay.
	// A valid decrease uses fresh direct-piece review, rather than the original pack request.
	decreaseSelection := map[string]any{"direction": "OUT", "reason_code": "DAMAGE", "explanation": "Damaged one physical piece", "lines": []any{map[string]any{"line_number": 1, "variant_id": variant, "input_count": "1"}}}
	path += "/prepare"
	request(increaseOnly, "", decreaseSelection, 403)
	var down struct {
		Lines []map[string]json.RawMessage `json:"lines"`
	}
	g.ok(json.Unmarshal(request(actor, "", decreaseSelection, 200), &down))
	downLine := map[string]any{"line_number": 1, "variant_id": variant, "input_count": "1", "expected_stock_reference_revision": g.text(down.Lines[0], "expected_stock_reference_revision")}
	decrease := map[string]any{"operation_id": uuid.New(), "direction": "OUT", "reason_code": "DAMAGE", "explanation": "Damaged one physical piece", "lines": []any{downLine}}
	path = strings.TrimSuffix(path, "/prepare")
	request(actor, uuid.NewString(), decrease, 201)
	// Original two-line reversal must fail atomically when one line is short.
	path = "/api/v1/inventory/warehouses/" + wh.ID.String() + "/stock-corrections/prepare"
	correction := map[string]any{"original_operation_id": op, "reason_code": "DATA_CORRECTION", "explanation": "Reverse mistaken receipt"}
	request(reverseOnly, "", correction, 403)
	var reversePreview struct {
		Direction string                       `json:"direction"`
		Lines     []map[string]json.RawMessage `json:"lines"`
	}
	g.ok(json.Unmarshal(request(otherActor, "", correction, 200), &reversePreview))
	g.require(reversePreview.Direction == "OUT", "caller controlled inverse direction")
	refs := []map[string]any{}
	for _, l := range reversePreview.Lines {
		refs = append(refs, map[string]any{"variant_id": g.text(l, "variant_id"), "expected_stock_reference_revision": g.text(l, "expected_stock_reference_revision")})
	}
	correction["references"] = refs
	correction["operation_id"] = uuid.New()
	reverseKey := uuid.NewString()
	path = strings.TrimSuffix(path, "/prepare")
	before = g.snapshot()
	request(otherActor, reverseKey, correction, 409)
	g.require(before == g.snapshot(), "insufficient reversal partially persisted")
	// Explicit replacement receipt is separate; it does not edit the original.
	path = "/api/v1/inventory/warehouses/" + wh.ID.String() + "/stock-adjustments"
	decrease["operation_id"] = uuid.New()
	decrease["direction"] = "IN"
	decrease["reason_code"] = "FOUND"
	request(actor, uuid.NewString(), decrease, 201)
	// The old pack is now obsolete and inactive, but its historical factor remains six.
	tx, e = catalog.Begin(ctx)
	g.ok(e)
	_, e = tx.Exec(ctx, "INSERT INTO catalog_pack_revisions(pack_id,revision,pieces_per_pack,label) VALUES($1,2,12,'Box of twelve')", pack)
	g.ok(e)
	_, e = tx.Exec(ctx, "UPDATE catalog_variant_packs SET current_revision=2,is_active=false WHERE id=$1", pack)
	g.ok(e)
	g.ok(tx.Commit(ctx))
	path = "/api/v1/inventory/warehouses/" + wh.ID.String() + "/stock-corrections/prepare"
	delete(correction, "references")
	delete(correction, "operation_id")
	g.ok(json.Unmarshal(request(otherActor, "", correction, 200), &reversePreview))
	refs = nil
	for _, l := range reversePreview.Lines {
		refs = append(refs, map[string]any{"variant_id": g.text(l, "variant_id"), "expected_stock_reference_revision": g.text(l, "expected_stock_reference_revision")})
	}
	g.require(g.text(reversePreview.Lines[1], "pieces") == "60", "obsolete pack revalued")
	correction["references"] = refs
	correction["operation_id"] = uuid.New()
	reverseKey = uuid.NewString()
	path = strings.TrimSuffix(path, "/prepare")
	request(otherActor, reverseKey, correction, 201)
	after = g.snapshot()
	request(otherActor, reverseKey, correction, 200)
	g.require(after == g.snapshot(), "reversal replay changed stock")
	correction["operation_id"] = uuid.New()
	request(otherActor, uuid.NewString(), correction, 409)
	// Completed result belongs to the reversing actor, independent of original actor.
	correctionOp := uuid.Nil
	g.ok(g.inventory.QueryRow(ctx, "SELECT operation_id FROM stock_operation_audit WHERE original_operation_id=$1", op).Scan(&correctionOp))
	correction["operation_id"] = correctionOp
	request(actor, reverseKey, correction, 403)
	balances := g.page("/api/v1/inventory/warehouses/"+wh.ID.String()+"/stock-balances", actor, "adjustment balances")
	for _, b := range balances.Items {
		g.require(g.text(b, "on_hand_pieces") == "0", "reversal balance not zero")
	}
	history := g.page("/api/v1/inventory/warehouses/"+wh.ID.String()+"/stock-movements", actor, "adjustment audit history")
	g.require(len(history.Items) == 6, "expected six immutable movements")
	reversed := 0
	for _, m := range history.Items {
		g.require(g.text(m, "reason_code") != "" && g.text(m, "explanation") != "", "missing audit DTO")
		if _, ok := m["reverses_operation_id"]; ok {
			reversed++
			g.require(g.text(m, "reverses_operation_id") == op.String(), "missing original linkage")
		}
	}
	g.require(reversed == 2, "whole reversal linkage")
	// Current inactivity blocks new corrections, completed replay needs no new Catalog check.
	_, e = catalog.Exec(ctx, "UPDATE catalog_products SET is_active=false WHERE id=$1", product)
	g.ok(e)
	request(otherActor, reverseKey, correction, 200)
	g.require(after == g.snapshot(), "inactive completed replay wrote stock")
	path += "/prepare"
	delete(correction, "references")
	delete(correction, "operation_id")
	request(otherActor, "", correction, 409)
	t.Log("PASS: real Auth login → Kong → HTTPS Catalog/machine issuance; IN 100+60, OUT 1, IN 1, whole OUT 100+60 = zero; six movements, four operations; cross-actor authorized reversal, owner-only one-effect replay; obsolete pack factor preserved")
}

package integration

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	f "github.com/Group-project-bridgeon/Inventory-Service/internal/fulfillment"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fulfillmentScopes = "inventory.dispatches:cancel inventory.dispatches:create inventory.dispatches:read"

type fulfillmentOutageState struct {
	Warehouse, Dispatch, CancelID, CancelOrder uuid.UUID
	Create, Confirm                            map[string]any
	CreateKey, ConfirmKey                      string
}

func fulfillmentTables(g *stockGateway) [32]byte {
	base := reservationTableSnapshot(g)
	var data string
	g.ok(g.inventory.QueryRow(g.ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM dispatches x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM fulfillment_transitions x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_kind,actor_id,key) FROM fulfillment_commands x))::text`).Scan(&data))
	return sha256.Sum256(append(base[:], []byte(data)...))
}
func fulfillmentFixtureFile(g *stockGateway, env string) string {
	p := os.Getenv(env)
	g.require(filepath.Dir(p) == filepath.Dir(os.Getenv("RESERVATION_SALES_CREDENTIAL_FILE")), "non-owned fulfillment fixture file")
	return p
}

// Retain only generated fixture credentials, never a JWT or any normal user's secret.
func prepareFulfillmentRelogin(g *stockGateway, token string) {
	claims, e := g.verifier.Verify(token)
	g.ok(e)
	random := make([]byte, 32)
	_, e = rand.Read(random)
	g.ok(e)
	password := hex.EncodeToString(random)
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	g.ok(e)
	var email string
	g.ok(g.auth.QueryRow(g.ctx, "SELECT email FROM identity_users WHERE id=$1", claims.UserID).Scan(&email))
	g.require(strings.HasSuffix(email, "@example.invalid"), "non-synthetic human fixture")
	tag, e := g.auth.Exec(g.ctx, "UPDATE identity_credentials SET password_hash=$1 WHERE user_id=$2", string(hash), claims.UserID)
	g.ok(e)
	g.require(tag.RowsAffected() == 1, "fixture human credential count")
	data, e := json.Marshal(map[string]string{"email": email, "password": password})
	g.ok(e)
	g.ok(os.WriteFile(fulfillmentFixtureFile(g, "FULFILLMENT_HUMAN_FILE"), data, 0600))
}
func fulfillmentRelogin(g *stockGateway) string {
	data, e := os.ReadFile(fulfillmentFixtureFile(g, "FULFILLMENT_HUMAN_FILE"))
	g.ok(e)
	var credentials map[string]string
	g.ok(json.Unmarshal(data, &credentials))
	var response struct {
		Access string `json:"access_token"`
	}
	g.ok(json.Unmarshal(g.request("/api/v1/auth/login", "", "fixture human relogin", 200, credentials), &response))
	_, e = g.verifier.Verify(response.Access)
	g.ok(e)
	return response.Access
}
func TestFulfillmentGateway(t *testing.T) {
	if os.Getenv("FULFILLMENT_GATEWAY_ISOLATED") != "1" {
		t.Skip("requires uniquely owned fulfillment HTTPS fixture")
	}
	g, catalog := reservationGateway(t)
	salesFile, otherFile := os.Getenv("RESERVATION_SALES_CREDENTIAL_FILE"), os.Getenv("RESERVATION_OTHER_CREDENTIAL_FILE")
	sales := issueReservationToken(g, salesFile, f.Audience, fulfillmentScopes, 200)
	other := issueReservationToken(g, otherFile, f.Audience, fulfillmentScopes, 200)
	readOnly := issueReservationToken(g, salesFile, f.Audience, f.ReadPermission, 200)
	resSales := issueReservationToken(g, salesFile, stock.ReservationAudience, reservationScopes, 200)
	resOther := issueReservationToken(g, otherFile, stock.ReservationAudience, reservationScopes, 200)
	cat := issueReservationToken(g, filepath.Join(filepath.Dir(salesFile), "credential.json"), "catalog-stock-references", "catalog.stock-references:read", 200)
	issueReservationToken(g, salesFile, f.Audience, f.DispatchPermission, 400)
	issueReservationToken(g, salesFile, f.Audience, f.DeliveryPermission, 400)
	var granted int
	g.ok(g.auth.QueryRow(g.ctx, "SELECT count(*) FROM identity_role_permissions rp JOIN identity_permissions p ON p.id=rp.permission_id WHERE p.resource IN('inventory.dispatches','inventory.deliveries')").Scan(&granted))
	g.require(granted == 0, "fulfillment migration automatically granted authority")
	seedActor := g.login([]string{stock.OpeningPermission, balanceRead, movementRead}, "GLOBAL")
	human := g.login([]string{f.DispatchPermission, f.DeliveryPermission, f.ReadPermission}, "GLOBAL")
	prepareFulfillmentRelogin(g, human)
	reader := g.login([]string{f.ReadPermission}, "GLOBAL")
	scoped := g.login([]string{f.DispatchPermission, f.DeliveryPermission, f.ReadPermission}, "WAREHOUSE")
	category, unit, product, variant := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx, e := catalog.Begin(g.ctx)
	g.ok(e)
	defer tx.Rollback(g.ctx)
	_, e = tx.Exec(g.ctx, "INSERT INTO catalog_categories(id,name,code) VALUES($1,'Fulfillment fixture',$2)", category, category.String())
	g.ok(e)
	_, e = tx.Exec(g.ctx, "INSERT INTO catalog_units(id,name,code) VALUES($1,$2,$3)", unit, "Fulfillment piece "+unit.String(), "PC-"+unit.String()[:20])
	g.ok(e)
	_, e = tx.Exec(g.ctx, "INSERT INTO catalog_products(id,category_id,name,product_code) VALUES($1,$2,'Fulfillment product',$3)", product, category, product.String())
	g.ok(e)
	_, e = tx.Exec(g.ctx, "INSERT INTO catalog_product_variants(id,product_id,sku,unit_id) VALUES($1,$2,$3,$4)", variant, product, variant.String(), unit)
	g.ok(e)
	_, e = tx.Exec(g.ctx, "INSERT INTO catalog_piece_units(unit_id) VALUES($1)", unit)
	g.ok(e)
	_, e = tx.Exec(g.ctx, "INSERT INTO catalog_stock_eligibility(variant_id,base_unit_id) VALUES($1,$2)", variant, unit)
	g.ok(e)
	g.ok(tx.Commit(g.ctx))
	warehouse := func(pieces string) uuid.UUID {
		w, e := postgres.NewWarehouseRepository(g.inventory).Create(g.ctx, repository.CreateWarehouseParams{ID: uuid.New(), Code: "FUL-" + strings.ToUpper(uuid.NewString()[:20]), Name: "Fulfillment fixture"})
		g.ok(e)
		base := "/api/v1/inventory/warehouses/" + w.ID.String()
		input := map[string]any{"lines": []any{map[string]any{"line_number": 1, "variant_id": variant, "input_count": pieces}}}
		var preview struct {
			Lines []map[string]json.RawMessage `json:"lines"`
		}
		g.ok(json.Unmarshal(reservationRequestHTTP(g, base+"/opening-stock/prepare", seedActor, "", "fulfillment seed prepare", input, 200), &preview))
		reservationRequestHTTP(g, base+"/opening-stock", seedActor, uuid.NewString(), "fulfillment seed stock", map[string]any{"operation_id": uuid.New(), "lines": []any{map[string]any{"line_number": 1, "variant_id": variant, "input_count": pieces, "expected_stock_reference_revision": g.text(preview.Lines[0], "expected_stock_reference_revision")}}}, 201)
		return w.ID
	}
	w1, w2 := warehouse("100"), warehouse("20")
	reserve := func(wh uuid.UUID, pieces, token string) (uuid.UUID, uuid.UUID) {
		id, order := uuid.New(), uuid.New()
		body := map[string]any{"command_id": uuid.New(), "reservation_id": id, "order_id": order, "generation": "1", "expected_previous_generation": "0", "expected_order_version": "0", "lines": []any{map[string]any{"order_line_id": uuid.New(), "variant_id": variant, "pieces": pieces}}}
		reservationRequestHTTP(g, "/api/v1/inventory/warehouses/"+wh.String()+"/reservations", token, uuid.NewString(), "fulfillment reserve", body, 201)
		return id, order
	}
	draft := func(hold, order uuid.UUID) map[string]any {
		return map[string]any{"command_id": uuid.New(), "dispatch_id": uuid.New(), "reservation_id": hold, "order_id": order, "generation": "1", "expected_reservation_version": "1", "expected_order_version": "1", "external_reference": "EXAMPLE"}
	}
	confirm := func() map[string]any {
		return map[string]any{"command_id": uuid.New(), "expected_dispatch_version": "1", "expected_reservation_version": "1", "expected_order_version": "1"}
	}
	hold, order := reserve(w1, "40", resSales)
	create := draft(hold, order)
	id := create["dispatch_id"].(uuid.UUID)
	machineBase := "/api/v1/inventory/sales/warehouses/" + w1.String() + "/dispatches"
	humanBase := "/api/v1/inventory/warehouses/" + w1.String() + "/dispatches/" + id.String()
	key := uuid.NewString()
	for _, token := range []string{human, resSales, cat} {
		reservationRequestHTTP(g, machineBase, token, key, "wrong token kind/audience on draft", create, 401)
	}
	reservationRequestHTTP(g, machineBase, readOnly, key, "read does not imply create", create, 403)
	reservationRequestHTTP(g, machineBase, other, uuid.NewString(), "wrong owner cannot create draft", create, 404)
	beforeStock := reservationTableSnapshot(g)
	reservationRequestHTTP(g, machineBase, sales, key, "create draft", create, 201)
	g.require(beforeStock == reservationTableSnapshot(g), "draft changed stock/reservation")
	before := fulfillmentTables(g)
	reservationRequestHTTP(g, machineBase, sales, key, "draft replay", create, 200)
	g.require(before == fulfillmentTables(g), "draft replay wrote data")
	changed := map[string]any{}
	for k, v := range create {
		changed[k] = v
	}
	changed["external_reference"] = "CHANGED"
	reservationRequestHTTP(g, machineBase, sales, key, "draft changed-payload conflict", changed, 409)
	duplicate := draft(hold, order)
	reservationRequestHTTP(g, machineBase, sales, uuid.NewString(), "different-key live draft rejected", duplicate, 409)
	reservationRequestHTTP(g, "/api/v1/inventory/sales/dispatches/"+id.String(), other, "", "wrong owner read", nil, 404)
	reservationRequestHTTP(g, machineBase+"/"+id.String()+"/cancel", other, uuid.NewString(), "wrong owner cancel", map[string]any{"command_id": uuid.New(), "expected_dispatch_version": "1"}, 404)
	terminal := func(o uuid.UUID) map[string]any {
		return map[string]any{"command_id": uuid.New(), "order_id": o, "generation": "1", "expected_reservation_version": "1", "expected_order_version": "1"}
	}
	reservationRequestHTTP(g, "/api/v1/inventory/warehouses/"+w1.String()+"/reservations/"+hold.String()+"/consume", resSales, uuid.NewString(), "standalone consume cannot bypass draft", terminal(order), 409)
	dispatch := confirm()
	dispatchKey := uuid.NewString()
	reservationRequestHTTP(g, humanBase+"/dispatch", sales, dispatchKey, "machine cannot human-dispatch", dispatch, 401)
	reservationRequestHTTP(g, humanBase+"/dispatch", reader, dispatchKey, "read does not imply dispatch", dispatch, 403)
	reservationRequestHTTP(g, humanBase+"/dispatch", scoped, dispatchKey, "WAREHOUSE grant cannot replace GLOBAL", dispatch, 403)
	reservationRequestHTTP(g, humanBase+"/dispatch", human, dispatchKey, "human confirms full dispatch", dispatch, 200)
	before = fulfillmentTables(g)
	reservationRequestHTTP(g, humanBase+"/dispatch", human, dispatchKey, "dispatch replay", dispatch, 200)
	g.require(before == fulfillmentTables(g), "dispatch replay changed data")
	var onhand, reserved int64
	g.ok(g.inventory.QueryRow(g.ctx, "SELECT on_hand_pieces,reserved_pieces FROM stock_balances WHERE warehouse_id=$1 AND variant_id=$2", w1, variant).Scan(&onhand, &reserved))
	g.require(onhand == 60 && reserved == 0, "dispatch deducted twice or left hold")
	claims, e := g.verifier.Verify(human)
	g.ok(e)
	var correct bool
	g.ok(g.inventory.QueryRow(g.ctx, "SELECT o.actor_kind='HUMAN' AND o.actor_user_id=$1 AND o.actor_machine_id IS NULL AND d.owner_machine_id<>o.actor_user_id FROM stock_operations o JOIN dispatches d ON d.operation_id=o.id WHERE d.id=$2", claims.UserID, id).Scan(&correct))
	g.require(correct, "actual actor/owner distinction lost")
	beforeStock = reservationTableSnapshot(g)
	failed := map[string]any{"command_id": uuid.New(), "expected_dispatch_version": "2", "outcome": "FAILED", "note": "Fixture failed delivery; no restock"}
	reservationRequestHTTP(g, humanBase+"/delivery-outcomes", reader, uuid.NewString(), "read does not imply delivery update", failed, 403)
	reservationRequestHTTP(g, humanBase+"/delivery-outcomes", human, uuid.NewString(), "failed delivery remains dispatched", failed, 200)
	g.require(beforeStock == reservationTableSnapshot(g), "failure changed physical/reservation facts")
	reservationRequestHTTP(g, "/api/v1/inventory/dispatches/"+id.String(), reader, "", "human exact read", nil, 200)
	reservationRequestHTTP(g, "/api/v1/inventory/sales/dispatches/"+id.String()+"/transitions?limit=1", sales, "", "owner paginated audit", nil, 200)
	// Cross-owner and cross-warehouse human authority is explicit, without impersonation.
	hold2, order2 := reserve(w2, "5", resOther)
	otherCreate := draft(hold2, order2)
	id2 := otherCreate["dispatch_id"].(uuid.UUID)
	reservationRequestHTTP(g, "/api/v1/inventory/sales/warehouses/"+w2.String()+"/dispatches", other, uuid.NewString(), "other-owner warehouse draft", otherCreate, 201)
	reservationRequestHTTP(g, "/api/v1/inventory/warehouses/"+w2.String()+"/dispatches/"+id2.String()+"/dispatch", human, uuid.NewString(), "GLOBAL human across owner/warehouse", confirm(), 200)
	reservationRequestHTTP(g, "/api/v1/inventory/sales/dispatches/"+id2.String(), sales, "", "machine read remains owner-restricted", nil, 404)
	reservationRequestHTTP(g, "/api/v1/inventory/dispatches/"+id2.String(), reader, "", "GLOBAL human cross-owner read", nil, 200)
	// Keep a DRAFT for actual Catalog outage, proving cancellation does not release.
	cancelHold, cancelOrder := reserve(w1, "10", resSales)
	cancelCreate := draft(cancelHold, cancelOrder)
	cancelID := cancelCreate["dispatch_id"].(uuid.UUID)
	reservationRequestHTTP(g, machineBase, sales, uuid.NewString(), "outage draft", cancelCreate, 201)
	_, e = catalog.Exec(g.ctx, "UPDATE catalog_products SET is_active=false WHERE id=$1", product)
	g.ok(e)
	before = fulfillmentTables(g)
	reservationRequestHTTP(g, "/api/v1/inventory/warehouses/"+w1.String()+"/dispatches/"+cancelID.String()+"/dispatch", human, uuid.NewString(), "inactive Catalog blocks dispatch", confirm(), 409)
	g.require(before == fulfillmentTables(g), "inactive attempt wrote data")
	_, e = catalog.Exec(g.ctx, "UPDATE catalog_products SET is_active=true WHERE id=$1", product)
	g.ok(e)
	state := fulfillmentOutageState{w1, id, cancelID, cancelOrder, create, dispatch, key, dispatchKey}
	data, e := json.Marshal(state)
	g.ok(e)
	g.ok(os.WriteFile(fulfillmentFixtureFile(g, "FULFILLMENT_STATE_FILE"), data, 0600))
	t.Log("PASS: actual Auth HTTPS/Kong machine issuance + human login; exact resource/action pairs; draft no physical effect; dispatch40 once to on-hand60/reserved0; FAILED no restock; actual human separate from Sales owner; cross-owner/warehouse GLOBAL operations; scoped/owner/audience denials")
}

// A stopped fixture can fail immediately (503) or exhaust the existing reference
// timeout (504). Accept only those exact documented status/code pairs, never success.
func fulfillmentDependencyFailure(g *stockGateway, path, token string, body any) {
	data, e := json.Marshal(body)
	g.ok(e)
	request, e := http.NewRequestWithContext(g.ctx, "POST", g.base+path, bytes.NewReader(data))
	g.ok(e)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response, e := g.client.Do(request)
	g.ok(e)
	defer response.Body.Close()
	data, e = io.ReadAll(io.LimitReader(response.Body, 16384))
	g.ok(e)
	var failure struct {
		Code string `json:"code"`
	}
	g.ok(json.Unmarshal(data, &failure))
	g.require((response.StatusCode == 503 && failure.Code == "reference_unavailable") || (response.StatusCode == 504 && failure.Code == "timeout"), "stopped Catalog returned unexpected status/code (body withheld)")
	g.require(response.Header.Get("Cache-Control") == "no-store" && strings.Contains(strings.ToLower(response.Header.Get("Via")), "kong"), "dependency response bypassed policy/Kong")
	g.t.Logf("Catalog-down new dispatch rejected HTTP %d code=%s", response.StatusCode, failure.Code)
}

func TestFulfillmentGatewayOutage(t *testing.T) {
	if os.Getenv("FULFILLMENT_OUTAGE_ISOLATED") != "1" {
		t.Skip("requires owner fixture Catalog stopped")
	}
	g, _ := reservationGateway(t)
	data, e := os.ReadFile(fulfillmentFixtureFile(g, "FULFILLMENT_STATE_FILE"))
	g.ok(e)
	var state fulfillmentOutageState
	g.ok(json.Unmarshal(data, &state))
	human := fulfillmentRelogin(g)
	salesFile := os.Getenv("RESERVATION_SALES_CREDENTIAL_FILE")
	sales := issueReservationToken(g, salesFile, f.Audience, fulfillmentScopes, 200)
	machineBase := "/api/v1/inventory/sales/warehouses/" + state.Warehouse.String() + "/dispatches"
	humanBase := "/api/v1/inventory/warehouses/" + state.Warehouse.String() + "/dispatches/" + state.Dispatch.String()
	before := fulfillmentTables(g)
	reservationRequestHTTP(g, machineBase, sales, state.CreateKey, "Catalog-down draft completed replay", state.Create, 200)
	reservationRequestHTTP(g, humanBase+"/dispatch", human, state.ConfirmKey, "Catalog-down dispatch completed replay", state.Confirm, 200)
	reservationRequestHTTP(g, "/api/v1/inventory/dispatches/"+state.Dispatch.String(), human, "", "Catalog-down dispatch detail", nil, 200)
	reservationRequestHTTP(g, "/api/v1/inventory/sales/dispatches?limit=1", sales, "", "Catalog-down owner list", nil, 200)
	g.require(before == fulfillmentTables(g), "completed replay/read wrote data")
	confirm := map[string]any{"command_id": uuid.New(), "expected_dispatch_version": "1", "expected_reservation_version": "1", "expected_order_version": "1"}
	fulfillmentDependencyFailure(g, "/api/v1/inventory/warehouses/"+state.Warehouse.String()+"/dispatches/"+state.CancelID.String()+"/dispatch", human, confirm)
	g.require(before == fulfillmentTables(g), "Catalog-down rejection wrote data")
	physical := reservationTableSnapshot(g)
	delivered := map[string]any{"command_id": uuid.New(), "expected_dispatch_version": "3", "outcome": "DELIVERED", "external_reference": "EXAMPLE-DELIVERED"}
	key := uuid.NewString()
	reservationRequestHTTP(g, humanBase+"/delivery-outcomes", human, key, "Catalog-down delivery succeeds without stock", delivered, 200)
	g.require(physical == reservationTableSnapshot(g), "delivery restocked or reissued")
	after := fulfillmentTables(g)
	reservationRequestHTTP(g, humanBase+"/delivery-outcomes", human, key, "delivery replay", delivered, 200)
	g.require(after == fulfillmentTables(g), "delivery replay wrote data")
	cancel := map[string]any{"command_id": uuid.New(), "expected_dispatch_version": "1"}
	reservationRequestHTTP(g, machineBase+"/"+state.CancelID.String()+"/cancel", sales, uuid.NewString(), "Catalog-down draft cancel leaves hold", cancel, 200)
	g.require(physical == reservationTableSnapshot(g), "draft cancellation released hold")
	claims, e := g.verifier.Verify(human)
	g.ok(e)
	_, e = g.auth.Exec(g.ctx, "DELETE FROM identity_role_permissions rp USING identity_user_roles ur,identity_permissions p WHERE ur.user_id=$1 AND ur.role_id=rp.role_id AND p.id=rp.permission_id AND p.resource='inventory.dispatches' AND p.action='dispatch'", claims.UserID)
	g.ok(e)
	reduced := fulfillmentRelogin(g)
	before = fulfillmentTables(g)
	reservationRequestHTTP(g, humanBase+"/dispatch", reduced, state.ConfirmKey, "current authority required before completed replay", state.Confirm, 403)
	g.require(before == fulfillmentTables(g), "unauthorized replay wrote data")
	t.Log("PASS: real Catalog stopped; fresh Auth issuance/login; completed replay/read, full delivery and draft-only cancellation work without Catalog; new dispatch rejected with documented dependency/timeout status; no stock compensation; renewed JWT without exact GLOBAL action denied replay")
}

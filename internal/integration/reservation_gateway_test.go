package integration

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
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

const reservationScopes = "inventory.reservations:consume inventory.reservations:read inventory.reservations:release inventory.reservations:reserve"

func reservationGateway(t *testing.T) (*stockGateway, *pgxpool.Pool) {
	if os.Getenv("RESERVATION_GATEWAY_ISOLATED") != "1" {
		t.Skip("requires disposable HTTPS reservation gateway fixture")
	}
	run := os.Getenv("STOCK_GATEWAY_RUN_ID")
	if !regexp.MustCompile(`^inventory-gateway-[a-f0-9]{12}$`).MatchString(run) {
		t.Fatal("invalid fixture run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	g := &stockGateway{t: t, ctx: ctx, base: os.Getenv("RESERVATION_TLS_GATEWAY_URL")}
	u, err := url.Parse(g.base)
	g.ok(err)
	g.require(u.Scheme == "https" && u.Hostname() == "localhost" && u.Port() != "" && u.User == nil && u.Path == "" && u.RawQuery == "", "non-fixture TLS gateway")
	ca := os.Getenv("RESERVATION_GATEWAY_CA_FILE")
	g.require(filepath.Base(filepath.Dir(ca)) == run && filepath.Base(ca) == "server.pem", "non-fixture CA")
	pem, err := os.ReadFile(ca)
	g.ok(err)
	roots := x509.NewCertPool()
	g.require(roots.AppendCertsFromPEM(pem), "fixture CA invalid")
	g.client = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(g.client.CloseIdleConnections)
	connect := func(env, database, user string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(os.Getenv(env))
		g.ok(err)
		g.require(cfg.ConnConfig.Host == "127.0.0.1" && cfg.ConnConfig.Database == database && cfg.ConnConfig.User == user, "non-fixture database")
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		g.ok(err)
		t.Cleanup(pool.Close)
		var marker string
		g.ok(pool.QueryRow(ctx, "SELECT current_setting('stock.gateway_fixture',true)").Scan(&marker))
		g.require(marker == run, "fixture marker mismatch")
		return pool
	}
	g.auth = connect("WAREHOUSE_AUTH_DATABASE_URL", "auth_security_test", "security_test")
	g.inventory = connect("WAREHOUSE_INVENTORY_DATABASE_URL", "inventory_db", "inventory_service")
	catalog := connect("OPENING_CATALOG_DATABASE_URL", "catalog_security_test", "security_test")
	public := os.Getenv("WAREHOUSE_PUBLIC_KEY")
	g.require(filepath.Base(filepath.Dir(public)) == run, "non-fixture public key")
	g.verifier, err = security.Load(public, "erp-auth", "erp-api")
	g.ok(err)
	return g, catalog
}

type fixtureCredential struct {
	Client string `json:"client_id"`
	ID     string `json:"credential_id"`
	Secret string `json:"secret"`
}

func reservationCredential(g *stockGateway, path string) fixtureCredential {
	g.require(filepath.Base(filepath.Dir(path)) == os.Getenv("STOCK_GATEWAY_RUN_ID"), "non-fixture credential file")
	data, err := os.ReadFile(path)
	g.ok(err)
	var c fixtureCredential
	g.ok(json.Unmarshal(data, &c))
	if c.Client == "" {
		c.Client = os.Getenv("OPENING_FIXTURE_MACHINE_ID")
	}
	id, err := uuid.Parse(c.Client)
	g.ok(err)
	g.require(id != uuid.Nil && id.String() == c.Client, "fixture client identity")
	return c
}
func issueReservationToken(g *stockGateway, file, audience, scope string, status int) string {
	credential := reservationCredential(g, file)
	form := url.Values{"grant_type": {"client_credentials"}, "audience": {audience}, "scope": {scope}}
	req, err := http.NewRequestWithContext(g.ctx, "POST", g.base+"/api/v1/auth/service-token", strings.NewReader(form.Encode()))
	g.ok(err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(credential.Client, credential.ID+"."+credential.Secret)
	response, err := g.client.Do(req)
	g.ok(err)
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16384))
	g.ok(err)
	g.require(response.StatusCode == status, fmt.Sprintf("actual machine issuance HTTP %d expected %d (values withheld)", response.StatusCode, status))
	g.require(response.TLS != nil, "machine credential path was not TLS")
	g.require(strings.Contains(strings.ToLower(response.Header.Get("Via")), "kong"), "issuance bypassed Kong")
	if status != 200 {
		return ""
	}
	var token struct {
		Access  string `json:"access_token"`
		Type    string `json:"token_type"`
		Expires int    `json:"expires_in"`
	}
	g.ok(json.Unmarshal(data, &token))
	g.require(token.Type == "Bearer" && token.Expires > 0 && token.Expires <= 300 && token.Access != "", "issued token contract")
	return token.Access
}
func reservationRequestHTTP(g *stockGateway, path, token, key, label string, body any, status int) []byte {
	method := "GET"
	var input io.Reader
	if body != nil {
		method = "POST"
		data, err := json.Marshal(body)
		g.ok(err)
		input = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(g.ctx, method, g.base+path, input)
	g.ok(err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := g.client.Do(req)
	g.ok(err)
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	g.ok(err)
	g.require(len(data) <= 1<<20, "fixture response limit")
	var failure struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(data, &failure)
	g.t.Logf("%s HTTP %d code=%s", label, response.StatusCode, failure.Code)
	g.require(response.StatusCode == status, "reservation HTTP status mismatch (body/token withheld)")
	g.require(response.Header.Get("Cache-Control") == "no-store", "missing no-store")
	g.require(strings.Contains(strings.ToLower(response.Header.Get("Via")), "kong"), "not real Kong")
	return data
}
func reservationTableSnapshot(g *stockGateway) [32]byte {
	base := g.snapshot()
	var value string
	g.ok(g.inventory.QueryRow(g.ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY owner_machine_id,order_id) FROM reservation_orders x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM stock_reservations x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY reservation_id,line_number) FROM reservation_lines x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM reservation_transitions x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY owner_machine_id,key) FROM reservation_commands x))::text`).Scan(&value))
	return sha256.Sum256(append(base[:], []byte(value)...))
}

type reservationOutageState struct {
	Warehouse  uuid.UUID
	HoldID     uuid.UUID
	Reserve    map[string]any
	ReserveKey string
	Consume    map[string]any
	ConsumeKey string
	ConsumeID  uuid.UUID
	Release    map[string]any
	ReleaseKey string
}

func TestReservationGateway(t *testing.T) {
	g, catalog := reservationGateway(t)
	salesFile := os.Getenv("RESERVATION_SALES_CREDENTIAL_FILE")
	otherFile := os.Getenv("RESERVATION_OTHER_CREDENTIAL_FILE")
	sales := issueReservationToken(g, salesFile, stock.ReservationAudience, reservationScopes, 200)
	readOnly := issueReservationToken(g, salesFile, stock.ReservationAudience, stock.ReservationReadPermission, 200)
	other := issueReservationToken(g, otherFile, stock.ReservationAudience, reservationScopes, 200)
	catFile := filepath.Join(filepath.Dir(salesFile), "credential.json")
	catalogToken := issueReservationToken(g, catFile, "catalog-stock-references", "catalog.stock-references:read", 200)
	issueReservationToken(g, catFile, stock.ReservationAudience, stock.ReservePermission, 401)
	issueReservationToken(g, salesFile, "catalog-stock-references", "catalog.stock-references:read", 401)
	issueReservationToken(g, salesFile, stock.ReservationAudience, "inventory.reservations:*", 400)
	actor := g.login([]string{stock.OpeningPermission, stock.IncreasePermission, stock.DecreasePermission, stock.ReversePermission, balanceRead, movementRead}, "GLOBAL")
	category, unit, product, a, b := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx, err := catalog.Begin(g.ctx)
	g.ok(err)
	defer tx.Rollback(g.ctx)
	_, err = tx.Exec(g.ctx, "INSERT INTO catalog_categories(id,name,code) VALUES($1,'Reservation fixture',$2)", category, category.String())
	g.ok(err)
	_, err = tx.Exec(g.ctx, "INSERT INTO catalog_units(id,name,code) VALUES($1,$2,$3)", unit, "Reservation piece "+unit.String(), "PC-"+unit.String()[:20])
	g.ok(err)
	_, err = tx.Exec(g.ctx, "INSERT INTO catalog_products(id,category_id,name,product_code) VALUES($1,$2,'Reservation product',$3)", product, category, product.String())
	g.ok(err)
	_, err = tx.Exec(g.ctx, "INSERT INTO catalog_piece_units(unit_id) VALUES($1)", unit)
	g.ok(err)
	for _, id := range []uuid.UUID{a, b} {
		_, err = tx.Exec(g.ctx, "INSERT INTO catalog_product_variants(id,product_id,sku,unit_id) VALUES($1,$2,$3,$4)", id, product, id.String(), unit)
		g.ok(err)
		_, err = tx.Exec(g.ctx, "INSERT INTO catalog_stock_eligibility(variant_id,base_unit_id) VALUES($1,$2)", id, unit)
		g.ok(err)
	}
	g.ok(tx.Commit(g.ctx))
	warehouse, err := postgres.NewWarehouseRepository(g.inventory).Create(g.ctx, repository.CreateWarehouseParams{ID: uuid.New(), Code: "RES-" + strings.ToUpper(uuid.NewString()[:20]), Name: "Reservation fixture"})
	g.ok(err)
	base := "/api/v1/inventory/warehouses/" + warehouse.ID.String()
	path := base + "/reservations"
	opening := map[string]any{"lines": []any{map[string]any{"line_number": 1, "variant_id": a, "input_count": "100"}, map[string]any{"line_number": 2, "variant_id": b, "input_count": "50"}}}
	var preview struct {
		Lines []map[string]json.RawMessage `json:"lines"`
	}
	g.ok(json.Unmarshal(reservationRequestHTTP(g, base+"/opening-stock/prepare", actor, "", "opening prepare", opening, 200), &preview))
	openingLines := []any{}
	for i, l := range preview.Lines {
		openingLines = append(openingLines, map[string]any{"line_number": i + 1, "variant_id": g.text(l, "variant_id"), "input_count": g.text(l, "input_count"), "expected_stock_reference_revision": g.text(l, "expected_stock_reference_revision")})
	}
	openingID := uuid.New()
	reservationRequestHTTP(g, base+"/opening-stock", actor, uuid.NewString(), "opening seed", map[string]any{"operation_id": openingID, "lines": openingLines}, 201)
	holdID, orderID := uuid.New(), uuid.New()
	reserve := map[string]any{"command_id": uuid.New(), "reservation_id": holdID, "order_id": orderID, "generation": "1", "expected_previous_generation": "0", "expected_order_version": "0", "lines": []any{map[string]any{"order_line_id": uuid.New(), "variant_id": a, "pieces": "70"}, map[string]any{"order_line_id": uuid.New(), "variant_id": b, "pieces": "20"}}}
	terminal := func(order uuid.UUID) map[string]any {
		return map[string]any{"command_id": uuid.New(), "order_id": order, "generation": "1", "expected_order_version": "1", "expected_reservation_version": "1"}
	}
	early := terminal(orderID)
	earlyKey := uuid.NewString()
	before := reservationTableSnapshot(g)
	reservationRequestHTTP(g, path+"/"+holdID.String()+"/release", sales, earlyKey, "release before reserve rejects", early, 409)
	g.require(before == reservationTableSnapshot(g), "early release wrote a tombstone or key")
	reservationRequestHTTP(g, path, actor, uuid.NewString(), "human rejected", reserve, 401)
	reservationRequestHTTP(g, path, catalogToken, uuid.NewString(), "Catalog audience rejected", reserve, 401)
	reservationRequestHTTP(g, path, readOnly, uuid.NewString(), "read-only scope rejected", reserve, 403)
	g.request("/api/v1/inventory/warehouses", sales, "reservation machine rejected by human route", 401, nil)
	reserveKey := uuid.NewString()
	reservationRequestHTTP(g, path, sales, reserveKey, "full order reserved", reserve, 201)
	after := reservationTableSnapshot(g)
	reservationRequestHTTP(g, path, sales, reserveKey, "reserve replay", reserve, 200)
	g.require(after == reservationTableSnapshot(g), "reserve replay changed data")
	reserve["command_id"] = uuid.New()
	reservationRequestHTTP(g, path, sales, reserveKey, "changed command key conflict", reserve, 409)
	reservationRequestHTTP(g, path, sales, uuid.NewString(), "different-key business duplicate", reserve, 409)
	var originalCommand uuid.UUID
	g.ok(g.inventory.QueryRow(g.ctx, "SELECT command_id FROM reservation_commands WHERE reservation_id=$1 AND action='RESERVE'", holdID).Scan(&originalCommand))
	reserve["command_id"] = originalCommand
	reservationRequestHTTP(g, "/api/v1/inventory/reservations/"+holdID.String(), other, "", "wrong owner status", nil, 404)
	reservationRequestHTTP(g, path+"/"+holdID.String()+"/consume", other, uuid.NewString(), "wrong owner consume", terminal(orderID), 409)
	pages := g.page(base+"/stock-balances", actor, "available balances")
	for _, row := range pages.Items {
		g.require(g.text(row, "available_pieces") == "30", "incorrect available quantity")
	}
	adjustment := map[string]any{"operation_id": uuid.New(), "direction": "OUT", "reason_code": "LOSS", "explanation": "Fixture held-stock protection", "lines": []any{map[string]any{"line_number": 1, "variant_id": a, "input_count": "40", "expected_stock_reference_revision": g.text(preview.Lines[0], "expected_stock_reference_revision")}}}
	reservationRequestHTTP(g, base+"/stock-adjustments", actor, uuid.NewString(), "OUT cannot spend held stock", adjustment, 409)
	correction := map[string]any{"original_operation_id": openingID, "reason_code": "DATA_CORRECTION", "explanation": "Fixture inverse must respect holds"}
	var inverse struct {
		Lines []map[string]json.RawMessage `json:"lines"`
	}
	g.ok(json.Unmarshal(reservationRequestHTTP(g, base+"/stock-corrections/prepare", actor, "", "inverse prepare", correction, 200), &inverse))
	refs := []any{}
	for _, l := range inverse.Lines {
		refs = append(refs, map[string]any{"variant_id": g.text(l, "variant_id"), "expected_stock_reference_revision": g.text(l, "expected_stock_reference_revision")})
	}
	correction["references"] = refs
	correction["operation_id"] = uuid.New()
	reservationRequestHTTP(g, base+"/stock-corrections", actor, uuid.NewString(), "inverse cannot spend held stock", correction, 409)
	consume := terminal(orderID)
	consumeKey := uuid.NewString()
	reservationRequestHTTP(g, path+"/"+holdID.String()+"/consume", sales, consumeKey, "full consume", consume, 200)
	after = reservationTableSnapshot(g)
	reservationRequestHTTP(g, path+"/"+holdID.String()+"/consume", sales, consumeKey, "consume replay", consume, 200)
	g.require(after == reservationTableSnapshot(g), "consume replay changed data")
	reservationRequestHTTP(g, path+"/"+holdID.String()+"/release", sales, uuid.NewString(), "terminal cannot release", terminal(orderID), 409)
	pages = g.page(base+"/stock-balances", actor, "consumed balances")
	for _, row := range pages.Items {
		g.require(g.text(row, "on_hand_pieces") == "30" && g.text(row, "reserved_pieces") == "0", "consume balance")
	}
	var movements, operations int
	g.ok(g.inventory.QueryRow(g.ctx, "SELECT (SELECT count(*) FROM stock_movements WHERE warehouse_id=$1),(SELECT count(*) FROM stock_operations WHERE warehouse_id=$1)", warehouse.ID).Scan(&movements, &operations))
	g.require(movements == 4 && operations == 2, "consume not one atomic physical operation")
	// A second order demonstrates full release and then immutable next generation.
	releaseOrder, releaseID := uuid.New(), uuid.New()
	releaseReserve := map[string]any{"command_id": uuid.New(), "reservation_id": releaseID, "order_id": releaseOrder, "generation": "1", "expected_previous_generation": "0", "expected_order_version": "0", "lines": []any{map[string]any{"order_line_id": uuid.New(), "variant_id": a, "pieces": "10"}}}
	reservationRequestHTTP(g, path, sales, uuid.NewString(), "second order reserve", releaseReserve, 201)
	reservationRequestHTTP(g, path+"/"+releaseID.String()+"/release", sales, uuid.NewString(), "full release", terminal(releaseOrder), 200)
	releaseReserve["command_id"] = uuid.New()
	reservationRequestHTTP(g, path, sales, uuid.NewString(), "delayed reserve cannot recreate released generation", releaseReserve, 409)
	releaseReserve["reservation_id"] = uuid.New()
	releaseReserve["generation"] = "2"
	releaseReserve["expected_previous_generation"] = "1"
	releaseReserve["expected_order_version"] = "2"
	reservationRequestHTTP(g, path, sales, uuid.NewString(), "explicit next generation", releaseReserve, 201)
	nextID := releaseReserve["reservation_id"].(uuid.UUID)
	nextRelease := terminal(releaseOrder)
	nextRelease["generation"] = "2"
	nextRelease["expected_order_version"] = "3"
	reservationRequestHTTP(g, path+"/"+nextID.String()+"/release", sales, uuid.NewString(), "next generation release", nextRelease, 200)
	// Keep a final hold for inactive cleanup and an actual Catalog-outage phase.
	outageID, outageOrder := uuid.New(), uuid.New()
	outageReserve := map[string]any{"command_id": uuid.New(), "reservation_id": outageID, "order_id": outageOrder, "generation": "1", "expected_previous_generation": "0", "expected_order_version": "0", "lines": []any{map[string]any{"order_line_id": uuid.New(), "variant_id": b, "pieces": "5"}}}
	outageKey := uuid.NewString()
	reservationRequestHTTP(g, path, sales, outageKey, "outage hold reserve", outageReserve, 201)
	_, err = catalog.Exec(g.ctx, "UPDATE catalog_products SET is_active=false WHERE id=$1", product)
	g.ok(err)
	reservationRequestHTTP(g, path+"/"+outageID.String()+"/consume", sales, uuid.NewString(), "inactive consume blocked", terminal(outageOrder), 409)
	// Separate hold released while references are inactive, proving no fresh check is required.
	before = reservationTableSnapshot(g)
	reservationRequestHTTP(g, path, sales, outageKey, "inactive completed reserve replay", outageReserve, 200)
	g.require(before == reservationTableSnapshot(g), "inactive replay changed data")
	state := reservationOutageState{Warehouse: warehouse.ID, HoldID: outageID, Reserve: outageReserve, ReserveKey: outageKey, Consume: consume, ConsumeKey: consumeKey, ConsumeID: holdID, Release: terminal(outageOrder), ReleaseKey: uuid.NewString()}
	stateFile := os.Getenv("RESERVATION_FIXTURE_STATE_FILE")
	g.require(filepath.Dir(stateFile) == filepath.Dir(salesFile), "non-fixture state path")
	data, err := json.Marshal(state)
	g.ok(err)
	g.ok(os.WriteFile(stateFile, data, 0600))
	t.Log("PASS: actual Auth machine issuance over verified TLS/Kong; separate audience/least scopes/owner; reserve 70+20, availability 30+30; consume 90 once, four physical movements/two operations; explicit releases/generations; held OUT/reversal rejected")
}
func TestReservationGatewayOutage(t *testing.T) {
	if os.Getenv("RESERVATION_OUTAGE_ISOLATED") != "1" {
		t.Skip("requires fixture Catalog stopped by owner harness")
	}
	g, _ := reservationGateway(t)
	file := os.Getenv("RESERVATION_SALES_CREDENTIAL_FILE")
	stateFile := os.Getenv("RESERVATION_FIXTURE_STATE_FILE")
	g.require(filepath.Dir(stateFile) == filepath.Dir(file), "non-fixture state path")
	data, err := os.ReadFile(stateFile)
	g.ok(err)
	var state reservationOutageState
	g.ok(json.Unmarshal(data, &state))
	sales := issueReservationToken(g, file, stock.ReservationAudience, reservationScopes, 200)
	base := "/api/v1/inventory/warehouses/" + state.Warehouse.String() + "/reservations"
	before := reservationTableSnapshot(g)
	reservationRequestHTTP(g, base, sales, state.ReserveKey, "Catalog-down reserve replay", state.Reserve, 200)
	reservationRequestHTTP(g, base+"/"+state.ConsumeID.String()+"/consume", sales, state.ConsumeKey, "Catalog-down consume replay", state.Consume, 200)
	reservationRequestHTTP(g, "/api/v1/inventory/reservations/"+state.HoldID.String(), sales, "", "Catalog-down owned status", nil, 200)
	reservationRequestHTTP(g, "/api/v1/inventory/reservations?limit=1", sales, "", "Catalog-down owned active list", nil, 200)
	g.require(before == reservationTableSnapshot(g), "Catalog-down reconciliation changed data")
	reservationRequestHTTP(g, base+"/"+state.HoldID.String()+"/release", sales, state.ReleaseKey, "Catalog-down release cleanup", state.Release, 200)
	after := reservationTableSnapshot(g)
	reservationRequestHTTP(g, base+"/"+state.HoldID.String()+"/release", sales, state.ReleaseKey, "Catalog-down release replay", state.Release, 200)
	g.require(after == reservationTableSnapshot(g), "release replay changed data")
	credential := reservationCredential(g, file)
	_, err = g.auth.Exec(g.ctx, "DELETE FROM machine_grants WHERE machine_id=$1 AND audience=$2 AND permission=$3", credential.Client, stock.ReservationAudience, stock.ConsumePermission)
	g.ok(err)
	issueReservationToken(g, file, stock.ReservationAudience, stock.ConsumePermission, 401)
	reservationRequestHTTP(g, base+"/"+state.ConsumeID.String()+"/consume", sales, state.ConsumeKey, "issued JWT retains bounded validity after grant revocation", state.Consume, 200)
	t.Log("PASS: real Catalog container stopped; new Auth TLS issuance, completed replay/status and owned release work without Catalog; revocation denies new issuance, existing short-lived JWT remains valid")
}

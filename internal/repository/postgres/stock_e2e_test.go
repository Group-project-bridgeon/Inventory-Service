package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/catalogclient"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/service"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
)

type e2eHost struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  io.ReadCloser
	decoder *json.Decoder
	stopped bool
}

func (h *e2eHost) stop() {
	if h.stopped {
		return
	}
	h.stopped = true
	_ = json.NewEncoder(h.input).Encode("stop")
	_ = h.input.Close()
	_, _ = io.Copy(io.Discard, h.output)
	_ = h.command.Wait()
}
func startE2EHost(t *testing.T, ctx context.Context, binary, test string, initial any) *e2eHost {
	t.Helper()
	cmd := exec.CommandContext(ctx, binary, "-test.timeout=180s", "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), "STOCK_E2E_HOST=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal("fixture input pipe")
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("fixture output pipe")
	}
	cmd.Stderr = io.Discard
	if cmd.Start() != nil {
		t.Fatal("fixture start")
	}
	h := &e2eHost{command: cmd, input: in, output: out, decoder: json.NewDecoder(out)}
	t.Cleanup(h.stop)
	if initial != nil {
		if json.NewEncoder(in).Encode(initial) != nil {
			t.Fatal("fixture initialization")
		}
	}
	return h
}
func TestStockHTTPSPostingE2E(t *testing.T) {
	if os.Getenv("STOCK_E2E_ENABLED") != "1" {
		t.Skip("requires disposable three-service harness")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	directory := t.TempDir()
	binaries := map[string]string{}
	for repo, pkg := range map[string]string{"Authentication": "./internal/integration", "Catalog-Service": "./internal/repository/postgres"} {
		root, err := filepath.Abs("../../../../" + repo)
		if err != nil {
			t.Fatal("fixture root")
		}
		binary := filepath.Join(directory, repo+".test.exe")
		cmd := exec.CommandContext(ctx, "go", "-C", root, "test", "-c", "-o", binary, pkg)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture compile failed: %s", output)
		}
		binaries[repo] = binary
	}
	auth := startE2EHost(t, ctx, binaries["Authentication"], "TestStockE2EAuthHost", nil)
	var identity struct{ URL, CA, PublicKey, Client, Credential string }
	if auth.decoder.Decode(&identity) != nil {
		t.Fatal("Auth fixture bootstrap failed; sensitive output withheld")
	}
	catalog := startE2EHost(t, ctx, binaries["Catalog-Service"], "TestStockE2ECatalogHost", identity.PublicKey)
	var refs struct{ URL, CA, Variant, Pack, PieceRevision, PackReferenceRevision string }
	if catalog.decoder.Decode(&refs) != nil {
		t.Fatal("Catalog fixture bootstrap failed")
	}
	secret := strings.Split(identity.Credential, ".")
	if len(secret) != 2 {
		t.Fatal("fixture credential shape")
	}
	secretBytes, _ := json.Marshal(map[string]string{"credential_id": secret[0], "secret": secret[1]})
	secretPath := filepath.Join(directory, "credential.json")
	caPath := filepath.Join(directory, "ca.pem")
	if os.WriteFile(secretPath, secretBytes, 0600) != nil || os.WriteFile(caPath, []byte(identity.CA+refs.CA), 0600) != nil {
		t.Fatal("fixture trust files")
	}
	configuration := catalogclient.Config{Enabled: true, TokenURL: identity.URL, BatchURL: refs.URL, ClientID: identity.Client, Issuer: "erp-auth", SecretFile: secretPath, CAFile: caPath, TokenTimeout: 5 * time.Second, ReferenceTimeout: 5 * time.Second, TokenBodyLimit: 16 << 10, ReferenceBodyLimit: 1 << 20}
	adapter, err := catalogclient.New(configuration)
	postingOK(t, err)
	defer adapter.Close()
	f := postingDB(t)
	f.ctx = ctx
	repo, err := NewStockRepositoryWithFreshness(f.pool, DefaultStockRepositoryOptions(), adapter.Freshness())
	postingOK(t, err)
	posting, err := stock.NewPostingService(repo, postingAuth{f.p.UserID}, adapter, adapter.Freshness(), stock.PostingOptions{TotalTimeout: 15 * time.Second, ReferenceTimeout: 6 * time.Second, TransactionBudget: 5 * time.Second, MaxAttempts: 2})
	postingOK(t, err)
	command := func(direction stock.Direction, count stock.Quantity, packed bool) stock.Command {
		line := stock.CommandLine{LineNumber: 1, VariantID: uuid.MustParse(refs.Variant), InputCount: count, ExpectedStockReferenceRevision: refs.PieceRevision}
		if packed {
			line.PackID = uuid.MustParse(refs.Pack)
			line.PackRevision = "1"
			line.ExpectedStockReferenceRevision = refs.PackReferenceRevision
		}
		return stock.Command{OperationID: uuid.New(), SourceEventID: uuid.New(), Producer: "synthetic-posting", WarehouseID: f.warehouse, Direction: direction, CommandVersion: 1, IdempotencyKey: uuid.New(), Lines: []stock.CommandLine{line}}
	}
	receipt := command(stock.In, 10, false)
	_, err = posting.Post(ctx, f.p, receipt)
	postingOK(t, err)
	_, err = posting.Post(ctx, f.p, command(stock.In, 2, true))
	postingOK(t, err)
	_, err = posting.Post(ctx, f.p, command(stock.Out, 5, false))
	postingOK(t, err)
	var balance int64
	postingOK(t, f.pool.QueryRow(ctx, "SELECT on_hand_pieces FROM stock_balances WHERE warehouse_id=$1 AND variant_id=$2", f.warehouse, uuid.MustParse(refs.Variant)).Scan(&balance))
	if balance != 17 {
		t.Fatal("piece/pack posting reconciliation")
	}
	var movements int
	postingOK(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM stock_movements WHERE warehouse_id=$1", f.warehouse).Scan(&movements))
	if movements != 3 {
		t.Fatal("unexpected stock effects")
	}
	control := func(host *e2eHost, command string) {
		t.Helper()
		if json.NewEncoder(host.input).Encode(command) != nil {
			t.Fatal("fixture control")
		}
		var ack bool
		if host.decoder.Decode(&ack) != nil || !ack {
			t.Fatal("fixture acknowledgement")
		}
	}
	control(auth, "revoke")
	if _, err := adapter.Resolve(ctx, command(stock.In, 1, false)); err != nil {
		t.Fatal("cached issued token lost after credential revocation")
	}
	uncached, err := catalogclient.New(configuration)
	postingOK(t, err)
	defer uncached.Close()
	if _, err := uncached.Resolve(ctx, command(stock.In, 1, false)); !errors.Is(err, catalogclient.ErrUnauthorized) {
		t.Fatal("revoked credential issued another token")
	}
	missing := command(stock.In, 1, false)
	missing.Lines[0].VariantID = uuid.New()
	if _, err := posting.Post(ctx, f.p, missing); !errors.Is(err, stock.ErrReferenceNotFound) {
		t.Fatal("missing reference not rejected")
	}
	control(catalog, "obsolete")
	if _, err := posting.Post(ctx, f.p, command(stock.In, 1, true)); !errors.Is(err, stock.ErrReferenceChanged) {
		t.Fatal("obsolete selected pack not rejected")
	}
	readPool := readOnlyStockPool(t, f)
	reads := service.NewStockReadService(NewStockReadRepository(readPool), NewWarehouseRepository(readPool), time.Second)
	readInput := service.StockReadInput{WarehouseID: f.warehouse}
	historyBefore, err := reads.ListMovements(ctx, readInput)
	postingOK(t, err)
	control(catalog, "rename")
	if _, err := posting.Post(ctx, f.p, command(stock.In, 1, false)); !errors.Is(err, stock.ErrReferenceChanged) {
		t.Fatal("changed revision not rejected")
	}
	control(catalog, "inactive")
	if _, err := posting.Post(ctx, f.p, command(stock.In, 1, false)); !errors.Is(err, stock.ErrReferenceInactive) {
		t.Fatal("inactive reference not rejected")
	}
	auth.stop()
	catalog.stop()
	historyAfter, err := reads.ListMovements(ctx, readInput)
	postingOK(t, err)
	if len(historyAfter.Items) != 3 || !reflect.DeepEqual(historyBefore, historyAfter) {
		t.Fatal("durable read changed after real Catalog rename/inactivation/outage")
	}
	balancesAfter, err := reads.ListBalances(ctx, readInput)
	postingOK(t, err)
	if len(balancesAfter.Items) != 1 || balancesAfter.Items[0].OnHandPieces != 17 {
		t.Fatal("balance read required live Catalog")
	}
	t.Log("stock reads preserve exact recorded snapshots after actual Catalog rename/inactivation and Auth/Catalog outage")
	replay, err := posting.Post(ctx, f.p, receipt)
	postingOK(t, err)
	if !replay.Replayed {
		t.Fatal("outage replay failed")
	}
	if _, err = posting.Post(ctx, f.p, command(stock.In, 1, false)); err == nil {
		t.Fatal("new posting accepted dependency outage")
	}
	postingOK(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM stock_movements WHERE warehouse_id=$1", f.warehouse).Scan(&movements))
	if movements != 3 {
		t.Fatal("outage changed stock")
	}
	postingOK(t, f.pool.QueryRow(ctx, "SELECT on_hand_pieces FROM stock_balances WHERE warehouse_id=$1 AND variant_id=$2", f.warehouse, uuid.MustParse(refs.Variant)).Scan(&balance))
	if balance != 17 {
		t.Fatal("outage balance changed")
	}
	t.Log("actual Auth HTTPS -> Catalog HTTPS -> Inventory PostgreSQL: pieces10 + packs12 - issue5 =17; movements3; outage replay unchanged")
}

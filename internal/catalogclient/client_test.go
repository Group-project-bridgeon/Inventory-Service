package catalogclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
)

func fixtureConfig(t *testing.T, handler http.HandlerFunc) (Config, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	secret := filepath.Join(dir, "secret.json")
	if os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600) != nil {
		t.Fatal("CA fixture")
	}
	data, _ := json.Marshal(map[string]string{"credential_id": uuid.NewString(), "secret": base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	if os.WriteFile(secret, data, 0600) != nil {
		t.Fatal("secret fixture")
	}
	return Config{Enabled: true, TokenURL: server.URL + "/api/v1/auth/service-token", BatchURL: server.URL + "/api/v1/catalog/stock-references/batch", ClientID: uuid.NewString(), Issuer: "erp-auth", SecretFile: secret, CAFile: ca, TokenTimeout: 2 * time.Second, ReferenceTimeout: 2 * time.Second, TokenBodyLimit: 16 << 10, ReferenceBodyLimit: 1 << 20}, server
}
func syntheticToken(client string, now time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"iss": "erp-auth", "aud": []string{audience}, "sub": "machine:" + client, "client_id": client, "token_kind": "machine", "permissions": []string{permission}, "jti": uuid.NewString(), "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Unix() + 300})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".c3ludGhldGlj"
}
func emitToken(w http.ResponseWriter, client string, now time.Time) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": syntheticToken(client, now), "token_type": "Bearer", "expires_in": 300})
}
func TestTokenCoalescingCancellationAndRotation(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var client string
	var password atomic.Value
	cfg, _ := fixtureConfig(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, secret, ok := r.BasicAuth()
		if !ok {
			w.WriteHeader(401)
			return
		}
		password.Store(secret)
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		emitToken(w, client, time.Now())
	})
	client = cfg.ClientID
	tokens, err := NewTokenClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tokens.Close()
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := tokens.Get(ctx); first <- err }()
	select {
	case <-started:
	case err := <-first:
		t.Fatalf("fixture request did not reach TLS server: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("fixture start deadline")
	}
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := tokens.Get(context.Background()); results <- err }()
	}
	cancel()
	if !errors.Is(<-first, context.Canceled) {
		t.Fatal("caller cancellation")
	}
	close(release)
	for i := 0; i < 8; i++ {
		if err := <-results; err != nil {
			t.Fatal("coalesced caller failed")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("refresh storm")
	}
	cached, _ := tokens.Get(context.Background())
	tokens.Invalidate(cached.Generation - 1)
	again, _ := tokens.Get(context.Background())
	if again.Generation != cached.Generation {
		t.Fatal("old rejection invalidated new generation")
	}
	rotatedID := uuid.NewString()
	secret := base64.RawURLEncoding.EncodeToString(bytes32(7))
	data, _ := json.Marshal(map[string]string{"credential_id": rotatedID, "secret": secret})
	if os.WriteFile(cfg.SecretFile, data, 0600) != nil {
		t.Fatal("rotate fixture")
	}
	tokens.Invalidate(cached.Generation)
	if _, err := tokens.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if password.Load() != rotatedID+"."+secret {
		t.Fatal("credential file not reloaded")
	}
}
func bytes32(v byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = v
	}
	return b
}
func TestTokenExpiryUnavailableAndTrust(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Now().Truncate(time.Second).Unix())
	var unavailable atomic.Bool
	var client string
	cfg, server := fixtureConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(503)
			return
		}
		emitToken(w, client, time.Unix(now.Load(), 0))
	})
	client = cfg.ClientID
	tokens, err := NewTokenClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tokens.Close()
	tokens.now = func() time.Time { return time.Unix(now.Load(), 0) }
	original, err := tokens.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unavailable.Store(true)
	now.Add(290)
	fallback, err := tokens.Get(context.Background())
	if err != nil || fallback.Generation != original.Generation {
		t.Fatal("unexpired outage fallback")
	}
	now.Add(11)
	if _, err := tokens.Get(context.Background()); err == nil {
		t.Fatal("expired fallback accepted")
	}
	bad := cfg
	bad.TokenURL = strings.Replace(cfg.TokenURL, "https:", "http:", 1)
	if _, err := NewTokenClient(bad); err == nil {
		t.Fatal("plaintext accepted")
	}
	bad = cfg
	bad.CAFile = ""
	untrusted, err := NewTokenClient(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer untrusted.Close()
	if _, err := untrusted.Get(context.Background()); err == nil {
		t.Fatal("untrusted CA accepted")
	}
	bad = cfg
	bad.TokenURL = strings.Replace(cfg.TokenURL, "127.0.0.1", "localhost", 1)
	mismatch, err := NewTokenClient(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer mismatch.Close()
	if _, err := mismatch.Get(context.Background()); err == nil {
		t.Fatal("hostname mismatch accepted")
	}
	_ = server
}
func fixtureCommand() stock.Command {
	return stock.Command{OperationID: uuid.New(), SourceEventID: uuid.New(), Producer: "test", WarehouseID: uuid.New(), Direction: stock.In, CommandVersion: 1, IdempotencyKey: uuid.New(), Lines: []stock.CommandLine{{LineNumber: 1, VariantID: uuid.New(), InputCount: 1, ExpectedStockReferenceRevision: "cat-stock-v1:" + strings.Repeat("a", 64)}}}
}
func fixtureBatch(c stock.Command, now time.Time) wireBatch {
	line := c.Lines[0]
	return wireBatch{Checked: now, Items: []wireItem{{Variant: line.VariantID.String(), Status: "ok", Reference: &wireReference{Product: uuid.NewString(), Variant: line.VariantID.String(), Unit: uuid.NewString(), Kind: "PIECE", Eligible: true, ProductActive: true, VariantActive: true, UnitActive: true, ProductCode: "P", ProductName: "Product", SKU: "SKU", UnitCode: "PC", UnitName: "Piece", ProductVersion: "1", VariantVersion: "1", UnitVersion: "1", Revision: line.ExpectedStockReferenceRevision, Checked: now}}}}
}
func TestReferenceResponsesAndFreshness(t *testing.T) {
	now := time.Now()
	command := fixtureCommand()
	a := &Adapter{freshness: Freshness{Now: func() time.Time { return now }}}
	cases := map[string]func(*wireBatch){"missing": func(b *wireBatch) { b.Items = nil }, "duplicate": func(b *wireBatch) { b.Items = append(b.Items, b.Items[0]) }, "unexpected": func(b *wireBatch) { b.Items[0].Variant = uuid.NewString() }, "ineligible": func(b *wireBatch) { b.Items[0].Reference.Eligible = false }, "inactive": func(b *wireBatch) { b.Items[0].Reference.ProductActive = false }, "kind": func(b *wireBatch) { b.Items[0].Reference.Kind = "BOX" }, "revision": func(b *wireBatch) { b.Items[0].Reference.Revision = "cat-stock-v1:" + strings.Repeat("b", 64) }, "coherence": func(b *wireBatch) { b.Items[0].Reference.Checked = now.Add(time.Millisecond) }, "missing ref": func(b *wireBatch) { b.Items[0].Reference = nil }, "metadata": func(b *wireBatch) { b.Items[0].Reference.ProductName = strings.Repeat("x", 201) }, "count": func(b *wireBatch) { b.Items[0].Reference.UnitVersion = "1.5" }, "missing status": func(b *wireBatch) { b.Items[0].Status = "variant_not_found" }, "obsolete": func(b *wireBatch) { b.Items[0].Status = "pack_revision_obsolete" }, "stale": func(b *wireBatch) {
		b.Checked = now.Add(-5*time.Second - time.Nanosecond)
		b.Items[0].Reference.Checked = b.Checked
	}, "future": func(b *wireBatch) {
		b.Checked = now.Add(time.Second + time.Nanosecond)
		b.Items[0].Reference.Checked = b.Checked
	}}
	valid, _ := json.Marshal(fixtureBatch(command, now))
	if _, err := a.decode(context.Background(), valid, command); err != nil {
		t.Fatal(err)
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := fixtureBatch(command, now)
			change(&b)
			data, _ := json.Marshal(b)
			if _, err := a.decode(context.Background(), data, command); err == nil {
				t.Fatal("invalid reference accepted")
			}
		})
	}
	for _, data := range [][]byte{append(append([]byte{}, valid...), []byte(` {}`)...), []byte(`{"checked_at":null,"checked_at":null,"items":[]}`), []byte(`{"Items":[],"items":[]}`)} {
		if _, err := a.decode(context.Background(), data, command); err == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
	for _, field := range []string{"eligible", "product_active", "variant_active", "base_unit_active"} {
		for _, replacement := range []string{"", `"` + field + `":null,`} {
			data := strings.Replace(string(valid), `"`+field+`":true,`, replacement, 1)
			if _, err := a.decode(context.Background(), []byte(data), command); !errors.Is(err, ErrMalformed) {
				t.Fatal("absent/null required field not classified as malformed")
			}
		}
	}
	for _, age := range []time.Duration{-time.Second, 5 * time.Second} {
		o := []stock.Observation{{ReferenceSnapshot: stock.ReferenceSnapshot{CheckedAt: now.Add(-age)}}}
		if a.freshness.Check(context.Background(), o) != nil {
			t.Fatal("inclusive freshness boundary")
		}
	}
}
func TestReferenceAuthenticationRetryBound(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var issued, lookups atomic.Int32
			var client string
			cfg, _ := fixtureConfig(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/service-token" {
					issued.Add(1)
					emitToken(w, client, time.Now())
					return
				}
				lookups.Add(1)
				w.WriteHeader(status)
			})
			client = cfg.ClientID
			a, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			_, err = a.Resolve(context.Background(), fixtureCommand())
			if err == nil {
				t.Fatal("rejection ignored")
			}
			want := int32(1)
			if status == 401 {
				want = 2
			}
			if issued.Load() != want || lookups.Load() != want {
				t.Fatal("unbounded or missing refresh")
			}
		})
	}
}
func TestRedirectAndResponseLimit(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	cfg, _ := fixtureConfig(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) })
	tokens, err := NewTokenClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tokens.Close()
	if _, err := tokens.Get(context.Background()); err == nil || received.Load() != 0 {
		t.Fatal("credential redirect followed")
	}
}

func TestSelectedPackValidation(t *testing.T) {
	now := time.Now()
	command := fixtureCommand()
	command.Lines[0].PackID = uuid.New()
	command.Lines[0].PackRevision = "1"
	a := &Adapter{freshness: Freshness{Now: func() time.Time { return now }}}
	makeBatch := func() wireBatch {
		b := fixtureBatch(command, now)
		b.Items[0].Reference.Pack = &wirePack{ID: command.Lines[0].PackID.String(), Variant: command.Lines[0].VariantID.String(), Code: "BOX6", Revision: "1", CurrentRevision: "1", Version: "1", Factor: "6", Label: "Six", Active: true, Current: true}
		return b
	}
	valid, _ := json.Marshal(makeBatch())
	if _, err := a.decode(context.Background(), valid, command); err != nil {
		t.Fatal("valid pack rejected")
	}
	cases := map[string]func(*wirePack){"owner": func(p *wirePack) { p.Variant = uuid.NewString() }, "identity": func(p *wirePack) { p.ID = uuid.NewString() }, "inactive": func(p *wirePack) { p.Active = false }, "not current": func(p *wirePack) { p.Current = false }, "old revision": func(p *wirePack) { p.CurrentRevision = "2" }, "fraction": func(p *wirePack) { p.Factor = "1.2" }, "zero": func(p *wirePack) { p.Factor = "0" }, "overflow": func(p *wirePack) { p.Factor = "9223372036854775808" }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := makeBatch()
			change(b.Items[0].Reference.Pack)
			data, _ := json.Marshal(b)
			if _, err := a.decode(context.Background(), data, command); err == nil {
				t.Fatal("invalid selected pack accepted")
			}
		})
	}
	b := makeBatch()
	b.Items[0].Reference.Pack = nil
	data, _ := json.Marshal(b)
	if _, err := a.decode(context.Background(), data, command); err == nil {
		t.Fatal("pack silently fell back to pieces")
	}
}
func TestMalformedTokenAndReferenceBodies(t *testing.T) {
	for _, mode := range []string{"token large", "token duplicate", "token type", "reference large", "reference trailing"} {
		t.Run(mode, func(t *testing.T) {
			var client string
			command := fixtureCommand()
			cfg, _ := fixtureConfig(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/service-token" {
					switch mode {
					case "token large":
						_, _ = w.Write([]byte(strings.Repeat("x", (16<<10)+1)))
					case "token duplicate":
						_, _ = w.Write([]byte(`{"access_token":"a","access_token":"b","token_type":"Bearer","expires_in":300}`))
					case "token type":
						_ = json.NewEncoder(w).Encode(map[string]any{"access_token": syntheticToken(client, time.Now()), "token_type": "other", "expires_in": 300})
					default:
						emitToken(w, client, time.Now())
					}
					return
				}
				if mode == "reference large" {
					_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
				} else {
					data, _ := json.Marshal(fixtureBatch(command, time.Now()))
					_, _ = w.Write(append(data, []byte(` {}`)...))
				}
			})
			client = cfg.ClientID
			a, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if _, err = a.Resolve(context.Background(), command); !errors.Is(err, ErrMalformed) {
				t.Fatal("malformed dependency response not rejected")
			}
		})
	}
}
func TestRefreshRecoversOnce(t *testing.T) {
	var client string
	var issued, lookups atomic.Int32
	command := fixtureCommand()
	cfg, _ := fixtureConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/service-token" {
			issued.Add(1)
			emitToken(w, client, time.Now())
			return
		}
		if lookups.Add(1) == 1 {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fixtureBatch(command, time.Now()))
	})
	client = cfg.ClientID
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.Resolve(context.Background(), command); err != nil || issued.Load() != 2 || lookups.Load() != 2 {
		t.Fatal("single refresh recovery failed")
	}
}
func TestRefreshDeadlineCleanup(t *testing.T) {
	closed := make(chan struct{})
	cfg, _ := fixtureConfig(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		close(closed)
	})
	cfg.TokenTimeout = 200 * time.Millisecond
	tokens, err := NewTokenClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tokens.Close()
	if _, err = tokens.Get(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("refresh deadline not preserved")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("refresh did not cancel server request")
	}
}

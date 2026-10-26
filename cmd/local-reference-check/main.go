// Non-posting local operator probe using the real configured reference adapter.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/url"
	"os"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/catalogclient"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func localReferenceTargets(auth, catalog string) bool {
	a, e := url.Parse(auth)
	if e != nil {
		return false
	}
	c, e := url.Parse(catalog)
	if e != nil {
		return false
	}
	loopback := func(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }
	return a.Scheme == "https" && c.Scheme == "https" && (a.Hostname() == "auth-service" || loopback(a.Hostname())) && (c.Hostname() == "catalog-service" || loopback(c.Hostname()))
}
func run() error {
	variant := flag.String("variant", "", "explicit confirmed variant UUID")
	confirm := flag.Bool("confirm-local", false, "acknowledge local-only machine credential use")
	flag.Parse()
	if !*confirm {
		return stock.ErrInvalidArgument
	}
	id, e := uuid.Parse(*variant)
	if e != nil || id == uuid.Nil || id.String() != *variant {
		return stock.ErrInvalidArgument
	}
	cfg, e := catalogclient.LoadConfig()
	if e != nil || !cfg.Enabled || !localReferenceTargets(cfg.TokenURL, cfg.BatchURL) {
		return catalogclient.ErrConfiguration
	}
	a, e := catalogclient.New(cfg)
	if e != nil {
		return e
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, e := a.Prepare(ctx, []stock.Selection{{LineNumber: 1, VariantID: id, InputCount: 1}})
	if e != nil {
		return e
	}
	if len(rows) != 1 {
		return stock.ErrReferenceMalformed
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Variant  uuid.UUID `json:"variant_id"`
		Unit     uuid.UUID `json:"base_unit_id"`
		Revision string    `json:"revision"`
		Checked  time.Time `json:"checked_at"`
		Kind     string    `json:"quantity_kind"`
	}{id, rows[0].BaseUnitID, rows[0].StockReferenceRevision, rows[0].CheckedAt, rows[0].QuantityKind})
}
func main() {
	if run() != nil {
		log := zerolog.New(os.Stderr)
		log.Error().Str("component", "local_reference_check").Str("operation", "prepare").Str("reason", "reference_check_failed").Msg("Secured reference check failed; remote details withheld")
		os.Exit(1)
	}
}

package catalogclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/strictjson"
	"github.com/google/uuid"
)

type Adapter struct {
	tokens    *TokenClient
	freshness Freshness
}

func New(c Config) (*Adapter, error) {
	tokens, err := NewTokenClient(c)
	if err != nil {
		return nil, err
	}
	return &Adapter{tokens: tokens, freshness: Freshness{}}, nil
}
func (a *Adapter) Close()                           { a.tokens.Close() }
func (a *Adapter) Freshness() stock.FreshnessPolicy { return a.freshness }

type requestItem struct {
	Variant      string `json:"variant_id"`
	Pack         string `json:"pack_id,omitempty"`
	PackRevision string `json:"pack_revision,omitempty"`
	Expected     string `json:"expected_revision,omitempty"`
}
type wirePack struct {
	ID              string `json:"id"`
	Variant         string `json:"variant_id"`
	Code            string `json:"code"`
	Revision        string `json:"revision"`
	CurrentRevision string `json:"current_revision"`
	Version         string `json:"version"`
	Factor          string `json:"pieces_per_pack"`
	Label           string `json:"label"`
	Active          bool   `json:"is_active"`
	Current         bool   `json:"is_current"`
}
type wireReference struct {
	Product        string    `json:"product_id"`
	Variant        string    `json:"variant_id"`
	Unit           string    `json:"base_unit_id"`
	Kind           string    `json:"quantity_kind"`
	Eligible       bool      `json:"eligible"`
	ProductActive  bool      `json:"product_active"`
	VariantActive  bool      `json:"variant_active"`
	UnitActive     bool      `json:"base_unit_active"`
	ProductCode    string    `json:"product_code"`
	ProductName    string    `json:"product_name"`
	SKU            string    `json:"sku"`
	UnitCode       string    `json:"base_unit_code"`
	UnitName       string    `json:"base_unit_name"`
	ProductVersion string    `json:"product_version"`
	VariantVersion string    `json:"variant_version"`
	UnitVersion    string    `json:"unit_version"`
	Pack           *wirePack `json:"pack,omitempty"`
	Revision       string    `json:"stock_reference_revision"`
	Checked        time.Time `json:"checked_at"`
}
type wireItem struct {
	Variant   string         `json:"variant_id"`
	Status    string         `json:"status"`
	Reference *wireReference `json:"reference,omitempty"`
}
type wireBatch struct {
	Checked time.Time  `json:"checked_at"`
	Items   []wireItem `json:"items"`
}

func canonicalID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	return id, err == nil && id != uuid.Nil && id.String() == s
}
func count(s string) (stock.Quantity, error) {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return 0, ErrMalformed
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, ErrMalformed
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, ErrMalformed
	}
	return stock.Quantity(v), nil
}
func text(s string, max int) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func revision(s string) bool {
	if len(s) != 77 || !strings.HasPrefix(s, "cat-stock-v1:") {
		return false
	}
	_, err := hex.DecodeString(s[13:])
	return err == nil
}
func (a *Adapter) Resolve(ctx context.Context, command stock.Command) ([]stock.Observation, error) {
	if err := command.Validate(); err != nil {
		return nil, err
	}
	items := make([]requestItem, 0, len(command.Lines))
	for _, line := range command.Lines {
		item := requestItem{Variant: line.VariantID.String(), Expected: line.ExpectedStockReferenceRevision}
		if !revision(item.Expected) {
			return nil, stock.ErrInvalidArgument
		}
		if line.PackID != uuid.Nil {
			if _, err := count(line.PackRevision); err != nil {
				return nil, stock.ErrInvalidArgument
			}
			item.Pack = line.PackID.String()
			item.PackRevision = line.PackRevision
		}
		items = append(items, item)
	}
	body, err := json.Marshal(struct {
		Items []requestItem `json:"items"`
	}{items})
	if err != nil || len(body) > 64<<10 {
		return nil, stock.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, a.tokens.cfg.ReferenceTimeout)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		token, err := a.tokens.Get(ctx)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokens.cfg.BatchURL, bytes.NewReader(body))
		if err != nil {
			return nil, ErrConfiguration
		}
		request.Header.Set("Authorization", "Bearer "+token.Value)
		request.Header.Set("Content-Type", "application/json")
		response, err := a.tokens.http.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrUnavailable
		}
		data, readErr := readResponse(response, a.tokens.cfg.ReferenceBodyLimit)
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, readErr
		}
		if response.StatusCode == 401 {
			a.tokens.Invalidate(token.Generation)
			if attempt == 0 {
				continue
			}
			return nil, ErrUnauthorized
		}
		if response.StatusCode == 403 {
			return nil, ErrDenied
		}
		if response.StatusCode != 200 {
			return nil, ErrUnavailable
		}
		if !jsonResponse(response) {
			return nil, ErrMalformed
		}
		return a.decode(ctx, data, command)
	}
	return nil, ErrUnauthorized
}
func (a *Adapter) decode(ctx context.Context, data []byte, command stock.Command) ([]stock.Observation, error) {
	var batch wireBatch
	if strictjson.Decode(data, &batch) != nil || batch.Checked.IsZero() || len(batch.Items) != len(command.Lines) {
		return nil, ErrMalformed
	}
	if !requiredReferenceFields(data) {
		return nil, ErrMalformed
	}
	byID := map[uuid.UUID]wireItem{}
	requested := map[uuid.UUID]stock.CommandLine{}
	for _, l := range command.Lines {
		requested[l.VariantID] = l
	}
	for _, item := range batch.Items {
		id, ok := canonicalID(item.Variant)
		if !ok {
			return nil, ErrMalformed
		}
		if _, exists := byID[id]; exists {
			return nil, ErrMalformed
		}
		if _, exists := requested[id]; !exists {
			return nil, ErrMalformed
		}
		byID[id] = item
	}
	observations := make([]stock.Observation, 0, len(command.Lines))
	for _, line := range command.Lines {
		item := byID[line.VariantID]
		switch item.Status {
		case "ok":
		case "variant_not_found", "pack_not_found", "pack_revision_not_found":
			return nil, stock.ErrReferenceNotFound
		case "reference_inactive":
			return nil, stock.ErrReferenceInactive
		case "not_stock_eligible":
			return nil, stock.ErrReferenceIneligible
		case "reference_changed", "pack_variant_mismatch", "pack_revision_obsolete":
			return nil, stock.ErrReferenceChanged
		default:
			return nil, ErrMalformed
		}
		r := item.Reference
		if r == nil || r.Variant != item.Variant || !r.Checked.Equal(batch.Checked) || !revision(r.Revision) || !text(r.ProductCode, 50) || !text(r.ProductName, 200) || !text(r.SKU, 100) || !text(r.UnitCode, 30) || !text(r.UnitName, 100) {
			return nil, ErrMalformed
		}
		product, pok := canonicalID(r.Product)
		unit, uok := canonicalID(r.Unit)
		if !pok || !uok {
			return nil, ErrMalformed
		}
		for _, v := range []string{r.ProductVersion, r.VariantVersion, r.UnitVersion} {
			if _, err := count(v); err != nil {
				return nil, ErrMalformed
			}
		}
		if !r.Eligible || r.Kind != "PIECE" {
			return nil, stock.ErrReferenceIneligible
		}
		if !r.ProductActive || !r.VariantActive || !r.UnitActive {
			return nil, stock.ErrReferenceInactive
		}
		if r.Revision != line.ExpectedStockReferenceRevision {
			return nil, stock.ErrReferenceChanged
		}
		o := stock.Observation{ReferenceSnapshot: stock.ReferenceSnapshot{ProductID: product, VariantID: line.VariantID, BaseUnitID: unit, QuantityKind: r.Kind, ProductActive: r.ProductActive, VariantActive: r.VariantActive, BaseUnitActive: r.UnitActive, StockReferenceRevision: r.Revision, CheckedAt: r.Checked}, ProductCode: r.ProductCode, ProductName: r.ProductName, SKU: r.SKU, BaseUnitCode: r.UnitCode}
		if line.PackID == uuid.Nil {
			if r.Pack != nil {
				return nil, stock.ErrReferenceChanged
			}
		} else {
			p := r.Pack
			if p == nil {
				return nil, stock.ErrReferenceChanged
			}
			id, ok := canonicalID(p.ID)
			if !ok || id != line.PackID || p.Variant != item.Variant || p.Revision != line.PackRevision || p.CurrentRevision != p.Revision {
				return nil, stock.ErrReferenceChanged
			}
			if !p.Active || !p.Current {
				return nil, stock.ErrReferenceInactive
			}
			if !text(p.Code, 50) || !text(p.Label, 200) {
				return nil, ErrMalformed
			}
			for _, v := range []string{p.Version, p.Revision, p.CurrentRevision} {
				if _, err := count(v); err != nil {
					return nil, ErrMalformed
				}
			}
			factor, err := count(p.Factor)
			if err != nil {
				return nil, err
			}
			o.Pack = &stock.PackSnapshot{ID: id, VariantID: line.VariantID, Revision: p.Revision, PiecesPerPack: factor, Active: true}
			label := p.Label
			o.PackLabel = &label
		}
		if err := o.ValidateFor(line); err != nil {
			return nil, err
		}
		observations = append(observations, o)
	}
	if err := a.freshness.Check(ctx, observations); err != nil {
		return nil, err
	}
	return observations, nil
}

// encoding/json maps absent/null booleans to false. Preserve the distinction
// between a malformed response and an explicit inactive/ineligible reference.
func requiredReferenceFields(data []byte) bool {
	var batch struct {
		Items []struct {
			Status    string          `json:"status"`
			Reference json.RawMessage `json:"reference"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &batch) != nil {
		return false
	}
	required := func(raw json.RawMessage, names []string) (map[string]json.RawMessage, bool) {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return nil, false
		}
		for _, name := range names {
			value, exists := fields[name]
			if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, false
			}
		}
		return fields, true
	}
	for _, item := range batch.Items {
		if item.Status != "ok" {
			continue
		}
		fields, ok := required(item.Reference, []string{"product_id", "variant_id", "base_unit_id", "quantity_kind", "eligible", "product_active", "variant_active", "base_unit_active", "product_code", "product_name", "sku", "base_unit_code", "base_unit_name", "product_version", "variant_version", "unit_version", "stock_reference_revision", "checked_at"})
		if !ok {
			return false
		}
		if pack, exists := fields["pack"]; exists && !bytes.Equal(bytes.TrimSpace(pack), []byte("null")) {
			if _, ok := required(pack, []string{"id", "variant_id", "code", "revision", "current_revision", "version", "pieces_per_pack", "label", "is_active", "is_current"}); !ok {
				return false
			}
		}
	}
	return true
}

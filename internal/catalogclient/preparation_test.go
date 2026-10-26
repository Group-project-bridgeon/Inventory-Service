package catalogclient

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
)

func TestPreparationUsesSharedTransportWithoutExpectedRevision(t *testing.T) {
	c := fixtureCommand()
	var client string
	cfg, _ := fixtureConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/service-token" {
			emitToken(w, client, time.Now())
			return
		}
		var request struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("request JSON")
		}
		if len(request.Items) != 1 {
			t.Error("items")
		} else if _, exists := request.Items[0]["expected_revision"]; exists {
			t.Error("fabricated expectation")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fixtureBatch(c, time.Now()))
	})
	client = cfg.ClientID
	a, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	l := c.Lines[0]
	observations, e := a.Prepare(context.Background(), []stock.Selection{{LineNumber: 1, VariantID: l.VariantID, InputCount: 100}})
	if e != nil || len(observations) != 1 || observations[0].StockReferenceRevision != l.ExpectedStockReferenceRevision {
		t.Fatal("preparation failed", e)
	}
}

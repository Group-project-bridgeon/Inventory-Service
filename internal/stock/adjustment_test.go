package stock

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func adjustmentTestCommand() Command {
	c := testCommand()
	c.Producer = AdjustmentProducer
	c.SourceEventID = c.OperationID
	c.CommandVersion = 2
	c.Audit = &AdjustmentAudit{ReasonCode: "FOUND", Explanation: "Physical pieces found"}
	return c
}
func TestAdjustmentAuditAndFingerprint(t *testing.T) {
	c := adjustmentTestCommand()
	fp, err := c.Fingerprint(testPrincipal())
	if err != nil || fp.Version != 2 {
		t.Fatal("v2 fingerprint", err)
	}
	for _, change := range []func(*Command){func(c *Command) { c.Audit.ReasonCode = "OTHER" }, func(c *Command) { c.Audit.Explanation = "Another explanation" }, func(c *Command) { c.Direction = Out }} {
		d := c
		a := *c.Audit
		d.Audit = &a
		change(&d)
		other, err := d.Fingerprint(testPrincipal())
		if err != nil || other == fp {
			t.Fatal("meaningful intent not fingerprinted")
		}
	}
	c.FirstRequestID = "retry"
	c.IdempotencyKey = uuid.New()
	other, err := c.Fingerprint(testPrincipal())
	if err != nil || other != fp {
		t.Fatal("execution/key changed intent")
	}
	for _, a := range []AdjustmentAudit{{ReasonCode: "BOGUS", Explanation: "Valid"}, {ReasonCode: "LOSS", Explanation: " "}, {ReasonCode: "LOSS", Explanation: strings.Repeat("x", 501)}, {ReasonCode: "LOSS", Explanation: "untrimmed "}, {ReasonCode: "LOSS", Explanation: "line\nbreak"}} {
		if ValidateAudit(&a) == nil {
			t.Fatal("invalid audit accepted")
		}
	}
	c = adjustmentTestCommand()
	c.CommandVersion = 1
	if c.Validate() == nil {
		t.Fatal("v1 audit accepted")
	}
}
func TestWholeReversalUsesOriginalConversion(t *testing.T) {
	c := adjustmentTestCommand()
	l := c.Lines[0]
	c.Producer = CorrectionProducer
	c.Direction = Out
	c.Original = &OriginalOperation{ID: uuid.New(), WarehouseID: c.WarehouseID, Producer: OpeningProducer, Direction: In, Lines: []RecordedLine{{LineNumber: l.LineNumber, VariantID: l.VariantID, ProductID: uuid.New(), BaseUnitID: uuid.New(), InputCount: l.InputCount, Factor: 6, Pieces: l.InputCount * 6, PackID: l.PackID, PackRevision: l.PackRevision}}}
	c.Audit.OriginalOperationID = c.Original.ID
	o := Observation{ReferenceSnapshot: ReferenceSnapshot{ProductID: c.Original.Lines[0].ProductID, VariantID: l.VariantID, BaseUnitID: c.Original.Lines[0].BaseUnitID, QuantityKind: "PIECE", ProductActive: true, VariantActive: true, BaseUnitActive: true, StockReferenceRevision: l.ExpectedStockReferenceRevision, CheckedAt: time.Now()}, ProductCode: "P", ProductName: "Product", SKU: "SKU", BaseUnitCode: "PC"}
	lines, err := Prepare(c, []Observation{o})
	if err != nil || lines[0].Factor != 6 || lines[0].Pieces != l.InputCount*6 || lines[0].Pack != nil {
		t.Fatal("historical conversion requires current pack", err)
	}
	before, err := c.Fingerprint(testPrincipal())
	if err != nil {
		t.Fatal(err)
	}
	originalID := c.Original.ID
	c.Original.ID = uuid.New()
	c.Audit.OriginalOperationID = c.Original.ID
	after, err := c.Fingerprint(testPrincipal())
	if err != nil || before == after {
		t.Fatal("reversal target not fingerprinted")
	}
	c.Original.ID = originalID
	c.Audit.OriginalOperationID = originalID
	o.VariantActive = false
	if _, err = Prepare(c, []Observation{o}); !errors.Is(err, ErrReferenceInactive) {
		t.Fatal("inactive reversal allowed")
	}
	c.Original.Producer = CorrectionProducer
	if c.Validate() == nil {
		t.Fatal("reversal of reversal allowed")
	}
	c.Original.Producer = AdjustmentProducer
	c.Lines[0].InputCount++
	if c.Validate() == nil {
		t.Fatal("partial/replacement reversal allowed")
	}
}

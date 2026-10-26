package stock

import (
	"testing"

	"github.com/google/uuid"
)

func TestOpeningPolicyAndAuditFingerprint(t *testing.T) {
	c := testCommand()
	c.Producer = OpeningProducer
	c.SourceEventID = c.OperationID
	c.Direction = In
	fp, e := c.Fingerprint(testPrincipal())
	if e != nil {
		t.Fatal(e)
	}
	c.FirstRequestID = "another-request"
	next, e := c.Fingerprint(testPrincipal())
	if e != nil || next != fp {
		t.Fatal("audit changes intent")
	}
	c.Direction = Out
	if c.Validate() == nil {
		t.Fatal("opening issue accepted")
	}
	c.Direction = In
	c.SourceEventID = uuid.New()
	if c.Validate() == nil {
		t.Fatal("unbound source accepted")
	}
}

func TestOpeningSelectionValidation(t *testing.T) {
	valid := []Selection{{LineNumber: 1, VariantID: uuid.New(), InputCount: 1}, {LineNumber: 2, VariantID: uuid.New(), InputCount: 2}}
	if err := ValidateSelections(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func([]Selection){
		func(s []Selection) { s[1].LineNumber = 1 },
		func(s []Selection) { s[1].LineNumber = 3 },
		func(s []Selection) { s[1].VariantID = s[0].VariantID },
		func(s []Selection) { s[0].InputCount = 0 },
		func(s []Selection) { s[0].InputCount = -1 },
		func(s []Selection) { s[0].PackRevision = "1" },
		func(s []Selection) { s[0].PackID = uuid.New() },
		func(s []Selection) { s[0].PackID, s[0].PackRevision = uuid.New(), "0" },
	} {
		selected := append([]Selection(nil), valid...)
		mutation(selected)
		if ValidateSelections(selected) == nil {
			t.Fatal("invalid selection accepted")
		}
	}
}

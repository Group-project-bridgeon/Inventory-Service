package stock

import (
	"github.com/google/uuid"
	"slices"
	"testing"
	"time"
)

func reservationCommandFixture() ReservationCommand {
	return ReservationCommand{CommandID: uuid.New(), Key: uuid.New(), ReservationID: uuid.New(), OrderID: uuid.New(), WarehouseID: uuid.New(), Action: Reserve, Generation: 1, Lines: []ReservationLine{{OrderLineID: uuid.New(), VariantID: uuid.New(), Pieces: 10}, {OrderLineID: uuid.New(), VariantID: uuid.New(), Pieces: 20}}}
}

func TestReservationConsumeFingerprintVersionAndExecutionExclusions(t *testing.T) {
	c := reservationCommandFixture()
	c.Action, c.Lines = Consume, nil
	c.ExpectedOrderVersion, c.ExpectedReservationVersion = 1, 1
	owner, variant := uuid.New(), uuid.New()
	s := ReservationSnapshot{ID: c.ReservationID, OwnerMachineID: owner, OrderID: c.OrderID, WarehouseID: c.WarehouseID, Generation: 1, State: ReservationActive,
		Lines: []ReservationRecordedLine{{ReservationLine: ReservationLine{OrderLineID: uuid.New(), VariantID: variant, Pieces: 10}, LineNumber: 1,
			Observation: Observation{ReferenceSnapshot: ReferenceSnapshot{ProductID: uuid.New(), BaseUnitID: uuid.New()}}}}}
	command, err := NewConsumeCommand(c, s)
	if err != nil {
		t.Fatal(err)
	}
	p := Principal{MachineID: owner}
	want, err := command.Fingerprint(p)
	if err != nil || want.Version != 3 {
		t.Fatal("consume fingerprint version", err)
	}
	// The immutable piece request defines intent. Refreshed execution evidence and
	// request tracing do not change it; no Catalog revision is fabricated.
	c.Key, c.FirstRequestID = uuid.New(), "reconciliation"
	s.Lines[0].CheckedAt = time.Now()
	s.Lines[0].StockReferenceRevision = "execution-observation"
	s.UpdatedAt = time.Now()
	command, err = NewConsumeCommand(c, s)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := command.Fingerprint(p); err != nil || got != want {
		t.Fatal("execution metadata changed fingerprint", err)
	}
	c.ExpectedOrderVersion++
	changed, err := NewConsumeCommand(c, s)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := changed.Fingerprint(p); err != nil || got == want {
		t.Fatal("expected version omitted", err)
	}
	if _, err := command.Fingerprint(Principal{UserID: owner}); err == nil {
		t.Fatal("human actor accepted for consume")
	}
	if _, err := command.Fingerprint(Principal{MachineID: uuid.New()}); err == nil {
		t.Fatal("different owner accepted")
	}
}
func TestReservationValidationAndCanonicalIntent(t *testing.T) {
	c := reservationCommandFixture()
	owner := uuid.New()
	fp, err := c.Fingerprint(owner)
	if err != nil || fp.Version != 1 {
		t.Fatal(err)
	}
	c.Lines = slices.Clone(c.Lines)
	slices.Reverse(c.Lines)
	c.Key = uuid.New()
	c.FirstRequestID = "retry"
	got, err := c.Fingerprint(owner)
	if err != nil || got != fp {
		t.Fatal("line order/key/metadata changed intent")
	}
	for _, change := range []func(*ReservationCommand){func(c *ReservationCommand) { c.Lines[0].Pieces++ }, func(c *ReservationCommand) { c.Lines[0].OrderLineID = uuid.New() }, func(c *ReservationCommand) { c.OrderID = uuid.New() }, func(c *ReservationCommand) { c.ReservationID = uuid.New() }, func(c *ReservationCommand) { c.WarehouseID = uuid.New() }, func(c *ReservationCommand) { c.ExpectedOrderVersion++ }, func(c *ReservationCommand) { c.CommandID = uuid.New() }} {
		changed := c
		changed.Lines = slices.Clone(c.Lines)
		change(&changed)
		other, e := changed.Fingerprint(owner)
		if e != nil || other == fp {
			t.Fatal("meaningful intent omitted")
		}
	}
	if other, _ := c.Fingerprint(uuid.New()); other == fp {
		t.Fatal("owner omitted")
	}
	for _, change := range []func(*ReservationCommand){func(c *ReservationCommand) { c.Lines[0].Pieces = 0 }, func(c *ReservationCommand) { c.Lines[0].Pieces = -1 }, func(c *ReservationCommand) { c.Lines[0].VariantID = c.Lines[1].VariantID }, func(c *ReservationCommand) { c.Lines[0].OrderLineID = c.Lines[1].OrderLineID }, func(c *ReservationCommand) { c.Generation = 3 }, func(c *ReservationCommand) { c.Key = uuid.Nil }, func(c *ReservationCommand) { c.Action = Release }} {
		changed := c
		changed.Lines = slices.Clone(c.Lines)
		change(&changed)
		if changed.Validate() == nil {
			t.Fatal("invalid reservation intent accepted")
		}
	}
	for _, n := range []int{0, 1, 100, 101} {
		changed := c
		changed.Lines = nil
		for i := 0; i < n; i++ {
			changed.Lines = append(changed.Lines, ReservationLine{OrderLineID: uuid.New(), VariantID: uuid.New(), Pieces: 1})
		}
		if (changed.Validate() == nil) != (n >= 1 && n <= 100) {
			t.Fatal("line bound")
		}
	}
}

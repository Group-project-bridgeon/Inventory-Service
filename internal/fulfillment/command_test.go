package fulfillment

import (
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"testing"
)

func commandFixture() Command {
	return Command{CommandID: uuid.New(), Key: uuid.New(), ID: uuid.New(), ReservationID: uuid.New(), OrderID: uuid.New(), WarehouseID: uuid.New(), OwnerMachineID: uuid.New(), Action: Create, Generation: 1, ExpectedReservationVersion: 1, ExpectedOrderVersion: 1, ExternalReference: "REF"}
}
func TestCanonicalFulfillmentIntent(t *testing.T) {
	c := commandFixture()
	a := Actor{"MACHINE", c.OwnerMachineID}
	fp, e := c.Fingerprint(a)
	if e != nil || fp.Version != 1 {
		t.Fatal(e)
	}
	c.Key = uuid.New()
	c.FirstRequestID = "retry"
	if got, e := c.Fingerprint(a); e != nil || got != fp {
		t.Fatal("volatile metadata changed intent")
	}
	for _, change := range []func(*Command){func(c *Command) { c.CommandID = uuid.New() }, func(c *Command) { c.ID = uuid.New() }, func(c *Command) { c.ReservationID = uuid.New() }, func(c *Command) { c.OrderID = uuid.New() }, func(c *Command) { c.WarehouseID = uuid.New() }, func(c *Command) { c.OwnerMachineID = uuid.New() }, func(c *Command) { c.Generation++ }, func(c *Command) { c.ExpectedReservationVersion++ }, func(c *Command) { c.ExpectedOrderVersion++ }, func(c *Command) { c.ExternalReference = "changed" }} {
		x := c
		change(&x)
		got, e := x.Fingerprint(a)
		if e != nil || got == fp {
			t.Fatal("meaningful intent omitted", e)
		}
	}
	other := a
	other.ID = uuid.New()
	if got, _ := c.Fingerprint(other); got == fp {
		t.Fatal("actor omitted")
	}
	for _, bad := range []func(*Command){func(c *Command) { c.Key = uuid.Nil }, func(c *Command) { c.ExpectedReservationVersion = 0 }, func(c *Command) { c.Generation = -1 }, func(c *Command) { c.Note = "not allowed on create" }, func(c *Command) { c.ExternalReference = " REF " }, func(c *Command) { c.ExternalReference = string([]byte{0xff}) }} {
		x := c
		bad(&x)
		if x.Validate() == nil {
			t.Fatal("invalid command accepted")
		}
	}
	if _, e := Text("line\nbreak", 500); e == nil {
		t.Fatal("control character accepted")
	}
	if v, e := Text("  ref  ", 100); e != nil || v != "ref" {
		t.Fatal("trim contract")
	}
}
func TestHumanConsumeVersionFour(t *testing.T) {
	c := commandFixture()
	c.Action = Dispatch
	c.ExpectedVersion = 1
	c.ExternalReference = ""
	a := Actor{"HUMAN", uuid.New()}
	snapshot := stock.ReservationSnapshot{ID: c.ReservationID, OwnerMachineID: c.OwnerMachineID, OrderID: c.OrderID, WarehouseID: c.WarehouseID, Generation: 1, Lines: []stock.ReservationRecordedLine{{ReservationLine: stock.ReservationLine{OrderLineID: uuid.New(), VariantID: uuid.New(), Pieces: 10}, LineNumber: 1, Observation: stock.Observation{ReferenceSnapshot: stock.ReferenceSnapshot{ProductID: uuid.New(), BaseUnitID: uuid.New()}}}}}
	command, e := ConsumeCommand(c, a, snapshot)
	if e != nil {
		t.Fatal(e)
	}
	fp, e := command.Fingerprint(stock.Principal{UserID: a.ID})
	if e != nil || fp.Version != 4 || command.CommandVersion != 4 || command.Producer != stock.DispatchProducer {
		t.Fatal("explicit human consume version", e)
	}
	if _, e = command.Fingerprint(stock.Principal{MachineID: c.OwnerMachineID}); e == nil {
		t.Fatal("owner substituted for human")
	}
	command.Consume.Request.DispatchVersion++
	if other, e := command.Fingerprint(stock.Principal{UserID: a.ID}); e != nil || other == fp {
		t.Fatal("dispatch version omitted", e)
	}
}

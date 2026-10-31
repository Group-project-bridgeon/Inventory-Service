package stock

import (
	"fmt"
	"github.com/google/uuid"
	"testing"
)

// Frozen pre-fulfillment consume v3 byte contract. The expected hash is generated
// independently from the documented v1 reservation and v3 stock binary layouts.
func TestLegacyConsumeV3GoldenAfterFulfillment(t *testing.T) {
	id := func(n int) uuid.UUID { return uuid.MustParse(fmt.Sprintf("01935f00-0000-7000-8000-%012d", n)) }
	req := ReservationCommand{CommandID: id(11), Key: id(15), ReservationID: id(12), OrderID: id(13), WarehouseID: id(1), Generation: 1, ExpectedOrderVersion: 1, ExpectedReservationVersion: 1, Action: Consume}
	s := ReservationSnapshot{ID: req.ReservationID, OwnerMachineID: id(10), OrderID: req.OrderID, WarehouseID: req.WarehouseID, Generation: 1, Lines: []ReservationRecordedLine{{ReservationLine: ReservationLine{OrderLineID: id(14), VariantID: id(2), Pieces: 10}, LineNumber: 1, Observation: Observation{ReferenceSnapshot: ReferenceSnapshot{ProductID: id(6), BaseUnitID: id(3)}}}}}
	c, e := NewConsumeCommand(req, s)
	if e != nil {
		t.Fatal(e)
	}
	fp, e := c.Fingerprint(Principal{MachineID: id(10)})
	if e != nil || fp.Version != 3 || fmt.Sprintf("%x", fp.Hash) != "86b5369d772bcf4b4f6b50250d92537c088db4eb3189e63f0388dd1d1556ad61" {
		t.Fatal("legacy consume canonical bytes drifted", e)
	}
}

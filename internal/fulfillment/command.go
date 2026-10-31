// Package fulfillment contains the transport-independent whole-reservation dispatch contract.
package fulfillment

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Audience           = "inventory-fulfillment"
	CreatePermission   = "inventory.dispatches:create"
	CancelPermission   = "inventory.dispatches:cancel"
	DispatchPermission = "inventory.dispatches:dispatch"
	DeliveryPermission = "inventory.deliveries:update"
	ReadPermission     = "inventory.dispatches:read"
)

type Action string

const (
	Create   Action = "CREATE"
	Cancel   Action = "CANCEL"
	Dispatch Action = "DISPATCH"
	Delivery Action = "DELIVERY"
)

var (
	ErrNotFound = errors.New("dispatch not found")
	ErrState    = errors.New("invalid dispatch state")
	ErrStale    = errors.New("stale dispatch or reservation version")
	ErrClaimed  = errors.New("reservation already has a dispatch")
)

type Actor struct {
	Kind string    `json:"kind"`
	ID   uuid.UUID `json:"id"`
}

func (a Actor) Validate() error {
	if a.ID == uuid.Nil || (a.Kind != "HUMAN" && a.Kind != "MACHINE") {
		return stock.ErrForbidden
	}
	return nil
}

type Access interface {
	Actor() Actor
	Authorize(context.Context, string) error
}

func Permission(a Action) string {
	switch a {
	case Create:
		return CreatePermission
	case Cancel:
		return CancelPermission
	case Dispatch:
		return DispatchPermission
	case Delivery:
		return DeliveryPermission
	}
	return ""
}

type Command struct {
	CommandID, Key, ID, ReservationID, OrderID, WarehouseID, OwnerMachineID       uuid.UUID
	Action                                                                        Action
	Generation, ExpectedVersion, ExpectedReservationVersion, ExpectedOrderVersion stock.Quantity
	Outcome, ExternalReference, Note, FirstRequestID                              string
}

func Text(s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return "", stock.ErrInvalidArgument
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", stock.ErrInvalidArgument
		}
	}
	return s, nil
}
func opID(id uuid.UUID) bool {
	return id != uuid.Nil && id.Variant() == uuid.RFC4122 && (id.Version() == 4 || id.Version() == 7)
}
func (c Command) Validate() error {
	if !opID(c.CommandID) || !opID(c.Key) || c.ID == uuid.Nil || c.WarehouseID == uuid.Nil || len(c.FirstRequestID) > 64 {
		return stock.ErrInvalidArgument
	}
	if x, e := Text(c.ExternalReference, 100); e != nil || x != c.ExternalReference {
		return stock.ErrInvalidArgument
	}
	if x, e := Text(c.Note, 500); e != nil || x != c.Note {
		return stock.ErrInvalidArgument
	}
	switch c.Action {
	case Create:
		if c.ReservationID == uuid.Nil || c.OrderID == uuid.Nil || c.Generation <= 0 || c.ExpectedVersion != 0 || c.ExpectedReservationVersion <= 0 || c.ExpectedOrderVersion <= 0 || c.Outcome != "" || c.Note != "" {
			return stock.ErrInvalidArgument
		}
	case Dispatch:
		if c.ExpectedVersion <= 0 || c.ExpectedReservationVersion <= 0 || c.ExpectedOrderVersion <= 0 || c.Outcome != "" || c.ExternalReference != "" || c.Note != "" {
			return stock.ErrInvalidArgument
		}
	case Cancel:
		if c.ExpectedVersion <= 0 || c.ExpectedReservationVersion != 0 || c.ExpectedOrderVersion != 0 || c.Outcome != "" || c.ExternalReference != "" {
			return stock.ErrInvalidArgument
		}
	case Delivery:
		if c.ExpectedVersion <= 0 || c.ExpectedReservationVersion != 0 || c.ExpectedOrderVersion != 0 || (c.Outcome != "FAILED" && c.Outcome != "DELIVERED") {
			return stock.ErrInvalidArgument
		}
	default:
		return stock.ErrInvalidArgument
	}
	return nil
}

// Owner is trusted immutable context resolved by Inventory, never body authority.
func (c Command) Fingerprint(a Actor) (stock.RequestFingerprint, error) {
	if e := a.Validate(); e != nil {
		return stock.RequestFingerprint{}, e
	}
	if e := c.Validate(); e != nil {
		return stock.RequestFingerprint{}, e
	}
	if c.OwnerMachineID == uuid.Nil {
		return stock.RequestFingerprint{}, stock.ErrInvalidArgument
	}
	b := []byte("inventory.fulfillment.request\x00")
	b = binary.BigEndian.AppendUint16(b, 1)
	text := func(s string) { b = binary.BigEndian.AppendUint32(b, uint32(len(s))); b = append(b, s...) }
	text(a.Kind)
	b = append(b, a.ID[:]...)
	for _, id := range []uuid.UUID{c.OwnerMachineID, c.CommandID, c.ID, c.ReservationID, c.OrderID, c.WarehouseID} {
		b = append(b, id[:]...)
	}
	text(string(c.Action))
	for _, v := range []stock.Quantity{c.Generation, c.ExpectedVersion, c.ExpectedReservationVersion, c.ExpectedOrderVersion} {
		b = binary.BigEndian.AppendUint64(b, uint64(v))
	}
	text(c.Outcome)
	text(c.ExternalReference)
	text(c.Note)
	return stock.RequestFingerprint{Version: 1, Hash: sha256.Sum256(b)}, nil
}

type Snapshot struct {
	ID                uuid.UUID      `json:"dispatch_id"`
	ReservationID     uuid.UUID      `json:"reservation_id"`
	OrderID           uuid.UUID      `json:"order_id"`
	WarehouseID       uuid.UUID      `json:"warehouse_id"`
	OwnerMachineID    uuid.UUID      `json:"owner_machine_id"`
	Generation        stock.Quantity `json:"generation"`
	State             string         `json:"state"`
	Version           stock.Quantity `json:"version"`
	OperationID       *uuid.UUID     `json:"operation_id"`
	ExternalReference string         `json:"external_reference"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}
type Result struct {
	ResultVersion string          `json:"result_version"`
	ID            uuid.UUID       `json:"dispatch_id"`
	ReservationID uuid.UUID       `json:"reservation_id"`
	OrderID       uuid.UUID       `json:"order_id"`
	WarehouseID   uuid.UUID       `json:"warehouse_id"`
	Generation    stock.Quantity  `json:"generation"`
	State         string          `json:"state"`
	Version       stock.Quantity  `json:"version"`
	ChangedAt     time.Time       `json:"changed_at"`
	OperationID   *uuid.UUID      `json:"operation_id"`
	Outcome       *string         `json:"delivery_outcome"`
	StockResult   json.RawMessage `json:"stock_result"`
	Replayed      bool            `json:"replayed"`
}
type Transition struct {
	ID                uuid.UUID      `json:"id"`
	Action            Action         `json:"action"`
	State             string         `json:"state"`
	Version           stock.Quantity `json:"version"`
	Outcome           *string        `json:"delivery_outcome"`
	ExternalReference string         `json:"external_reference"`
	Note              string         `json:"note"`
	ChangedAt         time.Time      `json:"changed_at"`
}
type Filter struct {
	State                                                      string
	WarehouseID, OrderID, ReservationID, OwnerMachineID, After uuid.UUID
	Limit                                                      int
	Cursor                                                     string
}
type Store interface {
	Get(context.Context, Actor, uuid.UUID) (Snapshot, error)
	Reservation(context.Context, uuid.UUID, uuid.UUID) (stock.ReservationSnapshot, error)
	Lookup(context.Context, Actor, Command, stock.RequestFingerprint) (Result, bool, error)
	Apply(context.Context, Actor, Command, stock.RequestFingerprint, []stock.PreparedLine) (Result, error)
	List(context.Context, Actor, Filter) ([]Snapshot, error)
	Audit(context.Context, Actor, uuid.UUID, uuid.UUID, int) ([]Transition, error)
}

func ConsumeCommand(c Command, a Actor, s stock.ReservationSnapshot) (stock.Command, error) {
	fp, e := c.Fingerprint(a)
	if e != nil {
		return stock.Command{}, e
	}
	if a.Kind != "HUMAN" || c.Action != Dispatch || s.OwnerMachineID != c.OwnerMachineID || s.ID != c.ReservationID {
		return stock.Command{}, stock.ErrForbidden
	}
	req := stock.ReservationCommand{CommandID: c.CommandID, Key: c.Key, ReservationID: c.ReservationID, OrderID: c.OrderID, WarehouseID: c.WarehouseID, Action: stock.Consume, Generation: c.Generation, ExpectedOrderVersion: c.ExpectedOrderVersion, ExpectedReservationVersion: c.ExpectedReservationVersion, FirstRequestID: c.FirstRequestID, ActorUserID: a.ID, DispatchID: c.ID, DispatchVersion: c.ExpectedVersion, DispatchRequestHash: fp.Hash}
	return stock.NewConsumeCommand(req, s)
}

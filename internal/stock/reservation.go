package stock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
)

const (
	ReservationAudience       = "inventory-reservations"
	ReservePermission         = "inventory.reservations:reserve"
	ReleasePermission         = "inventory.reservations:release"
	ConsumePermission         = "inventory.reservations:consume"
	ReservationReadPermission = "inventory.reservations:read"
	ConsumeProducer           = "inventory.reservations.consume.v1"
	DispatchProducer          = "inventory.dispatches.v1"
)

type ReservationAction string

const (
	Reserve ReservationAction = "RESERVE"
	Release ReservationAction = "RELEASE"
	Consume ReservationAction = "CONSUME"
)

type ReservationState string

const (
	ReservationActive   ReservationState = "ACTIVE"
	ReservationReleased ReservationState = "RELEASED"
	ReservationConsumed ReservationState = "CONSUMED"
)

var (
	ErrReservationNotFound   = errors.New("owned reservation not found")
	ErrReservationExists     = errors.New("reservation business action already exists")
	ErrReservationTerminal   = errors.New("reservation generation is terminal")
	ErrReservationOrder      = errors.New("reservation command out of order")
	ErrReservationStale      = errors.New("reservation generation or version is stale")
	ErrOrderConsumed         = errors.New("order already consumed")
	ErrInsufficientAvailable = errors.New("insufficient available stock")
)

type ReservationLine struct {
	OrderLineID, VariantID uuid.UUID
	Pieces                 Quantity
}
type ReservationCommand struct {
	// Internal fulfillment evidence. HTTP reservation input cannot populate these.
	ActorUserID, DispatchID                                                                  uuid.UUID
	DispatchVersion                                                                          Quantity
	DispatchRequestHash                                                                      [32]byte
	CommandID, Key, ReservationID, OrderID, WarehouseID                                      uuid.UUID
	Action                                                                                   ReservationAction
	Generation, ExpectedPreviousGeneration, ExpectedOrderVersion, ExpectedReservationVersion Quantity
	Lines                                                                                    []ReservationLine
	FirstRequestID                                                                           string
}

func commandUUID(id uuid.UUID) bool {
	return id != uuid.Nil && id.Variant() == uuid.RFC4122 && (id.Version() == 4 || id.Version() == 7)
}
func (c ReservationCommand) Validate() error {
	if c.DispatchID != uuid.Nil || c.ActorUserID != uuid.Nil || c.DispatchVersion != 0 || c.DispatchRequestHash != ([32]byte{}) {
		if c.Action != Consume || c.DispatchID == uuid.Nil || c.ActorUserID == uuid.Nil || c.DispatchVersion <= 0 || c.DispatchRequestHash == ([32]byte{}) {
			return ErrInvalidArgument
		}
	}
	if !commandUUID(c.CommandID) || !commandUUID(c.Key) || c.ReservationID == uuid.Nil || c.OrderID == uuid.Nil || c.WarehouseID == uuid.Nil || c.Generation <= 0 || c.ExpectedOrderVersion < 0 || len(c.FirstRequestID) > 64 {
		return ErrInvalidArgument
	}
	if c.Action == Reserve {
		if c.ExpectedPreviousGeneration < 0 || c.ExpectedPreviousGeneration == math.MaxInt64 || c.Generation != c.ExpectedPreviousGeneration+1 || c.ExpectedReservationVersion != 0 || len(c.Lines) < 1 || len(c.Lines) > MaxLines {
			return ErrInvalidArgument
		}
		variants, ids := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
		for _, l := range c.Lines {
			if l.OrderLineID == uuid.Nil || l.VariantID == uuid.Nil || l.Pieces <= 0 || variants[l.VariantID] || ids[l.OrderLineID] {
				return ErrInvalidArgument
			}
			variants[l.VariantID] = true
			ids[l.OrderLineID] = true
		}
	} else if c.Action == Release || c.Action == Consume {
		if len(c.Lines) != 0 || c.ExpectedPreviousGeneration != 0 || c.ExpectedReservationVersion <= 0 || c.ExpectedOrderVersion <= 0 {
			return ErrInvalidArgument
		}
	} else {
		return ErrInvalidArgument
	}
	return nil
}
func (c ReservationCommand) Fingerprint(owner uuid.UUID) (RequestFingerprint, error) {
	if owner == uuid.Nil {
		return RequestFingerprint{}, ErrForbidden
	}
	if err := c.Validate(); err != nil {
		return RequestFingerprint{}, err
	}
	data := []byte("inventory.reservation.request\x00")
	version := uint16(1)
	if c.DispatchID != uuid.Nil {
		version = 2
	}
	data = binary.BigEndian.AppendUint16(data, version)
	for _, id := range []uuid.UUID{owner, c.CommandID, c.ReservationID, c.OrderID, c.WarehouseID} {
		data = append(data, id[:]...)
	}
	data = appendText(data, string(c.Action))
	for _, v := range []Quantity{c.Generation, c.ExpectedPreviousGeneration, c.ExpectedOrderVersion, c.ExpectedReservationVersion} {
		data = binary.BigEndian.AppendUint64(data, uint64(v))
	}
	lines := slices.Clone(c.Lines)
	slices.SortFunc(lines, func(a, b ReservationLine) int { return bytes.Compare(a.OrderLineID[:], b.OrderLineID[:]) })
	data = binary.BigEndian.AppendUint16(data, uint16(len(lines)))
	for _, l := range lines {
		data = append(data, l.OrderLineID[:]...)
		data = append(data, l.VariantID[:]...)
		data = binary.BigEndian.AppendUint64(data, uint64(l.Pieces))
	}
	if version == 2 {
		data = append(data, c.ActorUserID[:]...)
		data = append(data, c.DispatchID[:]...)
		data = binary.BigEndian.AppendUint64(data, uint64(c.DispatchVersion))
		data = append(data, c.DispatchRequestHash[:]...)
	}
	return RequestFingerprint{Version: version, Hash: sha256.Sum256(data)}, nil
}

type ReservationRecordedLine struct {
	ReservationLine
	LineNumber int16
	Observation
}
type ReservationSnapshot struct {
	ID, OwnerMachineID, OrderID, WarehouseID uuid.UUID
	Generation, Version, OrderVersion        Quantity
	State                                    ReservationState
	CreatedAt, UpdatedAt                     time.Time
	Lines                                    []ReservationRecordedLine
}
type ReservationResult struct {
	ResultVersion string           `json:"result_version"`
	ReservationID uuid.UUID        `json:"reservation_id"`
	OrderID       uuid.UUID        `json:"order_id"`
	WarehouseID   uuid.UUID        `json:"warehouse_id"`
	Generation    Quantity         `json:"generation"`
	State         ReservationState `json:"state"`
	Version       Quantity         `json:"version"`
	OrderVersion  Quantity         `json:"order_version"`
	ChangedAt     time.Time        `json:"changed_at"`
	OperationID   *uuid.UUID       `json:"operation_id"`
	StockResult   json.RawMessage  `json:"stock_result"`
	Replayed      bool             `json:"replayed"` // Execution field; durable format omits this.
}

// Access is supplied only by an authenticated transport boundary. Future approved
// consumers can implement this contract without constructing a human or fake JWT.
type ReservationAccess interface {
	Owner() uuid.UUID
	AuthorizeReservation(context.Context, string) error
}

func PermissionFor(action ReservationAction) string {
	switch action {
	case Reserve:
		return ReservePermission
	case Release:
		return ReleasePermission
	case Consume:
		return ConsumePermission
	}
	return ""
}

type ReservationStore interface {
	Lookup(context.Context, uuid.UUID, ReservationCommand, RequestFingerprint) (ReservationResult, bool, error)
	Apply(context.Context, uuid.UUID, ReservationCommand, RequestFingerprint, []Observation) (ReservationResult, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (ReservationSnapshot, error)
	List(context.Context, uuid.UUID, *uuid.UUID, int) ([]ReservationSnapshot, error)
}
type ConsumePlan struct {
	Request  ReservationCommand
	Snapshot ReservationSnapshot
}

func NewConsumeCommand(c ReservationCommand, s ReservationSnapshot) (Command, error) {
	result := Command{OperationID: c.CommandID, SourceEventID: c.ReservationID, Producer: ConsumeProducer, WarehouseID: c.WarehouseID, Direction: Out, CommandVersion: 3, IdempotencyKey: c.Key, FirstRequestID: c.FirstRequestID, Consume: &ConsumePlan{c, s}}
	if c.DispatchID != uuid.Nil {
		result.CommandVersion = 4
		result.Producer = DispatchProducer
	}
	for _, l := range s.Lines {
		result.Lines = append(result.Lines, CommandLine{LineNumber: l.LineNumber, VariantID: l.ReservationLine.VariantID, InputCount: l.Pieces})
	}
	return result, result.Validate()
}
func (c Command) validateConsume() error {
	validVersion := (c.CommandVersion == 3 && c.Producer == ConsumeProducer && c.Consume != nil && c.Consume.Request.DispatchID == uuid.Nil) || (c.CommandVersion == 4 && c.Producer == DispatchProducer && c.Consume != nil && c.Consume.Request.DispatchID != uuid.Nil)
	if !validVersion || c.Direction != Out || c.Audit != nil || c.Original != nil || c.Consume == nil {
		return ErrInvalidArgument
	}
	req, s := c.Consume.Request, c.Consume.Snapshot
	if req.Validate() != nil || req.Action != Consume || req.CommandID != c.OperationID || req.Key != c.IdempotencyKey || req.ReservationID != c.SourceEventID || req.WarehouseID != c.WarehouseID || s.ID != req.ReservationID || s.OrderID != req.OrderID || s.WarehouseID != req.WarehouseID || s.Generation != req.Generation || s.OwnerMachineID == uuid.Nil || len(s.Lines) != len(c.Lines) {
		return ErrInvalidArgument
	}
	for _, l := range c.Lines {
		found := false
		for _, saved := range s.Lines {
			if saved.LineNumber == l.LineNumber && saved.ReservationLine.VariantID == l.VariantID && saved.Pieces == l.InputCount && saved.ProductID != uuid.Nil && saved.BaseUnitID != uuid.Nil {
				found = true
				break
			}
		}
		if !found || l.PackID != uuid.Nil || l.PackRevision != "" || l.ExpectedStockReferenceRevision != "" {
			return ErrInvalidArgument
		}
	}
	return nil
}
func (c Command) canonicalConsume(p Principal) ([]byte, error) {
	owner := c.Consume.Snapshot.OwnerMachineID
	kind := "MACHINE"
	actor := p.MachineID
	if c.CommandVersion == 4 {
		kind = "HUMAN"
		actor = p.UserID
		if actor == uuid.Nil || p.MachineID != uuid.Nil || actor != c.Consume.Request.ActorUserID {
			return nil, ErrForbidden
		}
	} else if p.UserID != uuid.Nil || p.MachineID == uuid.Nil || p.MachineID != owner {
		return nil, ErrForbidden
	}
	fp, err := c.Consume.Request.Fingerprint(owner)
	if err != nil {
		return nil, err
	}
	data := []byte("inventory.stock.request\x00")
	data = binary.BigEndian.AppendUint16(data, uint16(c.CommandVersion))
	data = binary.BigEndian.AppendUint16(data, uint16(c.CommandVersion))
	data = appendText(data, kind)
	data = append(data, actor[:]...)
	if c.CommandVersion == 4 {
		data = append(data, owner[:]...)
	}
	data = appendText(data, c.Producer)
	data = append(data, fp.Hash[:]...)
	lines := slices.Clone(c.Lines)
	slices.SortFunc(lines, func(a, b CommandLine) int { return int(a.LineNumber) - int(b.LineNumber) })
	data = binary.BigEndian.AppendUint16(data, uint16(len(lines)))
	for _, l := range lines {
		data = binary.BigEndian.AppendUint16(data, uint16(l.LineNumber))
		data = append(data, l.VariantID[:]...)
		data = binary.BigEndian.AppendUint64(data, uint64(l.InputCount))
	}
	return data, nil
}

func PrepareReservation(c ReservationCommand, observations []Observation) ([]ReservationRecordedLine, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Action != Reserve || len(observations) != len(c.Lines) {
		return nil, ErrReferenceChanged
	}
	byVariant := map[uuid.UUID]Observation{}
	for _, o := range observations {
		if _, ok := byVariant[o.VariantID]; ok {
			return nil, ErrReferenceChanged
		}
		byVariant[o.VariantID] = o
	}
	selected := slices.Clone(c.Lines)
	slices.SortFunc(selected, func(a, b ReservationLine) int { return bytes.Compare(a.OrderLineID[:], b.OrderLineID[:]) })
	result := make([]ReservationRecordedLine, 0, len(selected))
	for i, l := range selected {
		o, ok := byVariant[l.VariantID]
		if !ok {
			return nil, ErrReferenceChanged
		}
		if err := o.ValidateFor(CommandLine{LineNumber: int16(i + 1), VariantID: l.VariantID, InputCount: l.Pieces, ExpectedStockReferenceRevision: o.StockReferenceRevision}); err != nil {
			return nil, err
		}
		if !boundedText(o.ProductCode, 50) || !boundedText(o.ProductName, 200) || !boundedText(o.SKU, 100) || !boundedText(o.BaseUnitCode, 30) || o.PackLabel != nil {
			return nil, ErrReferenceMalformed
		}
		result = append(result, ReservationRecordedLine{ReservationLine: l, LineNumber: int16(i + 1), Observation: o})
	}
	return result, nil
}

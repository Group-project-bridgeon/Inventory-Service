package stock

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

const FingerprintVersion uint16 = 1

// RequestFingerprint is not an idempotency key or an authentication mechanism.
type RequestFingerprint struct {
	Version uint16
	Hash    [sha256.Size]byte
}

// Fingerprint validates and hashes caller intent plus the verified actor identity.
// Idempotency keys, Catalog observations and results are deliberately excluded.
func (c Command) Fingerprint(principal Principal) (RequestFingerprint, error) {
	if err := c.Validate(); err != nil {
		return RequestFingerprint{}, err
	}
	if c.CommandVersion >= 3 {
		data, err := c.canonicalConsume(principal)
		if err != nil {
			return RequestFingerprint{}, err
		}
		return RequestFingerprint{Version: uint16(c.CommandVersion), Hash: sha256.Sum256(data)}, nil
	}
	if principal.UserID == uuid.Nil || principal.MachineID != uuid.Nil {
		return RequestFingerprint{}, fmt.Errorf("actor identity: %w", ErrInvalidArgument)
	}
	version := FingerprintVersion
	if c.CommandVersion == 2 {
		version = 2
	}
	return RequestFingerprint{Version: version, Hash: sha256.Sum256(c.canonicalIntent(principal))}, nil
}

// canonicalIntent v1 is frozen: domain tag; big-endian uint16 fingerprint and
// command versions; operation UUID; producer text; source UUID; actor UUID;
// warehouse UUID; direction text; uint16 line count; sorted lines. Text uses a
// uint32 byte-length prefix followed by UTF-8 bytes. UUIDs use their 16 raw bytes.
// Each line is uint16 number, variant UUID, uint64 count, uint8 pack presence,
// optional pack UUID + revision text, then expected reference revision text.
func (c Command) canonicalIntent(principal Principal) []byte {
	data := []byte("inventory.stock.request\x00")
	version := FingerprintVersion
	if c.CommandVersion == 2 {
		version = 2
	}
	data = binary.BigEndian.AppendUint16(data, version)
	data = binary.BigEndian.AppendUint16(data, uint16(c.CommandVersion))
	data = append(data, c.OperationID[:]...)
	data = appendText(data, c.Producer)
	data = append(data, c.SourceEventID[:]...)
	data = append(data, principal.UserID[:]...)
	data = append(data, c.WarehouseID[:]...)
	data = appendText(data, string(c.Direction))
	data = binary.BigEndian.AppendUint16(data, uint16(len(c.Lines)))
	lines := slices.Clone(c.Lines)
	slices.SortFunc(lines, func(a, b CommandLine) int { return int(a.LineNumber) - int(b.LineNumber) })
	for _, line := range lines {
		data = binary.BigEndian.AppendUint16(data, uint16(line.LineNumber))
		data = append(data, line.VariantID[:]...)
		data = binary.BigEndian.AppendUint64(data, uint64(line.InputCount))
		if line.PackID == uuid.Nil {
			data = append(data, 0)
		} else {
			data = append(data, 1)
			data = append(data, line.PackID[:]...)
			data = appendText(data, line.PackRevision)
		}
		data = appendText(data, line.ExpectedStockReferenceRevision)
	}
	if c.CommandVersion == 2 {
		data = appendText(data, c.Audit.ReasonCode)
		data = appendText(data, c.Audit.Explanation)
		data = append(data, c.Audit.OriginalOperationID[:]...)
	}
	return data
}

func appendText(data []byte, text string) []byte {
	data = binary.BigEndian.AppendUint32(data, uint32(len(text)))
	return append(data, text...)
}

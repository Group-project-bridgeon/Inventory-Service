// Package stock defines exact stock contracts and internal posting orchestration.
// Persistence and authenticated remote adapters are separate dependencies.
package stock

import "errors"

var (
	ErrReferenceUnauthorized = errors.New("stock reference service authentication denied")
	ErrReferenceDenied       = errors.New("stock reference service permission denied")
	ErrReferenceMalformed    = errors.New("invalid stock reference dependency response")
	ErrReferenceStale        = errors.New("stock reference observation outside approved time window")
	ErrInvalidArgument       = errors.New("invalid stock input")
	ErrQuantityOverflow      = errors.New("stock quantity exceeds int64 range")
	ErrInsufficientStock     = errors.New("insufficient stock")
	ErrReferenceInactive     = errors.New("stock reference inactive")
	ErrReferenceChanged      = errors.New("stock reference changed")
)

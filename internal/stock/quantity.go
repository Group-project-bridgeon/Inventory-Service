package stock

import (
	"encoding/json"
	"math"
	"strconv"
)

// Quantity is an exact int64 count. Zero is valid for a balance, but not an
// operation count or pack factor. Validate values constructed by conversion too.
type Quantity int64

// ParseBalance accepts only canonical unsigned decimal text, including zero.
func ParseBalance(text string) (Quantity, error) {
	if text == "" || (len(text) > 1 && text[0] == '0') {
		return 0, ErrInvalidArgument
	}
	for i := range len(text) {
		if text[i] < '0' || text[i] > '9' {
			return 0, ErrInvalidArgument
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, ErrQuantityOverflow
	}
	return Quantity(n), nil
}

func ParsePositiveQuantity(text string) (Quantity, error) {
	q, err := ParseBalance(text)
	if err != nil {
		return 0, err
	}
	if q <= 0 {
		return 0, ErrInvalidArgument
	}
	return q, nil
}

// AddPieces adds a positive operation quantity to a nonnegative balance.
func AddPieces(balance, pieces Quantity) (Quantity, error) {
	if balance < 0 || pieces <= 0 {
		return 0, ErrInvalidArgument
	}
	if balance > Quantity(math.MaxInt64)-pieces {
		return 0, ErrQuantityOverflow
	}
	return balance + pieces, nil
}

// SubtractPieces rejects an issue exceeding the balance, without signed negation.
func SubtractPieces(balance, pieces Quantity) (Quantity, error) {
	if balance < 0 || pieces <= 0 {
		return 0, ErrInvalidArgument
	}
	if balance < pieces {
		return 0, ErrInsufficientStock
	}
	return balance - pieces, nil
}

// PackToPieces converts a whole count using a validated, item-specific factor.
// This arithmetic alone does not establish that Catalog supplied the factor.
func PackToPieces(count, piecesPerPack Quantity) (Quantity, error) {
	if count <= 0 || piecesPerPack <= 0 {
		return 0, ErrInvalidArgument
	}
	if count > Quantity(math.MaxInt64)/piecesPerPack {
		return 0, ErrQuantityOverflow
	}
	return count * piecesPerPack, nil
}

// MarshalJSON preserves exact integers for consumers such as JavaScript.
func (q Quantity) MarshalJSON() ([]byte, error) {
	if q < 0 {
		return nil, ErrInvalidArgument
	}
	return []byte(strconv.Quote(strconv.FormatInt(int64(q), 10))), nil
}

// UnmarshalJSON accepts decimal strings only. Operation validation rejects zero.
// This is a scalar serialization contract, not an HTTP request parser.
func (q *Quantity) UnmarshalJSON(data []byte) error {
	if q == nil {
		return ErrInvalidArgument
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return ErrInvalidArgument
	}
	parsed, err := ParseBalance(text)
	if err != nil {
		return err
	}
	*q = parsed
	return nil
}

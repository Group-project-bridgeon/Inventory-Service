package stock

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestQuantityParsing(t *testing.T) {
	for _, tc := range []struct {
		text string
		want Quantity
	}{
		{"0", 0}, {"1", 1}, {"9007199254740993", 9007199254740993}, {"9223372036854775807", math.MaxInt64},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := ParseBalance(tc.text)
			if err != nil || got != tc.want {
				t.Fatalf("balance=%d err=%v", got, err)
			}
			got, err = ParsePositiveQuantity(tc.text)
			if tc.want == 0 {
				if !errors.Is(err, ErrInvalidArgument) {
					t.Fatalf("positive zero: %v", err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("positive=%d err=%v", got, err)
			}
		})
	}
	for _, text := range []string{"", " ", " 1", "1 ", "+1", "-1", "-9223372036854775808", "01", "00", "1.5", "2.0", "1e3", "0x10", "1_000", "١", "１", "1\n", "1\x00", "NaN"} {
		for _, parse := range []func(string) (Quantity, error){ParseBalance, ParsePositiveQuantity} {
			if _, err := parse(text); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("accepted malformed input %q: %v", text, err)
			}
		}
	}
	for _, text := range []string{"9223372036854775808", "18446744073709551615", "999999999999999999999999999999999"} {
		for _, parse := range []func(string) (Quantity, error){ParseBalance, ParsePositiveQuantity} {
			if _, err := parse(text); !errors.Is(err, ErrQuantityOverflow) {
				t.Errorf("overflow %q: %v", text, err)
			}
		}
	}
}

func TestQuantityArithmetic(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(Quantity, Quantity) (Quantity, error)
		a, b Quantity
		want Quantity
		err  error
	}{
		{"add first", AddPieces, 0, 1, 1, nil},
		{"add max", AddPieces, 0, math.MaxInt64, math.MaxInt64, nil},
		{"add to max", AddPieces, math.MaxInt64 - 1, 1, math.MaxInt64, nil},
		{"add overflow", AddPieces, math.MaxInt64, 1, 0, ErrQuantityOverflow},
		{"add large overflow", AddPieces, 1, math.MaxInt64, 0, ErrQuantityOverflow},
		{"subtract to zero", SubtractPieces, math.MaxInt64, math.MaxInt64, 0, nil},
		{"subtract", SubtractPieces, 10, 7, 3, nil},
		{"insufficient", SubtractPieces, 3, 7, 0, ErrInsufficientStock},
		{"empty issue", SubtractPieces, 0, 1, 0, ErrInsufficientStock},
		{"pack", PackToPieces, 2, 6, 12, nil},
		{"piece factor", PackToPieces, math.MaxInt64, 1, math.MaxInt64, nil},
		{"largest factor", PackToPieces, 1, math.MaxInt64, math.MaxInt64, nil},
		{"pack boundary", PackToPieces, math.MaxInt64 / 2, 2, math.MaxInt64 - 1, nil},
		{"pack overflow", PackToPieces, math.MaxInt64/2 + 1, 2, 0, ErrQuantityOverflow},
		{"pack large overflow", PackToPieces, math.MaxInt64, math.MaxInt64, 0, ErrQuantityOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.fn(tc.a, tc.b)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("got=%d err=%v want=%d err=%v", got, err, tc.want, tc.err)
			}
		})
	}
	for _, fn := range []func(Quantity, Quantity) (Quantity, error){AddPieces, SubtractPieces, PackToPieces} {
		for _, invalid := range []Quantity{-1, math.MinInt64, 0} {
			if _, err := fn(1, invalid); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("accepted invalid second operand %d: %v", invalid, err)
			}
			if invalid < 0 {
				if _, err := fn(invalid, 1); !errors.Is(err, ErrInvalidArgument) {
					t.Errorf("accepted negative first operand %d: %v", invalid, err)
				}
			}
		}
	}
	if _, err := PackToPieces(0, 1); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("accepted zero pack count")
	}
}

func TestQuantityJSON(t *testing.T) {
	for _, q := range []Quantity{0, 1, 9007199254740993, math.MaxInt64} {
		data, err := json.Marshal(q)
		if err != nil || len(data) < 2 || data[0] != '"' {
			t.Fatalf("decimal string encoding: %s %v", data, err)
		}
		var decoded Quantity
		if err := json.Unmarshal(data, &decoded); err != nil || decoded != q {
			t.Fatalf("roundtrip %d: %d %v", q, decoded, err)
		}
	}
	for _, data := range []string{`0`, `1`, `1.5`, `null`, `true`, `{}`, `[]`, `""`, `"01"`, `"-1"`, `"2.0"`, `"1e3"`, `"9223372036854775808"`, `"1" "2"`} {
		q := Quantity(7)
		if err := json.Unmarshal([]byte(data), &q); err == nil || q != 7 {
			t.Errorf("invalid JSON %s modified quantity=%d err=%v", data, q, err)
		}
	}
	for _, q := range []Quantity{-1, math.MinInt64} {
		if _, err := json.Marshal(q); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("negative JSON: %v", err)
		}
	}
	var missing *Quantity
	if err := missing.UnmarshalJSON([]byte(`"1"`)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("nil receiver accepted")
	}
}

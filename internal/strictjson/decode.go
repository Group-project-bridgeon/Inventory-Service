// Package strictjson provides bounded-transport JSON structural validation.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid JSON contract")

func Decode(data []byte, target any) error {
	if !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return ErrInvalid
		}
		token, err := d.Token()
		if err != nil {
			return ErrInvalid
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return ErrInvalid
				}
				s, ok := key.(string)
				if !ok || s != strings.ToLower(s) || seen[s] {
					return ErrInvalid
				}
				seen[s] = true
				if walk(depth+1) != nil {
					return ErrInvalid
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return ErrInvalid
			}
		case '[':
			for d.More() {
				if walk(depth+1) != nil {
					return ErrInvalid
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		return nil
	}
	if walk(0) != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	decode := json.NewDecoder(bytes.NewReader(data))
	decode.DisallowUnknownFields()
	if decode.Decode(target) != nil {
		return ErrInvalid
	}
	return nil
}

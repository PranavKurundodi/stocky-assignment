// Package money keeps Stocky's decimal rules in one place: INR amounts and
// prices have 4 decimal places, share quantities have 6, and both travel as
// JSON strings, never as JSON numbers or float64.
package money

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/shopspring/decimal"
)

const (
	INRPlaces      = 4
	QuantityPlaces = 6
)

// INR formats a price or rupee amount with exactly 4 decimals, e.g. "2000.0000".
func INR(d decimal.Decimal) string { return d.StringFixed(INRPlaces) }

// Qty formats a share quantity with exactly 6 decimals, e.g. "2.500000".
func Qty(d decimal.Decimal) string { return d.StringFixed(QuantityPlaces) }

// ParseQuantity validates a share quantity taken straight from a JSON body.
// It is a json.RawMessage so we can tell a JSON string ("2.5") from a JSON
// number (2.5): numbers are rejected because many clients hold them as
// binary floats, which can change the value before it reaches us.
// Quantities with more than 6 decimals are rejected, never rounded.
func ParseQuantity(raw json.RawMessage) (decimal.Decimal, error) {
	return ParsePositive(raw, "quantity", QuantityPlaces)
}

// ParsePositive applies the same rules to any field and precision. It is
// also used for corporate action ratios.
func ParsePositive(raw json.RawMessage, field string, places int32) (decimal.Decimal, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return decimal.Zero, errors.New(field + " is required")
	}
	if raw[0] != '"' {
		return decimal.Zero, errors.New(field + ` must be a JSON string, e.g. "2.5"`)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return decimal.Zero, errors.New(field + " must be a JSON string")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, errors.New(field + " is not a valid decimal number")
	}
	if !d.IsPositive() {
		return decimal.Zero, errors.New(field + " must be greater than zero")
	}
	// If truncating changes the value, the client sent more precision than
	// we store. Trailing zeros ("2.50000000") do not change it and are fine.
	if !d.Equal(d.Truncate(places)) {
		return decimal.Zero, errors.New(field + " has too many decimal places")
	}
	return d, nil
}

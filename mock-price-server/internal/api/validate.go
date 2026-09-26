package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/shopspring/decimal"

	"github.com/PranavKurundodi/stocky-assignment/mock-price-server/internal/market"
)

// symbolPattern allows real NSE symbols such as M&M and BAJAJ-AUTO.
var symbolPattern = regexp.MustCompile(`^[A-Z0-9&-]{1,20}$`)

// ValidateSymbol normalizes a symbol and checks it against symbolPattern.
// It returns the normalized form so callers do not normalize twice.
func ValidateSymbol(s string) (string, error) {
	sym := market.NormalizeSymbol(s)
	if !symbolPattern.MatchString(sym) {
		return "", errors.New("symbol must be 1-20 characters of A-Z, 0-9, & or -")
	}
	return sym, nil
}

// ParsePrice validates a price taken straight from the request body. It is a
// json.RawMessage rather than a string so we can see whether the client sent
// a JSON string ("1200.00") or a JSON number (1200). Numbers are rejected
// because many JSON clients turn them into binary floats, which can silently
// change the value before it ever reaches us.
func ParsePrice(raw json.RawMessage) (decimal.Decimal, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return decimal.Zero, errors.New("price is required")
	}
	if raw[0] != '"' {
		return decimal.Zero, errors.New(`price must be a JSON string, e.g. "1200.0000"`)
	}

	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return decimal.Zero, errors.New("price must be a JSON string")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, errors.New("price is not a valid decimal number")
	}
	if !d.IsPositive() {
		return decimal.Zero, errors.New("price must be greater than zero")
	}
	// Reject rather than round: if truncating to 4 places changes the value,
	// the client sent more precision than we store. Trailing zeros such as
	// "1000.000000" are fine because they do not change the value.
	if !d.Equal(d.Truncate(market.PricePlaces)) {
		return decimal.Zero, errors.New("price must have at most 4 decimal places")
	}
	return d, nil
}

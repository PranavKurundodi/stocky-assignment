package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParsePriceRejects(t *testing.T) {
	bad := []string{
		`"0"`,
		`"-5"`,
		`"abc"`,
		`"1.23456"`,
		`1200`, // a JSON number, not a string
		`null`,
		``,
	}
	for _, raw := range bad {
		if d, err := ParsePrice(json.RawMessage(raw)); err == nil {
			t.Errorf("ParsePrice(%s) = %s, want error", raw, d)
		}
	}
}

func TestParsePriceAccepts(t *testing.T) {
	good := map[string]string{
		`"1200"`:      "1200.0000",
		`"1000.0000"`: "1000.0000",
		`"0.0001"`:    "0.0001",
		`"12.340000"`: "12.3400", // extra trailing zeros do not add precision
		` "600.5" `:   "600.5000",
	}
	for raw, want := range good {
		d, err := ParsePrice(json.RawMessage(raw))
		if err != nil {
			t.Errorf("ParsePrice(%s) error: %v", raw, err)
			continue
		}
		if got := d.StringFixed(4); got != want {
			t.Errorf("ParsePrice(%s) = %s, want %s", raw, got, want)
		}
	}
}

func TestValidateSymbol(t *testing.T) {
	good := map[string]string{
		"M&M":        "M&M",
		"BAJAJ-AUTO": "BAJAJ-AUTO",
		" wipro ":    "WIPRO",
	}
	for in, want := range good {
		got, err := ValidateSymbol(in)
		if err != nil || got != want {
			t.Errorf("ValidateSymbol(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := []string{"", "   ", "TC S", strings.Repeat("A", 21), "TCS.NS"}
	for _, in := range bad {
		if got, err := ValidateSymbol(in); err == nil {
			t.Errorf("ValidateSymbol(%q) = %q, want error", in, got)
		}
	}
}

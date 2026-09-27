package money

import (
	"encoding/json"
	"testing"
)

func TestParseQuantity(t *testing.T) {
	good := map[string]string{
		`"2"`:          "2.000000",
		`"0.000001"`:   "0.000001",
		`"1.50000000"`: "1.500000", // trailing zeros add no precision
	}
	for raw, want := range good {
		d, err := ParseQuantity(json.RawMessage(raw))
		if err != nil || Qty(d) != want {
			t.Errorf("ParseQuantity(%s) = %s, %v; want %s", raw, Qty(d), err, want)
		}
	}

	bad := []string{`2`, `"0"`, `"-1"`, `"abc"`, `"1.0000001"`, `null`, ``}
	for _, raw := range bad {
		if d, err := ParseQuantity(json.RawMessage(raw)); err == nil {
			t.Errorf("ParseQuantity(%s) = %s, want error", raw, d)
		}
	}
}

package fees

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// want lists expected components in order: brokerage, stt, exchange, sebi,
// stamp_duty, gst, total.
func check(t *testing.T, name string, b Breakdown, want [7]string) {
	t.Helper()
	got := [7]string{
		b.Brokerage.StringFixed(4), b.STT.StringFixed(4), b.Exchange.StringFixed(4),
		b.SEBI.StringFixed(4), b.StampDuty.StringFixed(4), b.GST.StringFixed(4),
		b.Total.StringFixed(4),
	}
	labels := [7]string{"brokerage", "stt", "exchange", "sebi", "stamp_duty", "gst", "total"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: %s = %s, want %s", name, labels[i], got[i], want[i])
		}
	}
}

func TestSpecExample2000(t *testing.T) {
	b := DefaultRates(decimal.Zero).Calculate(d("2000.0000"))
	check(t, "2000.0000", b, [7]string{"0.0000", "2.0000", "0.0594", "0.0020", "0.3000", "0.0111", "2.3725"})
}

func TestZeroVersusFlatBrokerage(t *testing.T) {
	zero := DefaultRates(decimal.Zero).Calculate(d("2000.0000"))
	flat := DefaultRates(d("20")).Calculate(d("2000.0000"))

	// Brokerage adds itself plus 18% GST on it:
	// GST = 0.18 * (20.0000 + 0.0594 + 0.0020) = 3.611052 -> 3.6111
	check(t, "flat 20", flat, [7]string{"20.0000", "2.0000", "0.0594", "0.0020", "0.3000", "3.6111", "25.9725"})

	// Taxes on the trade value do not depend on brokerage.
	if !zero.STT.Equal(flat.STT) || !zero.StampDuty.Equal(flat.StampDuty) {
		t.Error("STT and stamp duty should not change with brokerage")
	}
}

func TestEachComponentRoundsHalfUp(t *testing.T) {
	// For 1 INR: stamp duty is exactly 0.00015, which rounds up to 0.0002.
	// Exchange (0.0000297) and SEBI (0.000001) round down to zero, so GST is
	// zero too.
	b := DefaultRates(decimal.Zero).Calculate(d("1"))
	check(t, "1 INR", b, [7]string{"0.0000", "0.0010", "0.0000", "0.0000", "0.0002", "0.0000", "0.0012"})
}

func TestTotalIsSumOfRoundedComponents(t *testing.T) {
	for _, v := range []string{"0.01", "999.9999", "1234.5678", "123456789.1234"} {
		b := DefaultRates(d("15.5")).Calculate(d(v))
		sum := b.Brokerage.Add(b.STT).Add(b.Exchange).Add(b.SEBI).Add(b.StampDuty).Add(b.GST)
		if !sum.Equal(b.Total) {
			t.Errorf("%s: total %s != sum of components %s", v, b.Total, sum)
		}
		for _, c := range []decimal.Decimal{b.Brokerage, b.STT, b.Exchange, b.SEBI, b.StampDuty, b.GST, b.Total} {
			if c.Exponent() < -Places {
				t.Errorf("%s: component %s has more than 4 decimal places", v, c)
			}
		}
	}
}

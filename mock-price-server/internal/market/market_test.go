package market

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// fakeClock is a settable clock so tests can check AsOf exactly.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func newTestMarket(up bool) (*Market, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 9, 26, 5, 0, 0, 0, time.UTC)}
	m := New(BasePrices(), Options{
		Flip: func() bool { return up },
		Now:  clock.Now,
	})
	return m, clock
}

func mustGet(t *testing.T, m *Market, sym string) Quote {
	t.Helper()
	q, err := m.Get(sym)
	if err != nil {
		t.Fatalf("Get(%q): %v", sym, err)
	}
	return q
}

func TestStartsAtBasePrices(t *testing.T) {
	m, _ := newTestMarket(true)
	for sym, want := range BasePrices() {
		if got := mustGet(t, m, sym).Price; !got.Equal(want) {
			t.Errorf("%s = %s, want %s", sym, got, want)
		}
	}
}

func TestTickUpAndDown(t *testing.T) {
	cases := []struct {
		up   bool
		want string
	}{
		{true, "1212.0000"},
		{false, "1188.0000"},
	}
	for _, tc := range cases {
		m, _ := newTestMarket(tc.up)
		m.Tick()
		if got := mustGet(t, m, "RELIANCE").Price.StringFixed(PricePlaces); got != tc.want {
			t.Errorf("up=%v: RELIANCE = %s, want %s", tc.up, got, tc.want)
		}
	}
}

func TestTickUpdatesAsOf(t *testing.T) {
	m, clock := newTestMarket(true)
	clock.t = clock.t.Add(time.Minute)
	m.Tick()
	if got := mustGet(t, m, "TCS").AsOf; !got.Equal(clock.t) {
		t.Errorf("AsOf = %v, want %v", got, clock.t)
	}
}

func TestLookupIsCaseAndWhitespaceInsensitive(t *testing.T) {
	m, _ := newTestMarket(true)
	for _, sym := range []string{"reliance", "  Reliance ", "RELIANCE\t"} {
		if q := mustGet(t, m, sym); q.Symbol != "RELIANCE" {
			t.Errorf("Get(%q).Symbol = %q", sym, q.Symbol)
		}
	}
}

func TestAddThenGetAndDuplicate(t *testing.T) {
	m, _ := newTestMarket(true)
	if _, err := m.Add("wipro", decimal.NewFromInt(500)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := mustGet(t, m, "WIPRO").Price; !got.Equal(decimal.NewFromInt(500)) {
		t.Errorf("WIPRO = %s, want 500", got)
	}
	if _, err := m.Add(" WIPRO ", decimal.NewFromInt(1)); !errors.Is(err, ErrSymbolExists) {
		t.Errorf("duplicate Add err = %v, want ErrSymbolExists", err)
	}
}

func TestRemoveThenGet(t *testing.T) {
	m, _ := newTestMarket(true)
	if err := m.Remove("HDFCBANK"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := m.Get("HDFCBANK"); !errors.Is(err, ErrUnknownSymbol) {
		t.Errorf("Get after Remove err = %v, want ErrUnknownSymbol", err)
	}
	if err := m.Remove("HDFCBANK"); !errors.Is(err, ErrUnknownSymbol) {
		t.Errorf("second Remove err = %v, want ErrUnknownSymbol", err)
	}
}

func TestSetPriceChangesPriceAndAsOf(t *testing.T) {
	m, clock := newTestMarket(true)
	clock.t = clock.t.Add(5 * time.Minute)

	old, updated, err := m.SetPrice("reliance", decimal.NewFromInt(600))
	if err != nil {
		t.Fatalf("SetPrice: %v", err)
	}
	if !old.Price.Equal(decimal.NewFromInt(1200)) {
		t.Errorf("old = %s, want 1200", old.Price)
	}
	q := mustGet(t, m, "RELIANCE")
	if !q.Price.Equal(decimal.NewFromInt(600)) || !q.AsOf.Equal(clock.t) || q != updated {
		t.Errorf("after SetPrice got %+v", q)
	}

	if _, _, err := m.SetPrice("FAKECORP", decimal.NewFromInt(1)); !errors.Is(err, ErrUnknownSymbol) {
		t.Errorf("SetPrice unknown err = %v, want ErrUnknownSymbol", err)
	}
}

func TestTickAppliesToAddedAndSkipsRemoved(t *testing.T) {
	m, _ := newTestMarket(true)
	if _, err := m.Add("WIPRO", decimal.NewFromInt(500)); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("TCS"); err != nil {
		t.Fatal(err)
	}
	m.Tick()

	if got := mustGet(t, m, "WIPRO").Price.StringFixed(PricePlaces); got != "505.0000" {
		t.Errorf("WIPRO after tick = %s, want 505.0000", got)
	}
	if _, err := m.Get("TCS"); !errors.Is(err, ErrUnknownSymbol) {
		t.Errorf("TCS came back after tick: err = %v", err)
	}
	if n := len(m.All()); n != 5 {
		t.Errorf("len(All) = %d, want 5", n)
	}
}

func TestAllIsSorted(t *testing.T) {
	m, _ := newTestMarket(true)
	want := []string{"HDFCBANK", "ICICIBANK", "INFY", "RELIANCE", "TCS"}
	all := m.All()
	if len(all) != len(want) {
		t.Fatalf("len(All) = %d, want %d", len(all), len(want))
	}
	for i, q := range all {
		if q.Symbol != want[i] {
			t.Errorf("All()[%d] = %s, want %s", i, q.Symbol, want[i])
		}
	}
}

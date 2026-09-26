// Package market holds the in-memory stock prices of the mock vendor and
// moves them randomly on a timer.
package market

import (
	"context"
	"errors"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

// PricePlaces is the number of decimal places every price is kept at.
const PricePlaces = 4

var (
	ErrUnknownSymbol = errors.New("unknown stock symbol")
	ErrSymbolExists  = errors.New("stock symbol already exists")
)

var (
	upFactor   = decimal.RequireFromString("1.01")
	downFactor = decimal.RequireFromString("0.99")
)

// BasePrices returns the prices every stock starts at when the process boots.
// It returns a fresh map each call so callers cannot mutate a shared default.
func BasePrices() map[string]decimal.Decimal {
	return map[string]decimal.Decimal{
		"RELIANCE":  decimal.NewFromInt(1200),
		"TCS":       decimal.NewFromInt(2000),
		"INFY":      decimal.NewFromInt(1000),
		"ICICIBANK": decimal.NewFromInt(1400),
		"HDFCBANK":  decimal.NewFromInt(750),
	}
}

// Quote is the current price of one stock and the instant it last changed.
type Quote struct {
	Symbol string
	Price  decimal.Decimal
	AsOf   time.Time
}

// Options configures a Market. Zero values fall back to sensible defaults.
type Options struct {
	// Interval is how often Run ticks. Defaults to one minute.
	Interval time.Duration
	// Flip decides the direction of one stock's move: true means +1%, false
	// means -1%. Tests inject a fixed function to make ticks deterministic.
	Flip func() bool
	// Now is the clock used for AsOf. Tests inject a fixed clock.
	Now func() time.Time
	// Log receives tick debug lines. Defaults to the standard logrus logger.
	Log logrus.FieldLogger
}

// Market is a concurrency-safe set of quotes keyed by normalized symbol.
type Market struct {
	mu       sync.RWMutex
	quotes   map[string]Quote
	interval time.Duration
	flip     func() bool
	now      func() time.Time
	log      logrus.FieldLogger
}

// New builds a market seeded with the given base prices, all stamped with the
// current time from opts.Now.
func New(base map[string]decimal.Decimal, opts Options) *Market {
	if opts.Interval <= 0 {
		opts.Interval = time.Minute
	}
	if opts.Flip == nil {
		opts.Flip = func() bool { return rand.IntN(2) == 0 }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = logrus.StandardLogger()
	}

	m := &Market{
		quotes:   make(map[string]Quote, len(base)),
		interval: opts.Interval,
		flip:     opts.Flip,
		now:      opts.Now,
		log:      opts.Log,
	}
	now := m.now()
	for sym, price := range base {
		sym = NormalizeSymbol(sym)
		m.quotes[sym] = Quote{Symbol: sym, Price: price.Round(PricePlaces), AsOf: now}
	}
	return m
}

// NormalizeSymbol trims whitespace and uppercases, so " reliance " and
// "RELIANCE" refer to the same stock.
func NormalizeSymbol(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// Interval reports how often Run ticks.
func (m *Market) Interval() time.Duration {
	return m.interval
}

// Get returns the quote for one symbol.
func (m *Market) Get(symbol string) (Quote, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	q, ok := m.quotes[NormalizeSymbol(symbol)]
	if !ok {
		return Quote{}, ErrUnknownSymbol
	}
	return q, nil
}

// All returns every quote sorted by symbol.
func (m *Market) All() []Quote {
	m.mu.RLock()
	out := make([]Quote, 0, len(m.quotes))
	for _, q := range m.quotes {
		out = append(out, q)
	}
	m.mu.RUnlock()

	// Map iteration order is random in Go, so sort for stable responses.
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// SetPrice overrides the price of an existing stock. It returns the previous
// quote (for logging) and the new one. Ticking continues from the new price.
func (m *Market) SetPrice(symbol string, price decimal.Decimal) (old, updated Quote, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sym := NormalizeSymbol(symbol)
	old, ok := m.quotes[sym]
	if !ok {
		return Quote{}, Quote{}, ErrUnknownSymbol
	}
	updated = Quote{Symbol: sym, Price: price.Round(PricePlaces), AsOf: m.now()}
	m.quotes[sym] = updated
	return old, updated, nil
}

// Add lists a new stock. It will tick like the others from the next tick on.
func (m *Market) Add(symbol string, price decimal.Decimal) (Quote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sym := NormalizeSymbol(symbol)
	if _, ok := m.quotes[sym]; ok {
		return Quote{}, ErrSymbolExists
	}
	q := Quote{Symbol: sym, Price: price.Round(PricePlaces), AsOf: m.now()}
	m.quotes[sym] = q
	return q, nil
}

// Remove delists a stock so it is no longer quoted.
func (m *Market) Remove(symbol string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sym := NormalizeSymbol(symbol)
	if _, ok := m.quotes[sym]; !ok {
		return ErrUnknownSymbol
	}
	delete(m.quotes, sym)
	return nil
}

// Tick moves every stock by exactly +1% or -1%, chosen independently per
// stock, rounded to 4 decimal places.
func (m *Market) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	for sym, q := range m.quotes {
		factor := downFactor
		if m.flip() {
			factor = upFactor
		}
		newPrice := q.Price.Mul(factor).Round(PricePlaces)
		m.log.WithFields(logrus.Fields{
			"symbol": sym,
			"old":    q.Price.StringFixed(PricePlaces),
			"new":    newPrice.StringFixed(PricePlaces),
		}).Debug("price ticked")
		m.quotes[sym] = Quote{Symbol: sym, Price: newPrice, AsOf: now}
	}
}

// Run ticks every Interval until ctx is cancelled. It blocks, so start it
// with `go m.Run(ctx)`.
func (m *Market) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Tick()
		}
	}
}

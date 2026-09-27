// Integration tests for the read endpoints' logic, against Postgres.
package portfolio_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/fees"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/portfolio"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/rewards"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/testdb"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/users"
)

func TestMain(m *testing.M) { testdb.Main(m) }

var ctx = context.Background()

// ist builds a time in IST.
func ist(day, hour, min, sec int) time.Time {
	return time.Date(2026, 9, day, hour, min, sec, 0, timeutil.IST)
}

type env struct {
	pool      *pgxpool.Pool
	rewards   *rewards.Service
	portfolio *portfolio.Service
	now       time.Time
}

// setup creates user_1 and both services with a fixed clock.
func setup(t *testing.T, now time.Time) *env {
	t.Helper()
	pool := testdb.Pool(t)
	exec(t, pool, `INSERT INTO users (id, name) VALUES ('user_1', 'One')`)
	log, _ := logtest.NewNullLogger()

	e := &env{pool: pool, now: now}
	e.rewards = rewards.NewService(pool, fees.DefaultRates(decimal.Zero), 2*time.Hour, log)
	e.rewards.Now = func() time.Time { return e.now }
	e.portfolio = portfolio.NewService(pool, 2*time.Hour)
	e.portfolio.Now = func() time.Time { return e.now }
	return e
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func price(t *testing.T, e *env, symbol, p string, asOf time.Time) {
	t.Helper()
	exec(t, e.pool, `INSERT INTO stock_prices (symbol, price, as_of, fetched_at) VALUES ($1, $2, $3, $3)`,
		symbol, decimal.RequireFromString(p), asOf)
}

// reward creates a reward through the real service (fees, ledger and all).
// A price must already exist within 2h before at.
func reward(t *testing.T, e *env, key, symbol, qty string, at time.Time) rewards.Reward {
	t.Helper()
	ts := at.Format(time.RFC3339)
	in, err := rewards.Prepare(key, rewards.Request{
		UserID: "user_1", Symbol: symbol, Quantity: json.RawMessage(`"` + qty + `"`), Reason: "OTHER", RewardedAt: &ts,
	}, e.now)
	if err != nil {
		t.Fatal(err)
	}
	rw, _, err := e.rewards.Create(ctx, in)
	if err != nil {
		t.Fatalf("create reward %s: %v", key, err)
	}
	return rw
}

// grant books shares straight into the ledger, bypassing rewards. Used to
// set up holdings a reward could not create, such as a symbol with no price.
func grant(t *testing.T, e *env, symbol, qty string, at time.Time) {
	t.Helper()
	err := db.WithTx(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := ledger.Post(ctx, tx, ledger.Transaction{
			Type: ledger.TypeMerger, EffectiveAt: at,
			Entries: []ledger.Entry{
				ledger.Dr(ledger.UserStock, symbol, decimal.RequireFromString(qty)).ForUser("user_1"),
				ledger.Cr(ledger.CorporateAction, symbol, decimal.RequireFromString(qty)),
			},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTodayRespectsISTDayBoundaries(t *testing.T) {
	e := setup(t, ist(26, 23, 59, 59))
	price(t, e, "TCS", "100.0000", ist(25, 23, 0, 0))

	reward(t, e, "prev-day", "TCS", "1", ist(25, 23, 59, 59)) // yesterday, 1s before midnight
	reward(t, e, "midnight", "TCS", "2", ist(26, 0, 0, 0))    // exactly midnight: today
	reward(t, e, "early", "TCS", "3", ist(26, 0, 30, 0))      // 00:30 IST = 19:00 UTC on the 25th
	price(t, e, "TCS", "100.0000", ist(26, 23, 0, 0))
	reward(t, e, "late", "TCS", "4", ist(26, 23, 59, 59)) // last second of today

	date, list, err := e.portfolio.Today(ctx, "user_1")
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-09-26" {
		t.Errorf("date = %s", date)
	}
	var qtys []string
	for _, r := range list {
		qtys = append(qtys, r.Quantity.String())
	}
	// Newest first; the 23:59:59 reward from the 25th is excluded.
	want := []string{"4", "3", "2"}
	if len(qtys) != len(want) {
		t.Fatalf("got %v, want %v", qtys, want)
	}
	for i := range want {
		if qtys[i] != want[i] {
			t.Errorf("got %v, want %v", qtys, want)
			break
		}
	}
}

func TestPortfolioValuesHoldingsAndFlagsProblems(t *testing.T) {
	e := setup(t, ist(26, 12, 0, 0))
	price(t, e, "TCS", "2000.0000", ist(26, 11, 50, 0))
	price(t, e, "HDFCBANK", "750.0000", ist(26, 11, 0, 0))

	reward(t, e, "k1", "TCS", "1.5", ist(26, 11, 55, 0))
	reward(t, e, "k2", "HDFCBANK", "2", ist(26, 11, 5, 0))
	exec(t, e.pool, `UPDATE stocks SET status = 'DELISTED' WHERE symbol = 'HDFCBANK'`)
	exec(t, e.pool, `INSERT INTO stocks (symbol, status) VALUES ('WIPRO', 'ACTIVE')`)
	grant(t, e, "WIPRO", "1", ist(26, 11, 0, 0)) // held, but no WIPRO price exists

	e.now = ist(26, 14, 0, 0) // HDFCBANK's 11:00 price is now 3h old: stale
	p, err := e.portfolio.Portfolio(ctx, "user_1")
	if err != nil {
		t.Fatal(err)
	}

	if len(p.Holdings) != 3 {
		t.Fatalf("holdings = %+v", p.Holdings)
	}
	hd, tcs, wipro := p.Holdings[0], p.Holdings[1], p.Holdings[2] // sorted by symbol
	if hd.Symbol != "HDFCBANK" || !hd.Delisted || !hd.IsStale || hd.Value.StringFixed(4) != "1500.0000" {
		t.Errorf("HDFCBANK row = %+v", hd)
	}
	if tcs.Symbol != "TCS" || tcs.Delisted || !tcs.IsStale || tcs.Value.StringFixed(4) != "3000.0000" {
		t.Errorf("TCS row = %+v", tcs) // 11:50 price is 2h10m old at 14:00
	}
	if wipro.Symbol != "WIPRO" || wipro.Price != nil || wipro.Value != nil {
		t.Errorf("WIPRO row = %+v, want nil price and value", wipro)
	}
	if p.Total.StringFixed(4) != "4500.0000" {
		t.Errorf("total = %s, want 4500.0000 (WIPRO excluded)", p.Total)
	}
	if len(p.MissingPrices) != 1 || p.MissingPrices[0] != "WIPRO" {
		t.Errorf("missing = %v", p.MissingPrices)
	}
	if !p.AnyStale || !p.OldestAsOf.Equal(ist(26, 11, 0, 0)) {
		t.Errorf("stale = %v, oldest = %v", p.AnyStale, p.OldestAsOf)
	}
}

func TestPortfolioRoundsOnceAtTheEnd(t *testing.T) {
	e := setup(t, ist(26, 12, 0, 0))
	price(t, e, "TCS", "1.0000", ist(26, 11, 0, 0))
	price(t, e, "INFY", "1.0000", ist(26, 11, 0, 0))
	grant(t, e, "TCS", "1.00004", ist(26, 11, 0, 0))
	grant(t, e, "INFY", "1.00004", ist(26, 11, 0, 0))

	p, err := e.portfolio.Portfolio(ctx, "user_1")
	if err != nil {
		t.Fatal(err)
	}
	// Each row is 1.00004 -> shown as 1.0000. Summing the rounded rows would
	// give 2.0000; summing exactly and rounding once gives 2.00008 -> 2.0001.
	if p.Holdings[0].Value.StringFixed(4) != "1.0000" || p.Total.StringFixed(4) != "2.0001" {
		t.Errorf("row = %s, total = %s", p.Holdings[0].Value, p.Total)
	}
}

func TestStatsCountsOnlyTodaysActiveRewards(t *testing.T) {
	e := setup(t, ist(26, 12, 0, 0))
	price(t, e, "TCS", "100.0000", ist(25, 11, 0, 0))
	price(t, e, "TCS", "100.0000", ist(26, 11, 0, 0))
	reward(t, e, "yesterday", "TCS", "5", ist(25, 11, 30, 0))
	reward(t, e, "today-1", "TCS", "1", ist(26, 11, 30, 0))
	reward(t, e, "today-2", "TCS", "0.5", ist(26, 11, 45, 0))

	st, err := e.portfolio.Stats(ctx, "user_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.TodaySharesBySymbol) != 1 || st.TodaySharesBySymbol["TCS"].String() != "1.5" {
		t.Errorf("today = %v, want TCS 1.5", st.TodaySharesBySymbol)
	}
	if st.Portfolio.Total.StringFixed(4) != "650.0000" || st.Portfolio.AnyStale {
		t.Errorf("portfolio total = %s, stale = %v", st.Portfolio.Total, st.Portfolio.AnyStale)
	}
}

func TestHistoryValuesEachPastDay(t *testing.T) {
	e := setup(t, ist(26, 12, 0, 0))

	// TCS: rewarded Sep 23 at 100; closes 110 on Sep 23 and 120 on Sep 24.
	price(t, e, "TCS", "100.0000", ist(23, 9, 0, 0))
	reward(t, e, "tcs", "TCS", "2", ist(23, 10, 0, 0))
	price(t, e, "TCS", "110.0000", ist(23, 15, 0, 0))
	price(t, e, "TCS", "120.0000", ist(24, 15, 0, 0))
	// INFY: rewarded Sep 24 at 50; closes 55 on Sep 24.
	price(t, e, "INFY", "50.0000", ist(24, 9, 0, 0))
	reward(t, e, "infy", "INFY", "1", ist(24, 10, 0, 0))
	price(t, e, "INFY", "55.0000", ist(24, 15, 0, 0))
	// WIPRO arrives on Sep 25 with no price until today: Sep 25 has a gap.
	exec(t, e.pool, `INSERT INTO stocks (symbol, status) VALUES ('WIPRO', 'ACTIVE')`)
	grant(t, e, "WIPRO", "1", ist(25, 10, 0, 0))
	price(t, e, "WIPRO", "500.0000", ist(26, 9, 0, 0))
	// A price exactly at 00:00 Sep 24 belongs to Sep 24, not Sep 23's close.
	price(t, e, "TCS", "999.0000", ist(24, 0, 0, 0))

	hist, err := e.portfolio.History(ctx, "user_1")
	if err != nil {
		t.Fatal(err)
	}

	type row struct{ date, value, missing string }
	var got []row
	for _, d := range hist {
		r := row{date: d.Date, value: "null"}
		if d.Value != nil {
			r.value = d.Value.StringFixed(4)
		}
		for _, m := range d.MissingPrices {
			r.missing += m
		}
		got = append(got, r)
	}
	want := []row{
		{"2026-09-23", "220.0000", ""}, // 2 x 110
		{"2026-09-24", "295.0000", ""}, // 2 x 120 + 1 x 55
		{"2026-09-25", "null", "WIPRO"},
		// Sep 26 is today: excluded.
	}
	if len(got) != len(want) {
		t.Fatalf("history = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("day %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestHistoryEmptyAndUnknownUser(t *testing.T) {
	e := setup(t, ist(26, 12, 0, 0))

	hist, err := e.portfolio.History(ctx, "user_1")
	if err != nil || len(hist) != 0 {
		t.Errorf("history for user with no rewards = %v, err = %v", hist, err)
	}

	// A reward made today has no past days yet.
	price(t, e, "TCS", "100.0000", ist(26, 11, 0, 0))
	reward(t, e, "today", "TCS", "1", ist(26, 11, 30, 0))
	if hist, err := e.portfolio.History(ctx, "user_1"); err != nil || len(hist) != 0 {
		t.Errorf("history with only today's reward = %v, err = %v", hist, err)
	}

	for name, call := range map[string]func() error{
		"today":     func() error { _, _, err := e.portfolio.Today(ctx, "ghost"); return err },
		"portfolio": func() error { _, err := e.portfolio.Portfolio(ctx, "ghost"); return err },
		"stats":     func() error { _, err := e.portfolio.Stats(ctx, "ghost"); return err },
		"history":   func() error { _, err := e.portfolio.History(ctx, "ghost"); return err },
	} {
		if err := call(); !errors.Is(err, users.ErrNotFound) {
			t.Errorf("%s for unknown user: err = %v", name, err)
		}
	}
}

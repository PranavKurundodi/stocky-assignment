// Integration tests for the price job and the price queries. They need
// TEST_DATABASE_URL and skip without it.
package pricing_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/pricing"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/testdb"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
)

func TestMain(m *testing.M) { testdb.Main(m) }

var ctx = context.Background()

// newJob builds a job whose client talks to url with instant retries, and
// returns the log hook so tests can inspect warnings.
func newJob(pool *pgxpool.Pool, url string) (*pricing.Job, *logtest.Hook) {
	log, hook := logtest.NewNullLogger()
	c := pricing.NewClient(url, time.Second, log)
	c.Backoff = []time.Duration{0, 0, 0}
	return pricing.NewJob(c, pool, time.Hour, log), hook
}

// vendor serves body with status 200.
func vendor(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func countPrices(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stock_prices`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The vendor quotes RELIANCE plus a symbol we have never seen (WIPRO), and
// leaves out TCS, INFY, ICICIBANK and HDFCBANK.
const partialFeed = `{"prices":[
	{"symbol":"RELIANCE","price":"1200.0000","as_of":"2026-09-26T10:31:00+05:30"},
	{"symbol":"WIPRO","price":"500.0000","as_of":"2026-09-26T10:31:00+05:30"}]}`

func TestJobRegistersNewSymbolAndOnlyLogsMissing(t *testing.T) {
	pool := testdb.Pool(t)
	job, hook := newJob(pool, vendor(t, partialFeed))

	sum, err := job.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Inserted != 2 || sum.Skipped != 0 {
		t.Errorf("inserted=%d skipped=%d, want 2 and 0", sum.Inserted, sum.Skipped)
	}
	if len(sum.NewSymbols) != 1 || sum.NewSymbols[0] != "WIPRO" {
		t.Errorf("new symbols = %v, want [WIPRO]", sum.NewSymbols)
	}
	if len(sum.MissingSymbols) != 4 {
		t.Errorf("missing symbols = %v, want 4", sum.MissingSymbols)
	}

	// WIPRO is now an ACTIVE stock; the missing ones stay ACTIVE.
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM stocks WHERE symbol = 'WIPRO'`).Scan(&status); err != nil || status != "ACTIVE" {
		t.Errorf("WIPRO status = %q, err = %v", status, err)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stocks WHERE status = 'ACTIVE'`).Scan(&active); err != nil || active != 6 {
		t.Errorf("active stocks = %d, want 6 (no auto-delisting)", active)
	}

	warned := 0
	for _, e := range hook.AllEntries() {
		if e.Level == logrus.WarnLevel && e.Message == "active stock missing from price feed" {
			warned++
		}
	}
	if warned != 4 {
		t.Errorf("missing-symbol warnings = %d, want 4", warned)
	}

	// Same feed again: every quote is a duplicate (same symbol and as_of).
	sum, err = job.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Inserted != 0 || sum.Skipped != 2 || countPrices(t, pool) != 2 {
		t.Errorf("second run inserted=%d skipped=%d rows=%d", sum.Inserted, sum.Skipped, countPrices(t, pool))
	}
}

func TestJobFailedFetchWritesNothing(t *testing.T) {
	pool := testdb.Pool(t)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := "http://" + l.Addr().String()
	l.Close()

	for name, url := range map[string]string{"503": down.URL, "closed port": closedPort} {
		job, _ := newJob(pool, url)
		if _, err := job.RunOnce(ctx); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if n := countPrices(t, pool); n != 0 {
			t.Errorf("%s: %d price rows written, want 0", name, n)
		}
	}
}

// insertPrice stores one price directly.
func insertPrice(t *testing.T, pool *pgxpool.Pool, symbol, price string, asOf time.Time) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`INSERT INTO stock_prices (symbol, price, as_of, fetched_at) VALUES ($1, $2, $3, now())`,
		symbol, decimal.RequireFromString(price), asOf)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPriceQueries(t *testing.T) {
	pool := testdb.Pool(t)
	ist := timeutil.IST
	// Sep 26: 10:00 and 23:59 IST. Sep 27: 00:00 IST exactly (belongs to the 27th).
	insertPrice(t, pool, "TCS", "100.0000", time.Date(2026, 9, 26, 10, 0, 0, 0, ist))
	insertPrice(t, pool, "TCS", "110.0000", time.Date(2026, 9, 26, 23, 59, 0, 0, ist))
	insertPrice(t, pool, "TCS", "120.0000", time.Date(2026, 9, 27, 0, 0, 0, 0, ist))

	check := func(name string, p pricing.Price, found bool, err error, want string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := "none"
		if found {
			got = p.Price.StringFixed(4)
		}
		if got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}

	p, ok, err := pricing.LatestPrice(ctx, pool, "TCS")
	check("LatestPrice", p, ok, err, "120.0000")

	p, ok, err = pricing.PriceAt(ctx, pool, "TCS", time.Date(2026, 9, 26, 12, 0, 0, 0, ist))
	check("PriceAt noon Sep 26", p, ok, err, "100.0000")

	p, ok, err = pricing.PriceAt(ctx, pool, "TCS", time.Date(2026, 9, 26, 10, 0, 0, 0, ist))
	check("PriceAt exactly as_of", p, ok, err, "100.0000")

	p, ok, err = pricing.PriceAt(ctx, pool, "TCS", time.Date(2026, 9, 26, 9, 0, 0, 0, ist))
	check("PriceAt before first price", p, ok, err, "none")

	p, ok, err = pricing.ClosingPrice(ctx, pool, "TCS", time.Date(2026, 9, 26, 8, 0, 0, 0, ist))
	check("ClosingPrice Sep 26", p, ok, err, "110.0000") // midnight quote belongs to the 27th

	p, ok, err = pricing.ClosingPrice(ctx, pool, "TCS", time.Date(2026, 9, 25, 8, 0, 0, 0, ist))
	check("ClosingPrice Sep 25", p, ok, err, "none")

	p, ok, err = pricing.LatestPrice(ctx, pool, "INFY")
	check("LatestPrice with no rows", p, ok, err, "none")
}

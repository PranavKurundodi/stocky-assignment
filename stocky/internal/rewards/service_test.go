// Integration tests for Service.Create against Postgres.
package rewards_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/fees"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/rewards"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/testdb"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/users"
)

func TestMain(m *testing.M) { testdb.Main(m) }

var ctx = context.Background()

// now is the fixed "current time" for these tests.
var now = time.Date(2026, 9, 26, 6, 30, 0, 0, time.UTC) // 12:00 IST

// setup returns a service with a fixed clock, user_1, and a TCS price of
// 2000.0000 stamped 10 minutes before now.
func setup(t *testing.T) (*rewards.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.Pool(t)
	mustExec(t, pool, `INSERT INTO users (id, name) VALUES ('user_1', 'One')`)
	insertPrice(t, pool, "TCS", "2000.0000", now.Add(-10*time.Minute))

	log, _ := logtest.NewNullLogger()
	svc := rewards.NewService(pool, fees.DefaultRates(decimal.Zero), 2*time.Hour, log)
	svc.Now = func() time.Time { return now }
	return svc, pool
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func insertPrice(t *testing.T, pool *pgxpool.Pool, symbol, price string, asOf time.Time) {
	t.Helper()
	mustExec(t, pool, `INSERT INTO stock_prices (symbol, price, as_of, fetched_at) VALUES ($1, $2, $3, $3)`,
		symbol, decimal.RequireFromString(price), asOf)
}

// input prepares a request for qty of symbol under key.
func input(t *testing.T, key, symbol, qty string) rewards.CreateInput {
	t.Helper()
	in, err := rewards.Prepare(key, rewards.Request{
		UserID: "user_1", Symbol: symbol, Quantity: json.RawMessage(`"` + qty + `"`), Reason: "REFERRAL",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func countRewards(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reward_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// assertLedgerBalanced checks every transaction balances per asset,
// directly in SQL, independent of the Go validation.
func assertLedgerBalanced(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var bad int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (
		  SELECT transaction_id, asset
		  FROM ledger_entries
		  GROUP BY transaction_id, asset
		  HAVING SUM(CASE direction WHEN 'DEBIT' THEN 1 ELSE -1 END * COALESCE(quantity, inr_amount)) <> 0
		) x`).Scan(&bad)
	if err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Errorf("%d unbalanced (transaction, asset) pairs", bad)
	}
}

func TestCreateBooksBalancedEntriesWithCostAndFees(t *testing.T) {
	svc, pool := setup(t)

	rw, created, err := svc.Create(ctx, input(t, "k1", "TCS", "1"))
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if rw.CostINR.StringFixed(4) != "2000.0000" || rw.PriceUsed.StringFixed(4) != "2000.0000" {
		t.Errorf("cost = %s, price = %s", rw.CostINR, rw.PriceUsed)
	}
	// The spec's worked example for a 2000.0000 trade.
	f := rw.Fees
	got := []string{f.Brokerage.StringFixed(4), f.STT.StringFixed(4), f.Exchange.StringFixed(4),
		f.SEBI.StringFixed(4), f.StampDuty.StringFixed(4), f.GST.StringFixed(4), f.Total.StringFixed(4)}
	want := []string{"0.0000", "2.0000", "0.0594", "0.0020", "0.3000", "0.0111", "2.3725"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fee %d = %s, want %s", i, got[i], want[i])
		}
	}

	assertLedgerBalanced(t, pool)
	balances := map[string]string{
		ledger.CompanyCash:   "-2002.3725",
		ledger.RewardExpense: "2000.0000",
		ledger.FeeSTT:        "2.0000",
		ledger.FeeBrokerage:  "0.0000", // zero line was never written
	}
	for account, want := range balances {
		b, err := ledger.Balance(ctx, pool, account, "", ledger.AssetINR)
		if err != nil || b.StringFixed(4) != want {
			t.Errorf("%s = %s (err %v), want %s", account, b.StringFixed(4), err, want)
		}
	}
	units, _ := ledger.Balance(ctx, pool, ledger.UserStock, "user_1", "TCS")
	market, _ := ledger.Balance(ctx, pool, ledger.Market, "", "TCS")
	if units.StringFixed(6) != "1.000000" || market.StringFixed(6) != "-1.000000" {
		t.Errorf("user TCS = %s, market TCS = %s", units, market)
	}
}

func TestCostRoundsOnceHalfUp(t *testing.T) {
	svc, pool := setup(t)
	insertPrice(t, pool, "INFY", "1234.5678", now.Add(-time.Minute))

	// 0.333333 * 1234.5678 = 411.522188... -> 411.5222
	rw, _, err := svc.Create(ctx, input(t, "k1", "INFY", "0.333333"))
	if err != nil {
		t.Fatal(err)
	}
	if rw.CostINR.StringFixed(4) != "411.5222" {
		t.Errorf("cost = %s, want 411.5222", rw.CostINR.StringFixed(4))
	}
	assertLedgerBalanced(t, pool)
}

func TestReplayAndConflict(t *testing.T) {
	svc, pool := setup(t)

	first, created, err := svc.Create(ctx, input(t, "k1", "TCS", "1"))
	if err != nil || !created {
		t.Fatal(err)
	}

	// Same key, equivalent body ("1.000000" == "1"): the original reward.
	again, created, err := svc.Create(ctx, input(t, "k1", "TCS", "1.000000"))
	if err != nil || created || again.ID != first.ID {
		t.Errorf("replay: created = %v, id = %v, err = %v", created, again.ID, err)
	}
	if n := countRewards(t, pool); n != 1 {
		t.Errorf("rewards = %d, want 1", n)
	}

	// Same key, different body: conflict, nothing written.
	if _, _, err := svc.Create(ctx, input(t, "k1", "TCS", "2")); !errors.Is(err, rewards.ErrIdempotencyConflict) {
		t.Errorf("err = %v, want ErrIdempotencyConflict", err)
	}
	if n := countRewards(t, pool); n != 1 {
		t.Errorf("rewards after conflict = %d, want 1", n)
	}
}

func TestConcurrentSameKeyCreatesOneReward(t *testing.T) {
	svc, pool := setup(t)

	const n = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]rewards.Reward, n)
	createdFlags := make([]bool, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all goroutines at once to maximise the race
			results[i], createdFlags[i], errs[i] = svc.Create(ctx, input(t, "same-key", "TCS", "1"))
		}()
	}
	close(start)
	wg.Wait()

	createdCount := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if results[i].ID != results[0].ID {
			t.Errorf("request %d got a different reward id", i)
		}
		if createdFlags[i] {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Errorf("%d requests reported created, want exactly 1", createdCount)
	}
	if c := countRewards(t, pool); c != 1 {
		t.Errorf("rewards = %d, want 1", c)
	}
	units, _ := ledger.Balance(ctx, pool, ledger.UserStock, "user_1", "TCS")
	if units.StringFixed(6) != "1.000000" {
		t.Errorf("user TCS = %s, want 1 (booked once)", units)
	}
}

func TestCreateRejections(t *testing.T) {
	svc, pool := setup(t)
	mustExec(t, pool, `UPDATE stocks SET status = 'DELISTED' WHERE symbol = 'HDFCBANK'`)
	insertPrice(t, pool, "HDFCBANK", "750.0000", now.Add(-time.Minute))
	insertPrice(t, pool, "ICICIBANK", "1400.0000", now.Add(-3*time.Hour)) // older than 2h

	cases := []struct {
		name string
		in   rewards.CreateInput
		want error
	}{
		{"delisted", input(t, "k-delisted", "HDFCBANK", "1"), rewards.ErrDelisted},
		{"no price", input(t, "k-noprice", "INFY", "1"), rewards.ErrNoPrice},
		{"stale price", input(t, "k-stale", "ICICIBANK", "1"), rewards.ErrStalePrice},
		{"unknown stock", input(t, "k-unknown-stock", "FAKECORP", "1"), rewards.ErrUnknownStock},
	}
	unknownUser := input(t, "k-unknown-user", "TCS", "1")
	unknownUser.UserID = "nobody"
	cases = append(cases, struct {
		name string
		in   rewards.CreateInput
		want error
	}{"unknown user", unknownUser, users.ErrNotFound})

	for _, tc := range cases {
		if _, _, err := svc.Create(ctx, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if n := countRewards(t, pool); n != 0 {
		t.Errorf("rewards = %d, want 0 after only rejected requests", n)
	}
}

func TestBackdatedRewardUsesPriceAtRewardedAt(t *testing.T) {
	svc, pool := setup(t)
	past := now.Add(-48 * time.Hour)
	insertPrice(t, pool, "INFY", "900.0000", past.Add(-30*time.Minute))
	insertPrice(t, pool, "INFY", "1000.0000", now.Add(-time.Minute)) // today's price, must not be used

	in, err := rewards.Prepare("k1", rewards.Request{
		UserID: "user_1", Symbol: "INFY", Quantity: json.RawMessage(`"2"`), Reason: "MILESTONE",
		RewardedAt: func() *string { s := past.Format(time.RFC3339); return &s }(),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	rw, _, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if rw.PriceUsed.StringFixed(4) != "900.0000" || rw.CostINR.StringFixed(4) != "1800.0000" {
		t.Errorf("price = %s, cost = %s; want the price at rewarded_at", rw.PriceUsed, rw.CostINR)
	}
}

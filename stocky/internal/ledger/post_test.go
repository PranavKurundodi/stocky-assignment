// Integration tests: posting to Postgres and deriving holdings.
package ledger_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

var ctx = context.Background()

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testdb.Pool(t)
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, name) VALUES ('user_1', 'One')`); err != nil {
		t.Fatal(err)
	}
	return pool
}

// giveShares posts a balanced grant of qty shares to user_1 at effectiveAt.
func giveShares(t *testing.T, pool *pgxpool.Pool, symbol, qty string, effectiveAt time.Time) {
	t.Helper()
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := ledger.Post(ctx, tx, ledger.Transaction{
			Type:        ledger.TypeReward,
			EffectiveAt: effectiveAt,
			Entries: []ledger.Entry{
				ledger.Dr(ledger.UserStock, symbol, d(qty)).ForUser("user_1"),
				ledger.Cr(ledger.Market, symbol, d(qty)),
			},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostWritesTransactionAndEntries(t *testing.T) {
	pool := setup(t)
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := ledger.Post(ctx, tx, ledger.Transaction{
			Type:        ledger.TypeReward,
			EffectiveAt: time.Now(),
			Entries: []ledger.Entry{
				ledger.Dr(ledger.RewardExpense, ledger.AssetINR, d("2000.0000")),
				ledger.Dr(ledger.FeeSTT, ledger.AssetINR, d("2.0000")),
				ledger.Dr(ledger.FeeBrokerage, ledger.AssetINR, d("0")),
				ledger.Cr(ledger.CompanyCash, ledger.AssetINR, d("2002.0000")),
				ledger.Dr(ledger.UserStock, "TCS", d("2")).ForUser("user_1"),
				ledger.Cr(ledger.Market, "TCS", d("2")),
			},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "ledger_transactions"); n != 1 {
		t.Errorf("transactions = %d, want 1", n)
	}
	if n := count(t, pool, "ledger_entries"); n != 5 {
		t.Errorf("entries = %d, want 5 (zero brokerage line skipped)", n)
	}

	cash, err := ledger.Balance(ctx, pool, ledger.CompanyCash, "", ledger.AssetINR)
	if err != nil || cash.StringFixed(4) != "-2002.0000" {
		t.Errorf("COMPANY_CASH balance = %s, err = %v", cash, err)
	}
	units, err := ledger.Balance(ctx, pool, ledger.UserStock, "user_1", "TCS")
	if err != nil || units.StringFixed(6) != "2.000000" {
		t.Errorf("user_1 TCS balance = %s, err = %v", units, err)
	}
}

func TestUnbalancedPostWritesNothing(t *testing.T) {
	pool := setup(t)
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := ledger.Post(ctx, tx, ledger.Transaction{
			Type:        ledger.TypeReward,
			EffectiveAt: time.Now(),
			Entries: []ledger.Entry{
				ledger.Dr(ledger.UserStock, "TCS", d("2")).ForUser("user_1"),
				ledger.Cr(ledger.Market, "TCS", d("1")),
			},
		})
		return err
	})
	var ue *ledger.UnbalancedError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want UnbalancedError", err)
	}
	if count(t, pool, "ledger_transactions")+count(t, pool, "ledger_entries") != 0 {
		t.Error("rows were written for an unbalanced transaction")
	}
}

func TestUserHoldingsRespectsEffectiveAt(t *testing.T) {
	pool := setup(t)
	day1 := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)

	giveShares(t, pool, "TCS", "1.5", day1)
	giveShares(t, pool, "TCS", "0.25", day2)
	giveShares(t, pool, "INFY", "3", day2)

	all, err := ledger.UserHoldings(ctx, pool, "user_1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Symbol != "INFY" || all[1].Quantity.StringFixed(6) != "1.750000" {
		t.Errorf("all holdings = %+v", all)
	}

	// Before day2: only the first TCS grant counts.
	cut := day2
	early, err := ledger.UserHoldings(ctx, pool, "user_1", &cut)
	if err != nil {
		t.Fatal(err)
	}
	if len(early) != 1 || early[0].Symbol != "TCS" || early[0].Quantity.StringFixed(6) != "1.500000" {
		t.Errorf("holdings before day2 = %+v", early)
	}

	none, err := ledger.UserHoldings(ctx, pool, "nobody", nil)
	if err != nil || len(none) != 0 {
		t.Errorf("unknown user holdings = %+v, err = %v", none, err)
	}
}

func TestVerifyFindsImbalancesWrittenOutsidePost(t *testing.T) {
	pool := setup(t)
	giveShares(t, pool, "TCS", "1", time.Now()) // balanced, via Post

	bad, checked, err := ledger.Verify(ctx, pool)
	if err != nil || len(bad) != 0 || checked != 1 {
		t.Fatalf("clean ledger: bad = %v, checked = %d, err = %v", bad, checked, err)
	}

	// Raw SQL bypasses Post's validation, simulating a bug or a manual edit.
	if _, err := pool.Exec(ctx, `
		INSERT INTO ledger_transactions (id, type, effective_at) VALUES ('00000000-0000-0000-0000-0000000000aa', 'REWARD', now());
		INSERT INTO ledger_entries (transaction_id, account, asset, direction, inr_amount)
		VALUES ('00000000-0000-0000-0000-0000000000aa', 'REWARD_EXPENSE', 'INR', 'DEBIT', 100),
		       ('00000000-0000-0000-0000-0000000000aa', 'COMPANY_CASH', 'INR', 'CREDIT', 99.9999)`); err != nil {
		t.Fatal(err)
	}

	bad, checked, err = ledger.Verify(ctx, pool)
	if err != nil || checked != 2 || len(bad) != 1 {
		t.Fatalf("bad = %v, checked = %d, err = %v", bad, checked, err)
	}
	b := bad[0]
	if b.TransactionID.String() != "00000000-0000-0000-0000-0000000000aa" || b.Asset != "INR" ||
		b.Debits.StringFixed(4) != "100.0000" || b.Credits.StringFixed(4) != "99.9999" {
		t.Errorf("imbalance = %+v", b)
	}
}

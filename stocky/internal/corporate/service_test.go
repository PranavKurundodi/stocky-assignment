// Integration tests for splits, mergers and delistings against Postgres.
package corporate_test

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

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/corporate"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

var (
	ctx = context.Background()
	now = time.Date(2026, 9, 26, 6, 30, 0, 0, time.UTC)
)

func setup(t *testing.T) (*corporate.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.Pool(t)
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, name) VALUES ('user_1', 'One'), ('user_2', 'Two')`); err != nil {
		t.Fatal(err)
	}
	log, _ := logtest.NewNullLogger()
	svc := corporate.NewService(pool, log)
	svc.Now = func() time.Time { return now }
	return svc, pool
}

// give books qty of symbol into account (user "" for COMPANY_STOCK) an hour
// before now, balanced against MARKET.
func give(t *testing.T, pool *pgxpool.Pool, account, user, symbol, qty string) {
	t.Helper()
	q := decimal.RequireFromString(qty)
	holder := ledger.Entry{Account: account, UserID: user, Asset: symbol, Direction: ledger.Debit, Amount: q}
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := ledger.Post(ctx, tx, ledger.Transaction{
			Type: ledger.TypeReward, EffectiveAt: now.Add(-time.Hour),
			Entries: []ledger.Entry{holder, ledger.Cr(ledger.Market, symbol, q)},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func apply(t *testing.T, svc *corporate.Service, req corporate.Request) (corporate.Action, error) {
	t.Helper()
	a, err := svc.Prepare(req)
	if err != nil {
		return a, err
	}
	return svc.Apply(ctx, a)
}

func ratio(s string) json.RawMessage { return json.RawMessage(`"` + s + `"`) }

func units(t *testing.T, pool *pgxpool.Pool, account, user, symbol string) string {
	t.Helper()
	b, err := ledger.Balance(ctx, pool, account, user, symbol)
	if err != nil {
		t.Fatal(err)
	}
	return b.StringFixed(6)
}

func assertBalanced(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	bad, _, err := ledger.Verify(ctx, pool)
	if err != nil || len(bad) != 0 {
		t.Errorf("ledger unbalanced: %+v (err %v)", bad, err)
	}
}

func status(t *testing.T, pool *pgxpool.Pool, symbol string) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(ctx, `SELECT status FROM stocks WHERE symbol = $1`, symbol).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSplitDoublesEveryHolder(t *testing.T) {
	svc, pool := setup(t)
	give(t, pool, ledger.UserStock, "user_1", "TCS", "3")
	give(t, pool, ledger.UserStock, "user_2", "TCS", "0.333333")
	give(t, pool, ledger.CompanyStock, "", "TCS", "1")

	a, err := apply(t, svc, corporate.Request{Type: "split", Symbol: "tcs", RatioFrom: ratio("1"), RatioTo: ratio("2")})
	if err != nil {
		t.Fatal(err)
	}
	if a.HoldersAffected != 3 {
		t.Errorf("holders affected = %d, want 3", a.HoldersAffected)
	}
	if got := units(t, pool, ledger.UserStock, "user_1", "TCS"); got != "6.000000" {
		t.Errorf("user_1 TCS = %s, want 6", got)
	}
	if got := units(t, pool, ledger.UserStock, "user_2", "TCS"); got != "0.666666" {
		t.Errorf("user_2 TCS = %s, want 0.666666", got)
	}
	if got := units(t, pool, ledger.CompanyStock, "", "TCS"); got != "2.000000" {
		t.Errorf("company TCS = %s, want 2", got)
	}
	// The new units came from CORPORATE_ACTION; MARKET is untouched.
	if got := units(t, pool, ledger.CorporateAction, "", "TCS"); got != "-4.333333" {
		t.Errorf("CORPORATE_ACTION TCS = %s", got)
	}
	if status(t, pool, "TCS") != "ACTIVE" {
		t.Error("a split must not delist the stock")
	}
	assertBalanced(t, pool)
}

func TestReverseSplitHalvesHoldings(t *testing.T) {
	svc, pool := setup(t)
	give(t, pool, ledger.UserStock, "user_1", "TCS", "4")
	if _, err := apply(t, svc, corporate.Request{Type: "SPLIT", Symbol: "TCS", RatioFrom: ratio("2"), RatioTo: ratio("1")}); err != nil {
		t.Fatal(err)
	}
	if got := units(t, pool, ledger.UserStock, "user_1", "TCS"); got != "2.000000" {
		t.Errorf("user_1 TCS = %s, want 2", got)
	}
	assertBalanced(t, pool)
}

func TestMergerConvertsHoldingsAndDelistsTarget(t *testing.T) {
	svc, pool := setup(t)
	give(t, pool, ledger.UserStock, "user_1", "INFY", "7.5")
	give(t, pool, ledger.UserStock, "user_2", "INFY", "1")
	give(t, pool, ledger.UserStock, "user_2", "TCS", "1") // already holds the acquirer

	a, err := apply(t, svc, corporate.Request{
		Type: "MERGER", Symbol: "INFY", NewSymbol: "TCS", RatioFrom: ratio("3"), RatioTo: ratio("1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.HoldersAffected != 2 {
		t.Errorf("holders affected = %d, want 2", a.HoldersAffected)
	}

	checks := []struct{ user, symbol, want string }{
		{"user_1", "INFY", "0.000000"},
		{"user_1", "TCS", "2.500000"}, // 7.5 / 3
		{"user_2", "INFY", "0.000000"},
		{"user_2", "TCS", "1.333333"}, // 1 existing + 1/3 rounded to 6 dp
	}
	for _, c := range checks {
		if got := units(t, pool, ledger.UserStock, c.user, c.symbol); got != c.want {
			t.Errorf("%s %s = %s, want %s", c.user, c.symbol, got, c.want)
		}
	}
	if status(t, pool, "INFY") != "DELISTED" || status(t, pool, "TCS") != "ACTIVE" {
		t.Errorf("statuses: INFY %s, TCS %s", status(t, pool, "INFY"), status(t, pool, "TCS"))
	}
	assertBalanced(t, pool)
}

func TestDelistingOnlyChangesStatus(t *testing.T) {
	svc, pool := setup(t)
	give(t, pool, ledger.UserStock, "user_1", "HDFCBANK", "2")

	a, err := apply(t, svc, corporate.Request{Type: "DELISTING", Symbol: "HDFCBANK"})
	if err != nil {
		t.Fatal(err)
	}
	if a.HoldersAffected != 1 || status(t, pool, "HDFCBANK") != "DELISTED" {
		t.Errorf("holders = %d, status = %s", a.HoldersAffected, status(t, pool, "HDFCBANK"))
	}
	// Holding kept as is; no ledger transaction beyond the grant.
	if got := units(t, pool, ledger.UserStock, "user_1", "HDFCBANK"); got != "2.000000" {
		t.Errorf("holding = %s", got)
	}
	if _, checked, _ := ledger.Verify(ctx, pool); checked != 1 {
		t.Errorf("ledger transactions = %d, want 1", checked)
	}
}

func TestSplitWithNoHoldersRecordsAction(t *testing.T) {
	svc, pool := setup(t)
	a, err := apply(t, svc, corporate.Request{Type: "SPLIT", Symbol: "TCS", RatioFrom: ratio("1"), RatioTo: ratio("5")})
	if err != nil || a.HoldersAffected != 0 {
		t.Fatalf("holders = %d, err = %v", a.HoldersAffected, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM corporate_actions`).Scan(&n); err != nil || n != 1 {
		t.Errorf("corporate_actions rows = %d (err %v)", n, err)
	}
}

func TestCorporateActionRejections(t *testing.T) {
	svc, pool := setup(t)
	if _, err := pool.Exec(ctx, `UPDATE stocks SET status = 'DELISTED' WHERE symbol = 'HDFCBANK'`); err != nil {
		t.Fatal(err)
	}

	var ve *corporate.ValidationError
	invalid := map[string]corporate.Request{
		"bad type":            {Type: "BONUS", Symbol: "TCS"},
		"missing symbol":      {Type: "DELISTING"},
		"split without ratio": {Type: "SPLIT", Symbol: "TCS"},
		"ratio as number":     {Type: "SPLIT", Symbol: "TCS", RatioFrom: json.RawMessage(`1`), RatioTo: ratio("2")},
		"zero ratio":          {Type: "SPLIT", Symbol: "TCS", RatioFrom: ratio("0"), RatioTo: ratio("2")},
		"1:1 split":           {Type: "SPLIT", Symbol: "TCS", RatioFrom: ratio("1"), RatioTo: ratio("1")},
		"merger no target":    {Type: "MERGER", Symbol: "INFY", RatioFrom: ratio("3"), RatioTo: ratio("1")},
		"merger into itself":  {Type: "MERGER", Symbol: "INFY", NewSymbol: "infy", RatioFrom: ratio("3"), RatioTo: ratio("1")},
		"split with target":   {Type: "SPLIT", Symbol: "TCS", NewSymbol: "INFY", RatioFrom: ratio("1"), RatioTo: ratio("2")},
		"future effective_at": {Type: "DELISTING", Symbol: "TCS", EffectiveAt: func() *string { s := "2026-09-26T12:05:00+05:30"; return &s }()},
	}
	for name, req := range invalid {
		if _, err := apply(t, svc, req); !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}

	state := map[string]struct {
		req  corporate.Request
		want error
	}{
		"unknown symbol":     {corporate.Request{Type: "DELISTING", Symbol: "FAKECORP"}, corporate.ErrUnknownStock},
		"already delisted":   {corporate.Request{Type: "DELISTING", Symbol: "HDFCBANK"}, corporate.ErrNotActive},
		"unknown new_symbol": {corporate.Request{Type: "MERGER", Symbol: "INFY", NewSymbol: "NEWCO", RatioFrom: ratio("1"), RatioTo: ratio("1")}, corporate.ErrUnknownStock},
		"delisted target":    {corporate.Request{Type: "MERGER", Symbol: "INFY", NewSymbol: "HDFCBANK", RatioFrom: ratio("1"), RatioTo: ratio("1")}, corporate.ErrNotActive},
	}
	for name, c := range state {
		if _, err := apply(t, svc, c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM corporate_actions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("corporate_actions rows = %d after only rejected requests", n)
	}
}

// Integration tests for the schema and the transaction helper. They live in
// package db_test because testdb itself imports db.
package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

var ctx = context.Background()

func TestMigrationSeedsBaseStocks(t *testing.T) {
	pool := testdb.Pool(t)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stocks WHERE status = 'ACTIVE'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("active stocks = %d, want 5", n)
	}
}

func TestNumericScansIntoDecimal(t *testing.T) {
	pool := testdb.Pool(t)
	var d decimal.Decimal
	if err := pool.QueryRow(ctx, `SELECT 1234.5678::numeric(18,4)`).Scan(&d); err != nil {
		t.Fatalf("scan NUMERIC into decimal: %v", err)
	}
	if d.StringFixed(4) != "1234.5678" {
		t.Errorf("got %s", d.StringFixed(4))
	}
}

// insertTxn creates a ledger transaction and returns its id.
func insertTxn(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO ledger_transactions (id, type, effective_at) VALUES ($1, 'REWARD', now())`, id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLedgerEntryConstraints(t *testing.T) {
	pool := testdb.Pool(t)
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, name) VALUES ('u1', 'User One')`); err != nil {
		t.Fatal(err)
	}
	txn := insertTxn(t, pool)

	insert := `INSERT INTO ledger_entries (transaction_id, account, user_id, asset, direction, quantity, inr_amount)
	           VALUES ($1, $2, $3, $4, 'DEBIT', $5, $6)`
	one := decimal.NewFromInt(1)
	var noUser *string
	user := "u1"

	good := []struct {
		name     string
		account  string
		user     *string
		asset    string
		qty, inr *decimal.Decimal
	}{
		{"INR line", "COMPANY_CASH", noUser, "INR", nil, &one},
		{"stock line", "USER_STOCK", &user, "TCS", &one, nil},
	}
	for _, g := range good {
		if _, err := pool.Exec(ctx, insert, txn, g.account, g.user, g.asset, g.qty, g.inr); err != nil {
			t.Errorf("%s: unexpected error %v", g.name, err)
		}
	}

	zero := decimal.Zero
	bad := []struct {
		name     string
		account  string
		user     *string
		asset    string
		qty, inr *decimal.Decimal
	}{
		{"INR line with quantity", "COMPANY_CASH", noUser, "INR", &one, &one},
		{"INR line without amount", "COMPANY_CASH", noUser, "INR", nil, nil},
		{"stock line with INR amount", "MARKET", noUser, "TCS", nil, &one},
		{"zero amount", "COMPANY_CASH", noUser, "INR", nil, &zero},
		{"USER_STOCK without user", "USER_STOCK", noUser, "TCS", &one, nil},
		{"user on a company account", "MARKET", &user, "TCS", &one, nil},
	}
	for _, b := range bad {
		if _, err := pool.Exec(ctx, insert, txn, b.account, b.user, b.asset, b.qty, b.inr); err == nil {
			t.Errorf("%s: expected a CHECK violation", b.name)
		}
	}
}

func TestLedgerIsAppendOnly(t *testing.T) {
	pool := testdb.Pool(t)
	txn := insertTxn(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO ledger_entries (transaction_id, account, asset, direction, inr_amount)
	                             VALUES ($1, 'COMPANY_CASH', 'INR', 'CREDIT', 10)`, txn); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{
		`UPDATE ledger_entries SET inr_amount = 20`,
		`DELETE FROM ledger_entries`,
		`UPDATE ledger_transactions SET description = 'edited'`,
		`DELETE FROM ledger_transactions`,
	} {
		_, err := pool.Exec(ctx, q)
		if err == nil || !strings.Contains(err.Error(), "append only") {
			t.Errorf("%q: err = %v, want append only error", q, err)
		}
	}
}

func TestWithTx(t *testing.T) {
	pool := testdb.Pool(t)
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	insert := func(tx pgx.Tx, id string) error {
		_, err := tx.Exec(ctx, `INSERT INTO users (id, name) VALUES ($1, 'x')`, id)
		return err
	}

	// nil error: committed.
	if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error { return insert(tx, "a") }); err != nil {
		t.Fatal(err)
	}
	// error: rolled back, error returned unchanged.
	boom := errors.New("boom")
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if err := insert(tx, "b"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	// panic: rolled back and re-raised.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_ = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			_ = insert(tx, "c")
			panic("kaboom")
		})
	}()

	if n := count(); n != 1 {
		t.Errorf("users = %d, want 1 (only the committed insert)", n)
	}
}

func TestMigrateDownThenUp(t *testing.T) {
	pool := testdb.Pool(t)
	tableExists := func() bool {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.ledger_entries') IS NOT NULL`).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	if err := db.MigrateDown(testdb.URL()); err != nil {
		t.Fatalf("down: %v", err)
	}
	if tableExists() {
		t.Error("ledger_entries still exists after down migration")
	}
	version, err := db.MigrateUp(testdb.URL())
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if version != 1 || !tableExists() {
		t.Errorf("after up: version = %d, table exists = %v", version, tableExists())
	}
}

func TestResetDataLeavesOnlyTheActiveBaseStocks(t *testing.T) {
	pool := testdb.Pool(t)
	// Simulate earlier activity: a new listing from the feed and a delisting.
	for _, q := range []string{
		`INSERT INTO stocks (symbol, status) VALUES ('WIPRO', 'ACTIVE')`,
		`UPDATE stocks SET status = 'DELISTED' WHERE symbol = 'HDFCBANK'`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.ResetData(ctx, pool); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, `SELECT symbol || ':' || status FROM stocks ORDER BY symbol`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := "HDFCBANK:ACTIVE ICICIBANK:ACTIVE INFY:ACTIVE RELIANCE:ACTIVE TCS:ACTIVE"
	if strings.Join(got, " ") != want {
		t.Errorf("stocks after reset = %v, want %s", got, want)
	}
}

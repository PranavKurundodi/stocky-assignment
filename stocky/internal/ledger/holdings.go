package ledger

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Holding is a quantity of one stock.
type Holding struct {
	Symbol   string
	Quantity decimal.Decimal
}

// UserHoldings returns the user's non-zero share holdings, sorted by symbol.
// With before == nil it counts every entry; otherwise only transactions with
// effective_at < *before (for "holdings at the end of a day").
//
// The balance of an account is sum(DEBIT) - sum(CREDIT): USER_STOCK is an
// asset account from the user's point of view, so debits add shares.
func UserHoldings(ctx context.Context, q Querier, userID string, before *time.Time) ([]Holding, error) {
	rows, err := q.Query(ctx, `
		SELECT e.asset,
		       SUM(CASE e.direction WHEN 'DEBIT' THEN e.quantity ELSE -e.quantity END) AS qty
		FROM ledger_entries e
		JOIN ledger_transactions t ON t.id = e.transaction_id
		WHERE e.account = 'USER_STOCK'
		  AND e.user_id = $1
		  AND ($2::timestamptz IS NULL OR t.effective_at < $2)
		GROUP BY e.asset
		HAVING SUM(CASE e.direction WHEN 'DEBIT' THEN e.quantity ELSE -e.quantity END) <> 0
		ORDER BY e.asset`,
		userID, before)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Holding, error) {
		var h Holding
		err := r.Scan(&h.Symbol, &h.Quantity)
		return h, err
	})
}

// Balance returns sum(DEBIT) - sum(CREDIT) of one account for one asset
// across all time. userID is "" for company accounts.
func Balance(ctx context.Context, q Querier, account, userID, asset string) (decimal.Decimal, error) {
	var bal decimal.Decimal
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(
		         CASE e.direction WHEN 'DEBIT' THEN 1 ELSE -1 END
		         * COALESCE(e.quantity, e.inr_amount)), 0)
		FROM ledger_entries e
		WHERE e.account = $1
		  AND e.user_id IS NOT DISTINCT FROM NULLIF($2, '')
		  AND e.asset = $3`,
		account, userID, asset).Scan(&bal)
	return bal, err
}

package ledger

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Holder is one account's positive balance of a stock.
type Holder struct {
	Account  string // USER_STOCK or COMPANY_STOCK
	UserID   string // set for USER_STOCK
	Quantity decimal.Decimal
}

// Holders returns every account that currently holds symbol: each user's
// USER_STOCK and Stocky's own COMPANY_STOCK. MARKET and CORPORATE_ACTION are
// counterparty accounts, not holders, so corporate actions never adjust them.
func Holders(ctx context.Context, q Querier, symbol string) ([]Holder, error) {
	rows, err := q.Query(ctx, `
		SELECT account, COALESCE(user_id, ''),
		       SUM(CASE direction WHEN 'DEBIT' THEN quantity ELSE -quantity END)
		FROM ledger_entries
		WHERE asset = $1 AND account IN ('USER_STOCK', 'COMPANY_STOCK')
		GROUP BY account, user_id
		HAVING SUM(CASE direction WHEN 'DEBIT' THEN quantity ELSE -quantity END) > 0
		ORDER BY account, user_id`, symbol)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Holder, error) {
		var h Holder
		err := r.Scan(&h.Account, &h.UserID, &h.Quantity)
		return h, err
	})
}

// Imbalance is a (transaction, asset) pair whose debits and credits differ.
type Imbalance struct {
	TransactionID uuid.UUID
	Asset         string
	Debits        decimal.Decimal
	Credits       decimal.Decimal
}

// Verify re-checks the whole ledger in SQL, independently of the Go-side
// validation in Post, and returns every imbalance plus how many transactions
// were checked. A healthy ledger returns an empty list.
func Verify(ctx context.Context, q Querier) (unbalanced []Imbalance, checked int, err error) {
	if err := q.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions`).Scan(&checked); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `
		SELECT transaction_id, asset,
		       COALESCE(SUM(COALESCE(quantity, inr_amount)) FILTER (WHERE direction = 'DEBIT'), 0),
		       COALESCE(SUM(COALESCE(quantity, inr_amount)) FILTER (WHERE direction = 'CREDIT'), 0)
		FROM ledger_entries
		GROUP BY transaction_id, asset
		HAVING COALESCE(SUM(COALESCE(quantity, inr_amount)) FILTER (WHERE direction = 'DEBIT'), 0)
		    <> COALESCE(SUM(COALESCE(quantity, inr_amount)) FILTER (WHERE direction = 'CREDIT'), 0)
		ORDER BY transaction_id, asset`)
	if err != nil {
		return nil, 0, err
	}
	unbalanced, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Imbalance, error) {
		var im Imbalance
		err := r.Scan(&im.TransactionID, &im.Asset, &im.Debits, &im.Credits)
		return im, err
	})
	return unbalanced, checked, err
}

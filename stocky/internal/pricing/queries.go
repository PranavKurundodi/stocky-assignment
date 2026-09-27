package pricing

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx, so the lookups can
// run on their own or inside a larger transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Price is a stored price and the vendor time it was valid at.
type Price struct {
	Price decimal.Decimal
	AsOf  time.Time
}

// latestBefore returns the newest price for symbol with as_of <= t (or < t
// when inclusive is false). It walks the (symbol, as_of DESC) index.
func latestBefore(ctx context.Context, q Querier, symbol string, t time.Time, inclusive bool) (Price, bool, error) {
	op := "<="
	if !inclusive {
		op = "<"
	}
	var p Price
	err := q.QueryRow(ctx,
		`SELECT price, as_of FROM stock_prices
		 WHERE symbol = $1 AND as_of `+op+` $2
		 ORDER BY as_of DESC LIMIT 1`,
		symbol, t).Scan(&p.Price, &p.AsOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return Price{}, false, nil
	}
	if err != nil {
		return Price{}, false, err
	}
	return p, true, nil
}

// LatestPrice returns the newest stored price for symbol.
func LatestPrice(ctx context.Context, q Querier, symbol string) (Price, bool, error) {
	var p Price
	err := q.QueryRow(ctx,
		`SELECT price, as_of FROM stock_prices WHERE symbol = $1 ORDER BY as_of DESC LIMIT 1`,
		symbol).Scan(&p.Price, &p.AsOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return Price{}, false, nil
	}
	if err != nil {
		return Price{}, false, err
	}
	return p, true, nil
}

// PriceAt returns the price in force at t: the latest with as_of <= t.
func PriceAt(ctx context.Context, q Querier, symbol string, t time.Time) (Price, bool, error) {
	return latestBefore(ctx, q, symbol, t, true)
}

// ClosingPrice returns the closing price for the IST calendar day containing
// day: the latest price with as_of before the start of the next IST day.
func ClosingPrice(ctx context.Context, q Querier, symbol string, day time.Time) (Price, bool, error) {
	return latestBefore(ctx, q, symbol, timeutil.StartOfNextDayIST(day), false)
}

// IsStale reports whether a price valid at asOf is too old to trust at the
// reference time ref (now for reads, rewarded_at for rewards).
func IsStale(asOf, ref time.Time, staleAfter time.Duration) bool {
	return ref.Sub(asOf) > staleAfter
}

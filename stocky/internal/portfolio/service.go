// Package portfolio answers the read-only questions about a user: today's
// rewards, current holdings and their INR value, and value history.
package portfolio

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/money"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/pricing"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/users"
)

// Service reads holdings from the ledger and prices from stock_prices.
type Service struct {
	pool       *pgxpool.Pool
	staleAfter time.Duration
	Now        func() time.Time // injectable clock; defaults to time.Now
}

// NewService builds a Service.
func NewService(pool *pgxpool.Pool, staleAfter time.Duration) *Service {
	return &Service{pool: pool, staleAfter: staleAfter, Now: time.Now}
}

// ---- today ----

// TodayReward is one reward given during the current IST day.
type TodayReward struct {
	ID         uuid.UUID
	Symbol     string
	Quantity   decimal.Decimal
	Reason     string
	RewardedAt time.Time
	Status     string
}

// Today returns the user's rewards with rewarded_at inside today's IST
// window [00:00 today, 00:00 tomorrow), newest first. Reversed rewards are
// included and carry status REVERSED.
func (s *Service) Today(ctx context.Context, userID string) (date string, out []TodayReward, err error) {
	if err := users.MustExist(ctx, s.pool, userID); err != nil {
		return "", nil, err
	}
	now := s.Now()
	rows, err := s.pool.Query(ctx, `
		SELECT id, symbol, quantity, reason, rewarded_at, status
		FROM reward_events
		WHERE user_id = $1 AND rewarded_at >= $2 AND rewarded_at < $3
		ORDER BY rewarded_at DESC, created_at DESC`,
		userID, timeutil.StartOfDayIST(now), timeutil.StartOfNextDayIST(now))
	if err != nil {
		return "", nil, err
	}
	out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (TodayReward, error) {
		var t TodayReward
		err := r.Scan(&t.ID, &t.Symbol, &t.Quantity, &t.Reason, &t.RewardedAt, &t.Status)
		return t, err
	})
	return timeutil.ISTDate(now), out, err
}

// ---- portfolio ----

// Holding is one stock position valued at its latest known price. Price,
// PriceAsOf and Value are nil when we have never seen a price for it.
type Holding struct {
	Symbol    string
	Quantity  decimal.Decimal
	Price     *decimal.Decimal
	PriceAsOf *time.Time
	Value     *decimal.Decimal // quantity * price, rounded to 4 dp for display
	IsStale   bool
	Delisted  bool
}

// Portfolio is a user's current holdings and their total INR value.
type Portfolio struct {
	Holdings      []Holding
	Total         decimal.Decimal // excludes holdings with no price
	MissingPrices []string
	OldestAsOf    *time.Time // oldest price used, nil when nothing was valued
	AnyStale      bool
}

// Portfolio values every current holding at its latest stored price.
//
// Rounding: each holding's value is computed at full precision and the
// total is the sum of those unrounded values, rounded once at the end.
// Each row's displayed value is rounded separately, so the displayed rows
// can differ from the total by a few ten-thousandths of a rupee. The total
// is the more accurate number.
func (s *Service) Portfolio(ctx context.Context, userID string) (Portfolio, error) {
	if err := users.MustExist(ctx, s.pool, userID); err != nil {
		return Portfolio{}, err
	}
	held, err := ledger.UserHoldings(ctx, s.pool, userID, nil)
	if err != nil {
		return Portfolio{}, err
	}
	delisted, err := s.delistedSymbols(ctx)
	if err != nil {
		return Portfolio{}, err
	}

	now := s.Now()
	p := Portfolio{Holdings: []Holding{}, MissingPrices: []string{}}
	total := decimal.Zero
	for _, h := range held {
		row := Holding{Symbol: h.Symbol, Quantity: h.Quantity, Delisted: delisted[h.Symbol]}

		// A delisted stock has no new prices, so this is its last known one.
		price, found, err := pricing.LatestPrice(ctx, s.pool, h.Symbol)
		if err != nil {
			return Portfolio{}, err
		}
		if !found {
			p.MissingPrices = append(p.MissingPrices, h.Symbol)
			p.Holdings = append(p.Holdings, row)
			continue
		}

		exact := h.Quantity.Mul(price.Price) // full precision
		total = total.Add(exact)
		shown := exact.Round(money.INRPlaces)

		row.Price, row.PriceAsOf, row.Value = &price.Price, &price.AsOf, &shown
		row.IsStale = pricing.IsStale(price.AsOf, now, s.staleAfter)
		p.AnyStale = p.AnyStale || row.IsStale
		if p.OldestAsOf == nil || price.AsOf.Before(*p.OldestAsOf) {
			asOf := price.AsOf
			p.OldestAsOf = &asOf
		}
		p.Holdings = append(p.Holdings, row)
	}
	p.Total = total.Round(money.INRPlaces)
	return p, nil
}

func (s *Service) delistedSymbols(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT symbol FROM stocks WHERE status = 'DELISTED'`)
	if err != nil {
		return nil, err
	}
	syms, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(syms))
	for _, s := range syms {
		out[s] = true
	}
	return out, nil
}

// ---- stats ----

// Stats is today's rewarded shares plus the current portfolio value.
type Stats struct {
	TodaySharesBySymbol map[string]decimal.Decimal
	Portfolio           Portfolio
}

// Stats returns shares rewarded today per symbol (ACTIVE rewards only) and
// the current portfolio valuation.
func (s *Service) Stats(ctx context.Context, userID string) (Stats, error) {
	p, err := s.Portfolio(ctx, userID) // also checks the user exists
	if err != nil {
		return Stats{}, err
	}
	now := s.Now()
	rows, err := s.pool.Query(ctx, `
		SELECT symbol, SUM(quantity)
		FROM reward_events
		WHERE user_id = $1 AND status = 'ACTIVE'
		  AND rewarded_at >= $2 AND rewarded_at < $3
		GROUP BY symbol`,
		userID, timeutil.StartOfDayIST(now), timeutil.StartOfNextDayIST(now))
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()

	today := map[string]decimal.Decimal{}
	for rows.Next() {
		var sym string
		var qty decimal.Decimal
		if err := rows.Scan(&sym, &qty); err != nil {
			return Stats{}, err
		}
		today[sym] = qty
	}
	return Stats{TodaySharesBySymbol: today, Portfolio: p}, rows.Err()
}

// ---- history ----

// DayValue is the value of the user's holdings at the end of one IST day.
// Value is nil when a held symbol had no closing price that day.
type DayValue struct {
	Date          string
	Value         *decimal.Decimal
	MissingPrices []string
}

// History returns, for each IST date from the user's first ledger entry up
// to and including yesterday, the end-of-day value of everything they held:
// holdings from entries with effective_at before the next midnight, times
// each symbol's closing price for that day. Today is never included because
// the day has not closed.
func (s *Service) History(ctx context.Context, userID string) ([]DayValue, error) {
	if err := users.MustExist(ctx, s.pool, userID); err != nil {
		return nil, err
	}

	var first *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT MIN(t.effective_at)
		FROM ledger_entries e
		JOIN ledger_transactions t ON t.id = e.transaction_id
		WHERE e.user_id = $1`, userID).Scan(&first)
	if err != nil {
		return nil, err
	}
	out := []DayValue{}
	if first == nil {
		return out, nil // no rewards yet
	}

	today := timeutil.StartOfDayIST(s.Now())
	// Step by calendar day with AddDate; every day starts at IST midnight.
	for day := timeutil.StartOfDayIST(*first); day.Before(today); day = day.AddDate(0, 0, 1) {
		dv, err := s.valueAtEndOf(ctx, userID, day)
		if err != nil {
			return nil, err
		}
		out = append(out, dv)
	}
	return out, nil
}

// valueAtEndOf values the user's holdings at the close of one IST day.
func (s *Service) valueAtEndOf(ctx context.Context, userID string, day time.Time) (DayValue, error) {
	dv := DayValue{Date: timeutil.ISTDate(day), MissingPrices: []string{}}
	end := timeutil.StartOfNextDayIST(day)

	held, err := ledger.UserHoldings(ctx, s.pool, userID, &end)
	if err != nil {
		return dv, err
	}
	total := decimal.Zero
	for _, h := range held {
		price, found, err := pricing.ClosingPrice(ctx, s.pool, h.Symbol, day)
		if err != nil {
			return dv, err
		}
		if !found {
			dv.MissingPrices = append(dv.MissingPrices, h.Symbol)
			continue
		}
		total = total.Add(h.Quantity.Mul(price.Price))
	}
	if len(dv.MissingPrices) > 0 {
		sort.Strings(dv.MissingPrices)
		return dv, nil // an incomplete total would be misleading, so report none
	}
	v := total.Round(money.INRPlaces)
	dv.Value = &v
	return dv, nil
}

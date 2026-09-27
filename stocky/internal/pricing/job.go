package pricing

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
)

// Summary describes one fetch-and-store run.
type Summary struct {
	FetchedAt      time.Time
	Inserted       int      // new rows in stock_prices
	Skipped        int      // quotes we already had (same symbol and as_of)
	NewSymbols     []string // symbols registered as ACTIVE stocks this run
	MissingSymbols []string // ACTIVE stocks the vendor did not quote
}

// Job fetches prices from a Source and appends them to stock_prices.
type Job struct {
	src      Source
	pool     *pgxpool.Pool
	interval time.Duration
	log      logrus.FieldLogger
	now      func() time.Time

	// mu makes runs take turns, so the hourly tick and a manual refresh
	// cannot interleave and produce confusing summaries.
	mu sync.Mutex
}

// NewJob builds a Job that runs every interval.
func NewJob(src Source, pool *pgxpool.Pool, interval time.Duration, log logrus.FieldLogger) *Job {
	return &Job{src: src, pool: pool, interval: interval, log: log, now: time.Now}
}

// Run fetches once immediately, then every interval, until ctx is cancelled.
// A failed run is logged and the next tick tries again.
func (j *Job) Run(ctx context.Context) {
	_, _ = j.RunOnce(ctx) // errors are already logged inside RunOnce

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = j.RunOnce(ctx)
		}
	}
}

// RunOnce performs one fetch and stores the result. If the fetch fails it
// writes nothing at all: no zero prices, no nulls, no partial state.
func (j *Job) RunOnce(ctx context.Context) (Summary, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	sum := Summary{FetchedAt: j.now(), NewSymbols: []string{}, MissingSymbols: []string{}}

	quotes, err := j.src.Fetch(ctx)
	if err != nil {
		j.log.WithError(err).Warn("price fetch failed; keeping existing prices")
		return sum, err
	}

	err = db.WithTx(ctx, j.pool, func(tx pgx.Tx) error {
		quoted := make([]string, 0, len(quotes))
		for _, q := range quotes {
			quoted = append(quoted, q.Symbol)

			// Register symbols we have never seen. RowsAffected is 1 only
			// when the row was actually inserted.
			tag, err := tx.Exec(ctx,
				`INSERT INTO stocks (symbol, status) VALUES ($1, 'ACTIVE') ON CONFLICT (symbol) DO NOTHING`,
				q.Symbol)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				sum.NewSymbols = append(sum.NewSymbols, q.Symbol)
				j.log.WithField("symbol", q.Symbol).Info("new symbol registered from price feed")
			}

			// The same vendor quote fetched twice has the same as_of, so the
			// UNIQUE (symbol, as_of) constraint turns it into a no-op.
			tag, err = tx.Exec(ctx,
				`INSERT INTO stock_prices (symbol, price, as_of, fetched_at)
				 VALUES ($1, $2, $3, $4)
				 ON CONFLICT (symbol, as_of) DO NOTHING`,
				q.Symbol, q.Price, q.AsOf, sum.FetchedAt)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				sum.Inserted++
			} else {
				sum.Skipped++
			}
		}

		// ACTIVE stocks the vendor left out. We only warn: a missing quote
		// may be a vendor glitch, and delisting is a deliberate corporate
		// action, never something inferred from a feed.
		rows, err := tx.Query(ctx,
			`SELECT symbol FROM stocks WHERE status = 'ACTIVE' AND NOT (symbol = ANY($1)) ORDER BY symbol`,
			quoted)
		if err != nil {
			return err
		}
		sum.MissingSymbols, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		j.log.WithError(err).Error("storing prices failed")
		return sum, err
	}

	for _, sym := range sum.MissingSymbols {
		j.log.WithField("symbol", sym).Warn("active stock missing from price feed")
	}
	sort.Strings(sum.NewSymbols)
	j.log.WithFields(logrus.Fields{
		"inserted":        sum.Inserted,
		"skipped":         sum.Skipped,
		"new_symbols":     sum.NewSymbols,
		"missing_symbols": sum.MissingSymbols,
	}).Info("price fetch completed")
	return sum, nil
}

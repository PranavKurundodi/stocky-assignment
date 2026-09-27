// Package rewards records stock rewards and posts them to the ledger.
package rewards

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/fees"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/money"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/pricing"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/users"
)

// Errors the API maps to status codes.
var (
	ErrUnknownStock        = errors.New("unknown stock symbol")
	ErrDelisted            = errors.New("stock is delisted")
	ErrNoPrice             = errors.New("no price available at rewarded_at")
	ErrStalePrice          = errors.New("price data stale")
	ErrIdempotencyConflict = errors.New("Idempotency-Key was already used with a different request body")
	ErrNotFound            = errors.New("reward not found")
)

// Status values of a reward.
const (
	StatusActive   = "ACTIVE"
	StatusReversed = "REVERSED"
)

// Reward is a stored reward plus its cost and fees, read back from the ledger.
type Reward struct {
	ID             uuid.UUID
	IdempotencyKey string
	RequestHash    string
	UserID         string
	Symbol         string
	Quantity       decimal.Decimal
	Reason         string
	RewardedAt     time.Time
	PriceUsed      decimal.Decimal
	PriceAsOf      time.Time
	CostINR        decimal.Decimal
	Fees           fees.Breakdown
	Status         string
	ReversedAt     *time.Time
}

// Service creates and reverses rewards.
type Service struct {
	pool       *pgxpool.Pool
	rates      fees.Rates
	staleAfter time.Duration
	log        logrus.FieldLogger
	Now        func() time.Time // injectable clock; defaults to time.Now
}

// NewService builds a Service.
func NewService(pool *pgxpool.Pool, rates fees.Rates, staleAfter time.Duration, log logrus.FieldLogger) *Service {
	return &Service{pool: pool, rates: rates, staleAfter: staleAfter, log: log, Now: time.Now}
}

// errDuplicateKey signals, inside the transaction, that the idempotency key
// already exists. Returning it rolls the transaction back.
var errDuplicateKey = errors.New("duplicate idempotency key")

// Create records a reward. created is false when the request was a replay of
// an earlier one with the same key and body; the original reward is returned
// and nothing new is written.
func (s *Service) Create(ctx context.Context, in CreateInput) (rw Reward, created bool, err error) {
	// Fast path for retries: if the key is already known, answer from the
	// stored reward. This also means a replay still succeeds after the stock
	// is delisted or its price goes stale. It is only an optimisation; the
	// UNIQUE constraint below is what actually prevents duplicates.
	if existing, err := s.loadByKey(ctx, in.IdempotencyKey); err == nil {
		return s.replay(existing, in)
	} else if !errors.Is(err, ErrNotFound) {
		return Reward{}, false, err
	}

	var id uuid.UUID
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := users.MustExist(ctx, tx, in.UserID); err != nil {
			return err
		}

		// FOR SHARE locks the stock row until we commit, so a concurrent
		// delisting cannot slip in between this check and our insert.
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM stocks WHERE symbol = $1 FOR SHARE`, in.Symbol).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnknownStock
		}
		if err != nil {
			return err
		}
		if status != "ACTIVE" {
			return ErrDelisted
		}

		price, found, err := pricing.PriceAt(ctx, tx, in.Symbol, in.RewardedAt)
		if err != nil {
			return err
		}
		if !found {
			return ErrNoPrice
		}
		if pricing.IsStale(price.AsOf, in.RewardedAt, s.staleAfter) {
			return ErrStalePrice
		}

		// Round the cost once; fees are computed from the rounded cost.
		cost := in.Quantity.Mul(price.Price).Round(money.INRPlaces)
		fb := s.rates.Calculate(cost)

		// The insert is the idempotency check. If another request with the
		// same key committed first, ON CONFLICT DO NOTHING returns no row. If
		// that request is still in flight, Postgres makes us wait on the
		// unique index until it commits, so exactly one insert can win.
		id = uuid.New()
		var inserted uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO reward_events (id, idempotency_key, request_hash, user_id, symbol, quantity,
			                           reason, rewarded_at, price_used, price_as_of, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'ACTIVE')
			ON CONFLICT (idempotency_key) DO NOTHING
			RETURNING id`,
			id, in.IdempotencyKey, in.RequestHash, in.UserID, in.Symbol, in.Quantity,
			in.Reason, in.RewardedAt, price.Price, price.AsOf).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			return errDuplicateKey
		}
		if err != nil {
			return err
		}

		_, err = ledger.Post(ctx, tx, rewardPosting(id, in, cost, fb))
		return err
	})

	if errors.Is(err, errDuplicateKey) {
		existing, err := s.loadByKey(ctx, in.IdempotencyKey)
		if err != nil {
			return Reward{}, false, err
		}
		return s.replay(existing, in)
	}
	if err != nil {
		return Reward{}, false, err
	}

	rw, err = s.Get(ctx, id)
	if err != nil {
		return Reward{}, false, err
	}
	s.log.WithFields(logrus.Fields{
		"reward_id": rw.ID, "user_id": rw.UserID, "symbol": rw.Symbol,
		"quantity": money.Qty(rw.Quantity), "cost_inr": money.INR(rw.CostINR), "fees_inr": money.INR(rw.Fees.Total),
	}).Info("reward created")
	return rw, true, nil
}

// replay answers a request whose key already exists: same body returns the
// original reward, a different body is a conflict.
func (s *Service) replay(existing Reward, in CreateInput) (Reward, bool, error) {
	if existing.RequestHash != in.RequestHash {
		return Reward{}, false, ErrIdempotencyConflict
	}
	s.log.WithFields(logrus.Fields{"reward_id": existing.ID, "idempotency_key": in.IdempotencyKey}).Info("reward replayed")
	return existing, false, nil
}

// rewardPosting builds the REWARD ledger transaction:
//
//	Dr REWARD_EXPENSE  INR  cost
//	Dr FEE_*           INR  each fee component (zero ones are dropped by Post)
//	Cr COMPANY_CASH    INR  cost + total fees
//	Dr USER_STOCK(u)   SYM  quantity
//	Cr MARKET          SYM  quantity
func rewardPosting(id uuid.UUID, in CreateInput, cost decimal.Decimal, fb fees.Breakdown) ledger.Transaction {
	inr := ledger.AssetINR
	return ledger.Transaction{
		Type:        ledger.TypeReward,
		RewardID:    &id,
		EffectiveAt: in.RewardedAt,
		Description: fmt.Sprintf("reward %s %s to %s", money.Qty(in.Quantity), in.Symbol, in.UserID),
		Entries: []ledger.Entry{
			ledger.Dr(ledger.RewardExpense, inr, cost),
			ledger.Dr(ledger.FeeBrokerage, inr, fb.Brokerage),
			ledger.Dr(ledger.FeeSTT, inr, fb.STT),
			ledger.Dr(ledger.FeeExchange, inr, fb.Exchange),
			ledger.Dr(ledger.FeeSEBI, inr, fb.SEBI),
			ledger.Dr(ledger.FeeStampDuty, inr, fb.StampDuty),
			ledger.Dr(ledger.FeeGST, inr, fb.GST),
			ledger.Cr(ledger.CompanyCash, inr, cost.Add(fb.Total)),
			ledger.Dr(ledger.UserStock, in.Symbol, in.Quantity).ForUser(in.UserID),
			ledger.Cr(ledger.Market, in.Symbol, in.Quantity),
		},
	}
}

// Get loads one reward by id.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Reward, error) {
	return load(ctx, s.pool, `WHERE id = $1`, id)
}

func (s *Service) loadByKey(ctx context.Context, key string) (Reward, error) {
	return load(ctx, s.pool, `WHERE idempotency_key = $1`, key)
}

// querier is satisfied by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// load reads one reward row, then its cost and fees from the REWARD ledger
// transaction. The ledger is the source of truth for money, so the numbers
// returned on a replay are exactly the ones that were booked, even if the
// fee rates in config have changed since.
func load(ctx context.Context, q querier, where string, arg any) (Reward, error) {
	var r Reward
	err := q.QueryRow(ctx, `
		SELECT id, idempotency_key, request_hash, user_id, symbol, quantity, reason,
		       rewarded_at, price_used, price_as_of, status, reversed_at
		FROM reward_events `+where, arg).Scan(
		&r.ID, &r.IdempotencyKey, &r.RequestHash, &r.UserID, &r.Symbol, &r.Quantity, &r.Reason,
		&r.RewardedAt, &r.PriceUsed, &r.PriceAsOf, &r.Status, &r.ReversedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reward{}, ErrNotFound
	}
	if err != nil {
		return Reward{}, err
	}

	rows, err := q.Query(ctx, `
		SELECT e.account, SUM(e.inr_amount)
		FROM ledger_entries e
		JOIN ledger_transactions t ON t.id = e.transaction_id
		WHERE t.reward_id = $1 AND t.type = 'REWARD'
		  AND e.asset = 'INR' AND e.direction = 'DEBIT'
		GROUP BY e.account`, r.ID)
	if err != nil {
		return Reward{}, err
	}
	defer rows.Close()

	// Components that were zero have no ledger line; they stay zero here.
	for rows.Next() {
		var account string
		var amount decimal.Decimal
		if err := rows.Scan(&account, &amount); err != nil {
			return Reward{}, err
		}
		switch account {
		case ledger.RewardExpense:
			r.CostINR = amount
		case ledger.FeeBrokerage:
			r.Fees.Brokerage = amount
		case ledger.FeeSTT:
			r.Fees.STT = amount
		case ledger.FeeExchange:
			r.Fees.Exchange = amount
		case ledger.FeeSEBI:
			r.Fees.SEBI = amount
		case ledger.FeeStampDuty:
			r.Fees.StampDuty = amount
		case ledger.FeeGST:
			r.Fees.GST = amount
		}
	}
	if err := rows.Err(); err != nil {
		return Reward{}, err
	}
	f := r.Fees
	r.Fees.Total = f.Brokerage.Add(f.STT).Add(f.Exchange).Add(f.SEBI).Add(f.StampDuty).Add(f.GST)
	return r, nil
}

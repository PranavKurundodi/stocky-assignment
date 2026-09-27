// Package corporate applies splits, mergers and delistings. Splits and
// mergers change share counts, so they are posted to the ledger like any
// other movement; a delisting only changes the stock's status.
package corporate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/money"
)

// Action types.
const (
	Split     = "SPLIT"
	Merger    = "MERGER"
	Delisting = "DELISTING"
)

// Errors the API maps to status codes.
var (
	ErrUnknownStock = errors.New("unknown stock symbol")
	ErrNotActive    = errors.New("stock is not active")
)

// ValidationError is a request problem the client must fix (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Request is the POST /admin/corporate-actions body as sent.
type Request struct {
	Type        string          `json:"type"`
	Symbol      string          `json:"symbol"`
	NewSymbol   string          `json:"new_symbol"`
	RatioFrom   json.RawMessage `json:"ratio_from"`
	RatioTo     json.RawMessage `json:"ratio_to"`
	EffectiveAt *string         `json:"effective_at"`
}

// Action is a validated corporate action, and after Apply, the stored one.
type Action struct {
	ID              uuid.UUID
	Type            string
	Symbol          string
	NewSymbol       string          // MERGER only
	RatioFrom       decimal.Decimal // zero for DELISTING
	RatioTo         decimal.Decimal
	EffectiveAt     time.Time
	HoldersAffected int
}

// Service records and applies corporate actions.
type Service struct {
	pool *pgxpool.Pool
	log  logrus.FieldLogger
	Now  func() time.Time
}

// NewService builds a Service.
func NewService(pool *pgxpool.Pool, log logrus.FieldLogger) *Service {
	return &Service{pool: pool, log: log, Now: time.Now}
}

// Prepare validates a request. Ratios are "ratio_from:ratio_to": a 1:2
// split turns 1 share into 2; a 3:1 merger turns 3 shares of symbol into 1
// share of new_symbol.
func (s *Service) Prepare(req Request) (Action, error) {
	a := Action{
		Type:      strings.ToUpper(strings.TrimSpace(req.Type)),
		Symbol:    strings.ToUpper(strings.TrimSpace(req.Symbol)),
		NewSymbol: strings.ToUpper(strings.TrimSpace(req.NewSymbol)),
	}
	if a.Type != Split && a.Type != Merger && a.Type != Delisting {
		return a, invalid("type must be SPLIT, MERGER or DELISTING")
	}
	if a.Symbol == "" {
		return a, invalid("symbol is required")
	}

	switch a.Type {
	case Merger:
		if a.NewSymbol == "" {
			return a, invalid("new_symbol is required for a MERGER")
		}
		if a.NewSymbol == a.Symbol {
			return a, invalid("new_symbol must differ from symbol")
		}
	default:
		if a.NewSymbol != "" {
			return a, invalid("new_symbol is only allowed for a MERGER")
		}
	}

	if a.Type != Delisting {
		var err error
		if a.RatioFrom, err = money.ParsePositive(req.RatioFrom, "ratio_from", money.QuantityPlaces); err != nil {
			return a, invalid(err.Error())
		}
		if a.RatioTo, err = money.ParsePositive(req.RatioTo, "ratio_to", money.QuantityPlaces); err != nil {
			return a, invalid(err.Error())
		}
		if a.Type == Split && a.RatioFrom.Equal(a.RatioTo) {
			return a, invalid("a split needs ratio_from and ratio_to to differ")
		}
	}

	now := s.Now()
	a.EffectiveAt = now
	if req.EffectiveAt != nil {
		t, err := time.Parse(time.RFC3339, *req.EffectiveAt)
		if err != nil {
			return a, invalid("effective_at must be an RFC3339 timestamp")
		}
		if t.After(now.Add(time.Minute)) {
			return a, invalid("effective_at must not be in the future")
		}
		a.EffectiveAt = t
	}
	return a, nil
}

// Apply records the action and posts its ledger entries in one transaction.
func (s *Service) Apply(ctx context.Context, a Action) (Action, error) {
	a.ID = uuid.New()
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// FOR UPDATE on the stock rows blocks new rewards (which take FOR
		// SHARE) until this action commits, so no reward can land between
		// reading the holders and adjusting them.
		if err := lockActive(ctx, tx, a.Symbol); err != nil {
			return err
		}
		if a.Type == Merger {
			if err := lockActive(ctx, tx, a.NewSymbol); err != nil {
				return err
			}
		}

		var newSymbol *string
		var from, to *decimal.Decimal
		if a.Type == Merger {
			newSymbol = &a.NewSymbol
		}
		if a.Type != Delisting {
			from, to = &a.RatioFrom, &a.RatioTo
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO corporate_actions (id, type, symbol, new_symbol, ratio_from, ratio_to, effective_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			a.ID, a.Type, a.Symbol, newSymbol, from, to, a.EffectiveAt)
		if err != nil {
			return err
		}

		holders, err := ledger.Holders(ctx, tx, a.Symbol)
		if err != nil {
			return err
		}
		a.HoldersAffected = len(holders)

		if a.Type != Delisting && len(holders) > 0 {
			// ErrEmpty means every adjustment rounded to zero (a tiny
			// holding in a near-1:1 split): nothing to post, not an error.
			_, err := ledger.Post(ctx, tx, posting(a, holders))
			if err != nil && !errors.Is(err, ledger.ErrEmpty) {
				return err
			}
		}

		// Mergers and delistings retire the old symbol. Its holders keep
		// whatever the ledger says (nothing, after a merger).
		if a.Type == Merger || a.Type == Delisting {
			_, err := tx.Exec(ctx, `UPDATE stocks SET status = 'DELISTED', updated_at = now() WHERE symbol = $1`, a.Symbol)
			return err
		}
		return nil
	})
	if err != nil {
		return Action{}, err
	}

	s.log.WithFields(logrus.Fields{
		"id": a.ID, "type": a.Type, "symbol": a.Symbol, "new_symbol": a.NewSymbol,
		"ratio_from": a.RatioFrom.String(), "ratio_to": a.RatioTo.String(), "holders_affected": a.HoldersAffected,
	}).Info("corporate action applied")
	return a, nil
}

// lockActive locks a stock row and checks that it is ACTIVE.
func lockActive(ctx context.Context, tx pgx.Tx, symbol string) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM stocks WHERE symbol = $1 FOR UPDATE`, symbol).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrUnknownStock, symbol)
	}
	if err != nil {
		return err
	}
	if status != "ACTIVE" {
		return fmt.Errorf("%w: %s", ErrNotActive, symbol)
	}
	return nil
}

// convert returns h * to / from rounded to 6 dp. Multiplying first keeps
// full precision until the single rounding step.
func convert(h, from, to decimal.Decimal) decimal.Decimal {
	return h.Mul(to).DivRound(from, money.QuantityPlaces)
}

// holderEntry builds an entry on a holder's account.
func holderEntry(h ledger.Holder, dir ledger.Direction, asset string, qty decimal.Decimal) ledger.Entry {
	return ledger.Entry{Account: h.Account, UserID: h.UserID, Asset: asset, Direction: dir, Amount: qty}
}

// posting builds the ledger transaction for a split or merger.
//
// SPLIT from:to, per holder with holding h:
//
//	extra = h * to/from - h
//	Dr holder SYM extra ; Cr CORPORATE_ACTION SYM extra
//	(a reverse split has negative extra, so the directions flip)
//
// MERGER A into B at from:to, per holder with holding h of A:
//
//	Dr CORPORATE_ACTION A h          ; Cr holder A h
//	Dr holder B h*to/from            ; Cr CORPORATE_ACTION B h*to/from
func posting(a Action, holders []ledger.Holder) ledger.Transaction {
	t := ledger.Transaction{
		Type:              a.Type,
		CorporateActionID: &a.ID,
		EffectiveAt:       a.EffectiveAt,
	}
	switch a.Type {
	case Split:
		t.Description = fmt.Sprintf("%s split %s:%s", a.Symbol, a.RatioFrom, a.RatioTo)
		for _, h := range holders {
			extra := convert(h.Quantity, a.RatioFrom, a.RatioTo).Sub(h.Quantity)
			if extra.IsNegative() {
				t.Entries = append(t.Entries,
					holderEntry(h, ledger.Credit, a.Symbol, extra.Neg()),
					ledger.Dr(ledger.CorporateAction, a.Symbol, extra.Neg()))
			} else {
				t.Entries = append(t.Entries,
					holderEntry(h, ledger.Debit, a.Symbol, extra),
					ledger.Cr(ledger.CorporateAction, a.Symbol, extra))
			}
		}
	case Merger:
		t.Description = fmt.Sprintf("%s merged into %s at %s:%s", a.Symbol, a.NewSymbol, a.RatioFrom, a.RatioTo)
		for _, h := range holders {
			newQty := convert(h.Quantity, a.RatioFrom, a.RatioTo)
			t.Entries = append(t.Entries,
				ledger.Dr(ledger.CorporateAction, a.Symbol, h.Quantity),
				holderEntry(h, ledger.Credit, a.Symbol, h.Quantity),
				holderEntry(h, ledger.Debit, a.NewSymbol, newQty),
				ledger.Cr(ledger.CorporateAction, a.NewSymbol, newQty))
		}
	}
	return t
}

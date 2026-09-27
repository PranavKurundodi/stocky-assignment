// Package ledger is Stocky's double-entry book. Every movement of INR or of
// stock units is a transaction of debit and credit lines that must balance
// per asset. Rows are only ever inserted: corrections are new transactions.
// Holdings are never stored; they are derived by summing entries.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// AssetINR is the asset code for rupee lines. Every other asset is a stock symbol.
const AssetINR = "INR"

// Decimal places stored for each kind of amount (matches the NUMERIC columns).
const (
	INRPlaces      = 4
	QuantityPlaces = 6
)

// INR accounts.
const (
	CompanyCash         = "COMPANY_CASH"   // money Stocky spends
	RewardExpense       = "REWARD_EXPENSE" // cost of shares given away
	FeeBrokerage        = "FEE_BROKERAGE"
	FeeSTT              = "FEE_STT"
	FeeExchange         = "FEE_EXCHANGE"
	FeeSEBI             = "FEE_SEBI"
	FeeStampDuty        = "FEE_STAMP_DUTY"
	FeeGST              = "FEE_GST"
	StockInventoryValue = "STOCK_INVENTORY_VALUE" // value of shares Stocky took back
)

// Stock unit accounts.
const (
	UserStock       = "USER_STOCK"       // a user's shares; the only account with a user_id
	CompanyStock    = "COMPANY_STOCK"    // shares Stocky holds itself (from reversals)
	Market          = "MARKET"           // counterparty for shares bought on the exchange
	CorporateAction = "CORPORATE_ACTION" // counterparty for units created or removed by splits and mergers
)

// Transaction types.
const (
	TypeReward         = "REWARD"
	TypeRewardReversal = "REWARD_REVERSAL"
	TypeSplit          = "SPLIT"
	TypeMerger         = "MERGER"
)

// Direction of an entry.
type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

// Entry is one line of a transaction. Amount is always positive; Direction
// gives the sign. Amount is INR when Asset is "INR", otherwise share units.
type Entry struct {
	Account   string
	UserID    string // set only for USER_STOCK
	Asset     string
	Direction Direction
	Amount    decimal.Decimal
}

// Dr and Cr build entries for company accounts. Use ForUser for USER_STOCK.
func Dr(account, asset string, amount decimal.Decimal) Entry {
	return Entry{Account: account, Asset: asset, Direction: Debit, Amount: amount}
}

func Cr(account, asset string, amount decimal.Decimal) Entry {
	return Entry{Account: account, Asset: asset, Direction: Credit, Amount: amount}
}

// ForUser returns a copy of e attached to a user.
func (e Entry) ForUser(userID string) Entry {
	e.UserID = userID
	return e
}

// Transaction is a set of entries posted atomically.
type Transaction struct {
	Type              string
	RewardID          *uuid.UUID
	CorporateActionID *uuid.UUID
	EffectiveAt       time.Time // economic time; all holdings math uses this
	Description       string
	Entries           []Entry
}

// UnbalancedError reports an asset whose debits and credits differ.
type UnbalancedError struct {
	Asset   string
	Debits  decimal.Decimal
	Credits decimal.Decimal
}

func (e *UnbalancedError) Error() string {
	return fmt.Sprintf("ledger transaction does not balance for %s: debits %s, credits %s",
		e.Asset, e.Debits.String(), e.Credits.String())
}

// ErrEmpty means a transaction had no non-zero lines.
var ErrEmpty = errors.New("ledger transaction has no non-zero entries")

// Validate checks a transaction and returns the entries that should be
// written: zero-amount lines are dropped rather than stored. It checks, in
// order: every line is well formed, something is left after dropping zeros,
// and for each asset the debits equal the credits. INR and share units are
// separate assets, so rupees can never offset shares.
func Validate(t Transaction) ([]Entry, error) {
	kept := make([]Entry, 0, len(t.Entries))
	for _, e := range t.Entries {
		if err := checkEntry(e); err != nil {
			return nil, err
		}
		if e.Amount.IsZero() {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == 0 {
		return nil, ErrEmpty
	}

	type totals struct{ dr, cr decimal.Decimal }
	byAsset := map[string]*totals{}
	for _, e := range kept {
		t := byAsset[e.Asset]
		if t == nil {
			t = &totals{}
			byAsset[e.Asset] = t
		}
		if e.Direction == Debit {
			t.dr = t.dr.Add(e.Amount)
		} else {
			t.cr = t.cr.Add(e.Amount)
		}
	}

	// Check assets in a fixed order so the error is deterministic.
	assets := make([]string, 0, len(byAsset))
	for a := range byAsset {
		assets = append(assets, a)
	}
	sort.Strings(assets)
	for _, a := range assets {
		if t := byAsset[a]; !t.dr.Equal(t.cr) {
			return nil, &UnbalancedError{Asset: a, Debits: t.dr, Credits: t.cr}
		}
	}
	return kept, nil
}

// checkEntry validates one line on its own.
func checkEntry(e Entry) error {
	if e.Account == "" || strings.TrimSpace(e.Asset) == "" {
		return fmt.Errorf("ledger entry needs an account and an asset: %+v", e)
	}
	if e.Direction != Debit && e.Direction != Credit {
		return fmt.Errorf("ledger entry has invalid direction %q", e.Direction)
	}
	if e.Amount.IsNegative() {
		return fmt.Errorf("ledger entry amount must not be negative: %s %s", e.Account, e.Amount)
	}
	// Refuse amounts the column would silently round: NUMERIC(18,4) would
	// turn 0.00005 into 0.0001 and quietly unbalance the books.
	places := int32(QuantityPlaces)
	if e.Asset == AssetINR {
		places = INRPlaces
	}
	if !e.Amount.Equal(e.Amount.Truncate(places)) {
		return fmt.Errorf("ledger entry %s %s has more than %d decimal places: %s", e.Account, e.Asset, places, e.Amount)
	}
	if (e.Account == UserStock) != (e.UserID != "") {
		return fmt.Errorf("ledger entry %s: user_id must be set exactly for %s", e.Account, UserStock)
	}
	return nil
}

// Post validates t and writes it inside the caller's DB transaction. If
// validation fails nothing is written. It returns the new transaction id.
func Post(ctx context.Context, tx pgx.Tx, t Transaction) (uuid.UUID, error) {
	entries, err := Validate(t)
	if err != nil {
		return uuid.Nil, err
	}

	id := uuid.New()
	_, err = tx.Exec(ctx,
		`INSERT INTO ledger_transactions (id, type, reward_id, corporate_action_id, effective_at, description)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, t.Type, t.RewardID, t.CorporateActionID, t.EffectiveAt, t.Description)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert ledger transaction: %w", err)
	}

	for _, e := range entries {
		// Exactly one of quantity and inr_amount is set, by asset.
		var qty, inr *decimal.Decimal
		amount := e.Amount
		if e.Asset == AssetINR {
			inr = &amount
		} else {
			qty = &amount
		}
		var userID *string
		if e.UserID != "" {
			userID = &e.UserID
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO ledger_entries (transaction_id, account, user_id, asset, direction, quantity, inr_amount)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, e.Account, userID, e.Asset, string(e.Direction), qty, inr)
		if err != nil {
			return uuid.Nil, fmt.Errorf("insert ledger entry: %w", err)
		}
	}
	return id, nil
}

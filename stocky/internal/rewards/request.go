package rewards

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/money"
)

// maxFutureSkew is how far in the future rewarded_at may be. A little slack
// absorbs clock differences between Stocky and whoever calls it.
const maxFutureSkew = time.Minute

// validReasons are the reasons a reward can be given for.
var validReasons = map[string]bool{"ONBOARDING": true, "REFERRAL": true, "MILESTONE": true, "OTHER": true}

// Request is the POST /reward body exactly as the client sent it.
type Request struct {
	UserID     string          `json:"user_id"`
	Symbol     string          `json:"symbol"`
	Quantity   json.RawMessage `json:"quantity"` // raw so a JSON number can be rejected
	Reason     string          `json:"reason"`
	RewardedAt *string         `json:"rewarded_at"` // optional; nil means "now"
}

// CreateInput is a validated, normalized request, ready for Service.Create.
type CreateInput struct {
	IdempotencyKey string
	RequestHash    string
	UserID         string
	Symbol         string
	Quantity       decimal.Decimal
	Reason         string
	RewardedAt     time.Time
}

// ValidationError is a request problem the client must fix (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Prepare validates a request and normalizes it. now is the current time;
// it becomes rewarded_at when the client did not send one.
func Prepare(key string, req Request, now time.Time) (CreateInput, error) {
	in := CreateInput{
		IdempotencyKey: strings.TrimSpace(key),
		UserID:         strings.TrimSpace(req.UserID),
		Symbol:         strings.ToUpper(strings.TrimSpace(req.Symbol)),
		Reason:         strings.ToUpper(strings.TrimSpace(req.Reason)),
	}
	if in.IdempotencyKey == "" {
		return in, invalid("Idempotency-Key header is required")
	}
	if in.UserID == "" {
		return in, invalid("user_id is required")
	}
	if in.Symbol == "" {
		return in, invalid("symbol is required")
	}

	qty, err := money.ParseQuantity(req.Quantity)
	if err != nil {
		return in, invalid(err.Error())
	}
	in.Quantity = qty

	if !validReasons[in.Reason] {
		return in, invalid("reason must be one of ONBOARDING, REFERRAL, MILESTONE, OTHER")
	}

	in.RewardedAt = now
	if req.RewardedAt != nil {
		t, err := time.Parse(time.RFC3339, *req.RewardedAt)
		if err != nil {
			return in, invalid("rewarded_at must be an RFC3339 timestamp, e.g. 2026-09-26T10:30:00+05:30")
		}
		if t.After(now.Add(maxFutureSkew)) {
			return in, invalid("rewarded_at must not be in the future")
		}
		in.RewardedAt = t
	}

	in.RequestHash = RequestHash(in.UserID, in.Symbol, in.Quantity, in.Reason, req.RewardedAt)
	return in, nil
}

// RequestHash fingerprints a request so a reused Idempotency-Key can be
// checked against the body it was first used with. Fields are normalized
// first, so equivalent bodies hash the same: "2" and "2.000000", "tcs" and
// "TCS", or the same instant written with +05:30 or Z.
//
// rewardedAt is the value the client sent, not the resolved time. If it
// were the resolved "now", replaying a body without rewarded_at would hash
// differently every time and be wrongly rejected as a conflict.
func RequestHash(userID, symbol string, qty decimal.Decimal, reason string, rewardedAt *string) string {
	ts := ""
	if rewardedAt != nil {
		if t, err := time.Parse(time.RFC3339, *rewardedAt); err == nil {
			ts = t.UTC().Format(time.RFC3339Nano)
		}
	}
	// A JSON array of fixed fields is an unambiguous encoding: no separator
	// inside a field can make two different bodies collide.
	canonical, _ := json.Marshal([]string{userID, symbol, money.Qty(qty), reason, ts})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

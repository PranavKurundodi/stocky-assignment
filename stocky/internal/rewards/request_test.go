package rewards

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func strp(s string) *string { return &s }

func TestRequestHashIsStableForEquivalentBodies(t *testing.T) {
	base := RequestHash("user_1", "TCS", decimal.RequireFromString("2"), "REFERRAL", strp("2026-09-26T10:30:00+05:30"))

	same := []string{
		RequestHash("user_1", "TCS", decimal.RequireFromString("2.000000"), "REFERRAL", strp("2026-09-26T10:30:00+05:30")),
		RequestHash("user_1", "TCS", decimal.RequireFromString("2.0"), "REFERRAL", strp("2026-09-26T05:00:00Z")), // same instant in UTC
	}
	for i, h := range same {
		if h != base {
			t.Errorf("equivalent body %d hashed differently", i)
		}
	}

	different := []string{
		RequestHash("user_2", "TCS", decimal.RequireFromString("2"), "REFERRAL", strp("2026-09-26T10:30:00+05:30")),
		RequestHash("user_1", "INFY", decimal.RequireFromString("2"), "REFERRAL", strp("2026-09-26T10:30:00+05:30")),
		RequestHash("user_1", "TCS", decimal.RequireFromString("2.000001"), "REFERRAL", strp("2026-09-26T10:30:00+05:30")),
		RequestHash("user_1", "TCS", decimal.RequireFromString("2"), "OTHER", strp("2026-09-26T10:30:00+05:30")),
		RequestHash("user_1", "TCS", decimal.RequireFromString("2"), "REFERRAL", strp("2026-09-26T10:30:01+05:30")),
		RequestHash("user_1", "TCS", decimal.RequireFromString("2"), "REFERRAL", nil),
	}
	for i, h := range different {
		if h == base {
			t.Errorf("different body %d hashed the same", i)
		}
	}
}

func TestPrepareNormalizesAndHashes(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	a, err := Prepare("k1", Request{UserID: " user_1 ", Symbol: "tcs", Quantity: json.RawMessage(`"2"`), Reason: "referral"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Symbol != "TCS" || a.Reason != "REFERRAL" || a.UserID != "user_1" || !a.RewardedAt.Equal(now) {
		t.Errorf("not normalized: %+v", a)
	}

	// Same body without rewarded_at, a minute later: same hash, so a retry
	// of a request that relied on the default time is a replay, not a conflict.
	b, err := Prepare("k1", Request{UserID: "user_1", Symbol: "TCS", Quantity: json.RawMessage(`"2.000000"`), Reason: "REFERRAL"}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if a.RequestHash != b.RequestHash {
		t.Error("retry without rewarded_at hashed differently")
	}
}

func TestPrepareRejects(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	ok := func() Request {
		return Request{UserID: "user_1", Symbol: "TCS", Quantity: json.RawMessage(`"1"`), Reason: "OTHER"}
	}
	cases := map[string]func(*Request, *string){
		"missing key":        func(r *Request, k *string) { *k = "" },
		"missing user":       func(r *Request, k *string) { r.UserID = "" },
		"missing symbol":     func(r *Request, k *string) { r.Symbol = " " },
		"quantity number":    func(r *Request, k *string) { r.Quantity = json.RawMessage(`1`) },
		"quantity zero":      func(r *Request, k *string) { r.Quantity = json.RawMessage(`"0"`) },
		"quantity negative":  func(r *Request, k *string) { r.Quantity = json.RawMessage(`"-1"`) },
		"quantity 7 dp":      func(r *Request, k *string) { r.Quantity = json.RawMessage(`"1.0000001"`) },
		"invalid reason":     func(r *Request, k *string) { r.Reason = "BIRTHDAY" },
		"bad rewarded_at":    func(r *Request, k *string) { r.RewardedAt = strp("yesterday") },
		"rewarded_at future": func(r *Request, k *string) { r.RewardedAt = strp("2026-09-26T12:01:01Z") },
	}
	for name, mutate := range cases {
		req, key := ok(), "k1"
		mutate(&req, &key)
		_, err := Prepare(key, req, now)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}

	// Up to one minute ahead is tolerated (clock skew).
	req := ok()
	req.RewardedAt = strp("2026-09-26T12:00:59Z")
	if _, err := Prepare("k1", req, now); err != nil {
		t.Errorf("59s in the future: %v", err)
	}
}

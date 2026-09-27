package ledger

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// rewardLike is a balanced REWARD-shaped transaction: 2 TCS at 1000 plus fees.
func rewardLike() Transaction {
	return Transaction{Type: TypeReward, Entries: []Entry{
		Dr(RewardExpense, AssetINR, d("2000.0000")),
		Dr(FeeSTT, AssetINR, d("2.0000")),
		Dr(FeeBrokerage, AssetINR, d("0")), // zero line: dropped, not stored
		Cr(CompanyCash, AssetINR, d("2002.0000")),
		Dr(UserStock, "TCS", d("2")).ForUser("user_1"),
		Cr(Market, "TCS", d("2")),
	}}
}

func TestBalancedTransactionPasses(t *testing.T) {
	kept, err := Validate(rewardLike())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(kept) != 5 {
		t.Errorf("kept %d entries, want 5 (the zero brokerage line dropped)", len(kept))
	}
	for _, e := range kept {
		if e.Amount.IsZero() {
			t.Errorf("zero line kept: %+v", e)
		}
	}
}

func expectUnbalanced(t *testing.T, name string, tx Transaction, asset string) {
	t.Helper()
	_, err := Validate(tx)
	var ue *UnbalancedError
	if !errors.As(err, &ue) {
		t.Fatalf("%s: err = %v, want UnbalancedError", name, err)
	}
	if ue.Asset != asset {
		t.Errorf("%s: unbalanced asset = %s, want %s", name, ue.Asset, asset)
	}
}

func TestUnbalancedINRFails(t *testing.T) {
	tx := rewardLike()
	tx.Entries[3] = Cr(CompanyCash, AssetINR, d("2001.9999")) // one paisa-hundredth short
	expectUnbalanced(t, "INR short", tx, AssetINR)
}

func TestUnbalancedUnitsFail(t *testing.T) {
	tx := rewardLike()
	tx.Entries[5] = Cr(Market, "TCS", d("1.999999"))
	expectUnbalanced(t, "TCS short", tx, "TCS")
}

func TestINRCannotOffsetUnits(t *testing.T) {
	// Debits and credits both total 10 if you ignore the asset, but INR and
	// TCS must each balance on their own.
	tx := Transaction{Type: TypeReward, Entries: []Entry{
		Dr(RewardExpense, AssetINR, d("10")),
		Cr(Market, "TCS", d("10")),
	}}
	expectUnbalanced(t, "INR vs units", tx, AssetINR)
}

func TestDifferentStocksCannotOffset(t *testing.T) {
	tx := Transaction{Type: TypeReward, Entries: []Entry{
		Dr(UserStock, "TCS", d("1")).ForUser("user_1"),
		Cr(Market, "INFY", d("1")),
	}}
	expectUnbalanced(t, "TCS vs INFY", tx, "INFY")
}

func TestAllZeroTransactionIsRejected(t *testing.T) {
	tx := Transaction{Type: TypeSplit, Entries: []Entry{
		Dr(CompanyStock, "TCS", d("0")),
		Cr(CorporateAction, "TCS", d("0")),
	}}
	if _, err := Validate(tx); !errors.Is(err, ErrEmpty) {
		t.Errorf("err = %v, want ErrEmpty", err)
	}
}

func TestMalformedEntriesAreRejected(t *testing.T) {
	cases := map[string]Entry{
		"negative amount":         Dr(CompanyCash, AssetINR, d("-1")),
		"INR with 5 dp":           Dr(CompanyCash, AssetINR, d("1.00001")),
		"units with 7 dp":         Dr(Market, "TCS", d("1.0000001")),
		"USER_STOCK without user": Dr(UserStock, "TCS", d("1")),
		"company account w/ user": Dr(Market, "TCS", d("1")).ForUser("user_1"),
		"missing asset":           Dr(Market, "", d("1")),
		"bad direction":           {Account: Market, Asset: "TCS", Direction: "UP", Amount: d("1")},
	}
	for name, e := range cases {
		if _, err := Validate(Transaction{Entries: []Entry{e}}); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

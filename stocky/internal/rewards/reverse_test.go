package rewards_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/corporate"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/rewards"
)

func TestReversalMovesSharesButNotCashOrFees(t *testing.T) {
	svc, pool := setup(t)
	rw, _, err := svc.Create(ctx, input(t, "k1", "TCS", "1"))
	if err != nil {
		t.Fatal(err)
	}

	// Snapshot every INR account the reward touched.
	inrAccounts := []string{ledger.CompanyCash, ledger.FeeSTT, ledger.FeeExchange, ledger.FeeSEBI,
		ledger.FeeStampDuty, ledger.FeeGST, ledger.FeeBrokerage}
	before := map[string]string{}
	for _, a := range inrAccounts {
		b, _ := ledger.Balance(ctx, pool, a, "", ledger.AssetINR)
		before[a] = b.StringFixed(4)
	}

	out, err := svc.Reverse(ctx, rw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != rewards.StatusReversed || out.ReversedAt == nil || !out.ReversedAt.Equal(now) {
		t.Errorf("reversed reward = %+v", out)
	}

	// Shares left the user and landed in Stocky's inventory.
	user, _ := ledger.Balance(ctx, pool, ledger.UserStock, "user_1", "TCS")
	company, _ := ledger.Balance(ctx, pool, ledger.CompanyStock, "", "TCS")
	if !user.IsZero() || company.StringFixed(6) != "1.000000" {
		t.Errorf("user TCS = %s, company TCS = %s", user, company)
	}
	// The original cost moved from expense to inventory value.
	expense, _ := ledger.Balance(ctx, pool, ledger.RewardExpense, "", ledger.AssetINR)
	inventory, _ := ledger.Balance(ctx, pool, ledger.StockInventoryValue, "", ledger.AssetINR)
	if !expense.IsZero() || inventory.StringFixed(4) != "2000.0000" {
		t.Errorf("REWARD_EXPENSE = %s, STOCK_INVENTORY_VALUE = %s", expense, inventory)
	}
	// Cash and fees are sunk: unchanged.
	for _, a := range inrAccounts {
		b, _ := ledger.Balance(ctx, pool, a, "", ledger.AssetINR)
		if b.StringFixed(4) != before[a] {
			t.Errorf("%s changed from %s to %s", a, before[a], b.StringFixed(4))
		}
	}

	unbalanced, checked, err := ledger.Verify(ctx, pool)
	if err != nil || len(unbalanced) != 0 || checked != 2 {
		t.Errorf("verify: unbalanced = %v, checked = %d, err = %v", unbalanced, checked, err)
	}

	// Second reversal: same answer, nothing written.
	again, err := svc.Reverse(ctx, rw.ID)
	if err != nil || again.Status != rewards.StatusReversed || !again.ReversedAt.Equal(*out.ReversedAt) {
		t.Errorf("second reversal = %+v, err = %v", again, err)
	}
	if _, checked, _ := ledger.Verify(ctx, pool); checked != 2 {
		t.Errorf("ledger transactions = %d after second reversal, want 2", checked)
	}
}

func TestConcurrentReversalsPostOnce(t *testing.T) {
	svc, pool := setup(t)
	rw, _, err := svc.Create(ctx, input(t, "k1", "TCS", "1"))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = svc.Reverse(ctx, rw.ID)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("reversal %d: %v", i, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions WHERE type = 'REWARD_REVERSAL'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("reversal transactions = %d, want 1 (err %v)", n, err)
	}
	company, _ := ledger.Balance(ctx, pool, ledger.CompanyStock, "", "TCS")
	if company.StringFixed(6) != "1.000000" {
		t.Errorf("company TCS = %s, want 1 (taken back once)", company)
	}
}

func TestReverseUnknownAndAfterSplit(t *testing.T) {
	svc, pool := setup(t)
	if _, err := svc.Reverse(ctx, uuid.New()); !errors.Is(err, rewards.ErrNotFound) {
		t.Errorf("unknown id: err = %v", err)
	}

	rw, _, err := svc.Create(ctx, input(t, "k1", "TCS", "1"))
	if err != nil {
		t.Fatal(err)
	}
	log, _ := logtest.NewNullLogger()
	cs := corporate.NewService(pool, log)
	cs.Now = func() time.Time { return now.Add(time.Minute) }
	action, err := cs.Prepare(corporate.Request{
		Type: "SPLIT", Symbol: "TCS", RatioFrom: json.RawMessage(`"1"`), RatioTo: json.RawMessage(`"2"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Apply(ctx, action); err != nil {
		t.Fatal(err)
	}

	// The user now holds 2 TCS for a 1 TCS reward; a plain reversal would
	// leave them with the split bonus, so it is refused.
	if _, err := svc.Reverse(ctx, rw.ID); !errors.Is(err, rewards.ErrCannotReverse) {
		t.Errorf("reverse after split: err = %v, want ErrCannotReverse", err)
	}
}

package main

import (
	"os"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
)

func TestMain(m *testing.M) {
	if err := timeutil.Init(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestPricesAreDeterministicWithinADay(t *testing.T) {
	morning := time.Date(2026, 9, 26, 9, 10, 0, 0, timeutil.IST)
	evening := time.Date(2026, 9, 26, 20, 50, 0, 0, timeutil.IST)

	a, b := generatePrices(morning), generatePrices(evening)
	// The later run has more hours, but every hour both runs cover is identical,
	// so ON CONFLICT skips them on the second run.
	perSymbolA := len(a) / len(basePrices)
	perSymbolB := len(b) / len(basePrices)
	for s := range basePrices {
		for i := range perSymbolA {
			pa, pb := a[s*perSymbolA+i], b[s*perSymbolB+i]
			if pa.symbol != pb.symbol || !pa.asOf.Equal(pb.asOf) || !pa.price.Equal(pb.price) {
				t.Fatalf("runs differ at %s %v: %s vs %s", pa.symbol, pa.asOf, pa.price, pb.price)
			}
		}
	}
	if !a[0].asOf.Equal(time.Date(2026, 8, 27, 0, 0, 0, 0, timeutil.IST)) {
		t.Errorf("history starts at %v, want 30 days before today's IST midnight", a[0].asOf)
	}
	if last := a[perSymbolA-1].asOf; !last.Equal(time.Date(2026, 9, 26, 9, 0, 0, 0, timeutil.IST)) {
		t.Errorf("history ends at %v, want the last full hour", last)
	}
}

func TestEveryRewardHasAFreshPriceAndIsNotInTheFuture(t *testing.T) {
	now := time.Date(2026, 9, 26, 9, 10, 0, 0, timeutil.IST)
	prices := generatePrices(now)
	rs := generateRewards(now)
	if len(rs) < 15 {
		t.Fatalf("only %d rewards", len(rs))
	}

	seen := map[string]bool{}
	for _, r := range rs {
		if seen[r.key] {
			t.Errorf("duplicate key %s", r.key)
		}
		seen[r.key] = true
		if r.rewardedAt.After(now) {
			t.Errorf("%s is in the future", r.key)
		}
		// Prices are hourly, so one exists within the hour before rewarded_at.
		found := false
		for _, p := range prices {
			if p.symbol == r.symbol && !p.asOf.After(r.rewardedAt) && r.rewardedAt.Sub(p.asOf) < time.Hour {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: no price within the hour before %v", r.key, r.rewardedAt)
		}
	}
}

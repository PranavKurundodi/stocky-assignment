package main

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/shopspring/decimal"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
)

// historyDays is how far back the seeded price history goes.
const historyDays = 30

// basePrices match the mock price server's starting prices.
var basePrices = []struct {
	symbol string
	price  string
}{
	{"RELIANCE", "1200"},
	{"TCS", "2000"},
	{"INFY", "1000"},
	{"ICICIBANK", "1400"},
	{"HDFCBANK", "750"},
}

var seedUsers = []struct{ id, name string }{
	{"user_1", "Asha Rao"},
	{"user_2", "Vikram Shah"},
	{"user_3", "Meera Iyer"},
}

// seedPrice is one generated price point.
type seedPrice struct {
	symbol string
	price  decimal.Decimal
	asOf   time.Time
}

var (
	up   = decimal.RequireFromString("1.01")
	down = decimal.RequireFromString("0.99")
)

// generatePrices returns hourly prices for every base stock, starting at IST
// midnight historyDays before today and ending at the last full hour at or
// before now. Each hour moves each stock by exactly +1% or -1%.
//
// Each stock has its own random source with a fixed seed, and the window
// starts at a fixed point (midnight IST). So running the seed twice on the
// same day produces exactly the same rows for every hour both runs cover,
// which ON CONFLICT (symbol, as_of) then skips. (One shared source would not
// work: a later run generates more hours for the first stock, which would
// shift the random numbers every later stock gets.)
func generatePrices(now time.Time) []seedPrice {
	start := timeutil.StartOfDayIST(now).AddDate(0, 0, -historyDays)
	// Whole hours since start. Not now.Truncate(time.Hour): Truncate rounds
	// on UTC hour boundaries, which are :30 past the hour in IST (+05:30).
	end := start.Add(now.Sub(start).Truncate(time.Hour))
	var out []seedPrice
	for i, b := range basePrices {
		rng := rand.New(rand.NewPCG(2026, uint64(i))) // fixed seed per stock
		price := decimal.RequireFromString(b.price)
		for t := start; !t.After(end); t = t.Add(time.Hour) {
			out = append(out, seedPrice{symbol: b.symbol, price: price, asOf: t})
			if rng.IntN(2) == 0 {
				price = price.Mul(up).Round(4)
			} else {
				price = price.Mul(down).Round(4)
			}
		}
	}
	return out
}

// seedReward is one backdated reward to create through the rewards service.
type seedReward struct {
	key        string
	userID     string
	symbol     string
	quantity   string
	reason     string
	rewardedAt time.Time
}

// generateRewards returns about 15 rewards spread over the price history,
// plus one early this morning so /today-stocks has something to show. Keys
// and times are derived from today's IST date, so a rerun on the same day
// replays the same requests instead of creating new rewards.
func generateRewards(now time.Time) []seedReward {
	today := timeutil.StartOfDayIST(now)
	plan := []struct {
		user, symbol, qty, reason string
		daysAgo, hour, minute     int
	}{
		{"user_1", "RELIANCE", "2", "ONBOARDING", 28, 10, 15},
		{"user_1", "TCS", "0.5", "REFERRAL", 25, 14, 30},
		{"user_1", "INFY", "1.25", "MILESTONE", 21, 11, 0},
		{"user_1", "HDFCBANK", "3", "REFERRAL", 14, 16, 45},
		{"user_1", "RELIANCE", "0.75", "MILESTONE", 7, 9, 30},
		{"user_1", "ICICIBANK", "1", "OTHER", 2, 13, 10},
		{"user_2", "TCS", "1", "ONBOARDING", 27, 12, 0},
		{"user_2", "ICICIBANK", "2.5", "REFERRAL", 20, 15, 20},
		{"user_2", "INFY", "0.333333", "MILESTONE", 12, 10, 5},
		{"user_2", "HDFCBANK", "4", "REFERRAL", 5, 11, 40},
		{"user_2", "TCS", "0.25", "OTHER", 1, 17, 0},
		{"user_3", "INFY", "2", "ONBOARDING", 18, 9, 45},
		{"user_3", "RELIANCE", "1.5", "REFERRAL", 10, 14, 0},
		{"user_3", "ICICIBANK", "0.8", "MILESTONE", 4, 12, 30},
		{"user_3", "HDFCBANK", "1", "OTHER", 1, 10, 10},
	}

	var out []seedReward
	for i, p := range plan {
		day := today.AddDate(0, 0, -p.daysAgo)
		out = append(out, seedReward{
			key:        fmt.Sprintf("seed-%s-%02d", timeutil.ISTDate(today), i+1),
			userID:     p.user,
			symbol:     p.symbol,
			quantity:   p.qty,
			reason:     p.reason,
			rewardedAt: time.Date(day.Year(), day.Month(), day.Day(), p.hour, p.minute, 0, 0, timeutil.IST),
		})
	}

	// One reward today at 00:30 IST, once that time has passed (a price for
	// 00:00 exists by then).
	morning := today.Add(30 * time.Minute)
	if !morning.After(now) {
		out = append(out, seedReward{
			key: fmt.Sprintf("seed-%s-today", timeutil.ISTDate(today)), userID: "user_1",
			symbol: "TCS", quantity: "1", reason: "MILESTONE", rewardedAt: morning,
		})
	}
	return out
}

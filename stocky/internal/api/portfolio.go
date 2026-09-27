package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/money"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
)

// userID reads and trims the :userId path parameter.
func userID(c *gin.Context) string {
	return strings.TrimSpace(c.Param("userId"))
}

// todayStocks answers GET /today-stocks/:userId.
func (h *handlers) todayStocks(c *gin.Context) {
	uid := userID(c)
	date, list, err := h.Portfolio.Today(c.Request.Context(), uid)
	if err != nil {
		h.writeError(c, err, "user_id", uid)
		return
	}

	type item struct {
		RewardID   string `json:"reward_id"`
		Symbol     string `json:"symbol"`
		Quantity   string `json:"quantity"`
		Reason     string `json:"reason"`
		RewardedAt string `json:"rewarded_at"`
		Status     string `json:"status"`
	}
	items := make([]item, len(list))
	for i, r := range list {
		items[i] = item{
			RewardID:   r.ID.String(),
			Symbol:     r.Symbol,
			Quantity:   money.Qty(r.Quantity),
			Reason:     r.Reason,
			RewardedAt: timeutil.Format(r.RewardedAt),
			Status:     r.Status,
		}
	}
	c.JSON(http.StatusOK, gin.H{"user_id": uid, "date": date, "rewards": items})
}

// holdingJSON is one row of GET /portfolio. Price fields are null when no
// price has ever been seen for the symbol.
type holdingJSON struct {
	Symbol    string  `json:"symbol"`
	Quantity  string  `json:"quantity"`
	Price     *string `json:"price"`
	PriceAsOf *string `json:"price_as_of"`
	ValueINR  *string `json:"value_inr"`
	IsStale   bool    `json:"is_stale"`
	Delisted  bool    `json:"delisted"`
}

// portfolio answers GET /portfolio/:userId.
func (h *handlers) portfolio(c *gin.Context) {
	uid := userID(c)
	p, err := h.Portfolio.Portfolio(c.Request.Context(), uid)
	if err != nil {
		h.writeError(c, err, "user_id", uid)
		return
	}

	rows := make([]holdingJSON, len(p.Holdings))
	for i, hd := range p.Holdings {
		row := holdingJSON{Symbol: hd.Symbol, Quantity: money.Qty(hd.Quantity), IsStale: hd.IsStale, Delisted: hd.Delisted}
		if hd.Price != nil {
			price, asOf, value := money.INR(*hd.Price), timeutil.Format(*hd.PriceAsOf), money.INR(*hd.Value)
			row.Price, row.PriceAsOf, row.ValueINR = &price, &asOf, &value
		}
		rows[i] = row
	}

	body := gin.H{"user_id": uid, "holdings": rows, "total_value_inr": money.INR(p.Total)}
	if len(p.MissingPrices) > 0 {
		body["missing_prices"] = p.MissingPrices
	}
	c.JSON(http.StatusOK, body)
}

// stats answers GET /stats/:userId.
func (h *handlers) stats(c *gin.Context) {
	uid := userID(c)
	st, err := h.Portfolio.Stats(c.Request.Context(), uid)
	if err != nil {
		h.writeError(c, err, "user_id", uid)
		return
	}

	today := make(map[string]string, len(st.TodaySharesBySymbol))
	for sym, qty := range st.TodaySharesBySymbol {
		today[sym] = money.Qty(qty)
	}
	var asOf *string
	if st.Portfolio.OldestAsOf != nil {
		s := timeutil.Format(*st.Portfolio.OldestAsOf)
		asOf = &s
	}

	body := gin.H{
		"user_id":                uid,
		"today_shares_by_symbol": today,
		"current_portfolio_inr":  money.INR(st.Portfolio.Total),
		"prices_as_of":           asOf, // oldest price used; null if nothing is valued
		"is_stale":               st.Portfolio.AnyStale,
	}
	if len(st.Portfolio.MissingPrices) > 0 {
		body["missing_prices"] = st.Portfolio.MissingPrices
	}
	c.JSON(http.StatusOK, body)
}

// historicalINR answers GET /historical-inr/:userId.
func (h *handlers) historicalINR(c *gin.Context) {
	uid := userID(c)
	days, err := h.Portfolio.History(c.Request.Context(), uid)
	if err != nil {
		h.writeError(c, err, "user_id", uid)
		return
	}

	type dayJSON struct {
		Date          string   `json:"date"`
		INRValue      *string  `json:"inr_value"`
		MissingPrices []string `json:"missing_prices,omitempty"`
	}
	history := make([]dayJSON, len(days))
	for i, d := range days {
		row := dayJSON{Date: d.Date, MissingPrices: d.MissingPrices}
		if d.Value != nil {
			v := money.INR(*d.Value)
			row.INRValue = &v
		}
		history[i] = row
	}
	c.JSON(http.StatusOK, gin.H{"user_id": uid, "history": history})
}

package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/mock-price-server/internal/market"
)

// badRequest writes the common 400 error shape.
func badRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": msg})
}

// decodeJSON reads the request body into v, writing a 400 on failure.
// We use encoding/json directly rather than gin's binding so that
// json.RawMessage fields reach ParsePrice untouched.
func decodeJSON(c *gin.Context, v any) bool {
	if err := json.NewDecoder(c.Request.Body).Decode(v); err != nil {
		badRequest(c, "request body must be valid JSON")
		return false
	}
	return true
}

// overridePrice answers PUT /admin/prices/:symbol. Used to simulate stock
// splits (halve the price) or any sudden jump.
func (h *handlers) overridePrice(c *gin.Context) {
	var body struct {
		Price json.RawMessage `json:"price"`
	}
	if !decodeJSON(c, &body) {
		return
	}
	price, err := ParsePrice(body.Price)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	sym := market.NormalizeSymbol(c.Param("symbol"))
	old, updated, err := h.Market.SetPrice(sym, price)
	if errors.Is(err, market.ErrUnknownSymbol) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error(), "symbol": sym})
		return
	}

	h.Log.WithFields(logrus.Fields{
		"symbol": sym,
		"old":    old.Price.StringFixed(market.PricePlaces),
		"new":    updated.Price.StringFixed(market.PricePlaces),
	}).Info("price overridden")
	c.JSON(http.StatusOK, h.toJSON(updated))
}

// addStock answers POST /admin/stocks. Used to simulate a new listing or the
// surviving company of a merger.
func (h *handlers) addStock(c *gin.Context) {
	var body struct {
		Symbol string          `json:"symbol"`
		Price  json.RawMessage `json:"price"`
	}
	if !decodeJSON(c, &body) {
		return
	}
	sym, err := ValidateSymbol(body.Symbol)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	price, err := ParsePrice(body.Price)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	q, err := h.Market.Add(sym, price)
	if errors.Is(err, market.ErrSymbolExists) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "symbol": sym})
		return
	}

	h.Log.WithFields(logrus.Fields{
		"symbol": sym,
		"price":  q.Price.StringFixed(market.PricePlaces),
	}).Info("stock listed")
	c.JSON(http.StatusCreated, h.toJSON(q))
}

// removeStock answers DELETE /admin/stocks/:symbol. Used to simulate a
// delisting or the acquired company in a merger.
func (h *handlers) removeStock(c *gin.Context) {
	sym := market.NormalizeSymbol(c.Param("symbol"))
	if err := h.Market.Remove(sym); errors.Is(err, market.ErrUnknownSymbol) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error(), "symbol": sym})
		return
	}

	h.Log.WithField("symbol", sym).Info("stock delisted")
	c.Status(http.StatusNoContent)
}

// startOutage answers POST /admin/fail. It is idempotent: calling it while already
// down changes nothing. The market keeps ticking during the outage.
func (h *handlers) startOutage(c *gin.Context) {
	// Swap returns the previous value, so we only log the actual transition.
	if !h.Down.Swap(true) {
		h.Log.Warn("outage simulation started")
	}
	c.JSON(http.StatusOK, gin.H{"down": true})
}

// endOutage answers POST /admin/recover. Also idempotent. All state (prices,
// overrides, added and removed stocks) is untouched by an outage.
func (h *handlers) endOutage(c *gin.Context) {
	if h.Down.Swap(false) {
		h.Log.Warn("outage simulation ended")
	}
	c.JSON(http.StatusOK, gin.H{"down": false})
}

// state answers GET /admin/state with the full simulator state.
func (h *handlers) state(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"down":          h.Down.Load(),
		"tick_interval": h.Market.Interval().String(),
		"prices":        h.allJSON(),
	})
}

package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/PranavKurundodi/stocky-assignment/mock-price-server/internal/market"
)

// health answers GET /health. The outage middleware in front of it answers
// 503 while down, so reaching this handler means the server is up.
func (h *handlers) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// listPrices answers GET /prices with every quote sorted by symbol.
func (h *handlers) listPrices(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"prices": h.allJSON()})
}

// getPrice answers GET /prices/:symbol.
func (h *handlers) getPrice(c *gin.Context) {
	sym := market.NormalizeSymbol(c.Param("symbol"))
	q, err := h.Market.Get(sym)
	if errors.Is(err, market.ErrUnknownSymbol) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error(), "symbol": sym})
		return
	}
	c.JSON(http.StatusOK, h.toJSON(q))
}

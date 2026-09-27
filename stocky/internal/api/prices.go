package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/pricing"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
)

// refreshPrices answers POST /admin/prices/refresh: one fetch, right now.
func (h *handlers) refreshPrices(c *gin.Context) {
	sum, err := h.Prices.RunOnce(c.Request.Context())
	if errors.Is(err, pricing.ErrUnavailable) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "price service unavailable"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "price refresh failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"fetched_at":      timeutil.Format(sum.FetchedAt),
		"inserted":        sum.Inserted,
		"skipped":         sum.Skipped,
		"new_symbols":     sum.NewSymbols,
		"missing_symbols": sum.MissingSymbols,
	})
}

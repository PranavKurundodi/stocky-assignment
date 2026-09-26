// Package api wires the HTTP routes of the mock price server.
package api

import (
	"io"
	"net/http"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/mock-price-server/internal/market"
	"github.com/PranavKurundodi/stocky-assignment/mock-price-server/internal/middleware"
)

// Deps is everything the handlers need. main builds one; tests build their
// own with a fake clock and a null logger.
type Deps struct {
	Market *market.Market
	// Down is shared with the outage middleware so that flipping it through
	// the admin API changes behaviour on the very next request.
	Down       *atomic.Bool
	IST        *time.Location
	AdminToken string
	Log        *logrus.Logger
}

// NewRouter builds the gin engine with every route and middleware in place.
//
// Middleware order per route group:
//
//	/prices*  logger -> recovery -> outage -> handler
//	/health   logger -> recovery -> outage -> handler
//	/admin/*  logger -> recovery -> admin token -> handler
//
// The logger is outermost so it also records requests that panicked. Admin
// routes skip the outage check so an outage can always be ended.
func NewRouter(d Deps) *gin.Engine {
	h := &handlers{Deps: d}

	// gin.New, not gin.Default, so gin's own stdout logger is never installed.
	r := gin.New()
	r.Use(middleware.Logger(d.Log))
	// gin's recovery middleware, but reporting through logrus instead of
	// gin's default stderr writer. It runs inside the deferred recover, so
	// debug.Stack still shows where the panic happened.
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		d.Log.WithFields(logrus.Fields{"panic": err, "stack": string(debug.Stack())}).Error("panic recovered")
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}))
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "route not found"})
	})

	r.GET("/health", middleware.Outage(d.Down, gin.H{"status": "down"}), h.health)

	prices := r.Group("/prices", middleware.Outage(d.Down, gin.H{"error": "price service unavailable"}))
	prices.GET("", h.listPrices)
	prices.GET("/:symbol", h.getPrice)

	admin := r.Group("/admin", middleware.AdminToken(d.AdminToken))
	admin.PUT("/prices/:symbol", h.overridePrice)
	admin.POST("/stocks", h.addStock)
	admin.DELETE("/stocks/:symbol", h.removeStock)
	admin.POST("/fail", h.startOutage)
	admin.POST("/recover", h.endOutage)
	admin.GET("/state", h.state)

	return r
}

// handlers groups the route handlers so they share Deps.
type handlers struct {
	Deps
}

// quoteJSON is the wire format of one quote. Price is a string with exactly
// 4 decimal places and AsOf is RFC3339 in IST, e.g. 2026-09-26T10:31:00+05:30.
type quoteJSON struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
	AsOf   string `json:"as_of"`
}

func (h *handlers) toJSON(q market.Quote) quoteJSON {
	return quoteJSON{
		Symbol: q.Symbol,
		Price:  q.Price.StringFixed(market.PricePlaces),
		AsOf:   q.AsOf.In(h.IST).Format(time.RFC3339),
	}
}

func (h *handlers) allJSON() []quoteJSON {
	all := h.Market.All()
	out := make([]quoteJSON, len(all))
	for i, q := range all {
		out[i] = h.toJSON(q)
	}
	return out
}

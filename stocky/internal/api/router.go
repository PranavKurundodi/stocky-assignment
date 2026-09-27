// Package api wires Stocky's HTTP routes. Handlers parse the request, call a
// service and map its errors to status codes; the logic lives in the services.
package api

import (
	"context"
	"io"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/corporate"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/middleware"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/portfolio"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/pricing"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/rewards"
)

// Pinger is the part of the database pool /health needs. The real
// *pgxpool.Pool satisfies it; tests pass a fake.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Refresher runs one price fetch. *pricing.Job satisfies it.
type Refresher interface {
	RunOnce(ctx context.Context) (pricing.Summary, error)
}

// Deps is everything the handlers need. main builds one; tests build their own.
type Deps struct {
	DB        Pinger        // for /health only, so its test can use a fake
	Pool      *pgxpool.Pool // for the simple queries with no service of their own
	Prices    Refresher
	Rewards   *rewards.Service
	Portfolio *portfolio.Service
	Corporate *corporate.Service
	Log       *logrus.Logger
}

// NewRouter builds the gin engine with every route and middleware in place.
func NewRouter(d Deps) *gin.Engine {
	h := &handlers{Deps: d}

	// gin.New, not gin.Default, so gin's own stdout logger is never installed.
	r := gin.New()
	// The logger is outermost so it also records requests that panicked.
	r.Use(middleware.Logger(d.Log))
	// gin's recovery middleware, reporting through logrus instead of gin's
	// default stderr writer. It runs inside the deferred recover, so
	// debug.Stack still shows where the panic happened.
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		d.Log.WithFields(logrus.Fields{"panic": err, "stack": string(debug.Stack())}).Error("panic recovered")
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}))
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "route not found"})
	})

	r.GET("/health", h.health)
	r.POST("/users", h.createUser)
	r.POST("/reward", h.createReward)
	r.POST("/reward/:id/reverse", h.reverseReward)
	r.GET("/today-stocks/:userId", h.todayStocks)
	r.GET("/stats/:userId", h.stats)
	r.GET("/portfolio/:userId", h.portfolio)
	r.GET("/historical-inr/:userId", h.historicalINR)

	admin := r.Group("/admin")
	admin.POST("/prices/refresh", h.refreshPrices)
	admin.POST("/corporate-actions", h.createCorporateAction)
	admin.GET("/ledger/verify", h.verifyLedger)

	return r
}

// handlers groups the route handlers so they share Deps.
type handlers struct {
	Deps
}

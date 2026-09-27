// Command server runs the Stocky API: stock rewards, a double-entry ledger
// and INR valuations from an hourly price feed.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embed zone data so Asia/Kolkata loads without system tz files

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/api"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/config"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/corporate"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/fees"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/portfolio"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/pricing"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/rewards"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
)

const (
	// shutdownTimeout bounds how long we wait for in-flight requests on exit.
	shutdownTimeout = 5 * time.Second
	// dbConnectTimeout bounds the startup connection attempt.
	dbConnectTimeout = 10 * time.Second
)

func main() {
	log := logrus.StandardLogger()

	// The zone comes first: every log line and response needs it.
	if err := timeutil.Init(); err != nil {
		log.WithError(err).Fatal("cannot load Asia/Kolkata time zone")
	}
	log.SetFormatter(&timeutil.LogFormatter{
		Inner: &logrus.TextFormatter{FullTimestamp: true, TimestampFormat: time.RFC3339},
	})

	// Only a .env in the current directory (stocky/) is loaded. It never
	// overrides variables already set in the real environment, and the
	// server runs fine without one.
	envLoaded := false
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(".env"); err != nil {
			log.WithError(err).Fatal("cannot parse .env")
		}
		envLoaded = true
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.WithError(err).Fatal("invalid configuration")
	}
	log.SetLevel(cfg.LogLevel)
	if envLoaded {
		log.Info("loaded .env")
	}

	gin.SetMode(cfg.GinMode)
	// In debug mode gin prints route tables and warnings straight to stdout.
	// Send those through logrus too so every line has the same format.
	gin.DebugPrintFunc = func(format string, values ...any) {
		log.Debugf("gin: "+strings.TrimSpace(format), values...)
	}
	gin.DebugPrintRouteFunc = func(method, path, handler string, _ int) {
		log.WithFields(logrus.Fields{"method": method, "path": path, "handler": handler}).Debug("route registered")
	}

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), dbConnectTimeout)
	pool, err := db.NewPool(connectCtx, cfg.DatabaseURL)
	cancelConnect()
	if err != nil {
		log.WithError(err).Fatal("cannot connect to database")
	}
	log.Info("connected to database")

	version, err := db.MigrateUp(cfg.DatabaseURL)
	if err != nil {
		log.WithError(err).Fatal("cannot apply migrations")
	}
	log.WithField("version", version).Info("migrations applied")

	// jobCtx is cancelled during shutdown, after the HTTP server has stopped,
	// to stop background work such as the price job.
	jobCtx, cancelJobs := context.WithCancel(context.Background())

	priceClient := pricing.NewClient(cfg.PriceServerURL, cfg.PriceClientTimeout, log)
	priceJob := pricing.NewJob(priceClient, pool, cfg.PriceFetchInterval, log)
	jobDone := make(chan struct{})
	go func() {
		priceJob.Run(jobCtx) // fetches once now, then every PRICE_FETCH_INTERVAL
		close(jobDone)
	}()

	rewardService := rewards.NewService(pool, fees.DefaultRates(cfg.FeeBrokerageFlat), cfg.PriceStaleAfter, log)

	portfolioService := portfolio.NewService(pool, cfg.PriceStaleAfter)

	corporateService := corporate.NewService(pool, log)

	router := api.NewRouter(api.Deps{
		DB:        pool,
		Pool:      pool,
		Corporate: corporateService,
		Prices:    priceJob,
		Rewards:   rewardService,
		Portfolio: portfolioService,
		Log:       log,
	})
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// ctx is cancelled on Ctrl+C or SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.WithFields(logrus.Fields{
		"port":                 cfg.Port,
		"price_server_url":     cfg.PriceServerURL,
		"price_fetch_interval": cfg.PriceFetchInterval.String(),
		"price_stale_after":    cfg.PriceStaleAfter.String(),
		"log_level":            cfg.LogLevel.String(),
		"gin_mode":             cfg.GinMode,
	}).Info("stocky starting")

	// ListenAndServe blocks, so run it in a goroutine and report its error
	// on a channel. ErrServerClosed is the normal result of Shutdown.
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		log.WithError(err).Error("http server failed")
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// Shutdown order: stop accepting requests and drain in-flight ones,
	// then stop background jobs, then close the pool they were all using.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.WithError(err).Error("graceful shutdown failed")
	}
	cancelJobs()
	<-jobDone // let an in-progress fetch finish before closing the pool it uses
	pool.Close()
	log.Info("server stopped")
}

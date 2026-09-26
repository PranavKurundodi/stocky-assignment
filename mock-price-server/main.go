// Command mock-price-server pretends to be an external stock price vendor.
// It keeps NSE prices in memory, moves them on a timer and serves them over
// HTTP, with admin endpoints to simulate outages, price jumps and listings.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // embed zone data so Asia/Kolkata loads even without system tz files

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/mock-price-server/internal/api"
	"github.com/PranavKurundodi/stocky-assignment/mock-price-server/internal/market"
)

// shutdownTimeout bounds how long we wait for in-flight requests on exit.
const shutdownTimeout = 5 * time.Second

func main() {
	log := logrus.StandardLogger()

	// Load the zone before anything else: every log line and response needs it.
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		log.WithError(err).Fatal("cannot load Asia/Kolkata time zone")
	}
	log.SetFormatter(&istFormatter{
		loc:   ist,
		inner: &logrus.TextFormatter{FullTimestamp: true, TimestampFormat: time.RFC3339},
	})

	envFile := loadDotEnv()
	cfg, err := loadConfig()
	if err != nil {
		log.WithError(err).Fatal("invalid configuration")
	}
	log.SetLevel(cfg.LogLevel)
	if envFile != "" {
		log.WithField("file", envFile).Info("loaded .env")
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

	var down atomic.Bool
	down.Store(cfg.StartDown)

	m := market.New(market.BasePrices(), market.Options{Interval: cfg.TickInterval, Log: log})

	// ctx is cancelled on Ctrl+C or SIGTERM, which stops the ticker.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go m.Run(ctx)

	router := api.NewRouter(api.Deps{
		Market:     m,
		Down:       &down,
		IST:        ist,
		AdminToken: cfg.AdminToken,
		Log:        log,
	})

	srv := &http.Server{
		Addr:    ":" + strconv.Itoa(cfg.Port),
		Handler: router,
		// Every response is built from memory in microseconds, so these
		// limits only ever cut off clients that send or read too slowly.
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	log.WithFields(logrus.Fields{
		"port":            cfg.Port,
		"tick_interval":   cfg.TickInterval.String(),
		"start_down":      cfg.StartDown,
		"admin_token_set": cfg.AdminToken != "",
		"gin_mode":        cfg.GinMode,
		"log_level":       cfg.LogLevel.String(),
	}).Info("mock price server starting")
	if cfg.StartDown {
		log.Warn("outage simulation started")
	}

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
		log.WithError(err).Fatal("http server failed")
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.WithError(err).Error("graceful shutdown failed")
		return
	}
	log.Info("server stopped")
}

// istFormatter wraps another formatter and converts each entry's timestamp
// to IST first, so logs never depend on the machine's TZ setting.
type istFormatter struct {
	loc   *time.Location
	inner logrus.Formatter
}

func (f *istFormatter) Format(e *logrus.Entry) ([]byte, error) {
	// Each log call gets its own Entry, so changing its Time here is safe.
	e.Time = e.Time.In(f.loc)
	return f.inner.Format(e)
}

// loadDotEnv loads the first .env file it finds. The repo keeps .env at its
// root, so "../.env" covers running from inside mock-price-server/. godotenv
// never overwrites variables that are already set, so real environment
// variables win over the file. It returns the file used, or "".
func loadDotEnv() string {
	for _, path := range []string{".env", "../.env"} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := godotenv.Load(path); err != nil {
			logrus.WithError(err).WithField("file", path).Fatal("cannot parse .env")
		}
		return path
	}
	return ""
}

type config struct {
	Port         int
	TickInterval time.Duration
	StartDown    bool
	LogLevel     logrus.Level
	GinMode      string
	AdminToken   string
}

// loadConfig reads every setting from the environment, falling back to the
// defaults below, and rejects anything that does not parse.
func loadConfig() (config, error) {
	var cfg config
	var err error

	if cfg.Port, err = strconv.Atoi(env("PRICE_SERVER_PORT", "8081")); err != nil || cfg.Port < 1 || cfg.Port > 65535 {
		return cfg, fmt.Errorf("PRICE_SERVER_PORT must be a port number between 1 and 65535")
	}
	if cfg.TickInterval, err = time.ParseDuration(env("PRICE_TICK_INTERVAL", "1m")); err != nil || cfg.TickInterval <= 0 {
		return cfg, fmt.Errorf("PRICE_TICK_INTERVAL must be a positive duration such as 30s or 1m")
	}
	if cfg.StartDown, err = strconv.ParseBool(env("PRICE_SERVER_START_DOWN", "false")); err != nil {
		return cfg, fmt.Errorf("PRICE_SERVER_START_DOWN must be true or false")
	}
	if cfg.LogLevel, err = logrus.ParseLevel(env("LOG_LEVEL", "debug")); err != nil {
		return cfg, fmt.Errorf("LOG_LEVEL must be one of trace, debug, info, warn, error")
	}
	switch cfg.GinMode = env("GIN_MODE", gin.DebugMode); cfg.GinMode {
	case gin.DebugMode, gin.ReleaseMode, gin.TestMode:
	default:
		return cfg, fmt.Errorf("GIN_MODE must be debug, release or test")
	}
	cfg.AdminToken = os.Getenv("ADMIN_TOKEN")
	return cfg, nil
}

// env returns the trimmed value of key, or def if it is unset or blank.
func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

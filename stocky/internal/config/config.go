// Package config reads Stocky's settings from environment variables.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

// Config is every setting the server needs. Defaults are applied by Load.
type Config struct {
	Port               int
	DatabaseURL        string
	PriceServerURL     string
	PriceFetchInterval time.Duration
	PriceStaleAfter    time.Duration
	PriceClientTimeout time.Duration
	FeeBrokerageFlat   decimal.Decimal
	LogLevel           logrus.Level
	GinMode            string
}

// Load builds a Config. getenv is normally os.Getenv; tests pass a map
// lookup instead. Unset or blank variables take the default, and any value
// that does not parse is an error so the server fails fast at startup.
func Load(getenv func(string) string) (Config, error) {
	var cfg Config
	var err error

	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	if cfg.Port, err = strconv.Atoi(get("STOCKY_PORT", "8080")); err != nil || cfg.Port < 1 || cfg.Port > 65535 {
		return cfg, fmt.Errorf("STOCKY_PORT must be a port number between 1 and 65535")
	}

	cfg.DatabaseURL = get("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/assignment?sslmode=disable")

	cfg.PriceServerURL = strings.TrimRight(get("PRICE_SERVER_URL", "http://localhost:8081"), "/")
	if u, err := url.Parse(cfg.PriceServerURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return cfg, fmt.Errorf("PRICE_SERVER_URL must be an http(s) URL such as http://localhost:8081")
	}

	durations := []struct {
		key, def string
		dst      *time.Duration
	}{
		{"PRICE_FETCH_INTERVAL", "1h", &cfg.PriceFetchInterval},
		{"PRICE_STALE_AFTER", "2h", &cfg.PriceStaleAfter},
		{"PRICE_CLIENT_TIMEOUT", "5s", &cfg.PriceClientTimeout},
	}
	for _, d := range durations {
		v, err := time.ParseDuration(get(d.key, d.def))
		if err != nil || v <= 0 {
			return cfg, fmt.Errorf("%s must be a positive duration such as %s", d.key, d.def)
		}
		*d.dst = v
	}

	if cfg.FeeBrokerageFlat, err = decimal.NewFromString(get("FEE_BROKERAGE_FLAT", "20")); err != nil || cfg.FeeBrokerageFlat.IsNegative() {
		return cfg, fmt.Errorf("FEE_BROKERAGE_FLAT must be a non-negative decimal such as 0 or 20")
	}

	if cfg.LogLevel, err = logrus.ParseLevel(get("LOG_LEVEL", "info")); err != nil {
		return cfg, fmt.Errorf("LOG_LEVEL must be one of trace, debug, info, warn, error")
	}

	switch cfg.GinMode = get("GIN_MODE", gin.DebugMode); cfg.GinMode {
	case gin.DebugMode, gin.ReleaseMode, gin.TestMode:
	default:
		return cfg, fmt.Errorf("GIN_MODE must be debug, release or test")
	}

	return cfg, nil
}

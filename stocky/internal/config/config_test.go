package config

import (
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.PriceServerURL != "http://localhost:8081" ||
		cfg.PriceFetchInterval != time.Hour || cfg.PriceStaleAfter != 2*time.Hour ||
		cfg.PriceClientTimeout != 5*time.Second || cfg.FeeBrokerageFlat.String() != "20" ||
		cfg.LogLevel != logrus.InfoLevel || cfg.GinMode != "debug" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestInvalidValuesFail(t *testing.T) {
	bad := map[string]string{
		"STOCKY_PORT":          "abc",
		"PRICE_SERVER_URL":     "localhost:8081",
		"PRICE_FETCH_INTERVAL": "0s",
		"PRICE_STALE_AFTER":    "banana",
		"PRICE_CLIENT_TIMEOUT": "-1s",
		"FEE_BROKERAGE_FLAT":   "-20",
		"LOG_LEVEL":            "loud",
		"GIN_MODE":             "prod",
	}
	for k, v := range bad {
		if _, err := Load(env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%q: expected error", k, v)
		}
	}
}

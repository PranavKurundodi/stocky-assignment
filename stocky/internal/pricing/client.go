// Package pricing fetches stock prices from the vendor, stores them, and
// answers "what was the price of X at time t" questions.
package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

// ErrUnavailable means the vendor could not be reached or answered with a
// non-2xx status on every attempt. Callers treat outages, timeouts and
// refused connections the same way, so they all map to this one error.
var ErrUnavailable = errors.New("price service unavailable")

// Quote is one vendor price.
type Quote struct {
	Symbol string
	Price  decimal.Decimal
	AsOf   time.Time // the vendor's timestamp, not ours
}

// Source is anything that can fetch the current quotes. The HTTP Client is
// the real one; tests use a fake.
type Source interface {
	Fetch(ctx context.Context) ([]Quote, error)
}

// Client fetches GET {baseURL}/prices over HTTP.
type Client struct {
	baseURL string
	http    *http.Client
	log     logrus.FieldLogger

	// MaxAttempts and Backoff control retries. Backoff[i] is the wait after
	// failed attempt i+1. With 3 attempts only the first two waits (1s, 2s)
	// are used; 4s is there for anyone who raises MaxAttempts. Tests set
	// Backoff to zeros so they run instantly.
	MaxAttempts int
	Backoff     []time.Duration
}

// NewClient builds a Client. timeout bounds each attempt, not the total.
func NewClient(baseURL string, timeout time.Duration, log logrus.FieldLogger) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		http:        &http.Client{Timeout: timeout},
		log:         log,
		MaxAttempts: 3,
		Backoff:     []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
	}
}

// Fetch returns the vendor's current quotes, retrying transient failures.
func (c *Client) Fetch(ctx context.Context) ([]Quote, error) {
	var lastErr error
	for attempt := 1; attempt <= c.MaxAttempts; attempt++ {
		quotes, retryable, err := c.fetchOnce(ctx)
		if err == nil {
			return quotes, nil
		}
		// Our own caller gave up (shutdown, request cancelled): stop now.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retryable {
			return nil, err
		}
		lastErr = err
		c.log.WithError(err).WithField("attempt", attempt).Warn("price fetch attempt failed")

		if attempt == c.MaxAttempts {
			break
		}
		// Wait before the next attempt, but wake up at once on cancellation.
		timer := time.NewTimer(c.backoff(attempt))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
}

func (c *Client) backoff(attempt int) time.Duration {
	if attempt-1 < len(c.Backoff) {
		return c.Backoff[attempt-1]
	}
	if len(c.Backoff) > 0 {
		return c.Backoff[len(c.Backoff)-1]
	}
	return 0
}

// pricesResponse is the vendor's wire format. Prices and times arrive as
// strings and are parsed explicitly; nothing goes through float64.
type pricesResponse struct {
	Prices []struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
		AsOf   string `json:"as_of"`
	} `json:"prices"`
}

// fetchOnce makes a single request. retryable reports whether trying again
// could help: network errors, timeouts and non-2xx are retryable; a 200 with
// a body we cannot parse is not.
func (c *Client) fetchOnce(ctx context.Context) (quotes []Quote, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/prices", nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, err // refused, reset, DNS, timeout
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Drain a little of the body so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, true, fmt.Errorf("vendor returned HTTP %d", resp.StatusCode)
	}

	var body pricesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, false, fmt.Errorf("decode vendor response: %w", err)
	}

	quotes = make([]Quote, 0, len(body.Prices))
	for _, p := range body.Prices {
		q, err := parseQuote(p.Symbol, p.Price, p.AsOf)
		if err != nil {
			// One bad row should not throw away the good ones, and it must
			// never become a zero or null price in our table.
			c.log.WithError(err).WithField("symbol", p.Symbol).Warn("skipping invalid vendor quote")
			continue
		}
		quotes = append(quotes, q)
	}
	return quotes, false, nil
}

func parseQuote(symbol, price, asOf string) (Quote, error) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if sym == "" {
		return Quote{}, errors.New("empty symbol")
	}
	p, err := decimal.NewFromString(price)
	if err != nil {
		return Quote{}, fmt.Errorf("price %q is not a decimal", price)
	}
	if !p.IsPositive() {
		return Quote{}, fmt.Errorf("price %q is not positive", price)
	}
	t, err := time.Parse(time.RFC3339, asOf)
	if err != nil {
		return Quote{}, fmt.Errorf("as_of %q is not RFC3339", asOf)
	}
	return Quote{Symbol: sym, Price: p.Round(4), AsOf: t}, nil
}

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/corporate"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/fees"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/portfolio"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/rewards"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/testdb"
)

// testNow is the fixed clock for DB-backed API tests: 12:00 IST, Sep 26.
var testNow = time.Date(2026, 9, 26, 6, 30, 0, 0, time.UTC)

// dbServer builds the real router over the test database with user_1 and a
// fresh TCS price.
type dbServer struct {
	router *gin.Engine
	pool   *pgxpool.Pool
}

func newDBServer(t *testing.T) *dbServer {
	t.Helper()
	pool := testdb.Pool(t)
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users (id, name) VALUES ('user_1', 'One')`)
	exec(`INSERT INTO stock_prices (symbol, price, as_of, fetched_at) VALUES ('TCS', 2000, $1, $1)`, testNow.Add(-10*time.Minute))
	exec(`INSERT INTO stock_prices (symbol, price, as_of, fetched_at) VALUES ('HDFCBANK', 750, $1, $1)`, testNow.Add(-10*time.Minute))
	exec(`INSERT INTO stock_prices (symbol, price, as_of, fetched_at) VALUES ('INFY', 1000, $1, $1)`, testNow.Add(-5*time.Hour))
	exec(`UPDATE stocks SET status = 'DELISTED' WHERE symbol = 'HDFCBANK'`)

	log, _ := logtest.NewNullLogger()
	svc := rewards.NewService(pool, fees.DefaultRates(decimal.Zero), 2*time.Hour, log)
	svc.Now = func() time.Time { return testNow }
	ps := portfolio.NewService(pool, 2*time.Hour)
	ps.Now = func() time.Time { return testNow }
	cs := corporate.NewService(pool, log)
	cs.Now = func() time.Time { return testNow }
	return &dbServer{router: NewRouter(Deps{DB: pool, Pool: pool, Rewards: svc, Portfolio: ps, Corporate: cs, Log: log}), pool: pool}
}

func (s *dbServer) get(path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func (s *dbServer) post(path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

const tcsBody = `{"user_id":"user_1","symbol":"TCS","quantity":"1","reason":"REFERRAL"}`

func TestPostRewardCreatesThenReplays(t *testing.T) {
	s := newDBServer(t)

	w := s.post("/reward", "key-1", tcsBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	var got rewardJSON
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Quantity != "1.000000" || got.PriceUsed != "2000.0000" || got.CostINR != "2000.0000" ||
		got.Fees.Total != "2.3725" || got.Status != "ACTIVE" || got.ReversedAt != nil ||
		got.RewardedAt != "2026-09-26T12:00:00+05:30" || got.PriceAsOf != "2026-09-26T11:50:00+05:30" {
		t.Errorf("unexpected body: %s", w.Body.String())
	}

	// Replay: 200 with the identical body.
	w2 := s.post("/reward", "key-1", tcsBody)
	if w2.Code != http.StatusOK || w2.Body.String() != w.Body.String() {
		t.Errorf("replay: %d %s", w2.Code, w2.Body.String())
	}

	// Different body under the same key: 409.
	w3 := s.post("/reward", "key-1", strings.Replace(tcsBody, `"1"`, `"2"`, 1))
	if w3.Code != http.StatusConflict {
		t.Errorf("conflict: %d %s", w3.Code, w3.Body.String())
	}

	var n int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM reward_events`).Scan(&n); err != nil || n != 1 {
		t.Errorf("reward rows = %d, err = %v", n, err)
	}
}

func TestPostRewardStatusCodes(t *testing.T) {
	s := newDBServer(t)
	body := func(fields string) string { return `{"reason":"OTHER",` + fields + `}` }

	cases := []struct {
		name, key, body string
		want            int
	}{
		{"missing key", "", tcsBody, http.StatusBadRequest},
		{"malformed JSON", "k", `{"user_id":`, http.StatusBadRequest},
		{"quantity number", "k", body(`"user_id":"user_1","symbol":"TCS","quantity":1`), http.StatusBadRequest},
		{"quantity zero", "k", body(`"user_id":"user_1","symbol":"TCS","quantity":"0"`), http.StatusBadRequest},
		{"quantity 7 dp", "k", body(`"user_id":"user_1","symbol":"TCS","quantity":"0.0000001"`), http.StatusBadRequest},
		{"bad reason", "k", `{"user_id":"user_1","symbol":"TCS","quantity":"1","reason":"GIFT"}`, http.StatusBadRequest},
		{"future", "k", body(`"user_id":"user_1","symbol":"TCS","quantity":"1","rewarded_at":"2026-09-26T12:05:00+05:30"`), http.StatusBadRequest},
		{"unknown user", "k1", body(`"user_id":"ghost","symbol":"TCS","quantity":"1"`), http.StatusNotFound},
		{"unknown symbol", "k2", body(`"user_id":"user_1","symbol":"FAKECORP","quantity":"1"`), http.StatusNotFound},
		{"delisted", "k3", body(`"user_id":"user_1","symbol":"HDFCBANK","quantity":"1"`), http.StatusUnprocessableEntity},
		{"no price", "k4", body(`"user_id":"user_1","symbol":"RELIANCE","quantity":"1"`), http.StatusUnprocessableEntity},
		{"stale price", "k5", body(`"user_id":"user_1","symbol":"INFY","quantity":"1"`), http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		w := s.post("/reward", tc.key, tc.body)
		if w.Code != tc.want {
			t.Errorf("%s: status %d, want %d; body %s", tc.name, w.Code, tc.want, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("%s: body lacks the error field: %s", tc.name, w.Body.String())
		}
	}

	// Error bodies carry the offending id.
	w := s.post("/reward", "k6", body(`"user_id":"ghost","symbol":"TCS","quantity":"1"`))
	if w.Body.String() != `{"error":"unknown user","user_id":"ghost"}` {
		t.Errorf("unknown user body = %s", w.Body.String())
	}
	w = s.post("/reward", "k7", body(`"user_id":"user_1","symbol":"INFY","quantity":"1"`))
	if w.Body.String() != `{"error":"price data stale","symbol":"INFY"}` {
		t.Errorf("stale body = %s", w.Body.String())
	}
}

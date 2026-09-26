package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata" // embed zone data so the tests pass without system tz files

	"github.com/gin-gonic/gin"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/PranavKurundodi/stocky-assignment/mock-price-server/internal/market"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// testServer bundles the router with the pieces tests poke at directly.
type testServer struct {
	router *gin.Engine
	market *market.Market
	now    time.Time // current time of the fake clock; tests may move it
	up     bool      // direction of every tick
	down   *atomic.Bool
}

// startTime is 05:01 UTC, which is 10:31 IST.
var startTime = time.Date(2026, 9, 26, 5, 1, 0, 0, time.UTC)

func newTestServer(t *testing.T, adminToken string) *testServer {
	t.Helper()
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	log, _ := logtest.NewNullLogger()

	s := &testServer{now: startTime, up: true, down: &atomic.Bool{}}
	s.market = market.New(market.BasePrices(), market.Options{
		Flip: func() bool { return s.up },
		Now:  func() time.Time { return s.now },
		Log:  log,
	})
	s.router = NewRouter(Deps{
		Market:     s.market,
		Down:       s.down,
		IST:        ist,
		AdminToken: adminToken,
		Log:        log,
	})
	return s
}

// do sends a request with an optional JSON body and returns the recorder.
func (s *testServer) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func expectStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, want, w.Body.String())
	}
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return v
}

func expectIST(t *testing.T, q quoteJSON) {
	t.Helper()
	if !strings.HasSuffix(q.AsOf, "+05:30") {
		t.Errorf("%s as_of = %q, want +05:30 offset", q.Symbol, q.AsOf)
	}
}

// ---- price endpoints ----

func TestHealth(t *testing.T) {
	s := newTestServer(t, "")
	w := s.do(http.MethodGet, "/health", "")
	expectStatus(t, w, http.StatusOK)
	if w.Body.String() != `{"status":"ok"}` {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestListPrices(t *testing.T) {
	s := newTestServer(t, "")
	w := s.do(http.MethodGet, "/prices", "")
	expectStatus(t, w, http.StatusOK)

	got := decode[struct{ Prices []quoteJSON }](t, w).Prices
	want := []quoteJSON{
		{"HDFCBANK", "750.0000", "2026-09-26T10:31:00+05:30"},
		{"ICICIBANK", "1400.0000", "2026-09-26T10:31:00+05:30"},
		{"INFY", "1000.0000", "2026-09-26T10:31:00+05:30"},
		{"RELIANCE", "1200.0000", "2026-09-26T10:31:00+05:30"},
		{"TCS", "2000.0000", "2026-09-26T10:31:00+05:30"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d prices, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("prices[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGetPrice(t *testing.T) {
	s := newTestServer(t, "")

	w := s.do(http.MethodGet, "/prices/reliance", "")
	expectStatus(t, w, http.StatusOK)
	q := decode[quoteJSON](t, w)
	if q != (quoteJSON{"RELIANCE", "1200.0000", "2026-09-26T10:31:00+05:30"}) {
		t.Errorf("quote = %+v", q)
	}

	w = s.do(http.MethodGet, "/prices/FAKECORP", "")
	expectStatus(t, w, http.StatusNotFound)
	if w.Body.String() != `{"error":"unknown stock symbol","symbol":"FAKECORP"}` {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestUnknownRoute(t *testing.T) {
	s := newTestServer(t, "")
	expectStatus(t, s.do(http.MethodGet, "/nope", ""), http.StatusNotFound)
}

// ---- admin endpoints ----

func TestOverridePrice(t *testing.T) {
	s := newTestServer(t, "")
	s.now = startTime.Add(time.Minute)

	w := s.do(http.MethodPut, "/admin/prices/RELIANCE", `{"price":"1000.0000"}`)
	expectStatus(t, w, http.StatusOK)
	q := decode[quoteJSON](t, w)
	if q != (quoteJSON{"RELIANCE", "1000.0000", "2026-09-26T10:32:00+05:30"}) {
		t.Errorf("quote = %+v", q)
	}

	// Ticking continues from the new price.
	s.market.Tick()
	q = decode[quoteJSON](t, s.do(http.MethodGet, "/prices/RELIANCE", ""))
	if q.Price != "1010.0000" {
		t.Errorf("after tick price = %s, want 1010.0000", q.Price)
	}

	expectStatus(t, s.do(http.MethodPut, "/admin/prices/FAKECORP", `{"price":"1.0000"}`), http.StatusNotFound)
	for _, body := range []string{`{"price":1000}`, `{"price":"abc"}`, `{"price":"0"}`, `{}`, `not json`} {
		expectStatus(t, s.do(http.MethodPut, "/admin/prices/RELIANCE", body), http.StatusBadRequest)
	}
}

func TestAddStock(t *testing.T) {
	s := newTestServer(t, "")

	w := s.do(http.MethodPost, "/admin/stocks", `{"symbol":"wipro","price":"500.0000"}`)
	expectStatus(t, w, http.StatusCreated)
	q := decode[quoteJSON](t, w)
	if q.Symbol != "WIPRO" || q.Price != "500.0000" {
		t.Errorf("quote = %+v", q)
	}
	expectIST(t, q)

	expectStatus(t, s.do(http.MethodGet, "/prices/WIPRO", ""), http.StatusOK)

	w = s.do(http.MethodPost, "/admin/stocks", `{"symbol":"WIPRO","price":"1.0000"}`)
	expectStatus(t, w, http.StatusConflict)
	if w.Body.String() != `{"error":"stock symbol already exists","symbol":"WIPRO"}` {
		t.Errorf("body = %s", w.Body.String())
	}

	for _, body := range []string{
		`{"symbol":"TC S","price":"1.0000"}`,
		`{"symbol":"","price":"1.0000"}`,
		`{"symbol":"NEWCO","price":"1.23456"}`,
		`{"symbol":"NEWCO","price":5}`,
	} {
		expectStatus(t, s.do(http.MethodPost, "/admin/stocks", body), http.StatusBadRequest)
	}
}

func TestRemoveStock(t *testing.T) {
	s := newTestServer(t, "")
	expectStatus(t, s.do(http.MethodDelete, "/admin/stocks/hdfcbank", ""), http.StatusNoContent)
	expectStatus(t, s.do(http.MethodGet, "/prices/HDFCBANK", ""), http.StatusNotFound)
	expectStatus(t, s.do(http.MethodDelete, "/admin/stocks/HDFCBANK", ""), http.StatusNotFound)
}

func TestFailAndRecoverAreIdempotent(t *testing.T) {
	s := newTestServer(t, "")
	for range 2 {
		w := s.do(http.MethodPost, "/admin/fail", "")
		expectStatus(t, w, http.StatusOK)
		if w.Body.String() != `{"down":true}` {
			t.Errorf("fail body = %s", w.Body.String())
		}
	}
	for range 2 {
		w := s.do(http.MethodPost, "/admin/recover", "")
		expectStatus(t, w, http.StatusOK)
		if w.Body.String() != `{"down":false}` {
			t.Errorf("recover body = %s", w.Body.String())
		}
	}
}

func TestState(t *testing.T) {
	s := newTestServer(t, "")
	w := s.do(http.MethodGet, "/admin/state", "")
	expectStatus(t, w, http.StatusOK)

	st := decode[struct {
		Down         bool        `json:"down"`
		TickInterval string      `json:"tick_interval"`
		Prices       []quoteJSON `json:"prices"`
	}](t, w)
	if st.Down || st.TickInterval != "1m0s" || len(st.Prices) != 5 {
		t.Errorf("state = %+v", st)
	}
	for _, q := range st.Prices {
		expectIST(t, q)
	}
}

func TestAdminTokenRequired(t *testing.T) {
	s := newTestServer(t, "s3cret")
	expectStatus(t, s.do(http.MethodGet, "/admin/state", ""), http.StatusUnauthorized)

	req := httptest.NewRequest(http.MethodGet, "/admin/state", nil)
	req.Header.Set("X-Admin-Token", "s3cret")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	expectStatus(t, w, http.StatusOK)

	// Price routes never need the token.
	expectStatus(t, s.do(http.MethodGet, "/prices", ""), http.StatusOK)
}

// ---- IST ----

func TestAsOfIsISTAfterTickOverrideAndAdd(t *testing.T) {
	s := newTestServer(t, "")

	s.now = startTime.Add(time.Minute)
	s.market.Tick()
	q := decode[quoteJSON](t, s.do(http.MethodGet, "/prices/TCS", ""))
	if q.AsOf != "2026-09-26T10:32:00+05:30" {
		t.Errorf("after tick as_of = %s", q.AsOf)
	}

	q = decode[quoteJSON](t, s.do(http.MethodPut, "/admin/prices/TCS", `{"price":"1500.0000"}`))
	expectIST(t, q)

	q = decode[quoteJSON](t, s.do(http.MethodPost, "/admin/stocks", `{"symbol":"M&M","price":"3000.0000"}`))
	expectIST(t, q)

	for _, q := range decode[struct{ Prices []quoteJSON }](t, s.do(http.MethodGet, "/prices", "")).Prices {
		expectIST(t, q)
	}
}

// ---- outage ----

func TestOutageBlocksPricesButNotAdmin(t *testing.T) {
	s := newTestServer(t, "")
	expectStatus(t, s.do(http.MethodPost, "/admin/fail", ""), http.StatusOK)

	for _, path := range []string{"/prices", "/prices/RELIANCE"} {
		w := s.do(http.MethodGet, path, "")
		expectStatus(t, w, http.StatusServiceUnavailable)
		if w.Body.String() != `{"error":"price service unavailable"}` || w.Header().Get("Retry-After") != "30" {
			t.Errorf("%s: body = %s, Retry-After = %q", path, w.Body.String(), w.Header().Get("Retry-After"))
		}
	}
	w := s.do(http.MethodGet, "/health", "")
	expectStatus(t, w, http.StatusServiceUnavailable)
	if w.Body.String() != `{"status":"down"}` {
		t.Errorf("health body = %s", w.Body.String())
	}
	expectStatus(t, s.do(http.MethodGet, "/admin/state", ""), http.StatusOK)

	expectStatus(t, s.do(http.MethodPost, "/admin/recover", ""), http.StatusOK)
	for _, path := range []string{"/prices", "/prices/RELIANCE", "/health"} {
		expectStatus(t, s.do(http.MethodGet, path, ""), http.StatusOK)
	}
}

func TestPricesKeepTickingDuringOutage(t *testing.T) {
	s := newTestServer(t, "")
	before := decode[quoteJSON](t, s.do(http.MethodGet, "/prices/RELIANCE", ""))

	s.do(http.MethodPost, "/admin/fail", "")
	s.market.Tick()
	s.do(http.MethodPost, "/admin/recover", "")

	after := decode[quoteJSON](t, s.do(http.MethodGet, "/prices/RELIANCE", ""))
	if before.Price == after.Price {
		t.Errorf("price did not change during outage: %s", after.Price)
	}
}

func TestOverrideDuringOutageSurvivesRecovery(t *testing.T) {
	s := newTestServer(t, "")
	s.do(http.MethodPost, "/admin/fail", "")
	expectStatus(t, s.do(http.MethodPut, "/admin/prices/RELIANCE", `{"price":"600.0000"}`), http.StatusOK)
	s.do(http.MethodPost, "/admin/recover", "")

	q := decode[quoteJSON](t, s.do(http.MethodGet, "/prices/RELIANCE", ""))
	if q.Price != "600.0000" {
		t.Errorf("price after recovery = %s, want 600.0000", q.Price)
	}
}

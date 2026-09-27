package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/pricing"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
)

type fakeRefresher struct {
	sum pricing.Summary
	err error
}

func (f fakeRefresher) RunOnce(context.Context) (pricing.Summary, error) { return f.sum, f.err }

func TestRefreshPrices(t *testing.T) {
	if err := timeutil.Init(); err != nil {
		t.Fatal(err)
	}
	fetched := time.Date(2026, 9, 26, 5, 1, 0, 0, time.UTC)

	cases := []struct {
		name     string
		f        fakeRefresher
		wantCode int
		wantBody string
	}{
		{
			"success",
			fakeRefresher{sum: pricing.Summary{FetchedAt: fetched, Inserted: 4, Skipped: 1,
				NewSymbols: []string{"WIPRO"}, MissingSymbols: []string{}}},
			http.StatusOK,
			`{"fetched_at":"2026-09-26T10:31:00+05:30","inserted":4,"missing_symbols":[],"new_symbols":["WIPRO"],"skipped":1}`,
		},
		{
			"vendor down",
			fakeRefresher{err: fmt.Errorf("%w: HTTP 503", pricing.ErrUnavailable)},
			http.StatusServiceUnavailable,
			`{"error":"price service unavailable"}`,
		},
	}
	for _, tc := range cases {
		log, _ := logtest.NewNullLogger()
		r := NewRouter(Deps{DB: fakePinger{}, Prices: tc.f, Log: log})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/prices/refresh", nil))
		if w.Code != tc.wantCode || w.Body.String() != tc.wantBody {
			t.Errorf("%s: got %d %s", tc.name, w.Code, w.Body.String())
		}
	}
}

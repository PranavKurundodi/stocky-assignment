package pricing

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

const okBody = `{"prices":[
	{"symbol":"RELIANCE","price":"1200.0000","as_of":"2026-09-26T10:31:00+05:30"},
	{"symbol":"tcs","price":"2000.5000","as_of":"2026-09-26T10:31:00+05:30"}]}`

// newTestClient returns a client for url with instant backoff.
func newTestClient(url string, timeout time.Duration) *Client {
	log, _ := logtest.NewNullLogger()
	c := NewClient(url, timeout, log)
	c.Backoff = []time.Duration{0, 0, 0}
	return c
}

// sequenceServer answers with the given statuses in order (the last one
// repeats), counting requests. 200 responses carry okBody.
func sequenceServer(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(hits.Add(1))
		status := statuses[min(n, len(statuses))-1]
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(okBody))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestFetchParsesQuotes(t *testing.T) {
	srv, _ := sequenceServer(t, http.StatusOK)
	quotes, err := newTestClient(srv.URL, time.Second).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 2 {
		t.Fatalf("got %d quotes, want 2", len(quotes))
	}
	q := quotes[1]
	if q.Symbol != "TCS" || q.Price.StringFixed(4) != "2000.5000" {
		t.Errorf("quote = %+v", q)
	}
	if !q.AsOf.Equal(time.Date(2026, 9, 26, 5, 1, 0, 0, time.UTC)) {
		t.Errorf("as_of = %v", q.AsOf)
	}
}

func TestFetchRetriesThenSucceeds(t *testing.T) {
	srv, hits := sequenceServer(t, http.StatusServiceUnavailable, http.StatusOK)
	if _, err := newTestClient(srv.URL, time.Second).Fetch(context.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("hits = %d, want 2", hits.Load())
	}
}

func TestFetchGivesUpAfterThreeAttempts(t *testing.T) {
	srv, hits := sequenceServer(t, http.StatusServiceUnavailable)
	_, err := newTestClient(srv.URL, time.Second).Fetch(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	if hits.Load() != 3 {
		t.Errorf("hits = %d, want 3", hits.Load())
	}
}

func TestFetchConnectionRefusedIsUnavailable(t *testing.T) {
	// Grab a free port, then close it so nothing is listening there.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	_, err = newTestClient("http://"+addr, time.Second).Fetch(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestFetchTimeoutIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	_, err := newTestClient(srv.URL, 20*time.Millisecond).Fetch(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestFetchStopsDuringBackoffWhenCancelled(t *testing.T) {
	srv, hits := sequenceServer(t, http.StatusServiceUnavailable)
	c := newTestClient(srv.URL, time.Second)
	c.Backoff = []time.Duration{time.Minute} // would hang the test if not cancellable

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Fetch(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(start) > 5*time.Second || hits.Load() != 1 {
		t.Errorf("took %v with %d hits; want a prompt return after 1 attempt", time.Since(start), hits.Load())
	}
}

func TestInvalidQuotesAreSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"prices":[
			{"symbol":"GOOD","price":"10.0000","as_of":"2026-09-26T10:31:00+05:30"},
			{"symbol":"ZERO","price":"0","as_of":"2026-09-26T10:31:00+05:30"},
			{"symbol":"NAN","price":"abc","as_of":"2026-09-26T10:31:00+05:30"},
			{"symbol":"NOTIME","price":"1.0000","as_of":"yesterday"}]}`))
	}))
	t.Cleanup(srv.Close)

	quotes, err := newTestClient(srv.URL, time.Second).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 1 || quotes[0].Symbol != "GOOD" {
		t.Errorf("quotes = %+v, want only GOOD", quotes)
	}
}

func TestIsStale(t *testing.T) {
	ref := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if IsStale(ref.Add(-2*time.Hour), ref, 2*time.Hour) {
		t.Error("exactly 2h old should not be stale")
	}
	if !IsStale(ref.Add(-2*time.Hour-time.Second), ref, 2*time.Hour) {
		t.Error("2h01s old should be stale")
	}
}

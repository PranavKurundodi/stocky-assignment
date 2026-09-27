package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestReadEndpointsJSON checks the wire format of the four read endpoints:
// fixed-precision strings, IST timestamps, nulls and 404s.
func TestReadEndpointsJSON(t *testing.T) {
	s := newDBServer(t)
	// A WIPRO holding with no price at all, booked straight into the ledger.
	for _, q := range []string{
		`INSERT INTO stocks (symbol, status) VALUES ('WIPRO', 'ACTIVE')`,
		`INSERT INTO ledger_transactions (id, type, effective_at) VALUES ('00000000-0000-0000-0000-000000000001', 'MERGER', now())`,
		`INSERT INTO ledger_entries (transaction_id, account, user_id, asset, direction, quantity)
		 VALUES ('00000000-0000-0000-0000-000000000001', 'USER_STOCK', 'user_1', 'WIPRO', 'DEBIT', 1),
		        ('00000000-0000-0000-0000-000000000001', 'CORPORATE_ACTION', NULL, 'WIPRO', 'CREDIT', 1)`,
	} {
		if _, err := s.pool.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	if w := s.post("/reward", "k1", `{"user_id":"user_1","symbol":"TCS","quantity":"1.5","reason":"REFERRAL"}`); w.Code != http.StatusCreated {
		t.Fatalf("reward: %d %s", w.Code, w.Body.String())
	}

	// today-stocks
	w := s.get("/today-stocks/user_1")
	var today struct {
		UserID  string `json:"user_id"`
		Date    string `json:"date"`
		Rewards []struct {
			Symbol, Quantity, Reason, RewardedAt, Status string
		} `json:"rewards"`
	}
	mustDecode(t, w, &today)
	if today.Date != "2026-09-26" || len(today.Rewards) != 1 || today.Rewards[0].Quantity != "1.500000" || today.Rewards[0].Status != "ACTIVE" {
		t.Errorf("today-stocks = %s", w.Body.String())
	}

	// portfolio: TCS valued, WIPRO null and listed as missing.
	w = s.get("/portfolio/user_1")
	var pf struct {
		Holdings []struct {
			Symbol   string  `json:"symbol"`
			Quantity string  `json:"quantity"`
			Price    *string `json:"price"`
			ValueINR *string `json:"value_inr"`
			IsStale  bool    `json:"is_stale"`
			Delisted bool    `json:"delisted"`
		} `json:"holdings"`
		Total   string   `json:"total_value_inr"`
		Missing []string `json:"missing_prices"`
	}
	mustDecode(t, w, &pf)
	if len(pf.Holdings) != 2 || *pf.Holdings[0].Price != "2000.0000" || *pf.Holdings[0].ValueINR != "3000.0000" ||
		pf.Holdings[1].Price != nil || pf.Holdings[1].ValueINR != nil ||
		pf.Total != "3000.0000" || len(pf.Missing) != 1 || pf.Missing[0] != "WIPRO" {
		t.Errorf("portfolio = %s", w.Body.String())
	}

	// stats
	w = s.get("/stats/user_1")
	var st struct {
		Today      map[string]string `json:"today_shares_by_symbol"`
		Current    string            `json:"current_portfolio_inr"`
		PricesAsOf *string           `json:"prices_as_of"`
		IsStale    bool              `json:"is_stale"`
	}
	mustDecode(t, w, &st)
	if st.Today["TCS"] != "1.500000" || st.Current != "3000.0000" || st.PricesAsOf == nil ||
		*st.PricesAsOf != "2026-09-26T11:50:00+05:30" || st.IsStale {
		t.Errorf("stats = %s", w.Body.String())
	}

	// historical-inr: only today's activity, so no past days yet.
	w = s.get("/historical-inr/user_1")
	if w.Code != http.StatusOK || w.Body.String() != `{"history":[],"user_id":"user_1"}` {
		t.Errorf("historical-inr = %d %s", w.Code, w.Body.String())
	}

	// Unknown user: 404 on every read endpoint.
	for _, path := range []string{"/today-stocks/ghost", "/portfolio/ghost", "/stats/ghost", "/historical-inr/ghost"} {
		w := s.get(path)
		if w.Code != http.StatusNotFound || w.Body.String() != `{"error":"unknown user","user_id":"ghost"}` {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func mustDecode(t *testing.T, w interface{ Result() *http.Response }, v any) {
	t.Helper()
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

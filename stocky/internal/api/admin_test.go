package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPostUsers(t *testing.T) {
	s := newDBServer(t) // already has user_1

	cases := []struct {
		body string
		want int
	}{
		{`{"id":"user_9","name":"Nine"}`, http.StatusCreated},
		{`{"id":"user_9","name":"Nine again"}`, http.StatusConflict},
		{`{"id":"has space","name":"x"}`, http.StatusBadRequest},
		{`{"id":"user_10","name":"  "}`, http.StatusBadRequest},
		{`{"id":`, http.StatusBadRequest},
	}
	for _, c := range cases {
		w := s.post("/users", "", c.body)
		if w.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.body, w.Code, w.Body.String(), c.want)
		}
	}
	if w := s.get("/portfolio/user_9"); w.Code != http.StatusOK {
		t.Errorf("new user not usable: %d", w.Code)
	}
}

func TestReverseEndpoint(t *testing.T) {
	s := newDBServer(t)
	w := s.post("/reward", "k1", tcsBody)
	var rw rewardJSON
	if err := json.Unmarshal(w.Body.Bytes(), &rw); err != nil {
		t.Fatal(err)
	}

	for i := range 2 { // second call is a no-op with the same answer
		w := s.post("/reward/"+rw.RewardID+"/reverse", "", "")
		var got rewardJSON
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || got.Status != "REVERSED" || got.ReversedAt == nil {
			t.Errorf("reverse #%d: %d %s", i+1, w.Code, w.Body.String())
		}
	}

	for _, id := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		if w := s.post("/reward/"+id+"/reverse", "", ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", id, w.Code, w.Body.String())
		}
	}

	// today-stocks lists the reversed reward, labeled.
	if body := s.get("/today-stocks/user_1").Body.String(); !strings.Contains(body, `"status":"REVERSED"`) {
		t.Errorf("today-stocks = %s", body)
	}
}

func TestCorporateActionsEndpointAndVerify(t *testing.T) {
	s := newDBServer(t)
	if w := s.post("/reward", "k1", tcsBody); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	w := s.post("/admin/corporate-actions", "", `{"type":"SPLIT","symbol":"TCS","ratio_from":"1","ratio_to":"2"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("split: %d %s", w.Code, w.Body.String())
	}
	var action map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &action); err != nil {
		t.Fatal(err)
	}
	if action["ratio_from"] != "1.000000" || action["ratio_to"] != "2.000000" || action["new_symbol"] != nil ||
		action["holders_affected"] != float64(1) || action["effective_at"] != "2026-09-26T12:00:00+05:30" {
		t.Errorf("split response = %s", w.Body.String())
	}
	if body := s.get("/portfolio/user_1").Body.String(); !strings.Contains(body, `"quantity":"2.000000"`) {
		t.Errorf("portfolio after split = %s", body)
	}

	codes := []struct {
		body string
		want int
	}{
		{`{"type":"SPLIT","symbol":"TCS","ratio_from":1,"ratio_to":"2"}`, http.StatusBadRequest},
		{`{"type":"MERGER","symbol":"TCS","ratio_from":"1","ratio_to":"1"}`, http.StatusBadRequest},
		{`{"type":"DELISTING","symbol":"FAKECORP"}`, http.StatusNotFound},
		{`{"type":"DELISTING","symbol":"HDFCBANK"}`, http.StatusUnprocessableEntity}, // already delisted
		{`{"type":"MERGER","symbol":"INFY","new_symbol":"TCS","ratio_from":"3","ratio_to":"1"}`, http.StatusCreated},
	}
	for _, c := range codes {
		if w := s.post("/admin/corporate-actions", "", c.body); w.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.body, w.Code, w.Body.String(), c.want)
		}
	}

	// Reversing the reward after the split is refused.
	var rw rewardJSON
	_ = json.Unmarshal(s.post("/reward", "k1", tcsBody).Body.Bytes(), &rw) // replay returns the reward
	if w := s.post("/reward/"+rw.RewardID+"/reverse", "", ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("reverse after split: %d %s", w.Code, w.Body.String())
	}

	w = s.get("/admin/ledger/verify")
	if w.Code != http.StatusOK || w.Body.String() != `{"checked":2,"unbalanced":[]}` {
		t.Errorf("verify = %d %s", w.Code, w.Body.String())
	}
}

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/testdb"
)

// TestMain sets gin's test mode, then hands over to testdb, which runs the
// DB-backed tests when TEST_DATABASE_URL is set and skips them otherwise.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	testdb.Main(m)
}

// fakePinger returns err from every Ping.
type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	cases := []struct {
		name     string
		pingErr  error
		wantCode int
		wantBody string
	}{
		{"db up", nil, http.StatusOK, `{"status":"ok"}`},
		{"db down", errors.New("connection refused"), http.StatusServiceUnavailable, `{"status":"db unavailable"}`},
	}
	for _, tc := range cases {
		log, _ := logtest.NewNullLogger()
		r := NewRouter(Deps{DB: fakePinger{tc.pingErr}, Log: log})

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

		if w.Code != tc.wantCode || w.Body.String() != tc.wantBody {
			t.Errorf("%s: got %d %s, want %d %s", tc.name, w.Code, w.Body.String(), tc.wantCode, tc.wantBody)
		}
	}
}

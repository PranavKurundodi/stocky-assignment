package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func ok(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) }

func serve(r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestLoggerLevelFollowsStatus(t *testing.T) {
	log, hook := logtest.NewNullLogger()
	r := gin.New()
	r.Use(Logger(log))
	r.GET("/s/:code", func(c *gin.Context) {
		switch c.Param("code") {
		case "200":
			c.Status(http.StatusOK)
		case "404":
			c.Status(http.StatusNotFound)
		default:
			c.Status(http.StatusInternalServerError)
		}
	})

	cases := map[string]logrus.Level{
		"200": logrus.InfoLevel,
		"404": logrus.WarnLevel,
		"500": logrus.ErrorLevel,
	}
	for code, want := range cases {
		hook.Reset()
		serve(r, httptest.NewRequest(http.MethodGet, "/s/"+code, nil))
		e := hook.LastEntry()
		if e == nil {
			t.Fatalf("%s: no log entry", code)
		}
		if e.Level != want {
			t.Errorf("%s: level = %s, want %s", code, e.Level, want)
		}
		for _, f := range []string{"method", "path", "status", "latency_ms", "client_ip"} {
			if _, ok := e.Data[f]; !ok {
				t.Errorf("%s: missing field %q", code, f)
			}
		}
	}
}

func TestOutage(t *testing.T) {
	var down atomic.Bool
	r := gin.New()
	r.Use(Outage(&down, gin.H{"error": "price service unavailable"}))
	r.GET("/", ok)

	if w := serve(r, httptest.NewRequest(http.MethodGet, "/", nil)); w.Code != http.StatusOK {
		t.Fatalf("up: status = %d, want 200", w.Code)
	}

	down.Store(true)
	w := serve(r, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("down: status = %d, want 503", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	if got, want := w.Body.String(), `{"error":"price service unavailable"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestAdminToken(t *testing.T) {
	// Empty token: open.
	open := gin.New()
	open.Use(AdminToken(""))
	open.GET("/", ok)
	if w := serve(open, httptest.NewRequest(http.MethodGet, "/", nil)); w.Code != http.StatusOK {
		t.Errorf("no token configured: status = %d, want 200", w.Code)
	}

	// Token set: header required and must match.
	r := gin.New()
	r.Use(AdminToken("s3cret"))
	r.GET("/", ok)

	cases := map[string]int{"": http.StatusUnauthorized, "wrong": http.StatusUnauthorized, "s3cret": http.StatusOK}
	for header, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			req.Header.Set(AdminTokenHeader, header)
		}
		if w := serve(r, req); w.Code != want {
			t.Errorf("header %q: status = %d, want %d", header, w.Code, want)
		}
	}
}

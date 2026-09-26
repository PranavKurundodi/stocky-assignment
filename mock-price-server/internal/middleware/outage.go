package middleware

import (
	"net/http"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// RetryAfterSeconds is sent in the Retry-After header while the server is down.
const RetryAfterSeconds = "30"

// Outage short-circuits the request with 503 and the given body while down is
// true. The flag is shared with the admin handlers that flip it, and it is an
// atomic.Bool so every request can read it without taking a lock.
func Outage(down *atomic.Bool, body any) gin.HandlerFunc {
	return func(c *gin.Context) {
		if down.Load() {
			c.Header("Retry-After", RetryAfterSeconds)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, body)
			return
		}
		c.Next()
	}
}

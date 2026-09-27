// Package middleware holds Stocky's gin middleware.
package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Logger replaces gin's default logger. It writes one logrus line per request
// after the rest of the chain has run, at a level that matches the status.
func Logger(log logrus.FieldLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next() // run the remaining middleware and the handler

		status := c.Writer.Status()
		entry := log.WithFields(logrus.Fields{
			"method":     c.Request.Method,
			"path":       c.Request.URL.Path,
			"status":     status,
			"latency_ms": time.Since(start).Milliseconds(),
			"client_ip":  c.ClientIP(),
		})

		switch {
		case status >= 500:
			entry.Error("request")
		case status >= 400:
			entry.Warn("request")
		default:
			entry.Info("request")
		}
	}
}

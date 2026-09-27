package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// healthPingTimeout keeps /health fast even when the database hangs.
const healthPingTimeout = 2 * time.Second

// health answers GET /health by pinging the database.
func (h *handlers) health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), healthPingTimeout)
	defer cancel()

	if err := h.DB.Ping(ctx); err != nil {
		h.Log.WithError(err).Warn("health check: database ping failed")
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "db unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

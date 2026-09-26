package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
)

// AdminTokenHeader is the header clients send the admin token in.
const AdminTokenHeader = "X-Admin-Token"

// AdminToken requires the X-Admin-Token header to equal token. An empty token
// disables the check, which keeps local development friction-free.
func AdminToken(token string) gin.HandlerFunc {
	if token == "" {
		return func(c *gin.Context) { c.Next() }
	}
	want := []byte(token)

	return func(c *gin.Context) {
		got := []byte(c.GetHeader(AdminTokenHeader))
		// ConstantTimeCompare takes the same time whether the first or the
		// last byte differs, so response timing leaks nothing about the token.
		if subtle.ConstantTimeCompare(got, want) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid admin token"})
			return
		}
		c.Next()
	}
}

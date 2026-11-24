package controllers

import (
	"crypto/subtle"
	"net/http"
	"prophet-trader/config"

	"github.com/gin-gonic/gin"
)

// RequireAdminToken validates "Authorization: Bearer <ADMIN_TOKEN>".
// It fails closed when ADMIN_TOKEN is empty, so an unconfigured bot can never
// accept configuration changes.
func RequireAdminToken(c *gin.Context) bool {
	expected := ""
	if config.AppConfig != nil {
		expected = config.AppConfig.AdminToken
	}
	if expected == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "ADMIN_TOKEN is not configured. Configuration changes are disabled."})
		return false
	}

	header := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if len(header) < len(prefix) || header[:len(prefix)] != prefix {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header format"})
		return false
	}

	token := header[len(prefix):]
	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return false
	}
	return true
}

// AdminAuthMiddleware guards state-changing routes (config, agents, models…).
func AdminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !RequireAdminToken(c) {
			c.Abort()
			return
		}
		c.Next()
	}
}

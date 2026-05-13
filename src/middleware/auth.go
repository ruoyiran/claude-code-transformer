package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authKey := strings.TrimSpace(GetAuthKey(c))
		if authKey == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
}

func GetAuthKey(c *gin.Context) string {
	ak := c.Query("ak")
	if ak != "" {
		return ak
	}
	apiKeyHeaders := []string{"api-key", "API-KEY", "x-api-key", "X-API-Key"}
	for _, header := range apiKeyHeaders {
		if c.Request.Header.Get(header) != "" {
			return c.Request.Header.Get(header)
		}
	}
	if c.Request.Header.Get("Authorization") != "" {
		auth := c.Request.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			return auth[7:]
		}
		return auth
	}
	return ""
}

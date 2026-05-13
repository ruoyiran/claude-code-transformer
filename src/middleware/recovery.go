package middleware

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"net/http"
	"runtime/debug"
)

func CustomRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if p := recover(); p != nil {
				if err, ok := p.(error); ok {
					// ignore panic abort handler for text/event-stream SSE
					if errors.Is(err, http.ErrAbortHandler) {
						return
					}
				}
				logrus.WithFields(logrus.Fields{
					"method":    c.Request.Method,
					"path":      c.Request.URL.Path,
					"raw_query": c.Request.URL.RawQuery,
					"client_ip": c.ClientIP(),
				}).Errorf("panic recovered: %v\n%s", p, string(debug.Stack()))
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

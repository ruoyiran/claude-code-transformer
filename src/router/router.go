package router

import (
	"github/ruoyiran/claude-code-transformer/src/config"
	"github/ruoyiran/claude-code-transformer/src/handler"
	"github/ruoyiran/claude-code-transformer/src/middleware"
	"strings"

	"github.com/gin-gonic/gin"
)

func register(r *gin.Engine) {
	conf := config.GetConfig()

	base := strings.TrimRight(strings.TrimSpace(conf.BasePath), "/")
	if base == "/" {
		base = ""
	}
	if base != "" && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	conf.BasePath = base

	prefix := r.Group(base)

	anthropic := prefix.Group("/anthropic/v1")
	{
		anthropic.POST("/messages", handler.MessagesHandler)
		anthropic.POST("/messages/count_tokens", handler.CountTokensHandler)
	}
}

func CreateEngine() *gin.Engine {
	r := gin.Default()
	{
		// CORS must run before Auth so that browser preflight OPTIONS can succeed.
		r.Use(middleware.Cors())
		r.Use(middleware.Auth())
		r.Use(middleware.General())
		r.Use(middleware.CustomRecovery())
	}
	register(r)
	return r
}

package handler

import (
	"encoding/json"
	"github/ruoyiran/claude-code-transformer/src/claude/model"
	"net/http"

	"github.com/gin-gonic/gin"
)

func CountTokensHandler(c *gin.Context) {
	var req model.ClaudeTokenCountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}

	totalChars := 0
	// system
	if len(req.System) > 0 {
		var s string
		if err := json.Unmarshal(req.System, &s); err == nil {
			totalChars += len(s)
		} else {
			var blocks []map[string]any
			if err := json.Unmarshal(req.System, &blocks); err == nil {
				for _, b := range blocks {
					if txt, ok := b["text"].(string); ok {
						totalChars += len(txt)
					}
				}
			}
		}
	}

	// messages
	for _, m := range req.Messages {
		if len(m.Content) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(m.Content, &s); err == nil {
			totalChars += len(s)
			continue
		}
		var blocks []map[string]any
		if err := json.Unmarshal(m.Content, &blocks); err == nil {
			for _, b := range blocks {
				if txt, ok := b["text"].(string); ok && txt != "" {
					totalChars += len(txt)
				}
			}
		}
	}

	estimated := totalChars / 4
	if estimated < 1 {
		estimated = 1
	}
	c.JSON(http.StatusOK, gin.H{"input_tokens": estimated})
}

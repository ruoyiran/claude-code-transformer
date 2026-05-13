package model

import (
	"encoding/json"
	"strings"
)

type ClaudeContentBlockText struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

type ClaudeContentBlockImage struct {
	Type   string         `json:"type"` // "image"
	Source map[string]any `json:"source"`
}

type ClaudeContentBlockToolUse struct {
	Type  string         `json:"type"` // "tool_use"
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type ClaudeContentBlockToolResult struct {
	Type      string          `json:"type"` // "tool_result"
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // could be string/list/object; keep raw and parse later
}

type ClaudeSystemContent struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

type ClaudeMessage struct {
	Role    string          `json:"role"`            // user/assistant
	Content json.RawMessage `json:"content"`         // string or []blocks
	Phase   string          `json:"phase,omitempty"` // assistant phase passthrough for responses API
}

type ClaudeTool struct {
	Name        string         `json:"name"`
	Description *string        `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type ClaudeThinkingConfig struct {
	Type         string `json:"type,omitempty"` // "enabled" or "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Enabled      bool   `json:"enabled,omitempty"` // legacy support
}

func (c *ClaudeThinkingConfig) IsEnabled() bool {
	if c == nil {
		return false
	}
	return (c.Type == "enabled" || c.Type == "adaptive") || c.Enabled
}

type ClaudeMetaData struct {
	UserID string `json:"user_id"`
}

type ClaudeSession struct {
	SessionID string `json:"session_id"`
}

func (d ClaudeMetaData) GetSessionID() string {
	var session ClaudeSession
	err := json.Unmarshal([]byte(d.UserID), &session)
	if err == nil && session.SessionID != "" {
		return session.SessionID
	}
	if strings.Contains(d.UserID, "_session_") {
		return strings.Split(d.UserID, "_session_")[1]
	}
	return ""
}

type ClaudeOutputConfig struct {
	Effort        string `json:"effort,omitempty"`
	Summary       string `json:"summary,omitempty"`
	TextVerbosity string `json:"text_verbosity,omitempty"`
}
type ClaudeMessagesRequest struct {
	Model                   string                `json:"model"`
	MaxTokens               int                   `json:"max_tokens"`
	Messages                []ClaudeMessage       `json:"messages"`
	System                  json.RawMessage       `json:"system,omitempty"` // string or []{type,text}
	StopSequences           []string              `json:"stop_sequences,omitempty"`
	Stream                  bool                  `json:"stream,omitempty"`
	Temperature             *float64              `json:"temperature,omitempty"`
	TopP                    *float64              `json:"top_p,omitempty"`
	TopK                    *int                  `json:"top_k,omitempty"`
	MetaData                ClaudeMetaData        `json:"metadata,omitempty"`
	Tools                   []ClaudeTool          `json:"tools,omitempty"`
	ToolChoice              map[string]any        `json:"tool_choice,omitempty"`
	OutputConfig            ClaudeOutputConfig    `json:"output_config,omitempty"`
	Thinking                *ClaudeThinkingConfig `json:"thinking,omitempty"`
	IncludeEncryptedContent bool                  `json:"include_encrypted_content,omitempty"`
}

type ClaudeTokenCountRequest struct {
	Model      string                `json:"model"`
	Messages   []ClaudeMessage       `json:"messages"`
	System     json.RawMessage       `json:"system,omitempty"`
	Tools      []ClaudeTool          `json:"tools,omitempty"`
	Thinking   *ClaudeThinkingConfig `json:"thinking,omitempty"`
	ToolChoice map[string]any        `json:"tool_choice,omitempty"`
}

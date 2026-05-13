package conversion_test

import (
	"encoding/json"
	"github/ruoyiran/claude-code-transformer/src/claude/conversion"
	"github/ruoyiran/claude-code-transformer/src/claude/model"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertClaudeToOpenAI_Basic(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 128,
		System:    json.RawMessage(`"you are system"`),
		Stream:    false,
		Messages: []model.ClaudeMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)
	require.Equal(t, "claude-3-opus", out["model"])
	require.Equal(t, false, out["stream"])
	require.Equal(t, "you are system", out["instructions"])

	input := out["input"].([]map[string]any)
	require.Len(t, input, 1)
	require.Equal(t, "message", input[0]["type"])
	require.Equal(t, "user", input[0]["role"])
	content, ok := input[0]["content"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	require.Equal(t, "input_text", content[0]["type"])
	require.Equal(t, "hi", content[0]["text"])
}

func TestConvertClaudeToOpenAIResponses_Basic(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 256,
		System:    json.RawMessage(`"you are system"`),
		Stream:    true,
		Messages: []model.ClaudeMessage{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"hello"},{"type":"tool_use","id":"call_1","name":"get_weather","input":{"city":"beijing"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]`)},
		},
		OutputConfig: model.ClaudeOutputConfig{Effort: "high"},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)
	require.Equal(t, "claude-3-opus", out["model"])
	require.Equal(t, true, out["stream"])
	require.Equal(t, 256, out["max_output_tokens"])

	require.Equal(t, "you are system", out["instructions"])

	input := out["input"].([]map[string]any)
	require.GreaterOrEqual(t, len(input), 2)
	require.Equal(t, "message", input[0]["type"])
	require.Equal(t, "assistant", input[0]["role"])
	require.Equal(t, "function_call", input[1]["type"])
	require.Equal(t, "get_weather", input[1]["name"])
	require.Equal(t, "function_call_output", input[2]["type"])
}

func TestConvertClaudeToOpenAIResponses_AssistantThinkingSignature(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:                   "claude-3-opus",
		MaxTokens:               64,
		Stream:                  false,
		Thinking:                &model.ClaudeThinkingConfig{Enabled: true},
		IncludeEncryptedContent: true,
		Messages: []model.ClaudeMessage{
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"thinking","thinking":"internal","signature":"sig_abc"},
					{"type":"text","text":"done"}
				]`),
			},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)

	input := out["input"].([]map[string]any)
	require.Len(t, input, 2)
	require.Equal(t, "reasoning", input[0]["type"])
	require.Equal(t, "sig_abc", input[0]["encrypted_content"])
	require.Equal(t, "message", input[1]["type"])
}

func TestConvertClaudeToOpenAIResponses_MessageContentIsAlwaysArray(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 64,
		Stream:    true,
		Messages: []model.ClaudeMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
			{Role: "assistant", Content: json.RawMessage(`"hello"`)},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)

	input := out["input"].([]map[string]any)
	// input[0] is a message
	_, ok0 := input[0]["content"].([]map[string]any)
	require.True(t, ok0)
	// input[1] is a message
	_, ok1 := input[1]["content"].([]map[string]any)
	require.True(t, ok1)
}

func TestConvertClaudeToOpenAIResponses_AssistantTextAndToolUseOrder(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 64,
		Stream:    false,
		Messages: []model.ClaudeMessage{
			{
				Role:  "assistant",
				Phase: "tool_plan",
				Content: json.RawMessage(`[
					{"type":"thinking","signature":"sig_xyz"},
					{"type":"text","text":"call tool"},
					{"type":"tool_use","id":"call_1","name":"do_it","input":{"x":1}}
				]`),
			},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)

	input := out["input"].([]map[string]any)
	require.Len(t, input, 2)
	require.Equal(t, "message", input[0]["type"])
	require.Equal(t, "assistant", input[0]["role"])
	require.Equal(t, "tool_plan", input[0]["phase"])
	require.Equal(t, "function_call", input[1]["type"])
	require.Equal(t, "do_it", input[1]["name"])
	require.Equal(t, "call_1", input[1]["call_id"])
}

func TestConvertClaudeToOpenAI_AssistantPhasePassthrough(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 64,
		Messages: []model.ClaudeMessage{
			{Role: "assistant", Phase: "analysis", Content: json.RawMessage(`"ok"`)},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)

	input := out["input"].([]map[string]any)
	require.Len(t, input, 1)
	require.Equal(t, "message", input[0]["type"])
	require.Equal(t, "assistant", input[0]["role"])
	require.Equal(t, "analysis", input[0]["phase"])
}

func TestConvertClaudeToOpenAIResponses_AssistantToolUseOnlyProducesFunctionCallItems(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 64,
		Messages: []model.ClaudeMessage{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"call_1","name":"get_weather","input":{"city":"beijing"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]`)},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)

	input := out["input"].([]map[string]any)
	require.Len(t, input, 2)
	require.Equal(t, "function_call", input[0]["type"])
	require.Equal(t, "get_weather", input[0]["name"])
	require.Equal(t, "call_1", input[0]["call_id"])
	require.Equal(t, "function_call_output", input[1]["type"])
	require.Equal(t, "call_1", input[1]["call_id"])
}

func TestConvertClaudeToOpenAIResponses_AssistantThinkingOnlyMessageUsesEmptyContentArray(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 64,
		Messages: []model.ClaudeMessage{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","signature":"sig_abc"}]`)},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)

	input := out["input"].([]map[string]any)
	require.Len(t, input, 1)
	require.Equal(t, "message", input[0]["type"])
	content, ok := input[0]["content"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, content, 0)
}

func TestConvertClaudeToOpenAIResponses_DisablesToolsForCompactSummaryInstructions(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 64,
		System:    json.RawMessage(`"You are a helpful AI assistant tasked with summarizing conversations."`),
		Tools: []model.ClaudeTool{
			{
				Name:        "Read",
				Description: ptr("read a file"),
				InputSchema: map[string]any{"type": "object"},
			},
		},
		ToolChoice: map[string]any{"type": "auto"},
		Messages: []model.ClaudeMessage{
			{Role: "user", Content: json.RawMessage(`"summarize this chat"`)},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)
	require.NotContains(t, out, "tools")
	require.NotContains(t, out, "tool_choice")
}

func TestConvertClaudeToOpenAIResponses_KeepsToolsForNormalRequests(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 64,
		System:    json.RawMessage(`"you are system"`),
		Tools: []model.ClaudeTool{
			{
				Name:        "Read",
				Description: ptr("read a file"),
				InputSchema: map[string]any{"type": "object"},
			},
		},
		ToolChoice: map[string]any{"type": "auto"},
		Messages: []model.ClaudeMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)
	tools, ok := out["tools"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	require.Equal(t, "Read", tools[0]["name"])
	require.Equal(t, "auto", out["tool_choice"])
}

func ptr(s string) *string {
	return &s
}

func TestConvertClaudeToOpenAIResponses_UserEmptyBlocksAreDropped(t *testing.T) {
	req := &model.ClaudeMessagesRequest{
		Model:     "claude-3-opus",
		MaxTokens: 64,
		Messages: []model.ClaudeMessage{
			{Role: "user", Content: json.RawMessage(`[]`)},
		},
	}

	out, err := conversion.ConvertClaudeToOpenAIResponses(req, "sid")
	require.NoError(t, err)

	input := out["input"].([]map[string]any)
	require.Len(t, input, 0)
}

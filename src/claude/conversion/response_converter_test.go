package conversion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github/ruoyiran/claude-code-transformer/src/claude/conversion"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type flushingBuffer struct {
	bytes.Buffer
	flushCount int
}

func (b *flushingBuffer) Flush() {
	b.flushCount++
}

func TestConvertOpenAIToClaudeResponse_FromChatCompletion(t *testing.T) {
	openaiResp := map[string]any{
		"id":    "chatcmpl_x",
		"model": "gpt-5",
		"choices": []any{
			map[string]any{
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"content": "hello",
					"tool_calls": []any{
						map[string]any{
							"id": "call_1",
							"function": map[string]any{
								"name":      "get_weather",
								"arguments": `{"city":"beijing"}`,
							},
						},
					},
				},
			},
		},
	}
	out, err := conversion.ConvertOpenAIToClaudeResponse(openaiResp)
	require.NoError(t, err)
	require.Equal(t, "tool_use", out["stop_reason"])
	content := out["content"].([]map[string]any)
	require.Equal(t, "text", content[0]["type"])
	require.Equal(t, "tool_use", content[1]["type"])
}

func TestConvertOpenAIToClaudeResponse_FromResponsesAPI(t *testing.T) {
	resp := map[string]any{
		"id":     "resp_1",
		"object": "response",
		"model":  "gpt-5",
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{"type": "output_text", "text": "hello", "annotations": []any{map[string]any{"url": "https://openai.com", "title": "OpenAI"}}},
				},
			},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": `{"city":"beijing"}`},
			map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "plan"}}},
		},
		"usage": map[string]any{"input_tokens": 11, "output_tokens": 5},
	}
	out, err := conversion.ConvertOpenAIToClaudeResponse(resp)
	require.NoError(t, err)
	require.Equal(t, "resp_1", out["id"])
	require.Equal(t, "tool_use", out["stop_reason"])
	usage := out["usage"].(map[string]any)
	require.Equal(t, 11, usage["input_tokens"])
	require.Equal(t, 5, usage["output_tokens"])
	content := out["content"].([]map[string]any)
	require.GreaterOrEqual(t, len(content), 5)
	require.Equal(t, "thinking", content[0]["type"])
	require.Equal(t, "server_tool_use", content[1]["type"])
	require.Equal(t, "web_search_tool_result", content[2]["type"])
	require.Equal(t, "text", content[3]["type"])
	require.Equal(t, "tool_use", content[4]["type"])
}

func TestConvertOpenAIToClaudeResponse_FromResponsesAPIPreservesSignatureOnlyThinking(t *testing.T) {
	resp := map[string]any{
		"id":     "resp_sig_only",
		"object": "response",
		"model":  "gpt-5",
		"output": []any{
			map[string]any{"type": "reasoning", "encrypted_content": "sig_123"},
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{"type": "output_text", "text": "hello"},
				},
			},
		},
	}

	out, err := conversion.ConvertOpenAIToClaudeResponse(resp)
	require.NoError(t, err)

	content := out["content"].([]map[string]any)
	require.Len(t, content, 2)
	require.Equal(t, "thinking", content[0]["type"])
	require.Equal(t, "", content[0]["thinking"])
	require.Equal(t, "sig_123", content[0]["signature"])
	require.Equal(t, "text", content[1]["type"])
	require.Equal(t, "hello", content[1]["text"])
}

func TestConvertOpenAIToClaudeResponse_FromResponsesAPITextPartVariant(t *testing.T) {
	resp := map[string]any{
		"id":     "resp_text",
		"object": "response",
		"model":  "gpt-5",
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{"type": "text", "text": "<summary>ok</summary>"},
				},
			},
		},
	}

	out, err := conversion.ConvertOpenAIToClaudeResponse(resp)
	require.NoError(t, err)
	content := out["content"].([]map[string]any)
	require.Len(t, content, 1)
	require.Equal(t, "text", content[0]["type"])
	require.Equal(t, "<summary>ok</summary>", content[0]["text"])
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_FromResponsesEvents(t *testing.T) {
	lines := make(chan string, 16)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.output_text.delta","item_id":"it1","delta":"hello","response":{"model":"gpt-5"}}`
	lines <- `data: {"type":"response.output_text.annotation.added","item_id":"it1","annotation":{"url":"https://openai.com","title":"OpenAI"},"response":{"model":"gpt-5"}}`
	lines <- `data: {"type":"response.output_item.added","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"fn"},"response":{"model":"gpt-5"}}`
	lines <- `data: {"type":"response.function_call_arguments.delta","item_id":"fc1","delta":"{\"a\":"}`
	lines <- `data: {"type":"response.function_call_arguments.delta","item_id":"fc1","delta":"1}"}`
	lines <- `data: {"type":"response.reasoning_summary_text.delta","item_id":"r1","delta":"plan"}`
	lines <- `data: {"type":"response.output_item.done","item_id":"r1","item":{"type":"reasoning","encrypted_content":"sig_123"}}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[{"type":"function_call"}]}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)
	s := buf.String()
	require.Contains(t, s, "event: message_start")
	require.Contains(t, s, `"type":"text_delta"`)
	require.Contains(t, s, `"type":"web_search_tool_result"`)
	require.Contains(t, s, `"type":"tool_use"`)
	require.Equal(t, 2, strings.Count(s, `"type":"input_json_delta"`))
	require.Contains(t, s, `"type":"thinking_delta"`)
	require.Contains(t, s, `"type":"signature_delta"`)
	require.Contains(t, s, `"stop_reason":"tool_use"`)
	require.Contains(t, s, "event: message_stop")
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_FunctionCallArgsDoneWithoutDelta(t *testing.T) {
	lines := make(chan string, 8)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.output_item.added","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"fn"},"response":{"model":"gpt-5"}}`
	lines <- `data: {"type":"response.function_call_arguments.done","item_id":"fc1","name":"fn","arguments":"{\"a\":1}","response":{"model":"gpt-5"}}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[{"type":"function_call","id":"fc1","call_id":"call_1","name":"fn","arguments":"{\"a\":1}"}]}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)

	s := buf.String()
	require.Contains(t, s, `"type":"tool_use"`)
	require.Contains(t, s, `"partial_json":"{\"a\":1}"`)
	require.Contains(t, s, `"stop_reason":"tool_use"`)
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_ResponseCompletedBackfillsFunctionCall(t *testing.T) {
	lines := make(chan string, 8)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[{"id":"fc1","type":"function_call","call_id":"call_1","name":"fn","arguments":"{\"a\":1}"}]}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)

	s := buf.String()
	require.Contains(t, s, `"type":"tool_use"`)
	require.Contains(t, s, `"id":"call_1"`)
	require.Contains(t, s, `"partial_json":"{\"a\":1}"`)
	require.Contains(t, s, `"stop_reason":"tool_use"`)
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_ResponseCompletedAppendsMissingFunctionCallSuffix(t *testing.T) {
	lines := make(chan string, 8)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.output_item.added","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"fn"},"response":{"model":"gpt-5"}}`
	lines <- `data: {"type":"response.function_call_arguments.delta","item_id":"fc1","delta":"{\"a\":"}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[{"id":"fc1","type":"function_call","call_id":"call_1","name":"fn","arguments":"{\"a\":1}"}]}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)

	s := buf.String()
	require.Equal(t, 2, strings.Count(s, `"type":"input_json_delta"`))
	require.Contains(t, s, `"partial_json":"{\"a\":"`)
	require.Contains(t, s, `"partial_json":"1}"`)
	require.Contains(t, s, `"stop_reason":"tool_use"`)
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_FlushesEachEvent(t *testing.T) {
	lines := make(chan string, 4)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.output_text.delta","item_id":"it1","delta":"hello","response":{"model":"gpt-5"}}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[],"usage":{"input_tokens":1,"output_tokens":2}}}`
	lines <- `data: [DONE]`
	close(lines)

	buf := &flushingBuffer{}
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)
	require.Equal(t, strings.Count(buf.String(), "event: "), buf.flushCount)
	require.Greater(t, buf.flushCount, 0)
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_ConvertsKeepaliveToPing(t *testing.T) {
	lines := make(chan string, 5)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"keepalive","sequence_number":2}`
	lines <- `data: {"type":"response.output_text.delta","item_id":"it1","delta":"hello","response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[],"usage":{"input_tokens":1,"output_tokens":2}}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)

	s := buf.String()
	require.Contains(t, s, "event: ping")
	require.Contains(t, s, `data: {"type":"ping"}`)
	require.Less(t, strings.Index(s, "event: ping"), strings.Index(s, "event: message_start"))
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_FromResponsesMessageItemTextVariant(t *testing.T) {
	lines := make(chan string, 4)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.output_item.added","item":{"type":"message","content":[{"type":"text","text":"summary ready"}]},"response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[{"type":"message"}],"usage":{"input_tokens":1,"output_tokens":2}}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)
	s := buf.String()
	require.Contains(t, s, "event: message_start")
	require.Contains(t, s, `"type":"text_delta"`)
	require.Contains(t, s, "summary ready")
	require.Contains(t, s, `"stop_reason":"end_turn"`)
	require.Contains(t, s, "event: message_stop")
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_EmitsMessageStartBeforeEarlyError(t *testing.T) {
	lines := make(chan string, 8)
	errCh := make(chan error)
	defer close(errCh)

	lines <- `data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.in_progress","response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","summary":[]},"response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"error","error":{"type":"server_error","message":"boom"}}`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.ErrorContains(t, err, `Streaming error: {"message":"boom","type":"server_error"}`)
	out := buf.String()
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, `"type":"content_block_start"`)
	require.Contains(t, out, `"type":"thinking"`)
	require.Contains(t, out, `"input_tokens":0`)
	require.Contains(t, out, "event: error")
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_StartsThinkingBlockOnReasoningAdded(t *testing.T) {
	lines := make(chan string, 8)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","summary":[]},"response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.output_item.done","item_id":"rs_1","item":{"type":"reasoning","encrypted_content":"sig_123"},"response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[],"usage":{"input_tokens":1,"output_tokens":2}}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)

	s := buf.String()
	require.Contains(t, s, "event: message_start")
	require.Contains(t, s, `"type":"content_block_start"`)
	require.Contains(t, s, `"type":"thinking"`)
	require.Contains(t, s, `"type":"signature_delta"`)
	require.Contains(t, s, "event: content_block_stop")
	require.Contains(t, s, "event: message_stop")
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_KeepsLateReasoningOnThinkingBlock(t *testing.T) {
	lines := make(chan string, 8)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.output_item.added","item":{"id":"r0","type":"reasoning","encrypted_content":"sig_0"},"response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"hello","response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.output_item.added","item":{"id":"r1","type":"reasoning","encrypted_content":"sig_1"},"response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.reasoning_summary_text.delta","item_id":"r1","delta":"plan","response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[],"usage":{"input_tokens":1,"output_tokens":2}}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)

	events := decodeSSEDataEvents(t, buf.String())
	blockTypes := map[int]string{}
	textBlockIndex := -1
	thinkingBlocks := 0

	for _, event := range events {
		if event["type"] != "content_block_start" {
			continue
		}
		index := eventIndex(t, event)
		contentBlock, ok := event["content_block"].(map[string]any)
		require.True(t, ok)
		blockType, ok := contentBlock["type"].(string)
		require.True(t, ok)
		blockTypes[index] = blockType
		if blockType == "text" {
			textBlockIndex = index
		}
		if blockType == "thinking" {
			thinkingBlocks++
		}
	}

	require.NotEqual(t, -1, textBlockIndex)
	require.GreaterOrEqual(t, thinkingBlocks, 2)

	for _, event := range events {
		if event["type"] != "content_block_delta" {
			continue
		}
		delta, ok := event["delta"].(map[string]any)
		require.True(t, ok)
		deltaType, ok := delta["type"].(string)
		require.True(t, ok)
		if deltaType != "thinking_delta" && deltaType != "signature_delta" {
			continue
		}
		index := eventIndex(t, event)
		require.NotEqual(t, textBlockIndex, index)
		require.Equal(t, "thinking", blockTypes[index])
	}
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_ClosesOnDoneWithoutCompleted(t *testing.T) {
	lines := make(chan string, 8)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":"sig_123","summary":[]},"output_index":0,"sequence_number":3}`
	lines <- `data: {"type":"response.output_text.delta","content_index":0,"delta":"hello","item_id":"msg_1","output_index":1,"sequence_number":6}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)

	s := buf.String()
	require.Contains(t, s, "event: message_start")
	require.Contains(t, s, `"type":"signature_delta"`)
	require.Contains(t, s, `"type":"text_delta"`)
	require.Contains(t, s, `"stop_reason":"end_turn"`)
	require.Contains(t, s, "event: message_stop")
	require.NotContains(t, s, "event: error")
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_ReturnsErrorOnPrematureClose(t *testing.T) {
	lines := make(chan string, 4)
	errCh := make(chan error)
	defer close(errCh)

	lines <- `data: {"type":"response.output_text.delta","item_id":"it1","delta":"hello","response":{"model":"gpt-5"}}`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.EqualError(t, err, "Streaming error: upstream stream ended before completion")
	s := buf.String()
	require.Contains(t, s, "event: error")
	require.Contains(t, s, "upstream stream ended before completion")
	require.NotContains(t, s, "event: message_stop")
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_ReturnsErrorFromErrCh(t *testing.T) {
	lines := make(chan string)
	errCh := make(chan error, 1)
	errCh <- io.ErrUnexpectedEOF
	close(errCh)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	s := buf.String()
	require.Contains(t, s, "event: message_start")
	require.Contains(t, s, `"input_tokens":0`)
	require.Contains(t, s, "event: error")
	require.Contains(t, s, "Streaming error: unexpected EOF")
	require.NotContains(t, s, "event: message_stop")
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_CancelsOnClientDisconnect(t *testing.T) {
	lines := make(chan string, 1)
	errCh := make(chan error, 1)
	lines <- `data: {"type":"response.output_text.delta","item_id":"it1","delta":"hello","response":{"model":"gpt-5"}}`

	var buf bytes.Buffer
	cancelCalled := false
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return true },
		func() { cancelCalled = true },
	)
	require.NoError(t, err)
	require.True(t, cancelCalled)
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_ReturnsContextErrorWhenStillConnected(t *testing.T) {
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()

	lines := make(chan string)
	errCh := make(chan error)
	cancelCalled := false
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		ctx,
		lines,
		errCh,
		io.Discard,
		func() bool { return false },
		func() { cancelCalled = true },
	)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, cancelCalled)
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_UsesCompletedUsageInMessageDelta(t *testing.T) {
	lines := make(chan string, 8)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5","usage":null}}`
	lines <- `data: {"type":"response.in_progress","response":{"id":"resp_1","model":"gpt-5","usage":null}}`
	lines <- `data: {"type":"response.output_text.delta","item_id":"it1","delta":"hello","response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[],"usage":{"input_tokens":143947,"input_tokens_details":{"cached_tokens":18816},"output_tokens":859}}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)

	events := decodeSSEDataEvents(t, buf.String())
	messageStart := findStreamEvent(t, events, "message_start")
	startUsage := messageStart["message"].(map[string]any)["usage"].(map[string]any)
	require.EqualValues(t, 0, startUsage["input_tokens"])
	require.EqualValues(t, 0, startUsage["cache_read_input_tokens"])

	messageDelta := findStreamEvent(t, events, "message_delta")
	deltaUsage := messageDelta["usage"].(map[string]any)
	require.EqualValues(t, 143947, deltaUsage["input_tokens"])
	require.EqualValues(t, 859, deltaUsage["output_tokens"])
}

func TestConvertOpenAIStreamingToClaudeWithCancellation_DefaultsMissingCompletedUsage(t *testing.T) {
	lines := make(chan string, 8)
	errCh := make(chan error, 1)
	defer close(errCh)

	lines <- `data: {"type":"response.output_text.delta","item_id":"it1","delta":"hello","response":{"id":"resp_1","model":"gpt-5"}}`
	lines <- `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","output":[],"usage":null}}`
	lines <- `data: [DONE]`
	close(lines)

	var buf bytes.Buffer
	err := conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
		"req-1",
		context.Background(),
		lines,
		errCh,
		&buf,
		func() bool { return false },
		func() {},
	)
	require.NoError(t, err)

	events := decodeSSEDataEvents(t, buf.String())
	messageDelta := findStreamEvent(t, events, "message_delta")
	deltaUsage := messageDelta["usage"].(map[string]any)
	require.EqualValues(t, 0, deltaUsage["input_tokens"])
	require.EqualValues(t, 0, deltaUsage["output_tokens"])
}

func decodeSSEDataEvents(t *testing.T, stream string) []map[string]any {
	t.Helper()

	events := make([]map[string]any, 0)
	for _, block := range strings.Split(strings.TrimSpace(stream), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
			events = append(events, event)
		}
	}
	return events
}

func findStreamEvent(t *testing.T, events []map[string]any, eventType string) map[string]any {
	t.Helper()
	for _, event := range events {
		if event["type"] == eventType {
			return event
		}
	}
	t.Fatalf("stream event %q not found", eventType)
	return nil
}

func eventIndex(t *testing.T, event map[string]any) int {
	t.Helper()
	switch v := event["index"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		t.Fatalf("event index missing or invalid: %#v", event["index"])
		return -1
	}
}

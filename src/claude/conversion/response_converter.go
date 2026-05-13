package conversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github/ruoyiran/claude-code-transformer/src/claude/constants"
	"github/ruoyiran/claude-code-transformer/src/utils"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func ConvertOpenAIToClaudeResponse(openaiResp map[string]any) (map[string]any, error) {
	if fmt.Sprint(openaiResp["object"]) == "response" {
		return convertOpenAIResponsesToClaudeResponse(openaiResp), nil
	}

	choices, _ := openaiResp["choices"].([]any)
	if len(choices) == 0 {
		return nil, fmt.Errorf("No choices in OpenAI response")
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return nil, fmt.Errorf("No valid choice in OpenAI response")
	}
	message, _ := choice["message"].(map[string]any)
	if message == nil {
		message = map[string]any{}
	}

	contentBlocks := make([]map[string]any, 0)

	// annotations -> server_tool_use + web_search_tool_result
	if anns, ok := message["annotations"].([]any); ok && len(anns) > 0 {
		id := "srvtoolu_" + uuid.NewString()
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "server_tool_use",
			"id":   id,
			"name": "web_search",
			"input": map[string]any{
				"query": "",
			},
		})

		results := make([]map[string]any, 0, len(anns))
		for _, annAny := range anns {
			ann, _ := annAny.(map[string]any)
			if ann == nil {
				continue
			}
			urlCitation, _ := ann["url_citation"].(map[string]any)
			results = append(results, map[string]any{
				"type":  "web_search_result",
				"url":   fmt.Sprint(urlCitation["url"]),
				"title": fmt.Sprint(urlCitation["title"]),
			})
		}

		contentBlocks = append(contentBlocks, map[string]any{
			"type":        "web_search_tool_result",
			"tool_use_id": id,
			"content":     results,
		})
	}

	// text content
	if c, ok := message["content"]; ok && c != nil {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": constants.ContentText,
			"text": fmt.Sprint(c),
		})
	}

	// tool calls
	toolCalls, _ := message["tool_calls"].([]any)
	for _, tcAny := range toolCalls {
		tc, _ := tcAny.(map[string]any)
		if tc == nil {
			continue
		}
		fn, _ := tc[constants.ToolFunction].(map[string]any)
		if fn == nil {
			continue
		}

		parsedInput := map[string]any{}
		argsRaw, hasArgs := fn["arguments"]
		if hasArgs {
			switch v := argsRaw.(type) {
			case map[string]any:
				parsedInput = v
			case string:
				if strings.TrimSpace(v) == "" {
					parsedInput = map[string]any{}
				} else if err := json.Unmarshal([]byte(v), &parsedInput); err != nil {
					parsedInput = map[string]any{"text": v}
				}
			default:
				b, _ := json.Marshal(v)
				if len(b) > 0 && string(b) != "null" {
					_ = json.Unmarshal(b, &parsedInput)
				}
			}
		}

		contentBlocks = append(contentBlocks, map[string]any{
			"type":  constants.ContentToolUse,
			"id":    fmt.Sprint(tc["id"]),
			"name":  fmt.Sprint(fn["name"]),
			"input": parsedInput,
		})
	}

	// thinking
	if thinking, ok := message["thinking"].(map[string]any); ok && thinking != nil {
		if content, _ := thinking["content"].(string); content != "" {
			contentBlocks = append(contentBlocks, map[string]any{
				"type":      constants.ContentThinking,
				"thinking":  content,
				"signature": fmt.Sprint(thinking["signature"]),
			})
		}
	}

	usage, _ := openaiResp["usage"].(map[string]any)
	cacheRead := 0
	if ptd, ok := usage["prompt_tokens_details"].(map[string]any); ok && ptd != nil {
		cacheRead = intFromAny(ptd["cached_tokens"])
	}
	promptTokens := intFromAny(usage["prompt_tokens"]) - cacheRead
	completionTokens := intFromAny(usage["completion_tokens"])

	return map[string]any{
		"id":            fmt.Sprint(openaiResp["id"]),
		"type":          "message",
		"role":          constants.ROLEAssistant,
		"model":         fmt.Sprint(openaiResp["model"]),
		"content":       contentBlocks,
		"stop_reason":   mapFinishReason(fmt.Sprint(choice["finish_reason"])),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":            promptTokens,
			"output_tokens":           completionTokens,
			"cache_read_input_tokens": cacheRead,
		},
	}, nil
}

func convertOpenAIResponsesToClaudeResponse(resp map[string]any) map[string]any {
	output, _ := resp["output"].([]any)
	messageOutput := map[string]any(nil)
	functionCalls := make([]map[string]any, 0)
	reasoningOutputs := make([]map[string]any, 0)
	for _, it := range output {
		item, _ := it.(map[string]any)
		if item == nil {
			continue
		}
		switch fmt.Sprint(item["type"]) {
		case "message":
			if messageOutput == nil {
				messageOutput = item
			}
		case "function_call":
			functionCalls = append(functionCalls, item)
		case "reasoning":
			reasoningOutputs = append(reasoningOutputs, item)
		}
	}

	contentBlocks := make([]map[string]any, 0)
	thinkingContent := ""
	thinkingSignature := ""
	if messageOutput != nil {
		if v := strings.TrimSpace(fmt.Sprint(messageOutput["reasoning"])); v != "" && v != "<nil>" {
			thinkingContent = v
		}
	}
	if thinkingContent == "" {
		for _, r := range reasoningOutputs {
			if summary, ok := r["summary"].([]any); ok {
				parts := make([]string, 0)
				for _, sAny := range summary {
					s, _ := sAny.(map[string]any)
					if fmt.Sprint(s["type"]) == "summary_text" {
						parts = append(parts, fmt.Sprint(s["text"]))
					}
				}
				if len(parts) > 0 {
					thinkingContent = strings.Join(parts, "\n\n")
					break
				}
			}
		}
	}
	for _, r := range reasoningOutputs {
		if sig := strings.TrimSpace(fmt.Sprint(r["encrypted_content"])); sig != "" && sig != "<nil>" {
			thinkingSignature = sig
			break
		}
	}
	if thinkingContent != "" || thinkingSignature != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":      constants.ContentThinking,
			"thinking":  thinkingContent,
			"signature": thinkingSignature,
		})
	}

	if messageOutput != nil {
		if content, _ := messageOutput["content"].([]any); len(content) > 0 {
			textParts, annotations := extractResponsesMessageTextAndAnnotations(content)

			if len(annotations) > 0 {
				id := "srvtoolu_" + uuid.NewString()
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "server_tool_use",
					"id":   id,
					"name": "web_search",
					"input": map[string]any{
						"query": "",
					},
				})
				results := make([]map[string]any, 0, len(annotations))
				for _, ann := range annotations {
					results = append(results, map[string]any{
						"type":  "web_search_result",
						"url":   fmt.Sprint(ann["url"]),
						"title": fmt.Sprint(ann["title"]),
					})
				}
				contentBlocks = append(contentBlocks, map[string]any{
					"type":        "web_search_tool_result",
					"tool_use_id": id,
					"content":     results,
				})
			}

			if len(textParts) > 0 {
				contentBlocks = append(contentBlocks, map[string]any{
					"type": constants.ContentText,
					"text": strings.Join(textParts, ""),
				})
			}
		}
	}

	for _, fc := range functionCalls {
		parsedInput := map[string]any{}
		argsStr := fmt.Sprint(fc["arguments"])
		if strings.TrimSpace(argsStr) == "" {
			parsedInput = map[string]any{}
		} else if err := json.Unmarshal([]byte(argsStr), &parsedInput); err != nil {
			parsedInput = map[string]any{"text": argsStr}
		}
		contentBlocks = append(contentBlocks, map[string]any{
			"type":  constants.ContentToolUse,
			"id":    fmt.Sprint(firstNonEmpty(fc["call_id"], fc["id"])),
			"name":  fmt.Sprint(fc["name"]),
			"input": parsedInput,
		})
	}

	stopReason := constants.StopEndTurn
	if inc, ok := resp["incomplete_details"].(map[string]any); ok {
		if fmt.Sprint(inc["reason"]) == "max_output_tokens" {
			stopReason = constants.StopMaxTokens
		}
	}
	if stopReason != constants.StopMaxTokens && len(functionCalls) > 0 {
		stopReason = constants.StopToolUse
	}

	usage := normalizedAnthropicUsage(resp["usage"])

	return map[string]any{
		"id":            fmt.Sprint(resp["id"]),
		"type":          "message",
		"role":          constants.ROLEAssistant,
		"model":         fmt.Sprint(resp["model"]),
		"content":       contentBlocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	}
}

func extractResponsesMessageTextAndAnnotations(content []any) ([]string, []map[string]any) {
	annotations := make([]map[string]any, 0)
	textParts := make([]string, 0)
	for _, partAny := range content {
		part, _ := partAny.(map[string]any)
		if part == nil {
			continue
		}
		text, ok := extractResponsesTextPart(part)
		if !ok {
			continue
		}
		textParts = append(textParts, text)
		if anns, ok := part["annotations"].([]any); ok && len(anns) > 0 {
			for _, annAny := range anns {
				ann, _ := annAny.(map[string]any)
				if ann == nil {
					continue
				}
				annotations = append(annotations, ann)
			}
		}
	}
	return textParts, annotations
}

func extractResponsesTextPart(part map[string]any) (string, bool) {
	switch fmt.Sprint(part["type"]) {
	case "output_text", "text", "input_text":
	default:
		return "", false
	}

	switch text := part["text"].(type) {
	case string:
		if text == "" {
			return "", false
		}
		return text, true
	case map[string]any:
		value := firstNonEmpty(text["value"], text["text"])
		if value == "" {
			return "", false
		}
		return value, true
	default:
		value := fmt.Sprint(part["text"])
		if value == "" || value == "<nil>" {
			return "", false
		}
		return value, true
	}
}

func normalizedAnthropicUsage(rawUsage any) map[string]any {
	usage := map[string]any{
		"input_tokens":                0,
		"output_tokens":               0,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     0,
	}

	u, _ := rawUsage.(map[string]any)
	if u == nil {
		return usage
	}

	usage["input_tokens"] = intFromAny(u["input_tokens"])
	usage["output_tokens"] = intFromAny(u["output_tokens"])
	usage["cache_creation_input_tokens"] = intFromAny(u["cache_creation_input_tokens"])
	usage["cache_read_input_tokens"] = intFromAny(u["cache_read_input_tokens"])

	if inputDetails, ok := u["input_tokens_details"].(map[string]any); ok && inputDetails != nil {
		if intFromAny(usage["cache_creation_input_tokens"]) == 0 {
			usage["cache_creation_input_tokens"] = intFromAny(inputDetails["cache_creation_tokens"])
		}
		if intFromAny(usage["cache_read_input_tokens"]) == 0 {
			usage["cache_read_input_tokens"] = intFromAny(inputDetails["cached_tokens"])
		}
	}

	return usage
}

func normalizedStreamingUsage(rawUsage any) map[string]any {
	usage := normalizedAnthropicUsage(rawUsage)
	return map[string]any{
		"input_tokens":                usage["input_tokens"],
		"output_tokens":               usage["output_tokens"],
		"cache_creation_input_tokens": usage["cache_creation_input_tokens"],
		"cache_read_input_tokens":     usage["cache_read_input_tokens"],
		"prompt_tokens":               usage["input_tokens"],
		"completion_tokens":           usage["output_tokens"],
	}
}

// OpenAI stream input is a stream of SSE lines, usually "data: {json}".
func ConvertOpenAIStreamingToClaudeWithCancellation(
	reqID string,
	ctx context.Context,
	lines <-chan string,
	errCh <-chan error,
	writer io.Writer,
	clientDisconnected func() bool,
	cancelRequest func(),
) error {
	messageID := fmt.Sprintf("msg_%d", time.Now().UnixMilli())

	streamWrite := func(event string, data any) error {
		logrus.Debugf("reqID: %s, event: %s, data: %s", reqID, event, utils.MarshalIndentJsonToString(data, "  "))
		b, _ := json.Marshal(data)
		_, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, string(b))
		if err != nil {
			return err
		}
		if flusher, ok := writer.(interface{ Flush() }); ok {
			flusher.Flush()
		}
		return nil
	}

	stopReasonMessageDelta := map[string]any(nil)
	modelName := "unknown"
	messageUsage := normalizedAnthropicUsage(nil)
	hasStarted := false
	hasFinished := false
	toolCallIndexToContentBlockIndex := map[int]int{}
	contentIndex := 0
	currentContentBlockIndex := -1
	currentContentBlockType := ""
	currentThinkingItemID := ""
	pendingThinkingSignature := ""
	responsesToolCallIndexMap := map[string]int{}
	nextResponsesToolCallIndex := 0
	responsesRoleEmitted := false

	type toolCallInfo struct {
		ID                string
		Name              string
		Arguments         string
		ContentBlockIndex int
	}
	toolCalls := map[int]*toolCallInfo{}

	assignContentBlockIndex := func() int {
		idx := contentIndex
		contentIndex++
		return idx
	}

	normalizeToolArguments := func(v any) string {
		switch t := v.(type) {
		case nil:
			return ""
		case string:
			return t
		default:
			b, _ := json.Marshal(t)
			if len(b) == 0 || string(b) == "null" {
				return ""
			}
			return string(b)
		}
	}

	messageStart := func() map[string]any {
		return map[string]any{
			"type": constants.EventMessageStart,
			"message": map[string]any{
				"id":            messageID,
				"type":          "message",
				"role":          constants.ROLEAssistant,
				"content":       []any{},
				"model":         modelName,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         messageUsage,
			},
		}
	}

	emitMessageStart := func() {
		if hasStarted {
			return
		}
		_ = streamWrite(constants.EventMessageStart, messageStart())
		hasStarted = true
	}

	streamError := func(message string) error {
		emitMessageStart()
		_ = streamWrite("error", map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": message,
			},
		})
		return errors.New(message)
	}

	emitPendingThinkingSignature := func() {
		if currentContentBlockIndex < 0 || currentContentBlockType != constants.ContentThinking {
			return
		}
		if pendingThinkingSignature == "" {
			return
		}
		_ = streamWrite(constants.EventContentBlockDelta, map[string]any{
			"type":  constants.EventContentBlockDelta,
			"index": currentContentBlockIndex,
			"delta": map[string]any{
				"type":      "signature_delta",
				"signature": pendingThinkingSignature,
			},
		})
		pendingThinkingSignature = ""
	}

	closeCurrentContentBlock := func() {
		if currentContentBlockIndex < 0 {
			return
		}
		emitPendingThinkingSignature()
		_ = streamWrite(constants.EventContentBlockStop, map[string]any{
			"type":  constants.EventContentBlockStop,
			"index": currentContentBlockIndex,
		})
		currentContentBlockIndex = -1
		currentContentBlockType = ""
		currentThinkingItemID = ""
	}

	startContentBlock := func(blockType string, block map[string]any) int {
		blockIndex := assignContentBlockIndex()
		_ = streamWrite(constants.EventContentBlockStart, map[string]any{
			"type":          constants.EventContentBlockStart,
			"index":         blockIndex,
			"content_block": block,
		})
		currentContentBlockIndex = blockIndex
		currentContentBlockType = blockType
		if blockType != constants.ContentThinking {
			currentThinkingItemID = ""
			pendingThinkingSignature = ""
		}
		return blockIndex
	}

	getOrAssignToolCallIndex := func(keys ...string) int {
		for _, key := range keys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if idx, ok := responsesToolCallIndexMap[key]; ok {
				for _, alias := range keys {
					alias = strings.TrimSpace(alias)
					if alias != "" {
						responsesToolCallIndexMap[alias] = idx
					}
				}
				return idx
			}
		}

		idx := nextResponsesToolCallIndex
		nextResponsesToolCallIndex++
		for _, key := range keys {
			key = strings.TrimSpace(key)
			if key != "" {
				responsesToolCallIndexMap[key] = idx
			}
		}
		return idx
	}

	ensureToolCallBlock := func(toolCallIndex int, toolCallID, toolCallName string) *toolCallInfo {
		if existing := toolCalls[toolCallIndex]; existing != nil {
			if existing.ID == "" && toolCallID != "" {
				existing.ID = toolCallID
			}
			if existing.Name == "" && toolCallName != "" {
				existing.Name = toolCallName
			}
			return existing
		}

		emitMessageStart()
		closeCurrentContentBlock()

		if toolCallID == "" {
			toolCallID = fmt.Sprintf("call_%d_%d", time.Now().UnixMilli(), toolCallIndex)
		}
		if toolCallName == "" {
			toolCallName = fmt.Sprintf("tool_%d", toolCallIndex)
		}

		newContentBlockIndex := assignContentBlockIndex()
		toolCallIndexToContentBlockIndex[toolCallIndex] = newContentBlockIndex
		_ = streamWrite(constants.EventContentBlockStart, map[string]any{
			"type":  constants.EventContentBlockStart,
			"index": newContentBlockIndex,
			"content_block": map[string]any{
				"type":  constants.ContentToolUse,
				"id":    toolCallID,
				"name":  toolCallName,
				"input": map[string]any{},
			},
		})
		currentContentBlockIndex = newContentBlockIndex
		currentContentBlockType = constants.ContentToolUse

		tcInfo := &toolCallInfo{
			ID:                toolCallID,
			Name:              toolCallName,
			Arguments:         "",
			ContentBlockIndex: newContentBlockIndex,
		}
		toolCalls[toolCallIndex] = tcInfo
		return tcInfo
	}

	appendToolCallArguments := func(toolCallIndex int, argText string) {
		if argText == "" {
			return
		}
		tcInfo := toolCalls[toolCallIndex]
		if tcInfo == nil {
			return
		}
		blockIndex, ok := toolCallIndexToContentBlockIndex[toolCallIndex]
		if !ok {
			return
		}

		emitMessageStart()
		tcInfo.Arguments += argText
		_ = streamWrite(constants.EventContentBlockDelta, map[string]any{
			"type":  constants.EventContentBlockDelta,
			"index": blockIndex,
			"delta": map[string]any{
				"type":         constants.DeltaInputJSON,
				"partial_json": argText,
			},
		})
	}

	reconcileToolCall := func(toolCallIndex int, toolCallID, toolCallName, fullArgs string, allowCreate bool) {
		tcInfo := toolCalls[toolCallIndex]
		if tcInfo == nil {
			if !allowCreate {
				return
			}
			tcInfo = ensureToolCallBlock(toolCallIndex, toolCallID, toolCallName)
		}
		if tcInfo == nil {
			return
		}
		if fullArgs == "" {
			return
		}
		if tcInfo.Arguments == "" {
			appendToolCallArguments(toolCallIndex, fullArgs)
			return
		}
		if strings.HasPrefix(fullArgs, tcInfo.Arguments) && len(fullArgs) > len(tcInfo.Arguments) {
			appendToolCallArguments(toolCallIndex, fullArgs[len(tcInfo.Arguments):])
		}
	}

	reconcileToolCallFromOutputItem := func(item map[string]any, allowCreate bool) {
		if item == nil || fmt.Sprint(item["type"]) != "function_call" {
			return
		}
		callID := firstNonEmpty(item["call_id"])
		itemID := firstNonEmpty(item["id"])
		toolCallID := firstNonEmpty(callID, itemID)
		if toolCallID == "" {
			return
		}
		toolCallIndex := getOrAssignToolCallIndex(callID, itemID)
		reconcileToolCall(
			toolCallIndex,
			toolCallID,
			firstNonEmpty(item["name"]),
			normalizeToolArguments(item["arguments"]),
			allowCreate,
		)
	}

	reconcileToolCallsFromCompleted := func(responseObj map[string]any) {
		output, _ := responseObj["output"].([]any)
		for _, it := range output {
			item, _ := it.(map[string]any)
			reconcileToolCallFromOutputItem(item, true)
		}
	}

	ensureThinkingBlock := func(itemID string) {
		if currentContentBlockType == constants.ContentThinking && currentContentBlockIndex >= 0 {
			if itemID == "" || currentThinkingItemID == "" || currentThinkingItemID == itemID {
				if currentThinkingItemID == "" {
					currentThinkingItemID = itemID
				}
				return
			}
		}
		closeCurrentContentBlock()
		startContentBlock(constants.ContentThinking, map[string]any{
			"type":     constants.ContentThinking,
			"thinking": "",
		})
		currentThinkingItemID = itemID
	}

	ensureTextBlock := func() {
		if currentContentBlockType == constants.ContentText && currentContentBlockIndex >= 0 {
			return
		}
		closeCurrentContentBlock()
		startContentBlock(constants.ContentText, map[string]any{
			"type": constants.ContentText,
			"text": "",
		})
	}

	safeClose := func() {
		emitMessageStart()
		closeCurrentContentBlock()

		if stopReasonMessageDelta != nil {
			_ = streamWrite(constants.EventMessageDelta, stopReasonMessageDelta)
		} else {
			_ = streamWrite(constants.EventMessageDelta, map[string]any{
				"type": constants.EventMessageDelta,
				"delta": map[string]any{
					"stop_reason":   constants.StopEndTurn,
					"stop_sequence": nil,
				},
				"usage": map[string]any{
					"input_tokens":  intFromAny(messageUsage["input_tokens"]),
					"output_tokens": intFromAny(messageUsage["output_tokens"]),
				},
			})
		}

		_ = streamWrite(constants.EventMessageStop, map[string]any{
			"type": constants.EventMessageStop,
		})
	}

	for {
		select {
		case <-ctx.Done():
			if cancelRequest != nil {
				cancelRequest()
			}
			if clientDisconnected != nil && clientDisconnected() {
				return nil
			}
			return ctx.Err()
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				if clientDisconnected != nil && clientDisconnected() {
					return nil
				}
				emitMessageStart()
				_ = streamWrite("error", map[string]any{
					"type": "error",
					"error": map[string]any{
						"type":    "api_error",
						"message": "Streaming error: " + err.Error(),
					},
				})
				return fmt.Errorf("Streaming error: %w", err)
			}
		case line, ok := <-lines:
			if !ok {
				if hasFinished {
					goto END
				}
				if clientDisconnected != nil && clientDisconnected() {
					return nil
				}
				return streamError("Streaming error: upstream stream ended before completion")
			}

			if clientDisconnected != nil && clientDisconnected() {
				if cancelRequest != nil {
					cancelRequest()
				}
				return nil
			}

			if hasFinished {
				continue
			}

			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				if hasStarted || currentContentBlockIndex >= 0 || stopReasonMessageDelta != nil {
					hasFinished = true
					goto END
				}
				continue
			}

			var rawChunk map[string]any
			if err := json.Unmarshal([]byte(data), &rawChunk); err != nil {
				return streamError("Streaming error: failed to decode upstream stream chunk")
			}

			eventType := fmt.Sprint(rawChunk["type"])
			if eventType == "keepalive" {
				if err := streamWrite(constants.EventPing, map[string]any{
					"type": "ping",
				}); err != nil {
					return err
				}
				continue
			}
			if eventType == "response.created" || eventType == "response.in_progress" || eventType == "response.completed" {
				if responseObj, ok := rawChunk["response"].(map[string]any); ok && responseObj != nil {
					if m := fmt.Sprint(responseObj["model"]); m != "" {
						modelName = m
					}
					messageUsage = normalizedAnthropicUsage(responseObj["usage"])
					if eventType == "response.completed" {
						reconcileToolCallsFromCompleted(responseObj)
					}
				}
			}
			if eventType == "response.output_item.done" {
				if item, ok := rawChunk["item"].(map[string]any); ok && item != nil {
					reconcileToolCallFromOutputItem(item, true)
				}
			}
			if eventType == "response.function_call_arguments.done" {
				itemID := firstNonEmpty(rawChunk["item_id"])
				if itemID != "" {
					if toolCallIndex, ok := responsesToolCallIndexMap[itemID]; ok {
						reconcileToolCall(
							toolCallIndex,
							"",
							firstNonEmpty(rawChunk["name"]),
							normalizeToolArguments(rawChunk["arguments"]),
							false,
						)
					}
				}
			}

			chunks := normalizeSSEToChatChunks(rawChunk, &responsesToolCallIndexMap, &nextResponsesToolCallIndex, &responsesRoleEmitted)
			for _, chunk := range chunks {
				logrus.Debugf("chunk -> %s", strings.ReplaceAll(utils.MarshalJsonToString(chunk), "\n", ""))
				if chunkErr, ok := chunk["error"]; ok && chunkErr != nil {
					b, _ := json.Marshal(chunkErr)
					return streamError("Streaming error: " + string(b))
				}

				if m, _ := chunk["model"].(string); m != "" {
					modelName = m
				}

				emitMessageStart()

				if usage, ok := chunk["usage"].(map[string]any); ok && usage != nil {
					inputTokens := intFromAny(usage["prompt_tokens"])
					if inputTokens == 0 {
						inputTokens = intFromAny(usage["input_tokens"])
					}
					outputTokens := intFromAny(usage["completion_tokens"])
					if outputTokens == 0 {
						outputTokens = intFromAny(usage["output_tokens"]) // Responses API 兼容
					}

					messageDeltaUsage := map[string]any{
						"input_tokens":  inputTokens,
						"output_tokens": outputTokens,
					}
					if stopReasonMessageDelta == nil {
						stopReasonMessageDelta = map[string]any{
							"type": constants.EventMessageDelta,
							"delta": map[string]any{
								"stop_reason":   constants.StopEndTurn,
								"stop_sequence": nil,
							},
							"usage": messageDeltaUsage,
						}
					} else {
						stopReasonMessageDelta["usage"] = messageDeltaUsage
					}
				}

				choices, _ := chunk["choices"].([]any)
				if len(choices) == 0 {
					continue
				}
				choice, _ := choices[0].(map[string]any)
				if choice == nil {
					continue
				}
				delta, _ := choice["delta"].(map[string]any)

				if delta != nil {
					if thinking, ok := delta["thinking"].(map[string]any); ok && thinking != nil {
						thinkingItemID := strings.TrimSpace(fmt.Sprint(thinking["item_id"]))
						ensureThinkingBlock(thinkingItemID)
						if sig, _ := thinking["signature"].(string); sig != "" {
							pendingThinkingSignature = sig
						}
						if c, ok := thinking["content"]; ok && c != nil {
							_ = streamWrite(constants.EventContentBlockDelta, map[string]any{
								"type":  constants.EventContentBlockDelta,
								"index": currentContentBlockIndex,
								"delta": map[string]any{
									"type":     constants.DeltaThinking,
									"thinking": fmt.Sprint(c),
								},
							})
						}
					}
				}

				if delta != nil {
					if c, ok := delta["content"]; ok && c != nil {
						ensureTextBlock()

						_ = streamWrite(constants.EventContentBlockDelta, map[string]any{
							"type":  constants.EventContentBlockDelta,
							"index": currentContentBlockIndex,
							"delta": map[string]any{
								"type": constants.DeltaText,
								"text": fmt.Sprint(c),
							},
						})
					}
				}

				if delta != nil {
					if annotations, ok := delta["annotations"].([]any); ok && len(annotations) > 0 {
						if currentContentBlockType == constants.ContentText {
							closeCurrentContentBlock()
						}

						for _, annAny := range annotations {
							ann, _ := annAny.(map[string]any)
							urlCitation, _ := ann["url_citation"].(map[string]any)
							annotationBlockIndex := assignContentBlockIndex()
							_ = streamWrite(constants.EventContentBlockStart, map[string]any{
								"type":  constants.EventContentBlockStart,
								"index": annotationBlockIndex,
								"content_block": map[string]any{
									"type":        "web_search_tool_result",
									"tool_use_id": "srvtoolu_" + uuid.NewString(),
									"content": []map[string]any{
										{
											"type":  "web_search_result",
											"title": fmt.Sprint(urlCitation["title"]),
											"url":   fmt.Sprint(urlCitation["url"]),
										},
									},
								},
							})
							_ = streamWrite(constants.EventContentBlockStop, map[string]any{
								"type":  constants.EventContentBlockStop,
								"index": annotationBlockIndex,
							})
						}
					}
				}

				if delta != nil {
					if tcArr, ok := delta["tool_calls"].([]any); ok && len(tcArr) > 0 {
						processedInThisChunk := map[int]bool{}
						for _, tcAny := range tcArr {
							tc, _ := tcAny.(map[string]any)
							if tc == nil {
								continue
							}
							toolCallIndex := intFromAny(tc["index"])
							if processedInThisChunk[toolCallIndex] {
								continue
							}
							processedInThisChunk[toolCallIndex] = true

							fn, _ := tc[constants.ToolFunction].(map[string]any)
							toolCallID, _ := tc["id"].(string)
							toolCallName := ""
							if fn != nil {
								toolCallName = firstNonEmpty(fn["name"])
							}
							ensureToolCallBlock(toolCallIndex, toolCallID, toolCallName)

							if fn == nil {
								continue
							}
							argAny, hasArg := fn["arguments"]
							if !hasArg || argAny == nil {
								continue
							}
							appendToolCallArguments(toolCallIndex, fmt.Sprint(argAny))
						}
					}
				}

				if finishReason, _ := choice["finish_reason"].(string); finishReason != "" {
					closeCurrentContentBlock()

					inputTokens := 0
					outputTokens := 0
					if usage, ok := chunk["usage"].(map[string]any); ok && usage != nil {
						inputTokens = intFromAny(usage["prompt_tokens"])
						if inputTokens == 0 {
							inputTokens = intFromAny(usage["input_tokens"])
						}
						outputTokens = intFromAny(usage["completion_tokens"])
						if outputTokens == 0 {
							outputTokens = intFromAny(usage["output_tokens"]) // Responses API 兼容
						}
					}

					stopReasonMessageDelta = map[string]any{
						"type": constants.EventMessageDelta,
						"delta": map[string]any{
							"stop_reason":   mapFinishReason(finishReason),
							"stop_sequence": nil,
						},
						"usage": map[string]any{
							"input_tokens":  inputTokens,
							"output_tokens": outputTokens,
						},
					}
					hasFinished = true
					goto END
				}
			}
		}
	}

END:
	safeClose()
	return nil
}

func normalizeSSEToChatChunks(
	raw map[string]any,
	toolCallIndexMap *map[string]int,
	nextToolCallIndex *int,
	roleEmitted *bool,
) []map[string]any {
	if raw == nil {
		return nil
	}
	if fmt.Sprint(raw["object"]) == "chat.completion.chunk" {
		return []map[string]any{raw}
	}

	eventType := fmt.Sprint(raw["type"])
	if !strings.HasPrefix(eventType, "response.") {
		return []map[string]any{raw}
	}

	responseObj, _ := raw["response"].(map[string]any)
	modelName := fmt.Sprint(responseObj["model"])
	if modelName == "" {
		modelName = "unknown"
	}
	newChunk := func(delta map[string]any, finishReason any) map[string]any {
		return map[string]any{
			"id":     firstNonEmpty(raw["item_id"], raw["id"], fmt.Sprintf("chatcmpl-%d", time.Now().UnixMilli())),
			"object": "chat.completion.chunk",
			"model":  modelName,
			"choices": []any{
				map[string]any{
					"index":         0,
					"delta":         delta,
					"finish_reason": finishReason,
				},
			},
		}
	}

	switch eventType {
	case "response.created", "response.in_progress":
		return nil
	case "response.output_text.delta":
		return []map[string]any{newChunk(map[string]any{"content": fmt.Sprint(raw["delta"])}, nil)}
	case "response.output_item.added":
		item, _ := raw["item"].(map[string]any)
		if item == nil {
			return nil
		}
		switch fmt.Sprint(item["type"]) {
		case "function_call":
			callID := fmt.Sprint(item["call_id"])
			itemID := fmt.Sprint(item["id"])
			mapKey := firstNonEmpty(callID, itemID)
			toolIdx := 0
			if mapKey != "" {
				if idx, ok := (*toolCallIndexMap)[mapKey]; ok {
					toolIdx = idx
				} else {
					toolIdx = *nextToolCallIndex
					*nextToolCallIndex = *nextToolCallIndex + 1
					(*toolCallIndexMap)[mapKey] = toolIdx
					if callID != "" {
						(*toolCallIndexMap)[callID] = toolIdx
					}
					if itemID != "" {
						(*toolCallIndexMap)[itemID] = toolIdx
					}
				}
			}
			delta := map[string]any{
				"tool_calls": []any{
					map[string]any{
						"index": toolIdx,
						"id":    firstNonEmpty(callID, itemID),
						"function": map[string]any{
							"name":      fmt.Sprint(item["name"]),
							"arguments": "",
						},
						"type": constants.ToolFunction,
					},
				},
			}
			if !*roleEmitted {
				delta["role"] = constants.ROLEAssistant
				*roleEmitted = true
			}
			return []map[string]any{newChunk(delta, nil)}
		case "message":
			content, _ := item["content"].([]any)
			textParts, _ := extractResponsesMessageTextAndAnnotations(content)
			if len(textParts) == 0 {
				return nil
			}
			return []map[string]any{newChunk(map[string]any{
				"role":    constants.ROLEAssistant,
				"content": strings.Join(textParts, ""),
			}, nil)}
		case "reasoning":
			thinking := map[string]any{}
			if itemID := firstNonEmpty(item["id"], raw["item_id"]); itemID != "" {
				thinking["item_id"] = itemID
			}
			if encrypted := strings.TrimSpace(fmt.Sprint(item["encrypted_content"])); encrypted != "" && encrypted != "<nil>" {
				thinking["signature"] = encrypted
			}
			return []map[string]any{newChunk(map[string]any{
				"thinking": thinking,
			}, nil)}
		}
	case "response.output_text.annotation.added":
		ann, _ := raw["annotation"].(map[string]any)
		if ann == nil {
			return nil
		}
		return []map[string]any{newChunk(map[string]any{
			"annotations": []any{
				map[string]any{
					"type": "url_citation",
					"url_citation": map[string]any{
						"url":         fmt.Sprint(ann["url"]),
						"title":       fmt.Sprint(ann["title"]),
						"content":     "",
						"start_index": intFromAny(ann["start_index"]),
						"end_index":   intFromAny(ann["end_index"]),
					},
				},
			},
		}, nil)}
	case "response.function_call_arguments.delta":
		itemID := fmt.Sprint(raw["item_id"])
		toolIdx := 0
		if idx, ok := (*toolCallIndexMap)[itemID]; ok {
			toolIdx = idx
		}
		return []map[string]any{newChunk(map[string]any{
			"tool_calls": []any{
				map[string]any{
					"index": toolIdx,
					"function": map[string]any{
						"arguments": fmt.Sprint(raw["delta"]),
					},
				},
			},
		}, nil)}
	case "response.output_item.done":
		item, _ := raw["item"].(map[string]any)
		if item == nil || fmt.Sprint(item["type"]) != "reasoning" {
			return nil
		}
		thinking := map[string]any{}
		if itemID := firstNonEmpty(raw["item_id"], item["id"]); itemID != "" {
			thinking["item_id"] = itemID
		}
		encrypted := strings.TrimSpace(fmt.Sprint(item["encrypted_content"]))
		if encrypted != "" && encrypted != "<nil>" {
			thinking["signature"] = encrypted
		}
		if len(thinking) == 0 || (len(thinking) == 1 && thinking["item_id"] != nil) {
			return nil
		}
		return []map[string]any{newChunk(map[string]any{
			"thinking": thinking,
		}, nil)}
	case "response.reasoning_summary_text.delta":
		return []map[string]any{newChunk(map[string]any{
			"thinking": map[string]any{
				"item_id": firstNonEmpty(raw["item_id"]),
				"content": fmt.Sprint(raw["delta"]),
			},
		}, nil)}
	case "response.completed":
		finishReason := "stop"
		if output, ok := responseObj["output"].([]any); ok {
			for _, it := range output {
				item, _ := it.(map[string]any)
				if fmt.Sprint(item["type"]) == "function_call" {
					finishReason = "tool_calls"
					break
				}
			}
		}
		chunk := newChunk(map[string]any{}, finishReason)
		chunk["usage"] = normalizedStreamingUsage(responseObj["usage"])
		return []map[string]any{chunk}
	}
	return nil
}

func firstNonEmpty(values ...any) string {
	for _, v := range values {
		s := strings.TrimSpace(fmt.Sprint(v))
		if s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}

func mapFinishReason(finishReason string) string {
	switch finishReason {
	case "stop":
		return constants.StopEndTurn
	case "length":
		return constants.StopMaxTokens
	case "tool_calls", "function_call":
		return constants.StopToolUse
	case "content_filter":
		return "stop_sequence"
	default:
		return constants.StopEndTurn
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case string:
		var i int
		_, _ = fmt.Sscanf(x, "%d", &i)
		return i
	default:
		return 0
	}
}

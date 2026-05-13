package conversion

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github/ruoyiran/claude-code-transformer/src/claude/constants"
	"github/ruoyiran/claude-code-transformer/src/claude/model"
	"math"
	"strings"
)

func ConvertClaudeToOpenAIResponses(req *model.ClaudeMessagesRequest, sessionID string) (map[string]any, error) {
	openaiMessages, err := buildOpenAIMessages(req)
	if err != nil {
		return nil, err
	}

	systemText := ""

	openaiReq := map[string]any{
		"model":  req.Model,
		"stream": req.Stream,
	}

	// instructions
	if len(req.System) > 0 {
		systemText = strings.TrimSpace(extractSystemText(req.System))
		if systemText != "" {
			openaiReq["instructions"] = systemText
		}
	}
	disableToolsForSummary := shouldDisableToolsForSummary(systemText)

	// When thinking is enabled, Claude requires temperature to be unset or 1.
	// Some OpenAI-compatible backends may not support temperature with reasoning.
	if req.Thinking.IsEnabled() {
		openaiReq["temperature"] = 1.0
		effort := ""
		summary := ""
		if strings.TrimSpace(req.OutputConfig.Effort) != "" {
			effort = req.OutputConfig.Effort
			effort = convertToOpenAIEffort(effort)
		}
		if strings.TrimSpace(req.OutputConfig.Summary) != "" {
			summary = req.OutputConfig.Summary
		}
		if effort != "" || summary != "" {
			openaiReq["reasoning"] = map[string]any{
				"effort":  effort,
				"summary": summary,
			}
		}

		openaiReq["include"] = []string{"reasoning.encrypted_content"}
	} else {
		temp := 1.0
		if req.Temperature != nil {
			temp = *req.Temperature
		}
		openaiReq["temperature"] = temp
	}

	if len(req.StopSequences) > 0 {
		openaiReq["stop"] = req.StopSequences
	}
	if req.TopP != nil {
		openaiReq["top_p"] = *req.TopP
	}

	// tools
	if len(req.Tools) > 0 && !disableToolsForSummary {
		openaiTools := make([]map[string]any, 0)
		for _, t := range req.Tools {
			if strings.TrimSpace(t.Name) == "" {
				continue
			}
			desc := ""
			if t.Description != nil {
				desc = *t.Description
			}
			openaiTools = append(openaiTools, map[string]any{
				"type": constants.ToolFunction,
				constants.ToolFunction: map[string]any{
					"name":        t.Name,
					"description": desc,
					"parameters":  t.InputSchema,
				},
			})
		}
		if len(openaiTools) > 0 {
			openaiReq["tools"] = openaiTools
		}
	}

	// tool_choice
	if disableToolsForSummary {
		delete(openaiReq, "tool_choice")
	} else if req.ToolChoice != nil {
		ct, _ := req.ToolChoice["type"].(string)
		switch ct {
		case "auto", "any":
			openaiReq["tool_choice"] = "auto"
		case "tool":
			if name, ok := req.ToolChoice["name"].(string); ok && name != "" {
				openaiReq["tool_choice"] = map[string]any{
					"type": constants.ToolFunction,
					constants.ToolFunction: map[string]any{
						"name": name,
					},
				}
			} else {
				openaiReq["tool_choice"] = "auto"
			}
		default:
			openaiReq["tool_choice"] = "auto"
		}
	} else {
		openaiReq["tool_choice"] = "auto"
	}

	if req.MaxTokens > 0 {
		openaiReq["max_output_tokens"] = req.MaxTokens
	}

	if req.OutputConfig.TextVerbosity != "" {
		openaiReq["text"] = map[string]any{
			"verbosity": req.OutputConfig.TextVerbosity,
		}
	}
	openaiReq["parallel_tool_calls"] = true
	openaiReq["prompt_cache_key"] = sessionID
	openaiReq["store"] = false

	includeEncryptedContent := req.IncludeEncryptedContent
	input := buildResponsesInputAndInstructions(openaiMessages, includeEncryptedContent)

	openaiReq["input"] = input

	if tools, ok := openaiReq["tools"].([]map[string]any); ok && len(tools) > 0 {
		outTools := make([]map[string]any, 0, len(tools))
		hasWebSearch := false
		for _, t := range tools {
			fn, _ := t[constants.ToolFunction].(map[string]any)
			name := fmt.Sprint(fn["name"])
			if name == "web_search" {
				hasWebSearch = true
				continue
			}
			outTools = append(outTools, map[string]any{
				"type":        t["type"],
				"name":        name,
				"description": fmt.Sprint(fn["description"]),
				"parameters":  fn["parameters"],
			})
		}
		if hasWebSearch {
			outTools = append(outTools, map[string]any{"type": "web_search"})
		}
		openaiReq["tools"] = outTools
		openaiReq["tool_choice"] = "auto"
	}

	return openaiReq, nil
}

func shouldDisableToolsForSummary(systemText string) bool {
	lower := strings.ToLower(strings.TrimSpace(systemText))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "tasked with summarizing conversations") ||
		strings.Contains(lower, "tasked with summarizing conversation")
}

func convertToOpenAIEffort(effort string) string {
	if effort == "low" || effort == "medium" ||
		effort == "high" || effort == "xhigh" {
		return effort
	}
	if effort == "max" {
		return "xhigh"
	}
	if effort == "auto" {
		return "medium"
	}
	return "none"
}

func buildOpenAIMessages(req *model.ClaudeMessagesRequest) ([]map[string]any, error) {
	openaiMessages := make([]map[string]any, 0)

	// process messages with tool_result folding (assistant -> next user tool_result)
	for i := 0; i < len(req.Messages); i++ {
		msg := req.Messages[i]
		switch msg.Role {
		case constants.ROLEUser:
			m, err := convertClaudeUserMessage(msg)
			if err != nil {
				return nil, err
			}
			openaiMessages = append(openaiMessages, m)
		case constants.ROLEAssistant:
			m, err := convertClaudeAssistantMessage(msg)
			if err != nil {
				return nil, err
			}
			openaiMessages = append(openaiMessages, m)

			// lookahead tool_result
			if i+1 < len(req.Messages) {
				next := req.Messages[i+1]
				if next.Role == constants.ROLEUser && messageHasToolResult(next.Content) {
					i++
					tools, err := convertClaudeToolResults(next)
					if err != nil {
						return nil, err
					}
					openaiMessages = append(openaiMessages, tools...)
				}
			}
		}
	}

	return openaiMessages, nil
}

func buildResponsesInputAndInstructions(messages []map[string]any, includeEncryptedContent bool) []map[string]any {
	input := make([]map[string]any, 0)

	for _, msg := range messages {
		role := fmt.Sprint(msg["role"])
		content := msg["content"]
		if role == constants.ROLETool {
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": fmt.Sprint(msg["tool_call_id"]),
				"output":  stringifyAny(content),
			})
			continue
		}

		if role == constants.ROLEAssistant {
			if thinking, ok := msg["thinking"].(map[string]any); ok && thinking != nil {
				if signature, _ := thinking["signature"].(string); signature != "" {
					if includeEncryptedContent {
						input = append(input, map[string]any{
							"type":              "reasoning",
							"encrypted_content": signature,
							"summary":           []any{},
						})
					}
				}
			}
		}

		normalizedContent := normalizeChatMessageContentForResponses(content, role)
		if len(normalizedContent) == 0 && role != constants.ROLEAssistant {
			continue
		}

		if role == constants.ROLEAssistant {
			toolCalls := toAnySlice(msg["tool_calls"])
			if len(toolCalls) > 0 {
				if len(normalizedContent) > 0 {
					input = append(input, buildResponsesMessage(role, normalizedContent, fmt.Sprint(msg["phase"])))
				}
				for _, tcAny := range toolCalls {
					tc, _ := tcAny.(map[string]any)
					if tc == nil {
						continue
					}
					fn, _ := tc[constants.ToolFunction].(map[string]any)
					input = append(input, map[string]any{
						"type":      "function_call",
						"arguments": fmt.Sprint(fn["arguments"]),
						"name":      fmt.Sprint(fn["name"]),
						"call_id":   fmt.Sprint(tc["id"]),
					})
				}
				continue
			}
		}

		input = append(input, buildResponsesMessage(role, normalizedContent, fmt.Sprint(msg["phase"])))
	}

	return input
}

func addPhaseIfPresent(dst map[string]any, phase string) {
	phase = strings.TrimSpace(phase)
	if phase == "" || phase == "<nil>" {
		return
	}
	dst["phase"] = phase
}

func buildResponsesMessage(role string, content []map[string]any, phase string) map[string]any {
	if content == nil {
		content = []map[string]any{}
	}
	msg := map[string]any{
		"type":    "message",
		"role":    role,
		"content": content,
	}
	if role == constants.ROLEAssistant {
		addPhaseIfPresent(msg, phase)
	}
	return msg
}

func extractSystemText(raw json.RawMessage) string {
	// can be string or []{type,text}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b["type"] == constants.ContentText {
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func convertClaudeUserMessage(msg model.ClaudeMessage) (map[string]any, error) {
	if len(msg.Content) == 0 {
		return map[string]any{"role": constants.ROLEUser, "content": ""}, nil
	}

	// string?
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return map[string]any{"role": constants.ROLEUser, "content": s}, nil
	}

	// blocks
	var blocks []map[string]any
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, err
	}

	openaiContent := make([]map[string]any, 0)
	for _, b := range blocks {
		t, _ := b["type"].(string)
		switch t {
		case constants.ContentText:
			openaiContent = append(openaiContent, map[string]any{
				"type": "text",
				"text": fmt.Sprint(b["text"]),
			})
		case constants.ContentImage:
			src, _ := b["source"].(map[string]any)
			// expect {type:base64, media_type, data}
			if src != nil && src["type"] == "base64" && src["media_type"] != nil && src["data"] != nil {
				mediaType := fmt.Sprint(src["media_type"])
				data := fmt.Sprint(src["data"])
				// validate base64 roughly (optional)
				_, _ = base64.StdEncoding.DecodeString(data)
				openaiContent = append(openaiContent, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
					},
				})
			}
		}
	}

	if len(openaiContent) == 0 {
		return map[string]any{"role": constants.ROLEUser, "content": ""}, nil
	}
	if len(openaiContent) == 1 && openaiContent[0]["type"] == "text" {
		return map[string]any{"role": constants.ROLEUser, "content": openaiContent[0]["text"]}, nil
	}
	return map[string]any{"role": constants.ROLEUser, "content": openaiContent}, nil
}

func convertClaudeAssistantMessage(msg model.ClaudeMessage) (map[string]any, error) {
	if len(msg.Content) == 0 {
		out := map[string]any{"role": constants.ROLEAssistant, "content": ""}
		addPhaseIfPresent(out, msg.Phase)
		return out, nil
	}

	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		out := map[string]any{"role": constants.ROLEAssistant, "content": s}
		addPhaseIfPresent(out, msg.Phase)
		return out, nil
	}

	var blocks []map[string]any
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, err
	}

	assistantContent := make([]map[string]any, 0)
	toolCalls := make([]map[string]any, 0)
	thinking := map[string]any{}

	for _, b := range blocks {
		t, _ := b["type"].(string)
		switch t {
		case constants.ContentThinking:
			// Keep encrypted reasoning signature for Responses API conversion.
			if signature := strings.TrimSpace(fmt.Sprint(b["signature"])); signature != "" && signature != "<nil>" {
				thinking["signature"] = signature
			}
			if content := strings.TrimSpace(fmt.Sprint(b["thinking"])); content != "" && content != "<nil>" {
				thinking["content"] = content
			}
		case constants.ContentText:
			assistantContent = append(assistantContent, map[string]any{
				"type": "text",
				"text": fmt.Sprint(b["text"]),
			})
		case constants.ContentImage:
			src, _ := b["source"].(map[string]any)
			if src != nil {
				if src["type"] == "base64" && src["media_type"] != nil && src["data"] != nil {
					mediaType := fmt.Sprint(src["media_type"])
					data := fmt.Sprint(src["data"])
					assistantContent = append(assistantContent, map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
						},
					})
				} else if src["type"] == "url" && src["url"] != nil {
					assistantContent = append(assistantContent, map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": fmt.Sprint(src["url"]),
						},
					})
				}
			}
		case constants.ContentToolUse:
			id := fmt.Sprint(b["id"])
			name := fmt.Sprint(b["name"])
			args, _ := json.Marshal(b["input"])

			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": constants.ToolFunction,
				constants.ToolFunction: map[string]any{
					"name":      name,
					"arguments": string(args),
				},
			})
		}
	}

	out := map[string]any{"role": constants.ROLEAssistant}
	addPhaseIfPresent(out, msg.Phase)
	if len(assistantContent) == 1 && assistantContent[0]["type"] == "text" {
		out["content"] = assistantContent[0]["text"]
	} else if len(assistantContent) > 0 {
		out["content"] = assistantContent
	} else {
		out["content"] = ""
	}
	if len(toolCalls) > 0 {
		out["tool_calls"] = toolCalls
	}
	if len(thinking) > 0 {
		out["thinking"] = thinking
	}
	return out, nil
}

func messageHasToolResult(raw json.RawMessage) bool {
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return false
	}
	for _, b := range blocks {
		if b["type"] == constants.ContentToolResult {
			return true
		}
	}
	return false
}

func convertClaudeToolResults(msg model.ClaudeMessage) ([]map[string]any, error) {
	var blocks []map[string]any
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0)
	for _, b := range blocks {
		if b["type"] != constants.ContentToolResult {
			continue
		}
		toolUseID := fmt.Sprint(b["tool_use_id"])
		content := parseToolResultContent(b["content"])
		out = append(out, map[string]any{
			"role":         constants.ROLETool,
			"tool_call_id": toolUseID,
			"content":      content,
		})
	}
	return out, nil
}

func parseToolResultContent(v any) string {
	if v == nil {
		return "No content provided"
	}
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, 0)
		for _, it := range x {
			switch y := it.(type) {
			case string:
				parts = append(parts, y)
			case map[string]any:
				if y["type"] == constants.ContentText {
					parts = append(parts, fmt.Sprint(y["text"]))
				} else if _, ok := y["text"]; ok {
					parts = append(parts, fmt.Sprint(y["text"]))
				} else {
					b, _ := json.Marshal(y)
					parts = append(parts, string(b))
				}
			default:
				parts = append(parts, fmt.Sprint(it))
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]any:
		if x["type"] == constants.ContentText {
			return fmt.Sprint(x["text"])
		}
		b, err := json.Marshal(x)
		if err == nil {
			return string(b)
		}
		return fmt.Sprint(v)
	default:
		return fmt.Sprint(v)
	}
}

func clampInt(v, lo, hi int) int {
	return int(math.Min(float64(hi), math.Max(float64(lo), float64(v))))
}

func stringifyAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		if len(b) == 0 {
			return ""
		}
		return string(b)
	}
}

func extractTextFromChatMessageContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []map[string]any:
		parts := make([]string, 0, len(c))
		for _, item := range c {
			if fmt.Sprint(item["type"]) == "text" {
				parts = append(parts, fmt.Sprint(item["text"]))
			}
		}
		return strings.Join(parts, "")
	case []any:
		parts := make([]string, 0, len(c))
		for _, it := range c {
			item, _ := it.(map[string]any)
			if fmt.Sprint(item["type"]) == "text" {
				parts = append(parts, fmt.Sprint(item["text"]))
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

func normalizeChatMessageContentForResponses(content any, role string) []map[string]any {
	textType := "input_text"
	imageType := "input_image"
	if role == constants.ROLEAssistant {
		textType = "output_text"
		imageType = "output_image"
	}

	if s, ok := content.(string); ok {
		if s == "" {
			return nil
		}
		return []map[string]any{{
			"type": textType,
			"text": s,
		}}
	}

	items := make([]map[string]any, 0)
	switch c := content.(type) {
	case []map[string]any:
		for _, item := range c {
			t := fmt.Sprint(item["type"])
			if t == "text" {
				items = append(items, map[string]any{
					"type": textType,
					"text": fmt.Sprint(item["text"]),
				})
				continue
			}
			if t == "image_url" {
				img, _ := item["image_url"].(map[string]any)
				items = append(items, map[string]any{
					"type":      imageType,
					"image_url": fmt.Sprint(img["url"]),
				})
			}
		}
	case []any:
		for _, it := range c {
			item, _ := it.(map[string]any)
			t := fmt.Sprint(item["type"])
			if t == "text" {
				items = append(items, map[string]any{
					"type": textType,
					"text": fmt.Sprint(item["text"]),
				})
				continue
			}
			if t == "image_url" {
				img, _ := item["image_url"].(map[string]any)
				items = append(items, map[string]any{
					"type":      imageType,
					"image_url": fmt.Sprint(img["url"]),
				})
			}
		}
	}
	return items
}

func toAnySlice(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, 0, len(x))
		for _, it := range x {
			out = append(out, it)
		}
		return out
	default:
		return nil
	}
}

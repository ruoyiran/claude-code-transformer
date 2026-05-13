package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github/ruoyiran/claude-code-transformer/src/claude/conversion"
	"github/ruoyiran/claude-code-transformer/src/claude/model"
	"github/ruoyiran/claude-code-transformer/src/config"
	"github/ruoyiran/claude-code-transformer/src/middleware"
	"github/ruoyiran/claude-code-transformer/src/openai"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const customJSONParamsHeaderName = "X-Custom-Json-Params"

func MessagesHandler(c *gin.Context) {
	start := time.Now()
	var req model.ClaudeMessagesRequest
	logrus.WithFields(logrus.Fields{
		"method":    c.Request.Method,
		"path":      c.Request.URL.Path,
		"raw_query": c.Request.URL.RawQuery,
		"client_ip": c.ClientIP(),
	}).Info("anthropic messages received")

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logrus.Errorf("Error reading request body: %s", err)
		writeAnthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "failed to read request body", "")
		return
	}

	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		logrus.WithFields(logrus.Fields{
			"client_ip": c.ClientIP(),
			"path":      c.Request.URL.Path,
			"body_len":  len(bodyBytes),
		}).Errorf("error parsing request: %v, body_preview=%q", err, truncateForLogBytes(bodyBytes, 512))
		writeAnthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error(), "")
		return
	}

	sessionID := req.MetaData.GetSessionID()
	if sessionID == "" {
		sessionID = c.Request.Header.Get("X-Claude-Code-Session-Id")
	}
	apiKey := middleware.GetAuthKey(c)
	if apiKey == "" {
		logrus.Errorf("SessionID: %s, Error getting API key", sessionID)
		writeAnthropicErrorResponse(c, http.StatusUnauthorized, "authentication_error", "Unauthorized", "")
		return
	}

	customParamsHeader := c.Request.Header.Get(customJSONParamsHeaderName)
	// 在发起上游请求前删除该头，避免被透传。
	c.Request.Header.Del(customJSONParamsHeaderName)
	customParamsHeader = strings.TrimSpace(customParamsHeader)
	customParams := customJSONParams{}
	if customParamsHeader != "" {
		parsed, perr := parseCustomJSONParams(customParamsHeader)
		if perr != nil {
			logrus.WithFields(logrus.Fields{
				"session_id": sessionID,
				"client_ip":  c.ClientIP(),
			}).Warnf("invalid %s: %v", customJSONParamsHeaderName, perr)
			writeAnthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "invalid X-Custom-Json-Params", "")
			return
		}
		customParams = parsed
	}

	if customParams.effortOK {
		req.OutputConfig.Effort = strings.ToLower(customParams.effort)
	}

	if customParams.summary != "" {
		req.OutputConfig.Summary = strings.ToLower(customParams.summary)
	}

	if customParams.forceStreamingOK {
		if customParams.forceStreaming {
			logrus.Infof("Force using streaming mode, actually streaming mode: %v", req.Stream)
			req.Stream = true
		}
	}
	finalVerbosity := ""
	if customParams.textVerbosityOK {
		finalVerbosity = customParams.textVerbosity
	}
	req.OutputConfig.TextVerbosity = strings.ToLower(finalVerbosity)

	if customParams.includeEncryptedContentOK {
		req.IncludeEncryptedContent = customParams.includeEncryptedContent
	}
	msgCount, msgBytes := summarizeClaudeMessages(req.Messages)
	logrus.WithFields(logrus.Fields{
		"session_id":                sessionID,
		"model":                     req.Model,
		"stream":                    req.Stream,
		"max_tokens":                req.MaxTokens,
		"effort":                    req.OutputConfig.Effort,
		"text_verbosity":            req.OutputConfig.TextVerbosity,
		"include_encrypted_content": req.IncludeEncryptedContent,
		"messages_count":            msgCount,
		"messages_bytes":            msgBytes,
		"tools_count":               len(req.Tools),
		"body_len":                  len(bodyBytes),
	}).Debug("anthropic messages request parsed")

	convertStart := time.Now()
	openaiReq, err := conversion.ConvertClaudeToOpenAIResponses(&req, sessionID)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"session_id":   sessionID,
			"elapsed_ms":   time.Since(convertStart).Milliseconds(),
			"claude_model": req.Model,
			"stream":       req.Stream,
		}).Errorf("failed to convert claude messages -> openai responses: %v", err)
		writeAnthropicErrorResponse(c, http.StatusInternalServerError, "api_error", err.Error(), "")
		return
	}
	logrus.WithFields(logrus.Fields{
		"session_id":            sessionID,
		"elapsed_ms":            time.Since(convertStart).Milliseconds(),
		"stream":                req.Stream,
		"claude_model":          req.Model,
		"upstream_openai_model": fmt.Sprint(openaiReq["model"]),
	}).Info("claude request converted")

	err = createResponses(c, sessionID, apiKey, openaiReq)
	if err == nil || context.Canceled.Error() == err.Error() {
		if err != nil {
			logrus.Errorf("ReqID: %s, request model: %s, %s", sessionID, req.Model, err.Error())
		}
	}
	logrus.WithFields(logrus.Fields{
		"session_id": sessionID,
		"model":      req.Model,
		"stream":     req.Stream,
		"elapsed_ms": time.Since(start).Milliseconds(),
	}).Info("anthropic messages finished")
}

func createResponses(c *gin.Context, sessionID string, apiKey string, openaiReq map[string]any) error {
	stream, _ := openaiReq["stream"].(bool)
	start := time.Now()

	ctx := c.Request.Context()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	upstreamBaseURL := config.GetConfig().OpenAIBaseUrl
	logrus.WithFields(logrus.Fields{
		"session_id":        sessionID,
		"stream":            stream,
		"upstream_base_url": upstreamBaseURL,
		"upstream_model":    fmt.Sprint(openaiReq["model"]),
	}).Debug("dispatching request to upstream responses endpoint")
	openaiClient := openai.NewHTTPClient(upstreamBaseURL, apiKey, 0)
	if stream {
		lines, errCh := openaiClient.CreateResponsesStream(ctx, openaiReq, sessionID)
		primedLines, err := waitForFirstStreamChunk(ctx, lines, errCh)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"session_id":  sessionID,
				"elapsed_ms":  time.Since(start).Milliseconds(),
				"err_is_http": errors.As(err, new(*openai.HTTPError)),
			}).Errorf("error creating streaming response: %v", err)
			if !errors.Is(err, context.Canceled) {
				writeAnthropicError(c, http.StatusBadGateway, err)
			}
			return err
		}

		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "*")
		c.Status(http.StatusOK)

		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}

		err = conversion.ConvertOpenAIStreamingToClaudeWithCancellation(
			sessionID,
			ctx,
			primedLines,
			errCh,
			c.Writer,
			func() bool {
				select {
				case <-c.Request.Context().Done():
					return true
				default:
					return false
				}
			},
			cancel,
		)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"session_id": sessionID,
				"elapsed_ms": time.Since(start).Milliseconds(),
			}).Errorf("error converting openai stream -> claude: %v", err)
			return err
		}
		logrus.WithFields(logrus.Fields{
			"session_id": sessionID,
			"elapsed_ms": time.Since(start).Milliseconds(),
			"model":      openaiReq["model"],
		}).Info("streaming messages response finished")
	} else {
		resp, err := openaiClient.CreateResponses(ctx, openaiReq, sessionID)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"session_id": sessionID,
				"elapsed_ms": time.Since(start).Milliseconds(),
				"model":      fmt.Sprint(openaiReq["model"]),
			}).Errorf("error creating responses: %v", err)
			writeAnthropicError(c, http.StatusBadGateway, err)
			return err
		}
		claudeResp, err := conversion.ConvertOpenAIToClaudeResponse(resp)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"session_id": sessionID,
				"elapsed_ms": time.Since(start).Milliseconds(),
				"model":      fmt.Sprint(openaiReq["model"]),
			}).Errorf("error converting openai response -> claude: %v", err)
			writeAnthropicErrorResponse(c, http.StatusInternalServerError, "api_error", err.Error(), "")
			return err
		}
		logrus.WithFields(logrus.Fields{
			"session_id": sessionID,
			"elapsed_ms": time.Since(start).Milliseconds(),
			"model":      openaiReq["model"],
		}).Info("messages response 200 OK")
		c.JSON(http.StatusOK, claudeResp)
	}
	return nil
}

func waitForFirstStreamChunk(ctx context.Context, lines <-chan string, errCh <-chan error) (<-chan string, error) {
	buffered := make([]string, 0, 8)
	var pendingFailure error

	replayBuffered := func() <-chan string {
		replayed := make(chan string, 64)
		go func() {
			defer close(replayed)
			for _, line := range buffered {
				replayed <- line
			}
			for next := range lines {
				replayed <- next
			}
		}()
		return replayed
	}

	for {
		if lines == nil && errCh == nil {
			if pendingFailure != nil {
				return nil, pendingFailure
			}
			return nil, fmt.Errorf("upstream stream ended before first chunk")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				if pendingFailure != nil {
					return nil, pendingFailure
				}
				return nil, err
			}
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			buffered = append(buffered, line)
			streamErr, terminal, keepWaiting := inspectPrimingStreamLine(line)
			if streamErr != nil {
				pendingFailure = streamErr
				if terminal {
					return nil, streamErr
				}
			}
			if keepWaiting {
				continue
			}
			return replayBuffered(), nil
		}
	}
}

func inspectPrimingStreamLine(line string) (err error, terminal bool, keepWaiting bool) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "data:") {
		return nil, false, true
	}

	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return nil, false, true
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return fmt.Errorf("failed to decode upstream stream chunk: %w", err), true, false
	}

	switch fmt.Sprint(raw["type"]) {
	case "response.created", "response.in_progress", "keepalive":
		return nil, false, true
	case "response.output_item.added":
		return nil, false, false
	case "error":
		errBody, _ := raw["error"].(map[string]any)
		return newPrimingStreamHTTPError(
			firstNonEmptyString(errBody["message"], "upstream stream failed before first chunk"),
			firstNonEmptyString(errBody["code"], errBody["type"]),
		), true, false
	case "response.failed":
		response, _ := raw["response"].(map[string]any)
		errBody, _ := response["error"].(map[string]any)
		if errBody != nil {
			return newPrimingStreamHTTPError(
				firstNonEmptyString(errBody["message"], "upstream stream failed before first chunk"),
				firstNonEmptyString(errBody["code"], errBody["type"]),
			), true, false
		}
		return newPrimingStreamHTTPError("upstream stream failed before first chunk", "server_error"), false, true
	default:
		return nil, false, false
	}
}

func newPrimingStreamHTTPError(message, code string) error {
	status := http.StatusBadGateway
	normalizedCode := strings.ToLower(strings.TrimSpace(code))
	switch normalizedCode {
	case "invalid_request_error", "invalid_request", "bad_request", "unsupported_value", "context_length_exceeded":
		status = http.StatusBadRequest
	case "authentication_error", "invalid_api_key", "unauthorized":
		status = http.StatusUnauthorized
	case "permission_error", "forbidden":
		status = http.StatusForbidden
	case "not_found_error", "not_found":
		status = http.StatusNotFound
	case "too_many_requests", "rate_limit_error", "rate_limit_exceeded":
		status = http.StatusTooManyRequests
	case "overloaded_error":
		status = 529
	case "no_capacity":
		status = 529
	}

	errorBody := map[string]any{"message": message}
	if code != "" {
		errorBody["code"] = code
	}
	return &openai.HTTPError{
		StatusCode: status,
		Detail:     message,
		RawDetail: map[string]any{
			"error": errorBody,
		},
	}
}

func firstNonEmptyString(values ...any) string {
	for _, value := range values {
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

type customJSONParams struct {
	effortOK bool
	effort   string

	summaryOK bool
	summary   string

	forceStreamingOK bool
	forceStreaming   bool

	textVerbosityOK bool
	textVerbosity   string

	includeEncryptedContentOK bool
	includeEncryptedContent   bool
}

func parseCustomJSONParams(headerVal string) (customJSONParams, error) {
	var out customJSONParams
	headerVal = strings.TrimSpace(headerVal)
	if headerVal == "" {
		return out, nil
	}

	// Header 值为 JSON 对象字符串，允许扩展其它字段；目前支持：
	// - effort / reasoning_effort: string
	// - summary / reasoning_summary: string
	// - force_streaming: bool|0|1|"true"|"false"|"0"|"1"
	// - text_verbosity / verbosity: string
	// - include_encrypted_content: bool|0|1|"true"|"false"|"0"|"1"

	var raw map[string]any
	if err := json.Unmarshal([]byte(headerVal), &raw); err != nil {
		return out, err
	}
	for k, v := range raw {
		switch normalizeCustomParamKey(k) {
		case "effort", "reasoning_effort":
			if s, ok := parseStringValue(v); ok {
				out.effortOK = true
				out.effort = s
			}
		case "summary", "reasoning_summary":
			if s, ok := parseStringValue(v); ok {
				out.summaryOK = true
				out.summary = s
			}
		case "force_streaming":
			if b, ok := parseBoolValue(v); ok {
				out.forceStreamingOK = true
				out.forceStreaming = b
			}
		case "text_verbosity", "verbosity":
			if s, ok := parseStringValue(v); ok {
				out.textVerbosityOK = true
				out.textVerbosity = s
			}
		case "include_encrypted_content":
			if b, ok := parseBoolValue(v); ok {
				out.includeEncryptedContentOK = true
				out.includeEncryptedContent = b
			}
		}
	}

	return out, nil
}

func normalizeCustomParamKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "")
	if strings.HasPrefix(key, "x_") {
		key = strings.TrimPrefix(key, "x_")
	}
	return key
}

func parseStringValue(v any) (string, bool) {
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" || s == "<nil>" {
		return "", false
	}
	return s, true
}

func parseBoolValue(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case float64:
		if t == 0 {
			return false, true
		}
		if t == 1 {
			return true, true
		}
		return false, false
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		switch s {
		case "1", "true", "yes":
			return true, true
		case "0", "false", "no":
			return false, true
		default:
			return false, false
		}
	default:
		s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
		switch s {
		case "1", "true", "yes":
			return true, true
		case "0", "false", "no":
			return false, true
		default:
			return false, false
		}
	}
}

func writeAnthropicError(c *gin.Context, fallbackStatus int, err error) {
	status := fallbackStatus
	errorType := anthropicErrorType(status)
	message := err.Error()
	code := ""
	upstreamType := ""

	var httpErr *openai.HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode > 0 {
			status = httpErr.StatusCode
		}
		errorType = anthropicErrorType(status)
		message = httpErr.Detail
		if upstreamErr, ok := extractAnthropicErrorBody(httpErr.RawDetail); ok {
			if v := strings.TrimSpace(fmt.Sprint(upstreamErr["type"])); v != "" && v != "<nil>" {
				upstreamType = v
			}
			if v := strings.TrimSpace(fmt.Sprint(upstreamErr["message"])); v != "" && v != "<nil>" {
				message = v
			}
			if v := strings.TrimSpace(fmt.Sprint(upstreamErr["code"])); v != "" && v != "<nil>" {
				code = v
			}
		}
	}

	status, errorType = normalizeAnthropicError(status, upstreamType, code)
	method, path, clientIP := "", "", ""
	if c != nil && c.Request != nil {
		method = c.Request.Method
		path = c.Request.URL.Path
		clientIP = c.ClientIP()
	}
	logrus.WithFields(logrus.Fields{
		"status":        status,
		"fallback":      fallbackStatus,
		"error_type":    errorType,
		"upstream_type": upstreamType,
		"code":          code,
		"path":          path,
		"method":        method,
		"client_ip":     clientIP,
	}).Errorf("anthropic error response: %s", message)
	writeAnthropicErrorResponse(c, status, errorType, message, code)
}

func writeAnthropicErrorResponse(c *gin.Context, status int, errorType, message, code string) {
	errorBody := gin.H{
		"type":    errorType,
		"message": message,
	}
	if code != "" {
		errorBody["code"] = code
	}
	c.JSON(status, gin.H{
		"type":  "error",
		"error": errorBody,
	})
}

func anthropicErrorType(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func normalizeAnthropicError(status int, upstreamType, code string) (int, string) {
	if strings.EqualFold(strings.TrimSpace(code), "no_capacity") {
		return 529, "overloaded_error"
	}
	if upstreamType = strings.TrimSpace(upstreamType); upstreamType != "" {
		return status, upstreamType
	}
	return status, anthropicErrorType(status)
}

func extractAnthropicErrorBody(detail any) (map[string]any, bool) {
	body, ok := detail.(map[string]any)
	if !ok || body == nil {
		return nil, false
	}
	errBody, ok := body["error"].(map[string]any)
	if !ok || errBody == nil {
		return nil, false
	}
	return errBody, true
}

func summarizeClaudeMessages(messages []model.ClaudeMessage) (count int, totalBytes int) {
	if messages == nil {
		return 0, 0
	}
	count = len(messages)
	for _, m := range messages {
		totalBytes += len(m.Content)
	}
	return count, totalBytes
}

func truncateForLogBytes(b []byte, max int) string {
	if max <= 0 {
		return ""
	}
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

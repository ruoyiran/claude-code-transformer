package openai

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github/ruoyiran/claude-code-transformer/src/utils"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/sirupsen/logrus"
)

type Client interface {
	CreateChatCompletion(ctx context.Context, req map[string]any, sessionID string) (map[string]any, error)
	CreateChatCompletionStream(ctx context.Context, req map[string]any, sessionID string) (<-chan string, <-chan error)
	CreateResponses(ctx context.Context, req map[string]any, sessionID string) (map[string]any, error)
	CreateResponsesStream(ctx context.Context, req map[string]any, sessionID string) (<-chan string, <-chan error)
	ClassifyOpenAIError(errorDetail any) string
}

type HTTPClient struct {
	baseURL  string
	apiKey   string
	hc       *http.Client
	streamHC *http.Client
	headers  http.Header
}

func truncateForLog(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

func classifyRequestError(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "context_deadline_exceeded"
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return "net_timeout"
		}
		return "net_error"
	}
	return "error"
}

func NewHTTPClient(baseURL string, apiKey string, requestTimeoutSec int) *HTTPClient {
	timeout := time.Duration(requestTimeoutSec) * time.Second
	if requestTimeoutSec <= 0 {
		timeout = 0
	}
	return &HTTPClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		hc: &http.Client{
			Timeout: timeout,
		},
		streamHC: &http.Client{},
	}
}

func (c *HTTPClient) SetHeader(name, value string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	if c.headers == nil {
		c.headers = make(http.Header)
	}
	if value == "" {
		c.headers.Del(name)
		return
	}
	c.headers.Set(name, value)
}

func (c *HTTPClient) CreateChatCompletion(ctx context.Context, req map[string]any, sessionID string) (map[string]any, error) {
	return c.createJSONRequest(ctx, req, sessionID, c.chatCompletionsURL())
}

func (c *HTTPClient) CreateResponses(ctx context.Context, req map[string]any, sessionID string) (map[string]any, error) {
	return c.createJSONRequest(ctx, req, sessionID, c.responsesURL())
}

func (c *HTTPClient) createJSONRequest(ctx context.Context, req map[string]any, sessionID, url string) (map[string]any, error) {
	start := time.Now()
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	c.applyHeaders(httpReq, sessionID)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"session_id": sessionID,
			"method":     httpReq.Method,
			"url":        url,
			"elapsed_ms": time.Since(start).Milliseconds(),
			"err_kind":   classifyRequestError(ctx, err),
		}).Errorf("upstream json request failed: %v", err)
		if sessionID != "" && errors.Is(ctx.Err(), context.Canceled) {
			return nil, &HTTPError{StatusCode: 499, Detail: "Request cancelled by client"}
		}
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := decodeResponseBody(resp)
	if err != nil {
		return nil, &HTTPError{StatusCode: 499, Detail: err.Error()}
	}
	defer respBody.Close()

	var out map[string]any
	if err := json.NewDecoder(respBody).Decode(&out); err != nil {
		return nil, err
	}
	if _, ok := extractErrorEnvelope(out); ok {
		statusCode := resp.StatusCode
		if statusCode < http.StatusBadRequest {
			statusCode = http.StatusBadGateway
		}
		logrus.WithFields(logrus.Fields{
			"session_id": sessionID,
			"method":     httpReq.Method,
			"url":        url,
			"status":     resp.StatusCode,
			"elapsed_ms": time.Since(start).Milliseconds(),
		}).Errorf("upstream returned error envelope: %s", truncateForLog(utils.MarshalJsonToString(out), 1024))
		return nil, c.toHTTPError(statusCode, out)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		logrus.WithFields(logrus.Fields{
			"session_id": sessionID,
			"method":     httpReq.Method,
			"url":        url,
			"status":     resp.StatusCode,
			"elapsed_ms": time.Since(start).Milliseconds(),
		}).Errorf("upstream returned non-2xx: %s", truncateForLog(utils.MarshalJsonToString(out), 1024))
		return nil, c.toHTTPError(resp.StatusCode, out)
	}
	return out, nil
}

func (c *HTTPClient) CreateChatCompletionStream(ctx context.Context, req map[string]any, sessionID string) (<-chan string, <-chan error) {
	// Chat Completions stream includes usage via stream_options.
	req["stream"] = true
	so, _ := req["stream_options"].(map[string]any)
	if so == nil {
		so = map[string]any{}
		req["stream_options"] = so
	}
	so["include_usage"] = true
	return c.createSSERequest(ctx, req, sessionID, c.chatCompletionsURL())
}

func (c *HTTPClient) CreateResponsesStream(ctx context.Context, req map[string]any, sessionID string) (<-chan string, <-chan error) {
	req["stream"] = true
	return c.createSSERequest(ctx, req, sessionID, c.responsesURL())
}

func (c *HTTPClient) createSSERequest(ctx context.Context, req map[string]any, sessionID, url string) (<-chan string, <-chan error) {
	lines := make(chan string, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errCh)
		start := time.Now()

		body, _ := json.Marshal(req)
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		c.applyHeaders(httpReq, sessionID)
		httpReq.Header.Set("Accept", "text/event-stream")

		httpClient := c.streamHC
		if httpClient == nil {
			httpClient = c.hc
		}

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"session_id": sessionID,
				"method":     httpReq.Method,
				"url":        url,
				"elapsed_ms": time.Since(start).Milliseconds(),
				"err_kind":   classifyRequestError(ctx, err),
			}).Errorf("upstream sse request failed: %v", err)
			if sessionID != "" && errors.Is(ctx.Err(), context.Canceled) {
				errCh <- &HTTPError{StatusCode: 499, Detail: "Request cancelled by client"}
				return
			}
			errCh <- err
			return
		}
		defer resp.Body.Close()

		respBody, err := decodeResponseBody(resp)
		if err != nil {
			errCh <- &HTTPError{StatusCode: 499, Detail: err.Error()}
			return
		}
		defer respBody.Close()

		if resp.StatusCode >= http.StatusBadRequest {
			var out any
			_ = json.NewDecoder(respBody).Decode(&out)
			logrus.WithFields(logrus.Fields{
				"session_id": sessionID,
				"method":     httpReq.Method,
				"url":        url,
				"status":     resp.StatusCode,
				"elapsed_ms": time.Since(start).Milliseconds(),
			}).Errorf("upstream sse returned non-2xx: %s", truncateForLog(utils.MarshalJsonToString(out), 1024))
			errCh <- c.toHTTPError(resp.StatusCode, out)
			return
		}

		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		if !strings.Contains(contentType, "text/event-stream") {
			var out map[string]any
			if err := json.NewDecoder(respBody).Decode(&out); err == nil {
				if _, ok := extractErrorEnvelope(out); ok {
					errCh <- c.toHTTPError(http.StatusBadGateway, out)
					return
				}
			}
			errCh <- fmt.Errorf("unexpected streaming response content-type: %s", resp.Header.Get("Content-Type"))
			return
		}

		sc := bufio.NewScanner(respBody)
		sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

		for sc.Scan() {
			line := sc.Text()
			compact := strings.ReplaceAll(line, "\n", "")
			logrus.Debugf("sessionID: %s, raw -> %s", sessionID, truncateForLog(compact, 512))
			lines <- line
		}
		if err := sc.Err(); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return
			}
			logrus.WithFields(logrus.Fields{
				"session_id": sessionID,
				"method":     httpReq.Method,
				"url":        url,
				"elapsed_ms": time.Since(start).Milliseconds(),
				"err_kind":   classifyRequestError(ctx, err),
			}).Errorf("upstream sse scan failed: %v", err)
			errCh <- err
			return
		}
		lines <- "data: [DONE]"
	}()

	return lines, errCh
}

// --- HTTPError 与 classify_openai_error 等价实现 ---

type HTTPError struct {
	StatusCode int
	Detail     string
	RawDetail  any
}

func (e *HTTPError) Error() string { return e.Detail }

func (c *HTTPClient) toHTTPError(status int, detail any) error {
	msg := c.ClassifyOpenAIError(detail)
	return &HTTPError{StatusCode: status, Detail: msg, RawDetail: detail}
}

func (c *HTTPClient) ClassifyOpenAIError(errorDetail any) string {
	detailStr := utils.MarshalIndentJsonToString(&errorDetail, "")
	errorStr := strings.ToLower(detailStr)

	if strings.Contains(errorStr, "unsupported_country_region_territory") ||
		strings.Contains(errorStr, "country, region, or territory not supported") {
		return "OpenAI API is not available in your region. Consider using a VPN or Azure OpenAI service."
	}
	if strings.Contains(errorStr, "invalid_api_key") || strings.Contains(errorStr, "unauthorized") {
		return "Invalid API key. Please check your OPENAI_API_KEY configuration."
	}
	if strings.Contains(errorStr, "rate_limit") || strings.Contains(errorStr, "quota") {
		return "Rate limit exceeded. Please wait and try again, or upgrade your API plan."
	}
	if strings.Contains(errorStr, "model") && (strings.Contains(errorStr, "not found") || strings.Contains(errorStr, "does not exist")) {
		return "Model not found. Please check your BIG_MODEL and SMALL_MODEL configuration."
	}
	if strings.Contains(errorStr, "billing") || strings.Contains(errorStr, "payment") {
		return "Billing issue. Please check your OpenAI account billing status."
	}
	return detailStr
}

func extractErrorEnvelope(detail any) (map[string]any, bool) {
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

func (c *HTTPClient) chatCompletionsURL() string {
	return strings.TrimRight(c.baseURL, "/") + "/chat/completions"
}

func (c *HTTPClient) responsesURL() string {
	return strings.TrimRight(c.baseURL, "/") + "/responses"
}

func (c *HTTPClient) applyHeaders(r *http.Request, sessionID string) {
	r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "go-http-client/1.0.0")
	r.Header.Set("Connection", "keep-alive")
	r.Header.Set("api-key", c.apiKey)
	if sessionID != "" {
		r.Header.Set("Session_id", sessionID)
		r.Header.Set("X-Client-Request-Id", sessionID)
		r.Header.Set("Extra", fmt.Sprintf(`{"session_id":"%s"}`, sessionID))
	}
	for key, values := range c.headers {
		r.Header.Del(key)
		for _, value := range values {
			r.Header.Add(key, value)
		}
	}
}

type multiCloser struct {
	io.Reader
	closers []io.Closer
}

type noErrCloser struct {
	closeFn func()
}

func (n noErrCloser) Close() error {
	n.closeFn()
	return nil
}

func (m *multiCloser) Close() error {
	var firstErr error
	for i := len(m.closers) - 1; i >= 0; i-- {
		if err := m.closers[i].Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func decodeResponseBody(resp *http.Response) (io.ReadCloser, error) {
	encodings := parseResponseEncodings(resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Type"))
	if len(encodings) == 0 {
		return resp.Body, nil
	}

	reader := io.Reader(resp.Body)
	closers := []io.Closer{resp.Body}

	// Decode in reverse order because content-encoding values are applied in-order.
	for i := len(encodings) - 1; i >= 0; i-- {
		enc := encodings[i]
		switch enc {
		case "identity", "":
			continue
		case "gzip":
			gr, err := gzip.NewReader(reader)
			if err != nil {
				return nil, fmt.Errorf("failed to init gzip reader: %w", err)
			}
			reader = gr
			closers = append(closers, gr)
		case "deflate":
			zr, err := zlib.NewReader(reader)
			if err == nil {
				reader = zr
				closers = append(closers, zr)
				continue
			}

			// Some servers send raw deflate payloads without zlib wrapper.
			fr := flate.NewReader(reader)
			reader = fr
			closers = append(closers, fr)
		case "br":
			reader = brotli.NewReader(reader)
		case "zstd":
			zstdReader, err := zstd.NewReader(reader)
			if err != nil {
				return nil, fmt.Errorf("failed to init zstd reader: %w", err)
			}
			reader = zstdReader
			closers = append(closers, noErrCloser{closeFn: zstdReader.Close})
		default:
			return nil, fmt.Errorf("unsupported content encoding: %s", enc)
		}
	}

	return &multiCloser{
		Reader:  reader,
		closers: closers,
	}, nil
}

func parseResponseEncodings(contentEncoding, contentType string) []string {
	if strings.TrimSpace(contentEncoding) != "" {
		parts := strings.Split(strings.ToLower(contentEncoding), ",")
		encodings := make([]string, 0, len(parts))
		for _, p := range parts {
			enc := strings.TrimSpace(p)
			if enc != "" {
				encodings = append(encodings, enc)
			}
		}
		return encodings
	}

	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "application/gzip"), strings.Contains(ct, "application/x-gzip"):
		return []string{"gzip"}
	case strings.Contains(ct, "application/zlib"):
		return []string{"deflate"}
	case strings.Contains(ct, "application/zstd"), strings.Contains(ct, "application/x-zstd"):
		return []string{"zstd"}
	default:
		return nil
	}
}

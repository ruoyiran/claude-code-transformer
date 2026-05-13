package openai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github/ruoyiran/claude-code-transformer/src/openai"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestCreateResponses_UsesResponsesPath(t *testing.T) {
	var gotPath string
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Test-Header")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"gpt-5","output":[]}`))
	}))
	defer srv.Close()

	c := openai.NewHTTPClient(srv.URL, "k", 5)
	c.SetHeader("X-Test-Header", "header-value")
	_, err := c.CreateResponses(context.Background(), map[string]any{"model": "gpt-5"}, "rid")
	require.NoError(t, err)
	require.Equal(t, "/responses", gotPath)
	require.Equal(t, "header-value", gotHeader)
}

func TestCreateResponsesStream_UsesResponsesPathAndForcesStream(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5\",\"output\":[]}}\n\n"))
	}))
	defer srv.Close()

	c := openai.NewHTTPClient(srv.URL, "k", 5)
	lines, errCh := c.CreateResponsesStream(context.Background(), map[string]any{"model": "gpt-5"}, "rid")

	select {
	case <-lines:
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	require.Equal(t, "/responses", gotPath)
	require.Equal(t, true, gotBody["stream"])
}

func TestCreateResponsesStream_DoesNotUseTotalRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		_, _ = w.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"model\":\"gpt-5\"}}\n\n"))
		flusher.Flush()
		time.Sleep(1200 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5\",\"output\":[]}}\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	c := openai.NewHTTPClient(srv.URL, "k", 1)
	lines, errCh := c.CreateResponsesStream(ctx, map[string]any{"model": "gpt-5"}, "rid")

	seenCompleted := false
	for !seenCompleted {
		select {
		case line, ok := <-lines:
			require.True(t, ok)
			if strings.Contains(line, "response.completed") {
				seenCompleted = true
			}
		case err := <-errCh:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for completed stream event")
		}
	}
}

func TestCreateResponsesStream_ContextCancellationDoesNotLogScanFailure(t *testing.T) {
	var logs bytes.Buffer
	logger := logrus.StandardLogger()
	oldOut := logger.Out
	oldLevel := logger.GetLevel()
	logrus.SetOutput(&logs)
	logrus.SetLevel(logrus.ErrorLevel)
	t.Cleanup(func() {
		logrus.SetOutput(oldOut)
		logrus.SetLevel(oldLevel)
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		_, _ = w.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"model\":\"gpt-5\"}}\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := openai.NewHTTPClient(srv.URL, "k", 0)
	lines, errCh := c.CreateResponsesStream(ctx, map[string]any{"model": "gpt-5"}, "rid")

	select {
	case line := <-lines:
		require.Contains(t, line, "response.in_progress")
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first stream line")
	}

	cancel()

	select {
	case err, ok := <-errCh:
		require.False(t, ok, "expected cancellation to close errCh without an error, got %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for canceled stream to stop")
	}
	require.NotContains(t, logs.String(), "upstream sse scan failed")
}

func TestCreateResponses_DoesNotUseRequestTimeoutWhenDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"gpt-5","output":[]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c := openai.NewHTTPClient(srv.URL, "k", 0)
	_, err := c.CreateResponses(ctx, map[string]any{"model": "gpt-5"}, "rid")
	require.NoError(t, err)
}

func TestCreateResponses_StillUsesRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"gpt-5","output":[]}`))
	}))
	defer srv.Close()

	c := openai.NewHTTPClient(srv.URL, "k", 1)
	_, err := c.CreateResponses(context.Background(), map[string]any{"model": "gpt-5"}, "rid")
	require.Error(t, err)
}

func TestCreateResponses_Treats200ErrorEnvelopeAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"bad request"}}`))
	}))
	defer srv.Close()

	c := openai.NewHTTPClient(srv.URL, "k", 5)
	_, err := c.CreateResponses(context.Background(), map[string]any{"model": "gpt-5"}, "rid")
	require.Error(t, err)

	var httpErr *openai.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusBadGateway, httpErr.StatusCode)
	require.Contains(t, httpErr.Detail, "invalid_request_error")
}

func TestCreateResponsesStream_TreatsJSONErrorEnvelopeAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"bad request"}}`))
	}))
	defer srv.Close()

	c := openai.NewHTTPClient(srv.URL, "k", 5)
	lines, errCh := c.CreateResponsesStream(context.Background(), map[string]any{"model": "gpt-5"}, "rid")

	select {
	case err := <-errCh:
		require.Error(t, err)
		var httpErr *openai.HTTPError
		require.ErrorAs(t, err, &httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.StatusCode)
	case line := <-lines:
		t.Fatalf("unexpected stream line: %q", line)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

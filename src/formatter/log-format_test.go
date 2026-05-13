package formatter

import (
	"runtime"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestEasyFormatter_AppendsWithFieldsData(t *testing.T) {
	entry := &logrus.Entry{
		Time:    time.Date(2026, time.May, 13, 10, 11, 12, 0, time.UTC),
		Level:   logrus.InfoLevel,
		Message: "request finished",
		Data: logrus.Fields{
			"session_id": "sess-1",
			"elapsed_ms": int64(42),
			"stream":     true,
		},
	}

	got, err := (&EasyFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		LogFormat:       "[%lvl%] %time% - %msg%\n",
	}).Format(entry)
	require.NoError(t, err)
	require.Equal(t, "[INFO] 2026-05-13 10:11:12 - request finished elapsed_ms=42 session_id=sess-1 stream=true\n", string(got))
}

func TestEasyFormatter_DoesNotDuplicatePlaceholderFields(t *testing.T) {
	entry := &logrus.Entry{
		Time:    time.Date(2026, time.May, 13, 10, 11, 12, 0, time.UTC),
		Level:   logrus.WarnLevel,
		Message: "request failed",
		Data: logrus.Fields{
			"method": "POST",
			"path":   "/aitesting/anthropic/v1/messages",
		},
	}

	got, err := (&EasyFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		LogFormat:       "[%lvl%] %method% %msg%\n",
	}).Format(entry)
	require.NoError(t, err)
	require.Equal(t, "[WARNING] POST request failed path=/aitesting/anthropic/v1/messages\n", string(got))
	require.NotContains(t, string(got), "method=POST")
}

func TestEasyFormatter_FormatsQuotedAndStructuredFields(t *testing.T) {
	entry := &logrus.Entry{
		Time:    time.Date(2026, time.May, 13, 10, 11, 12, 0, time.UTC),
		Level:   logrus.ErrorLevel,
		Message: "request failed",
		Data: logrus.Fields{
			"err":   "upstream timeout exceeded",
			"extra": map[string]any{"code": 504},
		},
	}

	got, err := (&EasyFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		LogFormat:       "[%lvl%] %msg%\n",
	}).Format(entry)
	require.NoError(t, err)
	require.Equal(t, "[ERROR] request failed err=\"upstream timeout exceeded\" extra={\"code\":504}\n", string(got))
}

func TestEasyFormatter_KeepsCallerSource(t *testing.T) {
	logger := logrus.New()
	logger.SetReportCaller(true)

	entry := logrus.NewEntry(logger)
	entry.Time = time.Date(2026, time.May, 13, 10, 11, 12, 0, time.UTC)
	entry.Level = logrus.InfoLevel
	entry.Message = "request finished"
	entry.Caller = &runtime.Frame{
		Function: "main.main",
		File:     "/tmp/app.go",
		Line:     12,
	}

	got, err := (&EasyFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		LogFormat:       "[%lvl%] %src% %msg%\n",
		CallerPrettyfier: func(frame *runtime.Frame) (function string, file string) {
			return "", "app.go:12"
		},
	}).Format(entry)
	require.NoError(t, err)
	require.Equal(t, "[INFO] app.go:12 request finished\n", string(got))
}

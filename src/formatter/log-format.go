package formatter

import (
	"encoding/json"
	"fmt"
	"github.com/sirupsen/logrus"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// Default log format will output [INFO]: 2006-01-02T15:04:05Z07:00 - Log message
	defaultLogFormat       = "[%lvl%]: %time% - %msg%"
	defaultTimestampFormat = time.RFC3339
)

// EasyFormatter implements logrus.Formatter interface.
type EasyFormatter struct {
	// Timestamp format
	TimestampFormat string
	// Available standard keys: time, msg, lvl
	// Also can include custom fields with %field_name% placeholders.
	// Custom fields that are not explicitly referenced are appended
	// automatically as sorted key=value pairs.
	// All of fields need to be wrapped inside %% i.e %time% %msg%
	LogFormat string
	// CallerPrettyfier can be set by the user to modify the content
	// of the function and file keys in the json data when ReportCaller is
	// activated. If any of the returned value is the empty string the
	// corresponding key will be removed from json fields.
	CallerPrettyfier func(*runtime.Frame) (function string, file string)
}

// Format building log message.
func (f *EasyFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	format := f.LogFormat
	if format == "" {
		format = defaultLogFormat
	}
	output := format

	timestampFormat := f.TimestampFormat
	if timestampFormat == "" {
		timestampFormat = defaultTimestampFormat
	}

	output = strings.ReplaceAll(output, "%time%", entry.Time.Format(timestampFormat))

	output = strings.ReplaceAll(output, "%msg%", entry.Message)

	level := strings.ToUpper(entry.Level.String())
	output = strings.ReplaceAll(output, "%lvl%", level)

	src := ""
	if entry.HasCaller() {
		funcVal := entry.Caller.Function
		fileVal := fmt.Sprintf("%s:%d", entry.Caller.File, entry.Caller.Line)
		if f.CallerPrettyfier != nil {
			funcVal, fileVal = f.CallerPrettyfier(entry.Caller)
		}
		if funcVal != "" {
			src += funcVal
		}
		if fileVal != "" {
			if src != "" {
				src += ":"
			}
			src += fileVal
		}
	}
	output = strings.ReplaceAll(output, "%src%", src)

	usedFields := replaceFieldPlaceholders(&output, format, entry.Data)
	if fields := formatExtraFields(entry.Data, usedFields); fields != "" {
		output = appendBeforeTrailingNewline(output, " "+fields)
	}

	return []byte(output), nil
}

func replaceFieldPlaceholders(output *string, format string, data logrus.Fields) map[string]struct{} {
	if len(data) == 0 {
		return nil
	}

	used := make(map[string]struct{}, len(data))
	for _, key := range sortedFieldKeys(data) {
		placeholder := "%" + key + "%"
		if !strings.Contains(format, placeholder) {
			continue
		}
		used[key] = struct{}{}
		*output = strings.ReplaceAll(*output, placeholder, formatFieldPlaceholderValue(data[key]))
	}
	return used
}

func formatExtraFields(data logrus.Fields, skip map[string]struct{}) string {
	if len(data) == 0 {
		return ""
	}

	parts := make([]string, 0, len(data))
	for _, key := range sortedFieldKeys(data) {
		if _, ok := skip[key]; ok {
			continue
		}
		parts = append(parts, key+"="+formatFieldValue(data[key]))
	}
	return strings.Join(parts, " ")
}

func sortedFieldKeys(data logrus.Fields) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func formatFieldPlaceholderValue(val interface{}) string {
	switch v := val.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case error:
		return v.Error()
	case fmt.Stringer:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprint(v)
	}
}

func formatFieldValue(val interface{}) string {
	switch v := val.(type) {
	case nil:
		return "null"
	case string:
		return quoteFieldValueIfNeeded(v)
	case []byte:
		return quoteFieldValueIfNeeded(string(v))
	case error:
		return quoteFieldValueIfNeeded(v.Error())
	case fmt.Stringer:
		return quoteFieldValueIfNeeded(v.String())
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return quoteFieldValueIfNeeded(fmt.Sprint(v))
	}
}

func quoteFieldValueIfNeeded(val string) string {
	if val == "" {
		return `""`
	}
	for _, r := range val {
		if unicode.IsSpace(r) || r == '=' || r == '"' {
			return strconv.Quote(val)
		}
	}
	return val
}

func appendBeforeTrailingNewline(s, suffix string) string {
	idx := len(s)
	for idx > 0 {
		ch := s[idx-1]
		if ch != '\n' && ch != '\r' {
			break
		}
		idx--
	}
	return s[:idx] + suffix + s[idx:]
}

package kit

// This file ports sanitize.py. Every user-controlled string that reaches a
// tool result passes through here: control and format characters are
// stripped, invisible Unicode is removed (with exact codepoint parity to
// sanitize.py), excessive newlines collapse and long content truncates.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
)

const (
	// DefaultMaxContentLength mirrors sanitize_user_content's max_length.
	DefaultMaxContentLength = 4096
	// DefaultMaxNameLength mirrors sanitize_name's max_length.
	DefaultMaxNameLength = 256
	// EmptyMarker is returned for input that sanitizes to nothing.
	EmptyMarker = "[empty]"
	// TruncationMarker is appended to content longer than the limit.
	TruncationMarker = "... [truncated]"
)

// SanitizeOption customizes SanitizeUserContent/SanitizeName.
type SanitizeOption func(*sanitizeConfig)

type sanitizeConfig struct {
	maxLength     int
	preserveEmoji bool
}

// WithMaxLength overrides the truncation length.
func WithMaxLength(maxLength int) SanitizeOption {
	return func(c *sanitizeConfig) { c.maxLength = maxLength }
}

// WithPreserveEmoji keeps emoji joiners (U+200D) and the subdivision-flag
// tag characters (U+E0020..U+E007F), mirroring preserve_emoji=True.
func WithPreserveEmoji() SanitizeOption {
	return func(c *sanitizeConfig) { c.preserveEmoji = true }
}

func resolveSanitizeConfig(defaultMax int, opts []SanitizeOption) sanitizeConfig {
	config := sanitizeConfig{maxLength: defaultMax}
	for _, opt := range opts {
		opt(&config)
	}
	return config
}

// SanitizeUserContent mirrors sanitize_user_content: it drops Cc/Cf
// characters except newline and tab, drops the zero-width/invisible
// codepoints sanitize.py lists, collapses runs of 3+ newlines to 2, trims
// surrounding whitespace, returns "[empty]" when nothing remains and
// truncates to maxLength runes with the "... [truncated]" marker.
func SanitizeUserContent(text string, opts ...SanitizeOption) string {
	config := resolveSanitizeConfig(DefaultMaxContentLength, opts)
	return sanitize(text, config)
}

// SanitizeName mirrors sanitize_name: content sanitization followed by
// single-lining (newlines become spaces, runs of spaces collapse).
func SanitizeName(text string, opts ...SanitizeOption) string {
	config := resolveSanitizeConfig(DefaultMaxNameLength, opts)
	result := sanitize(text, config)
	result = strings.ReplaceAll(result, "\r", " ")
	result = strings.ReplaceAll(result, "\n", " ")
	return collapseSpaces(result)
}

func sanitize(text string, config sanitizeConfig) string {
	if text == "" {
		return EmptyMarker
	}

	var stripped strings.Builder
	stripped.Grow(len(text))
	for _, r := range text {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			if r == '\n' || r == '\t' {
				stripped.WriteRune(r)
				continue
			}
			if config.preserveEmoji && isEmojiJoinerOrTag(r) {
				stripped.WriteRune(r)
			}
			continue
		}
		stripped.WriteRune(r)
	}

	var visible strings.Builder
	visible.Grow(stripped.Len())
	for _, r := range stripped.String() {
		if isInvisible(r) && !(config.preserveEmoji && r == '\u200d') {
			continue
		}
		visible.WriteRune(r)
	}

	result := strings.TrimSpace(collapseNewlines(visible.String()))
	if result == "" {
		return EmptyMarker
	}
	if config.maxLength < 0 {
		config.maxLength = 0
	}
	runes := []rune(result)
	if len(runes) > config.maxLength {
		return string(runes[:config.maxLength]) + TruncationMarker
	}
	return result
}

// isEmojiJoinerOrTag covers the preserve_emoji exemptions of sanitize.py:
// the zero-width joiner and the Unicode tag characters.
func isEmojiJoinerOrTag(r rune) bool {
	return r == '\u200d' || (r >= 0xE0020 && r <= 0xE007F)
}

// isInvisible carries the exact codepoint ranges of sanitize.py's
// _INVISIBLE_CHARS regex, in the same order.
func isInvisible(r rune) bool {
	switch {
	case r == 0x200B || r == 0x200C || r == 0x200D:
		return true
	case r == 0x200E || r == 0x200F:
		return true
	case r == 0x2028 || r == 0x2029:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r == 0x2060:
		return true
	case r >= 0x2061 && r <= 0x2064:
		return true
	case r == 0xFEFF:
		return true
	case r >= 0xFFF9 && r <= 0xFFFB:
		return true
	}
	return false
}

// collapseNewlines mirrors re.sub(r"\n{3,}", "\n\n", s).
func collapseNewlines(s string) string {
	if !strings.Contains(s, "\n\n\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	run := 0
	flush := func() {
		if run >= 3 {
			run = 2
		}
		for i := 0; i < run; i++ {
			b.WriteByte('\n')
		}
		run = 0
	}
	for _, r := range s {
		if r == '\n' {
			run++
			continue
		}
		flush()
		b.WriteRune(r)
	}
	flush()
	return b.String()
}

// collapseSpaces mirrors re.sub(r" {2,}", " ", s).strip().
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if r == ' ' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// SanitizeDict mirrors sanitize_dict: string values are sanitized
// recursively through nested map[string]any / []any structures; every other
// value (including typed maps and slices) is returned unchanged.
func SanitizeDict(data any) any {
	switch t := data.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for key, value := range t {
			out[key] = SanitizeDict(value)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, value := range t {
			out[i] = SanitizeDict(value)
		}
		return out
	case string:
		return SanitizeUserContent(t)
	default:
		return data
	}
}

// FormatToolResult mirrors format_tool_result: a JSON payload of the form
// {"results": records, <metadata>} with HTML escaping off (the equivalent of
// ensure_ascii=False) and time.Time / []byte values converted the way
// Python's json_serializer converts datetime / bytes.
func FormatToolResult(records []any, metadata map[string]any) (string, error) {
	if records == nil {
		records = []any{}
	}
	payload := map[string]any{"results": records}
	for key, value := range metadata {
		payload[key] = value
	}
	safe, err := jsonSafe(payload)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(safe); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// jsonSafe converts a nested value tree into something encoding/json
// serializes with Python-json-compatible semantics: time.Time becomes an
// ISO-8601 string with a numeric offset, []byte becomes a lossy UTF-8
// string, maps and slices are walked, and unsupported kinds let json.Marshal
// fail (the analogue of json_serializer's TypeError).
func jsonSafe(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch t := value.(type) {
	case time.Time:
		return pythonISO(t), nil
	case []byte:
		return strings.ToValidUTF8(string(t), "\uFFFD"), nil
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
		return jsonSafe(rv.Elem().Interface())
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("json: unsupported map key type %s", rv.Type().Key())
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			converted, err := jsonSafe(iter.Value().Interface())
			if err != nil {
				return nil, err
			}
			out[iter.Key().String()] = converted
		}
		return out, nil
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			converted, err := jsonSafe(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	}
	return value, nil
}

// pythonISO renders a timestamp like datetime.isoformat(): fractional
// seconds only when non-zero, and a numeric UTC offset (+00:00 for UTC).
func pythonISO(t time.Time) string {
	base := t.Format("2006-01-02T15:04:05")
	if nano := t.Nanosecond(); nano != 0 {
		base += fmt.Sprintf(".%06d", nano/1000)
	}
	_, offset := t.Zone()
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	return fmt.Sprintf("%s%s%02d:%02d", base, sign, offset/3600, (offset%3600)/60)
}

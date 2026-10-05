package config

import (
	"strconv"
	"strings"
)

// parseBoolEnv mirrors runtime._parse_bool_env: unset -> default; a value is
// true only when it is 1/true/yes/on (case-insensitive, trimmed).
func parseBoolEnv(value string, def bool) bool {
	if value == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// lockGraceSeconds reads TELEGRAM_LOCK_GRACE_SECONDS (default 20, matching
// singleton.DEFAULT_GRACE_SECONDS); a non-numeric value falls back to 20.
func lockGraceSeconds(src EnvSource) float64 {
	const def = 20.0
	raw := strings.TrimSpace(mustGet(src, "TELEGRAM_LOCK_GRACE_SECONDS"))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}

// floodSleepThreshold reads TELEGRAM_FLOOD_SLEEP_THRESHOLD (default 60).
// A negative value is clamped to 0 (fail-fast mode); a non-numeric value
// falls back to 60. Mirrors runtime._get_flood_sleep_threshold.
func floodSleepThreshold(src EnvSource) int {
	const def = 60
	raw := strings.TrimSpace(mustGet(src, "TELEGRAM_FLOOD_SLEEP_THRESHOLD"))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if v < 0 {
		return 0
	}
	return v
}

// toolTimeoutSeconds reads TELEGRAM_TOOL_TIMEOUT_SECONDS. Unset/non-numeric
// -> the 55s default ceiling; a non-positive value means unbounded.
// Mirrors runtime._tool_timeout_seconds + TOOL_TIMEOUT_SECONDS_DEFAULT.
func toolTimeoutSeconds(src EnvSource) (float64, bool) {
	const def = 55.0
	raw := mustGet(src, "TELEGRAM_TOOL_TIMEOUT_SECONDS")
	if strings.TrimSpace(raw) == "" {
		return def, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return def, false
	}
	if v <= 0 {
		return 0, true
	}
	return v, false
}

// rootsRequestTimeout reads TELEGRAM_ROOTS_TIMEOUT_SECONDS (default 10s).
// 0 or a negative value -> nil (wait forever); non-numeric -> 10.
// Mirrors runtime._roots_request_timeout + ROOTS_REQUEST_TIMEOUT_DEFAULT.
func rootsRequestTimeout(src EnvSource) *float64 {
	const def = 10.0
	raw := strings.TrimSpace(mustGet(src, "TELEGRAM_ROOTS_TIMEOUT_SECONDS"))
	if raw == "" {
		v := def
		return &v
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		v := def
		return &v
	}
	if v <= 0 {
		return nil
	}
	return &v
}

// splitComma splits on commas, trimming spaces, dropping empty pieces.
func splitComma(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// envFloat mirrors transcription._env_float: unset/blank/non-numeric or a
// non-positive value all fall back to the default.
func envFloat(src EnvSource, key string, def float64) float64 {
	raw := strings.TrimSpace(mustGet(src, key))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

// envInt mirrors transcription's int(os.getenv(X, default)) pattern: a
// non-numeric value falls back to the default instead of failing loud.
func envInt(src EnvSource, key string, def int) int {
	raw := strings.TrimSpace(mustGet(src, key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// parseAllowedRoots splits TELEGRAM_ALLOWED_ROOTS (and CLI positional roots,
// which the runner merges in) on ';', ',' and ':' EXCEPT a ':' that begins a
// Windows drive-letter prefix. Mirrors the Python source's
// re.split(r"[;,]|(?<!\b[a-zA-Z]):", raw): a colon is a separator unless the
// char before it is an ASCII letter that starts a word run (the drive-letter
// pattern). This is a security boundary: a mis-parse could let a file tool
// act outside the operator's intended roots, so the semantics must match the
// Python source exactly (verified case-by-case in
// TestParseAllowedRootsDriveLetters).
func parseAllowedRoots(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range splitAllowedRootsDelims(raw) {
		part = strings.Trim(part, "\"' \t\r\n")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// isWordChar mirrors Python's \b word-class (letters, digits, underscore).
func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// isDriveLetter reports whether raw[i] is a ':' the Python regex keeps as
// part of the path: a single ASCII letter at i-1 that begins a word run
// (i-2 out of range or non-word) — the Windows drive-letter prefix.
func isDriveLetter(raw string, i int) bool {
	if i <= 0 || raw[i] != ':' {
		return false
	}
	prev := raw[i-1]
	if !(prev >= 'a' && prev <= 'z' || prev >= 'A' && prev <= 'Z') {
		return false
	}
	return i-1 == 0 || !isWordChar(raw[i-2])
}

func splitAllowedRootsDelims(raw string) []string {
	var out []string
	start := 0
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == ';' || ch == ',' || (ch == ':' && !isDriveLetter(raw, i)) {
			out = append(out, raw[start:i])
			start = i + 1
		}
	}
	out = append(out, raw[start:])
	return out
}

package kit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type recordingReporter struct {
	errors   []string
	warnings []string
}

func (r *recordingReporter) Error(message string)   { r.errors = append(r.errors, message) }
func (r *recordingReporter) Warning(message string) { r.warnings = append(r.warnings, message) }

func TestLogAndFormatErrorFloodWaitIsActionable(t *testing.T) {
	rep := &recordingReporter{}
	msg := LogAndFormatErrorTo(rep, "send_message", &FloodWaitError{Seconds: 45}, WithPrefix(CategoryMsg))
	for _, want := range []string{"Rate limit exceeded (FloodWait)", "45 seconds", "Do NOT retry immediately", "code: MSG-ERR-"} {
		if !strings.Contains(msg, want) {
			t.Errorf("FloodWait message %q missing %q", msg, want)
		}
	}
}

func TestLogAndFormatErrorFloodWaitUnknownDuration(t *testing.T) {
	msg := LogAndFormatErrorTo(&recordingReporter{}, "send_message", &FloodWaitError{}, WithPrefix(CategoryMsg))
	if !strings.Contains(msg, "an unknown duration") || strings.Contains(msg, "0 seconds") {
		t.Errorf("unexpected wait phrasing: %q", msg)
	}
}

func TestIsFloodWait(t *testing.T) {
	if !IsFloodWait(&FloodWaitError{Seconds: 120}) {
		t.Error("FloodWaitError not detected")
	}
	wrapped := fmt.Errorf("wrap: %w", &FloodWaitError{Seconds: 5})
	if !IsFloodWait(wrapped) {
		t.Error("wrapped FloodWaitError not detected")
	}
	if IsFloodWait(errors.New("other error")) {
		t.Error("plain error misclassified as FloodWait")
	}
}

func TestLogAndFormatErrorFloodWaitWarnsNotErrors(t *testing.T) {
	rep := &recordingReporter{}
	LogAndFormatErrorTo(rep, "get_history", &FloodWaitError{Seconds: 30})
	if len(rep.errors) != 0 {
		t.Errorf("FloodWait wrote ERROR records: %v", rep.errors)
	}
	want := "Telegram FloodWait; retry only after the reported delay."
	if len(rep.warnings) != 1 || rep.warnings[0] != want {
		t.Errorf("warnings = %v, want [%q]", rep.warnings, want)
	}
}

func TestLogAndFormatErrorFloodWaitCustomUserMessage(t *testing.T) {
	msg := LogAndFormatErrorTo(&recordingReporter{}, "send_message", &FloodWaitError{Seconds: 15}, WithUserMessage("Custom rate limit message"))
	if msg != "Custom rate limit message" {
		t.Errorf("got %q", msg)
	}
}

var genericErrorPattern = regexp.MustCompile(`^An error occurred \(code: ([A-Z]+)-ERR-\d{3}\)\.$`)

func TestLogAndFormatErrorGenericAndPrivacySafe(t *testing.T) {
	rep := &recordingReporter{}
	secret := errors.New("exception-payload-secret /private/path -100123456")
	msg := LogAndFormatErrorTo(rep, "send_message", secret)
	match := genericErrorPattern.FindStringSubmatch(msg)
	if match == nil {
		t.Fatalf("generic message has unexpected shape: %q", msg)
	}
	if match[1] != "GEN" {
		t.Errorf("prefix = %q, want GEN", match[1])
	}
	if strings.Contains(msg, "secret") || strings.Contains(msg, "/private") {
		t.Errorf("error text leaked into result: %q", msg)
	}
	if len(rep.errors) != 1 || rep.errors[0] != funnelErrorMessage {
		t.Errorf("reporter got %v, want the constant funnel message", rep.errors)
	}
}

func TestLogAndFormatErrorPrefixDerivation(t *testing.T) {
	cases := []struct {
		function string
		want     string
	}{
		{"get_chats", "CHAT"},
		{"send_msg", "MSG"},
		{"add_contact", "CONTACT"},
		{"create_group", "GROUP"},
		{"get_media", "MEDIA"},
		{"get_profile", "PROFILE"},
		{"check_auth", "AUTH"},
		{"edit_admin", "ADMIN"},
		{"list_folders", "FOLDER"},
		{"set_privacy_settings", "PRIVACY"},
		{"get_messages", "GEN"},
		{"send_message", "GEN"},
	}
	for _, tc := range cases {
		msg := LogAndFormatErrorTo(&recordingReporter{}, tc.function, errors.New("boom"))
		if !strings.Contains(msg, tc.want+"-ERR-") {
			t.Errorf("%s -> %q, want code prefix %s-ERR-", tc.function, msg, tc.want)
		}
	}
}

func TestLogAndFormatErrorCodeStable(t *testing.T) {
	first := LogAndFormatErrorTo(&recordingReporter{}, "get_chats", errors.New("one"))
	second := LogAndFormatErrorTo(&recordingReporter{}, "get_chats", errors.New("two"))
	if first != second {
		t.Errorf("codes unstable: %q vs %q", first, second)
	}
	other := LogAndFormatErrorTo(&recordingReporter{}, "get_chat_info", errors.New("one"))
	if other == first {
		t.Errorf("distinct functions share a code: %q", other)
	}
}

func TestLogAndFormatErrorReturnsCarriedMessages(t *testing.T) {
	if got := LogAndFormatErrorTo(&recordingReporter{}, "validate_user", &ValidationError{Message: "bad input"}); got != "bad input" {
		t.Errorf("ValidationError message = %q", got)
	}
	denied := &ChatAccessDeniedError{Message: "Access to chat '1' is restricted by privacy policy (TELEGRAM_ALLOWED_CHAT_IDS)."}
	if got := LogAndFormatErrorTo(&recordingReporter{}, "get_chat", denied); got != denied.Message {
		t.Errorf("ChatAccessDeniedError message = %q", got)
	}
	if got := LogAndFormatErrorTo(&recordingReporter{}, "anything", errors.New("x"), WithUserMessage("custom")); got != "custom" {
		t.Errorf("user message = %q", got)
	}
}

func TestErrorLoggerWritesJSONAtErrorLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp_errors.log")
	var console strings.Builder
	logger := NewErrorLogger(path, &console)
	defer logger.Close()

	logger.Error(funnelErrorMessage)
	logger.Warning(floodWaitWarningMessage)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("error log not written: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one JSON line (warnings are not persisted), got %d: %s", len(lines), data)
	}
	var record map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("record is not JSON: %v: %s", err, lines[0])
	}
	if record["levelname"] != "ERROR" || record["name"] != "telegram_mcp" || record["message"] != funnelErrorMessage {
		t.Errorf("unexpected record: %v", record)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{4}$`).MatchString(record["asctime"]) {
		t.Errorf("asctime %q does not match the python-json-logger datefmt", record["asctime"])
	}

	text := console.String()
	if !strings.Contains(text, "[ERROR] telegram_mcp - "+funnelErrorMessage) {
		t.Errorf("console missing plain-text error line: %q", text)
	}
	if !strings.Contains(text, "[WARNING] telegram_mcp - "+floodWaitWarningMessage) {
		t.Errorf("console missing plain-text warning line: %q", text)
	}
	if strings.Contains(string(data), floodWaitWarningMessage) {
		t.Error("FloodWait warning must not be persisted to the JSON error log")
	}
}

func TestErrorLoggerDegradesToConsoleOnly(t *testing.T) {
	var console strings.Builder
	logger := NewErrorLogger(t.TempDir(), &console)
	defer logger.Close()
	logger.Error(funnelErrorMessage)
	text := console.String()
	if !strings.Contains(text, "WARNING: Error setting up log file; using console logging only.") {
		t.Errorf("missing degradation warning: %q", text)
	}
	if !strings.Contains(text, funnelErrorMessage) {
		t.Errorf("console-only logging lost the record: %q", text)
	}
}

func TestErrorLoggerCloseIsIdempotent(t *testing.T) {
	logger := NewErrorLogger(filepath.Join(t.TempDir(), "mcp_errors.log"), &strings.Builder{})
	if err := logger.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestErrorLogPathOverrides(t *testing.T) {
	t.Setenv("TELEGRAM_MCP_ERROR_LOG", "/tmp/custom-errors.log")
	if got := ErrorLogPath(); got != "/tmp/custom-errors.log" {
		t.Errorf("TELEGRAM_MCP_ERROR_LOG not honored: %q", got)
	}
	t.Setenv("TELEGRAM_MCP_ERROR_LOG", "")
	t.Setenv("TELEGRAM_MCP_PROJECT_ROOT", "/tmp/project-root")
	if got := ErrorLogPath(); got != filepath.Join("/tmp/project-root", "mcp_errors.log") {
		t.Errorf("TELEGRAM_MCP_PROJECT_ROOT not honored: %q", got)
	}
}

package kit

// This file ports runtime.py's error funnel: log_and_format_error, the
// ErrorCategory prefixes, the FloodWait special case and the privacy-safe
// file+console logger.
//
// The funnel never records exception text, identifiers, user content,
// provider payloads or local paths: callers hand it a function name and an
// error, and every line it writes is a constant, server-authored string.

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrorCategory mirrors runtime.ErrorCategory: the stable prefix of a tool's
// error code.
type ErrorCategory string

const (
	CategoryChat    ErrorCategory = "CHAT"
	CategoryMsg     ErrorCategory = "MSG"
	CategoryContact ErrorCategory = "CONTACT"
	CategoryGroup   ErrorCategory = "GROUP"
	CategoryMedia   ErrorCategory = "MEDIA"
	CategoryProfile ErrorCategory = "PROFILE"
	CategoryAuth    ErrorCategory = "AUTH"
	CategoryAdmin   ErrorCategory = "ADMIN"
	CategoryFolder  ErrorCategory = "FOLDER"
	CategoryPrivacy ErrorCategory = "PRIVACY"
)

// errorCategoryOrder is the Python enum's definition order: prefix
// derivation scans it in exactly this order and the first substring match
// wins.
var errorCategoryOrder = []ErrorCategory{
	CategoryChat, CategoryMsg, CategoryContact, CategoryGroup, CategoryMedia,
	CategoryProfile, CategoryAuth, CategoryAdmin, CategoryFolder, CategoryPrivacy,
}

// funnelErrorMessage is the only message ever persisted for a generic
// failure (runtime.py logs the same constant, keeping logs useful without
// leaking anything).
const funnelErrorMessage = "Telegram MCP operation failed; see the returned stable error code."

// floodWaitWarningMessage is the categorical FloodWait warning; it is never
// persisted to the JSON error log.
const floodWaitWarningMessage = "Telegram FloodWait; retry only after the reported delay."

// ValidationError mirrors runtime.ValidationError: a rejected parameter
// value. Its message is server-authored, user-facing text that
// LogAndFormatError returns verbatim.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// ChatAccessDeniedError mirrors runtime.ChatAccessDeniedError: the privacy
// allowlist refused a chat parameter.
type ChatAccessDeniedError struct{ Message string }

func (e *ChatAccessDeniedError) Error() string { return e.Message }

// FloodWaitError is Telegram's FLOOD_WAIT_X: the operation may not be
// repeated until the reported number of seconds has elapsed. LLM agents
// retry generic errors blindly, which escalates the flood penalty, so the
// funnel turns this type into explicit "do NOT retry immediately" prose.
type FloodWaitError struct {
	// Seconds is the required wait; 0 means Telegram reported no duration.
	Seconds int
	// Cause is the underlying client error, when an adapter wrapped one.
	Cause error
}

func (e *FloodWaitError) Error() string {
	return fmt.Sprintf("Telegram FloodWait: retry only after %d seconds", e.Seconds)
}

// Unwrap exposes the underlying error for errors.Is/As.
func (e *FloodWaitError) Unwrap() error { return e.Cause }

// IsFloodWait reports whether err is (or wraps) a FloodWaitError.
func IsFloodWait(err error) bool {
	var f *FloodWaitError
	return errors.As(err, &f)
}

// Reporter receives the funnel's diagnostics. *ErrorLogger is the production
// implementation; tests inject a recorder.
type Reporter interface {
	// Error records a failed operation. message is always a constant,
	// server-authored line: never exception text, identifiers, content or
	// paths.
	Error(message string)
	// Warning records a non-persisted warning (FloodWait). Warnings are
	// never written to the JSON error log.
	Warning(message string)
}

// ErrorLogger is the production Reporter: JSON records at ERROR level are
// appended to a file and mirrored to the console as plain text. Both sinks
// receive only the constant lines callers pass in.
type ErrorLogger struct {
	mu      sync.Mutex
	file    *os.File
	console io.Writer
	now     func() time.Time
}

// logRecord mirrors the fields python-json-logger writes with the
// runtime.py format "%(asctime)s %(name)s %(levelname)s %(message)s".
type logRecord struct {
	Asctime   string `json:"asctime"`
	Name      string `json:"name"`
	Levelname string `json:"levelname"`
	Message   string `json:"message"`
}

// consoleTimestamp mirrors the default logging.Formatter asctime
// ("2026-10-05 20:52:31,123").
const consoleTimestamp = "2006-01-02 15:04:05,000"

// fileTimestamp mirrors datefmt="%Y-%m-%dT%H:%M:%S%z".
const fileTimestamp = "2006-01-02T15:04:05-0700"

// NewErrorLogger opens path in append mode and returns a logger writing JSON
// records there plus plain-text lines to console (os.Stderr when nil).
// Parent directories and file permissions are created best-effort; when the
// file cannot be opened the logger degrades to console-only and reports that
// once, mirroring runtime.py.
func NewErrorLogger(path string, console io.Writer) *ErrorLogger {
	if console == nil {
		console = os.Stderr
	}
	l := &ErrorLogger{console: console, now: time.Now}
	if path == "" {
		return l
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755) // best effort
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_ = f.Chmod(0o600) // best effort on platforms that allow it
		l.file = f
		return l
	}
	fmt.Fprintln(l.console, "WARNING: Error setting up log file; using console logging only.")
	l.Error("Failed to set up log file handler; using console logging only.")
	return l
}

// Error records a failed operation: a JSON line in the error log (when one
// is open) and a plain-text line on the console.
func (l *ErrorLogger) Error(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.file != nil {
		record := logRecord{
			Asctime:   now.Format(fileTimestamp),
			Name:      "telegram_mcp",
			Levelname: "ERROR",
			Message:   message,
		}
		if data, err := json.Marshal(record); err == nil {
			_, _ = l.file.Write(append(data, '\n'))
		}
	}
	fmt.Fprintf(l.console, "%s [ERROR] telegram_mcp - %s\n", now.Format(consoleTimestamp), message)
}

// Warning writes a plain-text warning to the console only. FloodWait
// warnings are deliberately not persisted (runtime.py's file handler stores
// ERROR records only).
func (l *ErrorLogger) Warning(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.console, "%s [WARNING] telegram_mcp - %s\n", l.now().Format(consoleTimestamp), message)
}

// Close closes the log file, when one is open.
func (l *ErrorLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// ErrorLogPath returns the file the default reporter appends JSON error
// records to: PROJECT_ROOT/mcp_errors.log, matching runtime.py's legacy repo
// root location (not the package directory). TELEGRAM_MCP_ERROR_LOG
// overrides the whole path; TELEGRAM_MCP_PROJECT_ROOT overrides the project
// root; otherwise the nearest ancestor of the working directory containing
// go.mod wins, falling back to the working directory.
func ErrorLogPath() string {
	if override := os.Getenv("TELEGRAM_MCP_ERROR_LOG"); override != "" {
		return override
	}
	root := os.Getenv("TELEGRAM_MCP_PROJECT_ROOT")
	if root == "" {
		root = findProjectRoot()
	}
	return filepath.Join(root, "mcp_errors.log")
}

// findProjectRoot walks up from the working directory to the nearest
// directory containing go.mod, or returns the working directory unchanged.
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	start := dir
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

var (
	defaultReporterMu sync.Mutex
	defaultReporter   Reporter
)

// SetDefaultReporter replaces the package-level reporter used by
// LogAndFormatError. Bootstrap and tests call this; nil restores lazy
// construction on the next use.
func SetDefaultReporter(rep Reporter) {
	defaultReporterMu.Lock()
	defer defaultReporterMu.Unlock()
	defaultReporter = rep
}

// DefaultReporter returns the package-level reporter, lazily building the
// file+console ErrorLogger on first use.
func DefaultReporter() Reporter {
	defaultReporterMu.Lock()
	defer defaultReporterMu.Unlock()
	if defaultReporter == nil {
		defaultReporter = NewErrorLogger(ErrorLogPath(), os.Stderr)
	}
	return defaultReporter
}

// ErrorOption customizes LogAndFormatError.
type ErrorOption func(*errorOptions)

type errorOptions struct {
	prefix      ErrorCategory
	userMessage string
}

// WithPrefix pins the error-code prefix instead of deriving it from the
// function name.
func WithPrefix(prefix ErrorCategory) ErrorOption {
	return func(o *errorOptions) { o.prefix = prefix }
}

// WithUserMessage returns a custom user-facing message verbatim, mirroring
// the user_message argument of runtime.log_and_format_error.
func WithUserMessage(message string) ErrorOption {
	return func(o *errorOptions) { o.userMessage = message }
}

// LogAndFormatError mirrors runtime.log_and_format_error, reporting through
// the package default reporter.
func LogAndFormatError(functionName string, err error, opts ...ErrorOption) string {
	return LogAndFormatErrorTo(DefaultReporter(), functionName, err, opts...)
}

// LogAndFormatErrorTo is LogAndFormatError with an explicit reporter.
//
// Error types carry their user-facing text: *ValidationError and
// *ChatAccessDeniedError return their message verbatim (runtime.py passes
// the same text as user_message at every call site). A *FloodWaitError
// produces the "do NOT retry immediately" prose with the reported wait.
// Every other error produces the stable "<PREFIX>-ERR-<NNN>" code with a
// generic message.
func LogAndFormatErrorTo(rep Reporter, functionName string, err error, opts ...ErrorOption) string {
	o := errorOptions{}
	for _, opt := range opts {
		opt(&o)
	}

	if o.userMessage == "" {
		var invalid *ValidationError
		if errors.As(err, &invalid) {
			rep.Error(funnelErrorMessage)
			return invalid.Message
		}
		var denied *ChatAccessDeniedError
		if errors.As(err, &denied) {
			rep.Error(funnelErrorMessage)
			return denied.Message
		}
	}

	code := errorCode(functionName, o.prefix)

	if IsFloodWait(err) {
		rep.Warning(floodWaitWarningMessage)
		if o.userMessage != "" {
			return o.userMessage
		}
		var flood *FloodWaitError
		_ = errors.As(err, &flood)
		wait := "an unknown duration"
		if flood.Seconds > 0 {
			wait = fmt.Sprintf("%d seconds", flood.Seconds)
		}
		return fmt.Sprintf(
			"Rate limit exceeded (FloodWait): Telegram requires waiting %s before repeating this operation. Do NOT retry immediately (code: %s).",
			wait, code,
		)
	}

	rep.Error(funnelErrorMessage)
	if o.userMessage != "" {
		return o.userMessage
	}
	return fmt.Sprintf("An error occurred (code: %s).", code)
}

// errorCode builds the stable "<PREFIX>-ERR-<NNN>" code. runtime.py used
// Python's per-process salted hash() there, so its codes were not actually
// stable across restarts; the Go port uses FNV-1a for a deterministic code
// per function name.
func errorCode(functionName string, prefix ErrorCategory) string {
	p := string(prefix)
	if p == "" {
		p = deriveErrorPrefix(functionName)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(functionName))
	return fmt.Sprintf("%s-ERR-%03d", p, h.Sum64()%1000)
}

// deriveErrorPrefix mirrors the function-name scan: the first category (in
// ErrorCategory definition order) whose name is a substring of the function
// name, else "GEN".
func deriveErrorPrefix(functionName string) string {
	lower := strings.ToLower(functionName)
	for _, category := range errorCategoryOrder {
		if strings.Contains(lower, strings.ToLower(string(category))) {
			return string(category)
		}
	}
	return "GEN"
}

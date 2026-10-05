// Package paths implements the fail-closed file-path gate for file-path
// tools, ported from telegram_mcp/runtime.py.
//
// The gate decides two things on every tool call:
//
//  1. Which directories a tool may touch. Client-supplied MCP Roots replace
//     the server CLI roots; an empty client roots list is deny-all unless
//     TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK opts back into the server roots;
//     TELEGRAM_SERVER_ROOTS_ONLY skips the roots/list call entirely; and a
//     client that never answers roots/list disables file tools after the
//     configured timeout instead of hanging.
//  2. Whether a requested path stays inside those roots. Wildcard and
//     traversal patterns are rejected up front, and symlinks are resolved
//     before the containment check, so neither "../" nor a symlink can
//     escape a root.
//
// Failures are errors whose messages are load-bearing contracts asserted
// verbatim by the tests. Do not reword them: "Path traversal is not
// allowed.", "Path is outside allowed roots.", "deny-all", "disabled".
package paths

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RootsStatus is the outcome of resolving the effective allowed roots. The
// values mirror ROOTS_STATUS_* in runtime.py byte-for-byte.
type RootsStatus string

const (
	// StatusReady: usable roots exist (client roots, or server roots when
	// there is no client session to ask).
	StatusReady RootsStatus = "ready"
	// StatusNotConfigured: no roots anywhere; file tools are disabled.
	StatusNotConfigured RootsStatus = "not_configured"
	// StatusUnsupportedFallback: the client cannot answer roots/list, so
	// the server CLI roots apply.
	StatusUnsupportedFallback RootsStatus = "unsupported_fallback"
	// StatusClientDenyAll: the client answered with an empty roots list.
	// Deny-all unless the server-roots fallback is opted in.
	StatusClientDenyAll RootsStatus = "client_deny_all"
	// StatusServerFallback: the client was empty or unusable and the opt-in
	// TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK made the server CLI roots apply.
	StatusServerFallback RootsStatus = "server_fallback"
	// StatusServerOnly: TELEGRAM_SERVER_ROOTS_ONLY skipped the roots/list
	// call and the server CLI roots apply.
	StatusServerOnly RootsStatus = "server_only"
	// StatusError: the roots/list call failed unexpectedly; deny-all.
	StatusError RootsStatus = "error"
	// StatusTimeout: the client never answered roots/list before the
	// configured timeout; deny-all.
	StatusTimeout RootsStatus = "timeout"
)

// DefaultRootsTimeout mirrors ROOTS_REQUEST_TIMEOUT_DEFAULT: some clients
// accept the server-initiated roots/list request but never answer it, which
// would otherwise hang every file-path tool forever instead of failing.
const DefaultRootsTimeout = 10 * time.Second

// DefaultDownloadSubdir mirrors DEFAULT_DOWNLOAD_SUBDIR: where writable
// paths without an explicit raw path land inside the first root.
const DefaultDownloadSubdir = "downloads"

// disallowedPathPatterns mirrors DISALLOWED_PATH_PATTERNS.
var disallowedPathPatterns = []string{"*", "?", "[", "]", "{", "}", "~", "\x00"}

// defaultExtensionAllowlists mirrors _DEFAULT_EXTENSION_ALLOWLISTS: the
// per-tool extension gates in force when the caller supplies no merged
// allowlists (TELEGRAM_FILE_EXTENSIONS can replace a tool's whole set).
func defaultExtensionAllowlists() map[string][]string {
	return map[string][]string{
		"send_voice":        {".ogg", ".opus"},
		"send_sticker":      {".webp"},
		"set_profile_photo": {".jpg", ".jpeg", ".png", ".webp"},
		"edit_chat_photo":   {".jpg", ".jpeg", ".png", ".webp"},
	}
}

// defaultMaxFileBytes mirrors MAX_FILE_BYTES.
func defaultMaxFileBytes() map[string]int64 {
	const mb = 1024 * 1024
	return map[string]int64{
		"download_media":    200 * mb,
		"send_file":         200 * mb,
		"upload_file":       200 * mb,
		"send_voice":        100 * mb,
		"send_sticker":      10 * mb,
		"set_profile_photo": 50 * mb,
		"edit_chat_photo":   50 * mb,
	}
}

// Root is one client-provided MCP Roots entry. It mirrors the URI of
// mcp.Root (the go-sdk type) so that the session layer can adapt a
// *mcp.ServerSession into a RootsLister without this package importing the
// SDK.
type Root struct {
	URI string
}

// RootsLister fetches the client's MCP Roots for one tool call. A nil
// lister means "no MCP client session" (ctx=None in the Python source).
type RootsLister interface {
	ListRoots(ctx context.Context) ([]Root, error)
}

// RootsFunc adapts a function to RootsLister.
type RootsFunc func(ctx context.Context) ([]Root, error)

// ListRoots implements RootsLister.
func (f RootsFunc) ListRoots(ctx context.Context) ([]Root, error) { return f(ctx) }

// Settings is the parsed configuration a Gate is built from. The roots
// fields come from internal/config (TELEGRAM_ALLOWED_ROOTS,
// TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK, TELEGRAM_SERVER_ROOTS_ONLY and
// TELEGRAM_ROOTS_TIMEOUT_SECONDS); the allowlists default to the Python
// hardcoded values when nil.
type Settings struct {
	// ServerRoots are the operator-configured allowed roots, already
	// expanded and resolved (see ResolveServerRoots).
	ServerRoots []string
	// AllowServerRootsFallback mirrors TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK:
	// server roots may replace an empty or unusable client roots list.
	AllowServerRootsFallback bool
	// ServerRootsOnly mirrors TELEGRAM_SERVER_ROOTS_ONLY: use the server
	// roots and skip the roots/list call entirely.
	ServerRootsOnly bool
	// RootsTimeout mirrors TELEGRAM_ROOTS_TIMEOUT_SECONDS; nil waits
	// forever, matching a value of 0 or less in the Python source.
	RootsTimeout *time.Duration
	// ExtensionAllowlists maps a tool name to the extensions it accepts
	// (lower-case, with a leading dot); nil uses the Python defaults.
	ExtensionAllowlists map[string][]string
	// MaxFileBytes maps a tool name to its size ceiling in bytes; nil uses
	// the Python defaults.
	MaxFileBytes map[string]int64
	// DownloadSubdir overrides DefaultDownloadSubdir when non-empty.
	DownloadSubdir string
	// Logger receives the fallback/deny diagnostics; nil uses
	// slog.Default().
	Logger *slog.Logger
}

// Gate is one resolved path-gate configuration. It is read-only after New
// and safe for concurrent use.
type Gate struct {
	serverRoots              []string
	allowServerRootsFallback bool
	serverRootsOnly          bool
	rootsTimeout             *time.Duration
	extensionAllowlists      map[string][]string
	maxFileBytes             map[string]int64
	downloadSubdir           string
	logger                   *slog.Logger
}

// New builds a Gate. Nil allowlist maps fall back to the Python defaults;
// an empty DownloadSubdir becomes DefaultDownloadSubdir.
func New(s Settings) *Gate {
	g := &Gate{
		serverRoots:              append([]string(nil), s.ServerRoots...),
		allowServerRootsFallback: s.AllowServerRootsFallback,
		serverRootsOnly:          s.ServerRootsOnly,
		rootsTimeout:             s.RootsTimeout,
		extensionAllowlists:      s.ExtensionAllowlists,
		maxFileBytes:             s.MaxFileBytes,
		downloadSubdir:           s.DownloadSubdir,
		logger:                   s.Logger,
	}
	if g.extensionAllowlists == nil {
		g.extensionAllowlists = defaultExtensionAllowlists()
	}
	if g.maxFileBytes == nil {
		g.maxFileBytes = defaultMaxFileBytes()
	}
	if g.downloadSubdir == "" {
		g.downloadSubdir = DefaultDownloadSubdir
	}
	if g.logger == nil {
		g.logger = slog.Default()
	}
	return g
}

// EffectiveRoots mirrors _get_effective_allowed_roots_with_status: it
// resolves the roots a tool call may use and reports how they were
// obtained. Client roots replace server roots; every unusable client state
// ends in deny-all unless the explicit fallback opt-in applies.
func (g *Gate) EffectiveRoots(ctx context.Context, lister RootsLister) ([]string, RootsStatus) {
	fallback := g.serverRoots
	if lister == nil {
		if len(fallback) > 0 {
			return append([]string(nil), fallback...), StatusReady
		}
		return nil, StatusNotConfigured
	}
	if len(fallback) > 0 && g.serverRootsOnly {
		return append([]string(nil), fallback...), StatusServerOnly
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if g.rootsTimeout != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *g.rootsTimeout)
		defer cancel()
	}
	result, err := lister.ListRoots(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			if len(fallback) > 0 && g.allowServerRootsFallback {
				g.logger.Warn("MCP client did not answer roots/list before the configured timeout (TELEGRAM_ROOTS_TIMEOUT_SECONDS); falling back to server CLI roots (TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK).")
				return append([]string(nil), fallback...), StatusServerFallback
			}
			g.logger.Error("MCP client did not answer roots/list before the configured timeout (TELEGRAM_ROOTS_TIMEOUT_SECONDS); disabling file-path tools instead of hanging.")
			return nil, StatusTimeout
		}
		if isRootsUnsupportedError(err) {
			if len(fallback) > 0 {
				return append([]string(nil), fallback...), StatusUnsupportedFallback
			}
			return nil, StatusNotConfigured
		}
		// Unexpected roots/list failures. Match empty-list behavior:
		// opt-in server fallback, otherwise deny-all.
		if len(fallback) > 0 && g.allowServerRootsFallback {
			g.logger.Warn("MCP roots request failed; falling back to server CLI roots (TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK).")
			return append([]string(nil), fallback...), StatusServerFallback
		}
		g.logger.Error("MCP roots request failed; disabling file-path tools for safety.")
		return nil, StatusError
	}

	var clientRoots []string
	recovered := false
	for _, root := range result {
		resolved, err := CoerceRootURIToPath(root.URI)
		if err != nil {
			// Go-SDK analogue of the bare-path recovery in
			// _coerce_paths_from_list_roots_validation_error: some clients
			// (notably Cursor) return workspace roots as plain absolute
			// paths instead of file:// URIs. The Python SDK rejects those
			// with a pydantic validation error and recovers the paths from
			// the error payload; the Go SDK decodes roots as plain strings,
			// so the recovery happens here, where the entry fails file://
			// coercion. Invalid entries stay ignored.
			if path, ok := recoverBareRootPath(root.URI); ok {
				clientRoots = append(clientRoots, path)
				recovered = true
			}
			continue
		}
		clientRoots = append(clientRoots, resolved)
	}
	if recovered {
		g.logger.Warn("MCP client returned non-URI roots; recovered validated paths.")
	}
	if len(clientRoots) > 0 {
		return dedupePaths(clientRoots), StatusReady
	}

	// Roots API succeeded but returned an empty list. By default this is an
	// explicit deny-all; the opt-in lets server-side roots take effect for
	// clients that advertise the Roots capability but expose no roots.
	if len(fallback) > 0 && g.allowServerRootsFallback {
		return append([]string(nil), fallback...), StatusServerFallback
	}
	return nil, StatusClientDenyAll
}

// EnsureRoots mirrors _ensure_allowed_roots: it returns the roots the tool
// may use, or the "disabled" denial message that the tool body must return
// verbatim.
func (g *Gate) EnsureRoots(ctx context.Context, lister RootsLister, toolName string) ([]string, error) {
	roots, status := g.EffectiveRoots(ctx, lister)
	if len(roots) > 0 {
		return roots, nil
	}
	switch status {
	case StatusClientDenyAll:
		return nil, fmt.Errorf("%s is disabled because the client provided an empty MCP Roots list (deny-all).", toolName)
	case StatusError:
		return nil, fmt.Errorf("%s is disabled because MCP Roots could not be verified safely. Check MCP client/server logs.", toolName)
	case StatusTimeout:
		return nil, fmt.Errorf("%s is disabled because the MCP client never answered the roots/list request. Configure server CLI roots and set TELEGRAM_SERVER_ROOTS_ONLY=1 (skip roots/list) or TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK=1, or raise TELEGRAM_ROOTS_TIMEOUT_SECONDS.", toolName)
	default:
		return nil, fmt.Errorf("%s is disabled until allowed roots are configured. Provide server CLI roots and/or client MCP Roots.", toolName)
	}
}

// ResolveServerRoots mirrors the root handling of
// runtime._configure_allowed_roots_from_cli: each root is ~-expanded,
// created when missing, resolved through symlinks, and de-duplicated in
// order. A root that cannot be created or resolved is an error the caller
// must abort startup on (SystemExit in Python).
func ResolveServerRoots(raw []string) ([]string, error) {
	var roots []string
	for _, rawRoot := range raw {
		root, err := expandUser(rawRoot)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(root); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			if mkErr := os.MkdirAll(root, 0o755); mkErr != nil {
				return nil, fmt.Errorf("Allowed root does not exist: %s", root)
			}
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, err
		}
		roots = append(roots, resolved)
	}
	return dedupePaths(roots), nil
}

// CoerceRootURIToPath mirrors _coerce_root_uri_to_path: only file:// URIs
// are accepted, and the path must exist (strict resolve) — the caller
// ignores a root entry that fails either check.
func CoerceRootURIToPath(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("Unsupported root URI scheme: %s", parsed.Scheme)
	}
	decoded := parsed.Path
	if parsed.Host != "" && parsed.Host != "localhost" {
		decoded = "//" + parsed.Host + decoded
	}
	if runtime.GOOS == "windows" && len(decoded) > 2 && decoded[0] == '/' && decoded[2] == ':' {
		// file:///C:/tmp -> C:/tmp on Windows.
		decoded = decoded[1:]
	}
	if decoded == "" {
		// pathlib.Path("") is Path("."), so an empty file: URI resolves to
		// the process working directory.
		decoded = "."
	}
	resolved, err := filepath.EvalSymlinks(decoded)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

// recoverBareRootPath is the Go-SDK analogue of the bare-path recovery in
// _coerce_paths_from_list_roots_validation_error: it accepts the two shapes
// that recovery accepts — a Unix absolute path, or a Windows drive path
// such as C:\Users\dev\workspace — ~-expands it, and resolves it
// (non-strict, like pathlib.Path.resolve()).
func recoverBareRootPath(raw string) (string, bool) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return "", false
	}
	isAbs := strings.HasPrefix(candidate, "/") || (len(candidate) > 2 && candidate[1] == ':')
	if !isAbs {
		return "", false
	}
	expanded, err := expandUser(candidate)
	if err != nil {
		return "", false
	}
	resolved, err := realpathLenient(expanded)
	if err != nil {
		return "", false
	}
	return resolved, true
}

// isRootsUnsupportedError mirrors _is_roots_unsupported_error: a client
// that does not implement roots/list (JSON-RPC -32601, "method not found",
// or "not implemented") is unsupported, not an unexpected failure. The
// Python source also inspects the wire error code; the go-sdk surfaces
// that code only inside its own error type, so the adapter that owns the
// SDK dependency may surface it as a message (or extend this check when it
// exposes the code).
func isRootsUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "method not found") ||
		strings.Contains(message, "not implemented") ||
		strings.Contains(message, "list_roots")
}

// expandUser expands a leading "~" or "~/" like pathlib.Path.expanduser.
// The "~user" form is not supported; operator roots use plain "~".
func expandUser(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// dedupePaths mirrors _dedupe_paths: first occurrence wins, order kept.
func dedupePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	var out []string
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

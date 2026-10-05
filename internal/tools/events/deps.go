package events

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
)

// Deps is the injectable seam between this tool module and the rest of the
// server: everything events.py reached for through the Python runtime's
// star-import (clients, allowlists, env, clock). A tool body never touches a
// package global directly, so the whole module runs offline against fakes.
//
// The zero value is usable and safe: no entity resolution (a chat_id must then
// already be an integer), no allowlist gate, no saved aliases, the
// platform-default feed path and the real clock. The boot layer fills the
// fields from internal/config plus a session-layer entity source.
type Deps struct {
	// Resolver turns a chat_id given as a username, phone number or free-text
	// alias into a marked Telegram ID. nil leaves such an identifier
	// unresolved, which the wait tools report through the error funnel.
	Resolver *kit.Resolver

	// Aliases is the saved-contact store (TELEGRAM_ALIASES_FILE). nil means
	// apply_alias is a no-op.
	Aliases *kit.Aliases

	// Allowlist is TELEGRAM_ALLOWED_CHAT_IDS. nil disables the privacy gate.
	Allowlist kit.ChatAllowlist

	// FeedFile overrides TELEGRAM_EVENT_FEED_FILE. An explicit path is never
	// created together with its parent directory, so a typo fails loudly
	// instead of scattering directories.
	FeedFile string

	// XDGStateHome is the base of the default feed path; empty means
	// HomeDir/.local/state.
	XDGStateHome string

	// HomeDir is the base of the default state directory; empty uses
	// os.UserHomeDir.
	HomeDir string

	// EventFeed mirrors TELEGRAM_EVENT_FEED: start the feed on the first
	// incoming message.
	EventFeed bool

	// Monotonic is the burst clock (elapsed seconds); nil uses a monotonic
	// clock anchored at process start.
	Monotonic func() float64

	// Wall is the feed-line timestamp clock; nil uses time.Now.
	Wall func() time.Time

	// Reporter receives this module's diagnostics; nil uses
	// kit.DefaultReporter().
	Reporter kit.Reporter
}

func (d Deps) withDefaults() Deps {
	if d.HomeDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			d.HomeDir = home
		} else {
			d.HomeDir = "."
		}
	}
	if d.Monotonic == nil {
		origin := time.Now()
		d.Monotonic = func() float64 { return time.Since(origin).Seconds() }
	}
	if d.Wall == nil {
		d.Wall = time.Now
	}
	if d.Reporter == nil {
		d.Reporter = kit.DefaultReporter()
	}
	return d
}

// app is one configured module instance: the dependencies plus the pending
// burst tracker they share.
type app struct {
	Deps
	tracker *tracker
}

var (
	appMu      sync.RWMutex
	currentApp = newApp(Deps{})

	defaultSettleMS = 6000
)

func newApp(d Deps) *app {
	d = d.withDefaults()
	return &app{Deps: d, tracker: newTracker(d)}
}

// Configure installs the module's dependencies and returns a fresh tracker,
// discarding every pending burst. Any feed the previous instance was running is
// stopped first, so a reconfigure can never leave an orphaned consumer
// writing to a stale feed file.
func Configure(d Deps) {
	a := newApp(d)

	appMu.Lock()
	previous := currentApp
	currentApp = a
	appMu.Unlock()

	previous.tracker.stopFeed()
}

// Snapshot returns the currently configured dependencies.
func Snapshot() Deps {
	appMu.RLock()
	defer appMu.RUnlock()
	return currentApp.Deps
}

func active() *app {
	appMu.RLock()
	defer appMu.RUnlock()
	return currentApp
}

// feedFilePath ports events.feed_file_path: TELEGRAM_EVENT_FEED_FILE when set,
// otherwise telegram-mcp/incoming_feed.jsonl under the XDG state directory. The
// default follows the XDG state convention rather than the install directory,
// which may be a read-only site-packages or container layer.
func (d Deps) feedFilePath() string {
	if override := strings.TrimSpace(d.FeedFile); override != "" {
		return override
	}
	base := d.XDGStateHome
	if base == "" {
		base = filepath.Join(d.HomeDir, ".local", "state")
	}
	return filepath.Join(base, "telegram-mcp", "incoming_feed.jsonl")
}

// chatGate builds the allowlist gate, mirroring runtime.validate_id's
// decorator half for the chat_id parameter.
func (d Deps) chatGate() *kit.ChatGate {
	gate := &kit.ChatGate{Allowlist: d.Allowlist}
	if d.Resolver != nil {
		gate.ResolveEntity = d.Resolver.Resolve
	}
	return gate
}

// feedState is the incoming_feed_state payload: whether the feed is running,
// where it writes, and the watch commands an external agent arms on.
type feedState struct {
	Enabled                bool   `json:"enabled"`
	FeedFile               string `json:"feed_file"`
	SettleMS               int    `json:"settle_ms"`
	WatchCommand           string `json:"watch_command"`
	WatchCommandForOneChat string `json:"watch_command_for_one_chat"`
	AutostartPending       bool   `json:"autostart_pending"`
}

func (t *tracker) feedState(deps Deps) feedState {
	path := deps.feedFilePath()
	quoted := shellQuote(path)
	enabled, settleMS, autostart := t.feedStatus(deps)
	return feedState{
		Enabled:                enabled,
		FeedFile:               path,
		SettleMS:               settleMS,
		WatchCommand:           "tail -n 0 -F " + quoted,
		WatchCommandForOneChat: "tail -n 0 -F " + quoted + " | grep --line-buffered '\"chat_id\": <ID>'",
		// A pending autostart is one the user has neither consumed nor
		// cancelled: the next incoming message would start the feed.
		AutostartPending: !autostart && !enabled && deps.EventFeed,
	}
}

// shellQuote ports shlex.quote: only quote when the word contains a character
// the shell would interpret, so a plain path stays copy-pasteable.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, needsShellQuote) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// needsShellQuote reports whether r is outside shlex's safe character set.
func needsShellQuote(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("_@%+=:,./-", r)
}

// markedTarget ports events._wait_target: nil/empty means "any chat"; a SAVED
// alias resolves to its stored ID; an integer is already a marked ID;
// anything else goes through entity resolution. A nil result means the caller
// must not filter by chat.
func (d Deps) waitTarget(chatID any) (*int64, error) {
	if chatID == nil {
		return nil, nil
	}
	resolved, err := kit.ValidateID("chat_id", chatID)
	if err != nil {
		return nil, err
	}
	if text, ok := resolved.(string); ok {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		// Exact saved aliases only: a real @handle or phone number is never
		// shadowed by a remembered wording.
		if id, found := d.Aliases.Resolve(text); found {
			if parsed, parseErr := strconv.ParseInt(id, 10, 64); parseErr == nil {
				return &parsed, nil
			}
		}
	}
	switch v := resolved.(type) {
	case int64:
		return &v, nil
	case string:
		if d.Resolver == nil {
			// A plain error, not a *ValidationError: the identifier is
			// well-formed, the server simply has no entity source wired, so
			// the funnel must report a stable code instead of echoing prose as
			// if the caller's input were wrong.
			return nil, fmt.Errorf("chat resolution is unavailable; pass the numeric Telegram ID for chat %q", v)
		}
		entity, resolveErr := d.Resolver.Resolve(v)
		if resolveErr != nil {
			return nil, resolveErr
		}
		marked := kit.GetMarkedID(entity)
		return &marked, nil
	}
	return nil, &kit.ValidationError{Message: "Invalid chat_id: chat must be an ID, a username, or a saved contact alias."}
}

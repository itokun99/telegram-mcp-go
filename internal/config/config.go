package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Config is the fully-parsed, validated server configuration.
//
// Load parses every variable in one pass and fails loudly on any value the
// Python runtime layer would have aborted startup on (SystemExit there, an
// error return here). Each field names the environment variable it comes from.
type Config struct {
	// Credentials: TELEGRAM_API_ID fails at load when unset or non-integer
	// (the Python `int(os.getenv("TELEGRAM_API_ID"))` raises at import).
	APIID      int
	APIHash    string
	APIHashSet bool

	// Accounts discovered from TELEGRAM_SESSION_STRING_<LABEL> /
	// TELEGRAM_SESSION_NAME_<LABEL> / the unsuffixed pair, keyed by
	// lower-cased label. The unsuffixed variables yield "default".
	Accounts map[string]Account

	// Session pool from TELEGRAM_SESSION_STRINGS (whitespace/,/;-separated,
	// de-duplicated in order) for concurrent clients of the default account.
	SessionPool []string

	// Session lock: TELEGRAM_SESSION_LOCK, exclusive | shared (default).
	SessionLockMode string
	// TELEGRAM_LOCK_GRACE_SECONDS (default 20).
	LockGraceSeconds float64

	// Per-label expected usernames: TELEGRAM_EXPECTED_USERNAME_<LABEL>
	// else TELEGRAM_EXPECTED_USERNAME, normalized (trim, strip @, lowercase).
	ExpectedUsernames map[string]string

	// Tool exposure: TELEGRAM_EXPOSED_TOOLS -> "all" | "read-only"
	// (+ write-tool allowlist after "+").
	ExposedMode      string
	ExposedAllowlist []string

	// File extensions: TELEGRAM_FILE_EXTENSIONS -> tool -> extensions,
	// plus the hardcoded defaults and the merged view (a named tool
	// replaces its whole default set).
	ExtensionOverrides    map[string][]string
	DefaultExtensionLists map[string][]string
	ExtensionAllowlists   map[string][]string

	// File-path roots: TELEGRAM_ALLOWED_ROOTS, plus the server-roots
	// toggles and the client roots/list timeout (nil = wait forever).
	AllowedRoots             []string
	AllowServerRootsFallback bool
	ServerRootsOnly          bool
	RootsRequestTimeout      *float64

	// MCP tool-call ceiling: TELEGRAM_TOOL_TIMEOUT_SECONDS. A positive
	// value (float, like the Python source) caps every tool call; 0 or a
	// negative value means unbounded (the operator disables the ceiling).
	ToolTimeoutSeconds   float64
	ToolTimeoutUnlimited bool

	// Chat access control: TELEGRAM_ALLOWED_CHAT_IDS; nil = no allowlist.
	AllowedChats *ChatAllowlist

	// Transport: MCP_TRANSPORT (default stdio), MCP_HOST (default
	// 127.0.0.1), MCP_PORT (default 8765).
	Transport string
	Host      string
	Port      int

	// HTTP transport security: MCP_ALLOWED_HOSTS / MCP_ALLOWED_ORIGINS.
	AllowedHosts   []string
	AllowedOrigins []string

	// Proxy: TELEGRAM_PROXY_* globals; per-label overrides resolved on demand.
	Proxy ProxyConfig

	// TELEGRAM_FLOOD_SLEEP_THRESHOLD (default 60, clamped to 0 when negative).
	FloodSleepThreshold int
	// TELEGRAM_CONTACT_FUZZY (default true).
	ContactFuzzy bool
	// TELEGRAM_EVENT_FEED / TELEGRAM_EVENT_FEED_FILE (event feed callback).
	EventFeed     bool
	EventFeedFile string

	// TELEGRAM_LINK_DOMAIN (default t.me): the domain used to build
	// message permalinks.
	LinkDomain string
	// XDG_STATE_HOME: state dir for the event-feed file and the contact
	// aliases store; empty means the platform default (~/.local/state).
	XDGStateHome string

	// Transcription: TELEGRAM_TRANSCRIBE (default on-demand) and
	// TELEGRAM_TRANSCRIBE_ENGINE (default groq); engine sub-settings live in
	// Transcribe.
	TranscribeMode   string
	TranscribeEngine string
	Transcribe       TranscribeSettings

	// Device identity: TELEGRAM_DEVICE_MODEL / _SYSTEM_VERSION / _APP_VERSION.
	DeviceModel   string
	SystemVersion string
	AppVersion    string

	// TELEGRAM_ALIASES_FILE: where remembered-contacts are stored.
	AliasesFile string

	// Source is kept so per-label proxy overrides can be resolved on demand.
	Source EnvSource
}

// Account is one discovered Telegram account.
type Account struct {
	// Label is lower-cased; "default" is the unsuffixed account.
	Label string
	// SessionString is set when the account came from
	// TELEGRAM_SESSION_STRING_<LABEL>; SessionName when it came from
	// TELEGRAM_SESSION_NAME_<LABEL>.
	SessionString string
	SessionName   string
}

// TranscribeSettings holds the transcription engine sub-settings
// (mirrors transcription.py's TELEGRAM_TRANSCRIBE_* variables).
type TranscribeSettings struct {
	// Language: TELEGRAM_TRANSCRIBE_LANGUAGE, ISO-639-1 hint; empty lets
	// engines auto-detect.
	Language string
	// TimeoutSeconds: TELEGRAM_TRANSCRIBE_TIMEOUT for HTTP engines,
	// default 120.
	TimeoutSeconds float64

	// OpenAI (engine=openai): TELEGRAM_TRANSCRIBE_OPENAI_URL / _API_KEY /
	// _MODEL / _MAX_MB (default 25 MB).
	OpenAIURL    string
	OpenAIAPIKey string
	OpenAIModel  string
	OpenAIMaxMB  float64
	// Groq: GROQ_API_KEY; TELEGRAM_TRANSCRIBE_GROQ_MAX_MB (default 25 MB).
	GroqAPIKey string
	GroqMaxMB  float64
	// Whisper (engine=whisper): TELEGRAM_TRANSCRIBE_WHISPER_MODEL (default
	// "small"), _DEVICE (default "auto"), _COMPUTE_TYPE (default
	// "default"), _MODEL_DIR (empty = engine default).
	WhisperModel       string
	WhisperDevice      string
	WhisperComputeType string
	WhisperModelDir    string
	// Auto-mode prefetch budget: TELEGRAM_TRANSCRIBE_MAX_VOICES (default 5)
	// and _MAX_SECONDS (default 300).
	MaxVoices  int
	MaxSeconds int
	// CacheDir: TELEGRAM_TRANSCRIPT_CACHE_DIR (default "data/transcripts").
	CacheDir string
}

// transcribeModes mirrors transcription._TRANSCRIBE_MODES.
var transcribeModes = map[string]bool{"off": true, "on-demand": true, "auto": true}

// transcribeEngines mirrors transcription.ENGINES.
var transcribeEngines = map[string]bool{"telegram": true, "groq": true, "openai": true, "whisper": true}

// defaultExtensionLists mirrors runtime._DEFAULT_EXTENSION_ALLOWLISTS
// (merged into by TELEGRAM_FILE_EXTENSIONS).
var defaultExtensionLists = map[string][]string{
	"send_voice":        {".ogg", ".opus"},
	"send_sticker":      {".webp"},
	"set_profile_photo": {".jpg", ".jpeg", ".png", ".webp"},
	"edit_chat_photo":   {".jpg", ".jpeg", ".png", ".webp"},
}

// Load parses and validates every server environment variable from src.
func Load(src EnvSource) (*Config, error) {
	c := &Config{Source: src}

	if err := c.loadCredentials(); err != nil {
		return nil, err
	}
	if err := c.discoverAccounts(); err != nil {
		return nil, err
	}
	if err := c.loadSessionLock(); err != nil {
		return nil, err
	}
	if err := c.loadExposedTools(); err != nil {
		return nil, err
	}
	if err := c.loadFileExtensions(); err != nil {
		return nil, err
	}
	c.loadRoots()
	c.ToolTimeoutSeconds, c.ToolTimeoutUnlimited = toolTimeoutSeconds(src)
	c.AllowedChats = parseAllowedChatIDs(mustGet(src, "TELEGRAM_ALLOWED_CHAT_IDS"))
	if err := c.loadTransport(); err != nil {
		return nil, err
	}
	if err := c.loadProxy(); err != nil {
		return nil, err
	}
	c.loadToggles()
	if err := c.loadTranscription(); err != nil {
		return nil, err
	}
	c.DeviceModel = strings.TrimSpace(mustGet(src, "TELEGRAM_DEVICE_MODEL"))
	c.SystemVersion = strings.TrimSpace(mustGet(src, "TELEGRAM_SYSTEM_VERSION"))
	c.AppVersion = strings.TrimSpace(mustGet(src, "TELEGRAM_APP_VERSION"))
	c.AliasesFile = strings.TrimSpace(mustGet(src, "TELEGRAM_ALIASES_FILE"))
	c.LinkDomain = linkDomain(src)
	c.XDGStateHome = mustGet(src, "XDG_STATE_HOME")

	return c, nil
}

func (c *Config) loadCredentials() error {
	rawAPIID, ok := c.Source.Get("TELEGRAM_API_ID")
	if !ok || strings.TrimSpace(rawAPIID) == "" {
		return fmt.Errorf("TELEGRAM_API_ID is not set. Get an api_id at https://my.telegram.org/apps and set TELEGRAM_API_ID (e.g. in .env)")
	}
	apiID, err := strconv.Atoi(strings.TrimSpace(rawAPIID))
	if err != nil {
		return fmt.Errorf("invalid TELEGRAM_API_ID '%s': must be an integer (the numeric api_id from https://my.telegram.org/apps)", rawAPIID)
	}
	c.APIID = apiID
	c.APIHash = strings.TrimSpace(mustGet(c.Source, "TELEGRAM_API_HASH"))
	c.APIHashSet = c.APIHash != ""
	return nil
}

func (c *Config) loadSessionLock() error {
	raw, _ := c.Source.Get("TELEGRAM_SESSION_LOCK")
	mode := normalizeLockMode(raw)
	switch mode {
	case "exclusive", "shared":
		c.SessionLockMode = mode
	default:
		return fmt.Errorf("invalid TELEGRAM_SESSION_LOCK '%s'. Expected one of: exclusive, shared.", raw)
	}
	c.LockGraceSeconds = lockGraceSeconds(c.Source)
	return nil
}

func (c *Config) loadRoots() {
	c.AllowedRoots = parseAllowedRoots(mustGet(c.Source, "TELEGRAM_ALLOWED_ROOTS"))
	c.AllowServerRootsFallback = parseBoolEnv(mustGet(c.Source, "TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK"), false)
	c.ServerRootsOnly = parseBoolEnv(mustGet(c.Source, "TELEGRAM_SERVER_ROOTS_ONLY"), false)
	c.RootsRequestTimeout = rootsRequestTimeout(c.Source)
}

func (c *Config) loadToggles() {
	c.FloodSleepThreshold = floodSleepThreshold(c.Source)
	c.ContactFuzzy = parseBoolEnv(mustGet(c.Source, "TELEGRAM_CONTACT_FUZZY"), true)
	c.EventFeed = parseBoolEnv(mustGet(c.Source, "TELEGRAM_EVENT_FEED"), false)
	c.EventFeedFile = strings.TrimSpace(mustGet(c.Source, "TELEGRAM_EVENT_FEED_FILE"))
}

func (c *Config) loadTranscription() error {
	// Python reads these with os.getenv(X, default): a variable set to an
	// empty string aborts startup (it is not in the accepted set), it does
	// not silently fall back to the default.
	mode, set := c.Source.Get("TELEGRAM_TRANSCRIBE")
	mode = normalizeLower(mode)
	if !set {
		mode = "on-demand"
	}
	if !transcribeModes[mode] {
		return fmt.Errorf("invalid TELEGRAM_TRANSCRIBE '%s'. Expected one of: auto, off, on-demand.", mode)
	}
	c.TranscribeMode = mode

	engine, set := c.Source.Get("TELEGRAM_TRANSCRIBE_ENGINE")
	engine = normalizeLower(engine)
	if !set {
		engine = "groq"
	}
	if !transcribeEngines[engine] {
		return fmt.Errorf("invalid TELEGRAM_TRANSCRIBE_ENGINE '%s'. Expected one of: groq, openai, telegram, whisper.", engine)
	}
	c.TranscribeEngine = engine
	c.Transcribe = c.loadTranscribeSettings()
	return nil
}

// loadTranscribeSettings reads the TELEGRAM_TRANSCRIBE_* sub-settings
// (mirrors transcription.py). Invalid numeric values fall back to the
// same defaults the Python source uses (it catches the parse error and
// returns the default, instead of failing loud).
func (c *Config) loadTranscribeSettings() TranscribeSettings {
	src := c.Source
	return TranscribeSettings{
		Language:       normalizeLower(mustGet(src, "TELEGRAM_TRANSCRIBE_LANGUAGE")),
		TimeoutSeconds: envFloat(src, "TELEGRAM_TRANSCRIBE_TIMEOUT", 120.0),

		OpenAIURL:    strings.TrimSpace(mustGet(src, "TELEGRAM_TRANSCRIBE_OPENAI_URL")),
		OpenAIAPIKey: mustGet(src, "TELEGRAM_TRANSCRIBE_OPENAI_API_KEY"),
		OpenAIModel:  strings.TrimSpace(mustGet(src, "TELEGRAM_TRANSCRIBE_OPENAI_MODEL")),
		OpenAIMaxMB:  envFloat(src, "TELEGRAM_TRANSCRIBE_OPENAI_MAX_MB", 25.0),

		GroqAPIKey: mustGet(src, "GROQ_API_KEY"),
		GroqMaxMB:  envFloat(src, "TELEGRAM_TRANSCRIBE_GROQ_MAX_MB", 25.0),

		WhisperModel:       transcribeWhisperField(src, "TELEGRAM_TRANSCRIBE_WHISPER_MODEL", "small"),
		WhisperDevice:      transcribeWhisperField(src, "TELEGRAM_TRANSCRIBE_WHISPER_DEVICE", "auto"),
		WhisperComputeType: transcribeWhisperField(src, "TELEGRAM_TRANSCRIBE_WHISPER_COMPUTE_TYPE", "default"),
		WhisperModelDir:    strings.TrimSpace(mustGet(src, "TELEGRAM_TRANSCRIBE_WHISPER_MODEL_DIR")),

		MaxVoices:  envInt(src, "TELEGRAM_TRANSCRIBE_MAX_VOICES", 5),
		MaxSeconds: envInt(src, "TELEGRAM_TRANSCRIBE_MAX_SECONDS", 300),

		// Python's cache_dir() defaults the unset variable to data/transcripts
		// before creating it; record the same default so a consumer never sees
		// an empty cache directory.
		CacheDir: transcriptCacheDir(src),
	}
}

// transcriptCacheDir mirrors transcription.cache_dir(): an unset (or empty)
// TELEGRAM_TRANSCRIPT_CACHE_DIR falls back to data/transcripts.
func transcriptCacheDir(src EnvSource) string {
	v := strings.TrimSpace(mustGet(src, "TELEGRAM_TRANSCRIPT_CACHE_DIR"))
	if v == "" {
		return "data/transcripts"
	}
	return v
}

// transcribeWhisperField mirrors transcription.py's `os.getenv(X).strip() or
// DEFAULT`: an empty value yields the engine default.
func transcribeWhisperField(src EnvSource, key, def string) string {
	v := strings.TrimSpace(mustGet(src, key))
	if v == "" {
		return def
	}
	return v
}

func (c *Config) loadTransport() error {
	// Same os.getenv(X, default) semantics: set-but-empty aborts, unset
	// uses the stdio default.
	transport, set := c.Source.Get("MCP_TRANSPORT")
	transport = normalizeLower(transport)
	if !set {
		transport = "stdio"
	}
	switch transport {
	case "stdio", "http", "sse":
		c.Transport = transport
	default:
		return fmt.Errorf("invalid MCP_TRANSPORT '%s'. Expected one of: stdio, http, sse.", transport)
	}
	c.Host = strings.TrimSpace(mustGet(c.Source, "MCP_HOST"))
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	portRaw := strings.TrimSpace(mustGet(c.Source, "MCP_PORT"))
	if portRaw == "" {
		portRaw = "8765"
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		return fmt.Errorf("invalid MCP_PORT '%s': must be an integer (default 8765).", portRaw)
	}
	c.Port = port
	c.AllowedHosts = splitComma(mustGet(c.Source, "MCP_ALLOWED_HOSTS"))
	c.AllowedOrigins = splitComma(mustGet(c.Source, "MCP_ALLOWED_ORIGINS"))
	return nil
}

// linkDomain mirrors tools/messages.py: TELEGRAM_LINK_DOMAIN with the
// t.me default (overridable because the .me registry has been put on
// serverHold before).
func linkDomain(src EnvSource) string {
	v := strings.TrimSpace(mustGet(src, "TELEGRAM_LINK_DOMAIN"))
	if v == "" {
		return "t.me"
	}
	return v
}

func normalizeLower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func normalizeLockMode(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		v = "exclusive"
	}
	return v
}

package kit

// This file ports telegram_mcp/transcription.py: the four transcription
// engines (telegram native, groq, openai-compatible, whisper), the
// cache-first single-flight path (transcribe_cached), and the auto-mode batch
// budget (TELEGRAM_TRANSCRIBE_MAX_VOICES / TELEGRAM_TRANSCRIBE_MAX_SECONDS).
//
// Ported semantics:
//   - Transcribe is the merged port of the transcribe_voice tool's flow and
//     transcription.transcribe_cached: pinned-source cache lookup, at most one
//     paid call per (chat, message, engine) under a process-wide keyed lock,
//     then cache write-back. Every user-facing validation string is
//     byte-for-byte the Python one (see EngineConfigError, tooLarge,
//     NormalizeEngine and the engine errors).
//   - engine "telegram" runs Telegram's server-side transcription through an
//     injected NativeTranscriber; entity resolution, the premium check and the
//     raw messages.transcribeAudio call live in the session-layer adapter.
//     Polling while pending=True (10 attempts, 2s apart) is ported here.
//   - engines "groq"/"openai"/"whisper" share one OpenAI-compatible
//     /audio/transcriptions HTTP path (multipart, model, response_format,
//     optional language, optional bearer key); only their settings differ.
//
// Deviations from Python, both deliberate:
//   - Cache: Python stores transcripts in SQLite (cache_dir()/transcripts.db);
//     the Go port cannot add a SQLite driver, so the same rows live in a JSON
//     file (cache_dir()/transcripts.json). Semantics are identical: the key is
//     (chat_id, message_id, source), a save replaces the row, a pinned read
//     never sees another engine's row, an unpinned read prefers the default
//     engine's row and then the newest, and entries never expire (Python's
//     SQLite cache has no TTL either).
//   - KNOWN-GAP whisper: Python runs a local faster-whisper model
//     (TELEGRAM_TRANSCRIBE_WHISPER_MODEL/_DEVICE/_COMPUTE_TYPE/_MODEL_DIR,
//     loaded in process; the audio never leaves the machine). There is no Go
//     equivalent that can be added here without new dependencies, so engine
//     "whisper" maps onto an OpenAI-compatible /audio/transcriptions endpoint
//     configured with TELEGRAM_WHISPER_BASE_URL / TELEGRAM_WHISPER_MODEL /
//     TELEGRAM_WHISPER_DEVICE (which fall back to the
//     TELEGRAM_TRANSCRIBE_WHISPER_* values internal/config already parses).
//     The audio therefore leaves the machine unless that endpoint is itself
//     local. Python's "faster-whisper is not installed" message cannot apply
//     and is replaced by the base-URL validation in EngineConfigError.
//
// Mode and engine-toggle validation (TELEGRAM_TRANSCRIBE=off|on-demand|auto,
// TELEGRAM_TRANSCRIBE_ENGINE) is owned by internal/config, which fails loudly
// at startup exactly like transcription.transcribe_mode/default_engine.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/itokun99/telegram-mcp-go/internal/config"
)

// Engine names, mirroring transcription.ENGINES.
const (
	EngineTelegram = "telegram"
	EngineGroq     = "groq"
	EngineOpenAI   = "openai"
	EngineWhisper  = "whisper"
)

const (
	// GroqTranscribeURL mirrors transcription.GROQ_TRANSCRIBE_URL.
	GroqTranscribeURL = "https://api.groq.com/openai/v1/audio/transcriptions"
	// GroqModel mirrors transcription.GROQ_MODEL.
	GroqModel = "whisper-large-v3-turbo"
	// OpenAIDefaultModel mirrors transcription.OPENAI_DEFAULT_MODEL.
	OpenAIDefaultModel = "whisper-1"
	// WhisperDefaultModel mirrors transcription.WHISPER_DEFAULT_MODEL.
	WhisperDefaultModel = "small"
	// HTTPDefaultTimeout mirrors transcription.HTTP_DEFAULT_TIMEOUT_SECONDS.
	HTTPDefaultTimeout = 120 * time.Second
	// GroqDefaultMaxMB mirrors transcription.GROQ_DEFAULT_MAX_MB. It is also
	// the OpenAI-compatible default (transcription.py passes 25.0 to
	// _env_float for TELEGRAM_TRANSCRIBE_OPENAI_MAX_MB).
	GroqDefaultMaxMB = 25.0
	// NativePollAttempts and NativePollInterval mirror
	// transcription._POLL_ATTEMPTS / _POLL_INTERVAL_SECONDS.
	NativePollAttempts = 10
	NativePollInterval = 2 * time.Second
)

// transcribeEngineNames is the accepted set, sorted like
// "'groq', 'openai', 'telegram', 'whisper'" in messages.py.
var transcribeEngineNames = []string{EngineGroq, EngineOpenAI, EngineTelegram, EngineWhisper}

// NormalizeEngine mirrors the transcribe_voice tool's engine resolution
// `(engine or default_engine()).strip().lower()` plus its validation
// message. An empty raw value falls back to "groq" (the Python default);
// callers that resolved TELEGRAM_TRANSCRIBE_ENGINE should pass that value.
func NormalizeEngine(raw string) (string, error) {
	engine := strings.ToLower(strings.TrimSpace(raw))
	if engine == "" {
		engine = EngineGroq
	}
	for _, accepted := range transcribeEngineNames {
		if engine == accepted {
			return engine, nil
		}
	}
	return "", fmt.Errorf("Invalid engine '%s'. Use one of: 'groq', 'openai', 'telegram', 'whisper'.", raw)
}

// VoiceMessage is one transcribable message (voice note or video note) as the
// engines see it. It is the Go view of the Telethon message attributes
// transcription.py reads: msg.id, msg.file.duration, msg.file.size,
// msg.file.ext, msg.file.mime_type and msg.message (existing text).
type VoiceMessage struct {
	// MessageID is Telegram's message ID (the cache key's second half).
	MessageID int64
	// DurationSeconds is msg.file.duration; nil means unknown, like Python's
	// voice_duration() returning None.
	DurationSeconds *int
	// DeclaredSize is msg.file.size as Telegram states it, in bytes; 0 means
	// unknown. It feeds the pre-download upload-limit check.
	DeclaredSize int64
	// Ext is msg.file.ext (e.g. "oga"); "" defaults to "ogg".
	Ext string
	// MIME is msg.file.mime_type; "" defaults to "audio/ogg".
	MIME string
	// HasText marks a message that already carries text; auto-mode prefetch
	// skips those, mirroring `if getattr(msg, "message", None): continue`.
	HasText bool
}

// VoiceDownloader fetches voice-note bytes in memory (Python's
// cl.download_media(msg, file=bytes)). The session layer implements it over
// gogram's DownloadMedia with a bytes buffer (DownloadOptions.Buffer accepts
// an io.Writer); tests inject fakes.
type VoiceDownloader interface {
	DownloadVoice(ctx context.Context, messageID int64) ([]byte, error)
}

// NativeTranscriber runs Telegram's server-side transcription
// (messages.transcribeAudio). The adapter resolves the peer, checks Premium
// and returns ErrPremiumRequired when the account cannot use it.
type NativeTranscriber interface {
	TranscribeNative(ctx context.Context, messageID int64) (NativeTranscription, error)
}

// NativeTranscription is one messages.transcribeAudio response:
// Pending mirrors result.pending (Telegram is still processing a long
// recording), Text mirrors result.text.
type NativeTranscription struct {
	Text    string
	Pending bool
}

// ErrPremiumRequired mirrors the telegram engine's "premium_required" status:
// the account is not (or no longer) Premium.
var ErrPremiumRequired = errors.New("premium_required")

// ErrBudgetExceeded reports that a TranscribeBudget cannot afford a
// recording. Python has no budget error string: prefetch_transcripts silently
// skips unaffordable recordings and voice_attachment_info renders them as
// "transcript pending" (budget exhaustion is one of the two meanings that
// marker covers). The Go error carries that exact Python marker so a caller
// can render it directly, and errors.Is keeps the cause checkable.
var ErrBudgetExceeded = errors.New("transcript pending")

// TranscribeBudget mirrors transcription.TranscribeBudget: the per-call
// batch budget for auto-mode prefetching. It is not goroutine-safe; one
// budget is charged by one prefetch loop (or one caller), as in Python.
type TranscribeBudget struct {
	maxVoices   int
	maxSeconds  int
	usedVoices  int
	usedSeconds int
}

// NewTranscribeBudget builds a budget with the given caps. Non-positive caps
// fall back to the Python env defaults (5 voices, 300 seconds), matching the
// values internal/config resolves for TELEGRAM_TRANSCRIBE_MAX_VOICES /
// _MAX_SECONDS.
func NewTranscribeBudget(maxVoices, maxSeconds int) *TranscribeBudget {
	if maxVoices <= 0 {
		maxVoices = 5
	}
	if maxSeconds <= 0 {
		maxSeconds = 300
	}
	return &TranscribeBudget{maxVoices: maxVoices, maxSeconds: maxSeconds}
}

// CanAfford mirrors TranscribeBudget.can_afford.
func (b *TranscribeBudget) CanAfford(durationSeconds *int) bool {
	if b.usedVoices >= b.maxVoices {
		return false
	}
	duration := 0
	if durationSeconds != nil {
		duration = *durationSeconds
	}
	return b.usedSeconds+duration <= b.maxSeconds
}

// Charge mirrors TranscribeBudget.charge.
func (b *TranscribeBudget) Charge(durationSeconds *int) {
	b.usedVoices++
	if durationSeconds != nil {
		b.usedSeconds += *durationSeconds
	}
}

// UsedVoices returns the charged voice count.
func (b *TranscribeBudget) UsedVoices() int { return b.usedVoices }

// UsedSeconds returns the charged seconds total.
func (b *TranscribeBudget) UsedSeconds() int { return b.usedSeconds }

// ---------------------------------------------------------------------------
// Cache (JSON-file port of the SQLite cache)
// ---------------------------------------------------------------------------

const (
	transcribeCacheFileName = "transcripts.json"
	transcribeCacheVersion  = 1
)

// CachedTranscript is one stored transcript row.
type CachedTranscript struct {
	Source          string
	Text            string
	DurationSeconds *int
	Lang            string
	CreatedAt       time.Time
}

type cacheEntry struct {
	ChatID    int64  `json:"chat_id"`
	MessageID int64  `json:"message_id"`
	Source    string `json:"source"`
	Text      string `json:"text"`
	Duration  *int   `json:"duration,omitempty"`
	Lang      string `json:"lang,omitempty"`
	CreatedAt string `json:"created_at"`
}

type transcribeCacheFile struct {
	Version int          `json:"version"`
	Entries []cacheEntry `json:"entries"`
}

// TranscribeCache stores transcripts the way transcription.get_cached_
// transcript / save_transcript do, but in cache_dir()/transcripts.json
// instead of transcripts.db (no SQLite driver may be added). The key is
// (chat_id, message_id, source), a save replaces the row, and entries never
// expire - Python's cache has no TTL either. Concurrent users within one
// process should share an instance; Transcribe uses a package-level instance
// per directory when none is supplied.
type TranscribeCache struct {
	// Dir is the cache directory (TELEGRAM_TRANSCRIPT_CACHE_DIR); "" means
	// the Python default "data/transcripts".
	Dir string

	mu      sync.Mutex
	entries map[string]cacheEntry
	loaded  bool
}

// NewTranscribeCache builds a cache rooted at dir ("" -> data/transcripts).
// The file is created lazily by Save, exactly like SQLite creates the table
// on first connect.
func NewTranscribeCache(dir string) *TranscribeCache {
	if strings.TrimSpace(dir) == "" {
		dir = "data/transcripts"
	}
	return &TranscribeCache{Dir: dir, entries: map[string]cacheEntry{}}
}

func (c *TranscribeCache) path() string {
	return filepath.Join(c.Dir, transcribeCacheFileName)
}

func transcribeCacheKey(chatID, messageID int64, source string) string {
	return fmt.Sprintf("%d:%d:%s", chatID, messageID, source)
}

// loadLocked reads the JSON file once. A missing file is an empty cache.
func (c *TranscribeCache) loadLocked() error {
	if c.loaded {
		return nil
	}
	if c.entries == nil {
		c.entries = map[string]cacheEntry{}
	}
	if strings.TrimSpace(c.Dir) == "" {
		c.Dir = "data/transcripts"
	}
	// cache_dir() creates the directory on every access, mode 700.
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return fmt.Errorf("creating transcript cache dir: %w", err)
	}
	_ = os.Chmod(c.Dir, 0o700) // best effort, like Python's try/except

	data, err := os.ReadFile(c.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.loaded = true
			return nil
		}
		return fmt.Errorf("reading transcript cache: %w", err)
	}
	var file transcribeCacheFile
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &file); err != nil {
			return fmt.Errorf("corrupt transcript cache %s: %w", c.path(), err)
		}
	}
	for _, entry := range file.Entries {
		c.entries[transcribeCacheKey(entry.ChatID, entry.MessageID, entry.Source)] = entry
	}
	c.loaded = true
	return nil
}

// Get mirrors transcription.get_cached_transcript. source pins the engine (a
// pinned read never returns another engine's row). source "" mirrors
// source=None: the defaultEngine row wins, then the newest created_at. The
// second return reports whether a row was found.
func (c *TranscribeCache) Get(chatID, messageID int64, source, defaultEngine string) (CachedTranscript, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return CachedTranscript{}, false, err
	}
	if source != "" {
		entry, ok := c.entries[transcribeCacheKey(chatID, messageID, source)]
		if !ok {
			return CachedTranscript{}, false, nil
		}
		return entry.cached(), true, nil
	}
	if defaultEngine == "" {
		defaultEngine = EngineGroq
	}
	var best cacheEntry
	found := false
	for _, entry := range c.entries {
		if entry.ChatID != chatID || entry.MessageID != messageID {
			continue
		}
		if !found || entryPreferred(entry, best, defaultEngine) {
			best = entry
			found = true
		}
	}
	if !found {
		return CachedTranscript{}, false, nil
	}
	return best.cached(), true, nil
}

// entryPreferred implements ORDER BY source = <default> DESC,
// created_at DESC for two rows of the same message.
func entryPreferred(candidate, current cacheEntry, defaultEngine string) bool {
	candidateDefault := candidate.Source == defaultEngine
	currentDefault := current.Source == defaultEngine
	if candidateDefault != currentDefault {
		return candidateDefault
	}
	return candidate.createdTime().After(current.createdTime())
}

// Save mirrors transcribe_cache.save_transcript: INSERT OR REPLACE keyed by
// (chat_id, message_id, source), with created_at = now UTC.
func (c *TranscribeCache) Save(chatID, messageID int64, source, text string, durationSeconds *int, lang string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return err
	}
	var duration *int
	if durationSeconds != nil {
		value := *durationSeconds
		duration = &value
	}
	c.entries[transcribeCacheKey(chatID, messageID, source)] = cacheEntry{
		ChatID:    chatID,
		MessageID: messageID,
		Source:    source,
		Text:      text,
		Duration:  duration,
		Lang:      lang,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	return c.persistLocked()
}

// persistLocked writes the whole cache atomically (temp file + rename), mode
// 600 like the Python DB file.
func (c *TranscribeCache) persistLocked() error {
	entries := make([]cacheEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ChatID != entries[j].ChatID {
			return entries[i].ChatID < entries[j].ChatID
		}
		if entries[i].MessageID != entries[j].MessageID {
			return entries[i].MessageID < entries[j].MessageID
		}
		return entries[i].Source < entries[j].Source
	})
	data, err := json.MarshalIndent(transcribeCacheFile{Version: transcribeCacheVersion, Entries: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding transcript cache: %w", err)
	}
	tmp, err := os.CreateTemp(c.Dir, "transcripts-*.json.tmp")
	if err != nil {
		return fmt.Errorf("writing transcript cache: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing transcript cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing transcript cache: %w", err)
	}
	_ = os.Chmod(tmpName, 0o600)
	if err := os.Rename(tmpName, c.path()); err != nil {
		// Windows refuses rename over an existing file; retry once without it.
		if removeErr := os.Remove(c.path()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("writing transcript cache: %w", err)
		}
		if err := os.Rename(tmpName, c.path()); err != nil {
			return fmt.Errorf("writing transcript cache: %w", err)
		}
	}
	_ = os.Chmod(c.path(), 0o600) // best effort
	return nil
}

func (e cacheEntry) createdTime() time.Time {
	t, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (e cacheEntry) cached() CachedTranscript {
	var duration *int
	if e.Duration != nil {
		value := *e.Duration
		duration = &value
	}
	return CachedTranscript{
		Source:          e.Source,
		Text:            e.Text,
		DurationSeconds: duration,
		Lang:            e.Lang,
		CreatedAt:       e.createdTime(),
	}
}

// ---------------------------------------------------------------------------
// Engine settings and validation
// ---------------------------------------------------------------------------

// EngineConfigError mirrors transcription.engine_config_error: why engine
// cannot run with the current settings, or "" when it can. Checked before any
// download, so a misconfigured engine costs nothing. The groq and openai
// messages are byte-for-byte Python's; the whisper message replaces Python's
// "faster-whisper is not installed" (see the KNOWN-GAP note at the top).
func EngineConfigError(engine string, settings config.TranscribeSettings, env config.EnvSource) string {
	switch engine {
	case EngineGroq:
		if settings.GroqAPIKey == "" {
			return "GROQ_API_KEY is not configured on this server. Use another engine or set GROQ_API_KEY."
		}
	case EngineOpenAI:
		if strings.TrimSpace(settings.OpenAIURL) == "" {
			return "TELEGRAM_TRANSCRIBE_OPENAI_URL is not configured on this server. Set it to the base URL of an OpenAI-compatible API (e.g. https://api.openai.com/v1)."
		}
	case EngineWhisper:
		base, _, device := whisperSettings(env, settings)
		if base == "" {
			return "TELEGRAM_WHISPER_BASE_URL is not configured on this server. Set it to the base URL of an OpenAI-compatible whisper endpoint (e.g. http://localhost:8000/v1)."
		}
		switch device {
		case "auto", "cpu", "cuda":
		default:
			return fmt.Sprintf("Invalid TELEGRAM_WHISPER_DEVICE '%s'. Expected one of: auto, cpu, cuda.", device)
		}
	}
	return ""
}

// whisperSettings resolves the KNOWN-GAP whisper mapping: the
// TELEGRAM_WHISPER_* variables win, then the TELEGRAM_TRANSCRIBE_WHISPER_*
// values internal/config parsed, then the Python defaults.
func whisperSettings(env config.EnvSource, settings config.TranscribeSettings) (base, model, device string) {
	base = lookupEnv(env, "TELEGRAM_WHISPER_BASE_URL")
	model = lookupEnv(env, "TELEGRAM_WHISPER_MODEL")
	if model == "" {
		model = settings.WhisperModel
	}
	if model == "" {
		model = WhisperDefaultModel
	}
	device = lookupEnv(env, "TELEGRAM_WHISPER_DEVICE")
	if device == "" {
		device = settings.WhisperDevice
	}
	if device == "" {
		device = "auto"
	}
	return base, model, device
}

func lookupEnv(env config.EnvSource, key string) string {
	if env == nil {
		env = config.OS{}
	}
	value, _ := env.Get(key)
	return strings.TrimSpace(value)
}

// endpoint mirrors transcription._endpoint: accept either a base URL or the
// full /audio/transcriptions URL.
func endpoint(base string) string {
	url := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(url, "/audio/transcriptions") {
		return url
	}
	return url + "/audio/transcriptions"
}

// httpEngineConfig is the Go port of transcription.HttpEngineConfig.
type httpEngineConfig struct {
	engine         string
	label          string
	url            string
	apiKey         string
	model          string
	responseFormat string
	maxBytes       int64
	maxMBEnv       string
	timeout        time.Duration
}

// buildHTTPEngineConfig mirrors transcription.http_engine_config for the
// three HTTP engines. Whisper has no response-format or size limit in Python
// (the local model reads the bytes directly), so it uses "json" and no limit.
func buildHTTPEngineConfig(engine string, opts TranscribeOptions) httpEngineConfig {
	settings := opts.Settings
	timeout := HTTPDefaultTimeout
	if settings.TimeoutSeconds > 0 {
		timeout = time.Duration(settings.TimeoutSeconds * float64(time.Second))
	}
	switch engine {
	case EngineGroq:
		url := strings.TrimSpace(opts.GroqURL)
		if url == "" {
			url = GroqTranscribeURL
		} else {
			url = endpoint(url)
		}
		maxMB := settings.GroqMaxMB
		if maxMB <= 0 {
			maxMB = GroqDefaultMaxMB
		}
		return httpEngineConfig{
			engine:         EngineGroq,
			label:          "Groq",
			url:            url,
			apiKey:         settings.GroqAPIKey,
			model:          GroqModel,
			responseFormat: "verbose_json",
			maxBytes:       int64(maxMB * 1048576),
			maxMBEnv:       "TELEGRAM_TRANSCRIBE_GROQ_MAX_MB",
			timeout:        timeout,
		}
	case EngineOpenAI:
		model := strings.TrimSpace(settings.OpenAIModel)
		if model == "" {
			model = OpenAIDefaultModel
		}
		maxMB := settings.OpenAIMaxMB
		if maxMB <= 0 {
			maxMB = GroqDefaultMaxMB
		}
		return httpEngineConfig{
			engine:         EngineOpenAI,
			label:          "OpenAI-compatible",
			url:            endpoint(settings.OpenAIURL),
			apiKey:         settings.OpenAIAPIKey,
			model:          model,
			responseFormat: "json",
			maxBytes:       int64(maxMB * 1048576),
			maxMBEnv:       "TELEGRAM_TRANSCRIBE_OPENAI_MAX_MB",
			timeout:        timeout,
		}
	case EngineWhisper:
		base, model, _ := whisperSettings(opts.Env, settings)
		return httpEngineConfig{
			engine:         EngineWhisper,
			label:          "Whisper",
			url:            endpoint(base),
			model:          model,
			responseFormat: "json",
			timeout:        timeout,
		}
	}
	return httpEngineConfig{engine: engine, timeout: timeout}
}

// ---------------------------------------------------------------------------
// The single-record transcribe path
// ---------------------------------------------------------------------------

// TranscribeOptions configures one Transcribe call. Settings is the
// parsed internal/config transcription block; the Telegram-facing client
// surfaces are injected.
type TranscribeOptions struct {
	// Engine is the chosen engine (telegram|groq|openai|whisper); "" falls
	// back to "groq". Callers normally pass Config.TranscribeEngine.
	Engine string
	// ChatID is the marked chat ID; (ChatID, Message.MessageID, engine) keys
	// the cache.
	ChatID int64
	// Message describes the voice/video note.
	Message VoiceMessage
	// Downloader fetches the audio for the HTTP engines.
	Downloader VoiceDownloader
	// Native runs Telegram's server-side transcription (engine "telegram").
	Native NativeTranscriber
	// Settings carries language, timeouts, keys, URLs, upload limits and the
	// cache directory.
	Settings config.TranscribeSettings
	// Env resolves the TELEGRAM_WHISPER_* variables; nil means the process
	// environment.
	Env config.EnvSource
	// Cache is the transcript cache; nil uses a package-level cache per
	// Settings.CacheDir. Concurrent callers should share one instance.
	Cache *TranscribeCache
	// Budget, when non-nil, caps this process's auto-mode spend. On a cache
	// miss with no budget left, Transcribe returns ErrBudgetExceeded.
	Budget *TranscribeBudget
	// GroqURL overrides the Groq endpoint ("" = the production Groq URL);
	// used by tests and Groq-compatible self-hosted deployments.
	GroqURL string
	// HTTPClient overrides the HTTP client (nil = a client with the
	// configured timeout).
	HTTPClient *http.Client
	// NativePollAttempts and NativePollInterval override the pending-poll
	// loop; zero values use Python's 10 attempts / 2 seconds.
	NativePollAttempts int
	NativePollInterval time.Duration
}

// TranscribeResult is one transcript plus its provenance. Source is the
// engine that produced the text; callers must surface it alongside Text -
// every transcript is a machine reading, not a verbatim quote.
type TranscribeResult struct {
	Text            string
	Source          string
	Lang            string
	DurationSeconds *int
	// Cached reports whether the answer came from the cache (no engine call).
	Cached bool
	// Pending reports that Telegram is still processing the recording
	// (engine "telegram" exhausted its poll attempts).
	Pending bool
}

// Transcribe transcribes one voice/video note, cache-first, under a
// process-wide lock keyed by (chat, message, engine) so concurrent callers
// pay for at most one engine call. Validation and budget errors carry the
// Python strings documented above.
func Transcribe(ctx context.Context, opts TranscribeOptions) (TranscribeResult, error) {
	engine, err := NormalizeEngine(opts.Engine)
	if err != nil {
		return TranscribeResult{}, err
	}
	cache := opts.Cache
	if cache == nil {
		cache = defaultTranscribeCache(opts.Settings.CacheDir)
	}

	hit, ok, err := cache.Get(opts.ChatID, opts.Message.MessageID, engine, engine)
	if err != nil {
		return TranscribeResult{}, err
	}
	if ok {
		return cachedResult(hit), nil
	}

	// Python checks engine_config_error before any download.
	if message := EngineConfigError(engine, opts.Settings, opts.Env); message != "" {
		return TranscribeResult{}, errors.New(message)
	}

	unlock := transcribeInflight.lock(transcribeCacheKey(opts.ChatID, opts.Message.MessageID, engine))
	defer unlock()

	// Re-read: a concurrent caller may have paid for this one while we were
	// waiting for the lock.
	hit, ok, err = cache.Get(opts.ChatID, opts.Message.MessageID, engine, engine)
	if err != nil {
		return TranscribeResult{}, err
	}
	if ok {
		return cachedResult(hit), nil
	}

	if opts.Budget != nil {
		if !opts.Budget.CanAfford(opts.Message.DurationSeconds) {
			return TranscribeResult{}, ErrBudgetExceeded
		}
		opts.Budget.Charge(opts.Message.DurationSeconds)
	}

	result, err := transcribeWithEngine(ctx, engine, opts)
	if err != nil {
		return result, err
	}
	if result.Pending {
		return result, nil
	}
	if result.DurationSeconds == nil {
		result.DurationSeconds = opts.Message.DurationSeconds
	}
	if err := cache.Save(opts.ChatID, opts.Message.MessageID, engine, result.Text, result.DurationSeconds, result.Lang); err != nil {
		return result, err
	}
	return result, nil
}

func cachedResult(hit CachedTranscript) TranscribeResult {
	return TranscribeResult{
		Text:            hit.Text,
		Source:          hit.Source,
		Lang:            hit.Lang,
		DurationSeconds: hit.DurationSeconds,
		Cached:          true,
	}
}

// transcribeWithEngine mirrors transcription.transcribe's dispatch, including
// its "Unknown engine '%s'" fallback (NormalizeEngine normally rejects first).
func transcribeWithEngine(ctx context.Context, engine string, opts TranscribeOptions) (TranscribeResult, error) {
	if err := ctx.Err(); err != nil {
		return TranscribeResult{}, err
	}
	switch engine {
	case EngineTelegram:
		return transcribeViaNative(ctx, opts)
	case EngineGroq, EngineOpenAI, EngineWhisper:
		return transcribeViaHTTP(ctx, opts, buildHTTPEngineConfig(engine, opts))
	default:
		return TranscribeResult{}, fmt.Errorf("Unknown engine '%s'", engine)
	}
}

// transcribeViaNative mirrors transcription._transcribe_via_telegram: one
// call, then up to 10 polls (2s apart) while pending=True.
func transcribeViaNative(ctx context.Context, opts TranscribeOptions) (TranscribeResult, error) {
	if opts.Native == nil {
		return TranscribeResult{}, errors.New("native transcriber is not configured")
	}
	attempts := opts.NativePollAttempts
	if attempts <= 0 {
		attempts = NativePollAttempts
	}
	interval := opts.NativePollInterval
	if interval <= 0 {
		interval = NativePollInterval
	}

	result, err := opts.Native.TranscribeNative(ctx, opts.Message.MessageID)
	if err != nil {
		return TranscribeResult{Source: EngineTelegram}, err
	}
	polls := 0
	for result.Pending && polls < attempts {
		if err := sleepContext(ctx, interval); err != nil {
			return TranscribeResult{Source: EngineTelegram}, err
		}
		polls++
		result, err = opts.Native.TranscribeNative(ctx, opts.Message.MessageID)
		if err != nil {
			return TranscribeResult{Source: EngineTelegram}, err
		}
	}
	if result.Pending {
		return TranscribeResult{Source: EngineTelegram, Pending: true}, nil
	}
	return TranscribeResult{Text: result.Text, Source: EngineTelegram}, nil
}

// sleepContext waits for d or ctx cancellation, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// transcribeViaHTTP mirrors _transcribe_via_openai_compatible: download
// (with the upload-limit checks), POST multipart, read text/language.
func transcribeViaHTTP(ctx context.Context, opts TranscribeOptions, cfg httpEngineConfig) (TranscribeResult, error) {
	if opts.Downloader == nil {
		return TranscribeResult{}, errors.New("voice downloader is not configured")
	}
	data, err := downloadAudio(ctx, opts.Downloader, opts.Message, cfg)
	if err != nil {
		return TranscribeResult{}, err
	}
	language := opts.Settings.Language
	payload, err := postAudio(ctx, opts.httpClient(cfg.timeout), cfg, data, opts.Message, language)
	if err != nil {
		return TranscribeResult{}, err
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return TranscribeResult{}, fmt.Errorf("empty transcript from %s", cfg.engine)
	}
	lang := payload.Language
	if lang == "" {
		lang = language
	}
	return TranscribeResult{Text: text, Source: cfg.engine, Lang: lang}, nil
}

// httpClient returns the injected client or one bounded by timeout, like
// httpx.AsyncClient(timeout=cfg.timeout).
func (o TranscribeOptions) httpClient(timeout time.Duration) *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: timeout}
}

// downloadAudio mirrors transcription._download_audio: the declared size is
// checked before downloading, the actual bytes again afterwards.
func downloadAudio(ctx context.Context, downloader VoiceDownloader, msg VoiceMessage, cfg httpEngineConfig) ([]byte, error) {
	if cfg.maxBytes > 0 && msg.DeclaredSize > cfg.maxBytes {
		return nil, tooLarge(msg.DeclaredSize, cfg)
	}
	data, err := downloader.DownloadVoice(ctx, msg.MessageID)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("empty media download")
	}
	if cfg.maxBytes > 0 && int64(len(data)) > cfg.maxBytes {
		return nil, tooLarge(int64(len(data)), cfg)
	}
	return data, nil
}

// tooLarge mirrors transcription._too_large, message included.
func tooLarge(size int64, cfg httpEngineConfig) error {
	return fmt.Errorf(
		"recording is %.1f MB, above the %.0f MB %s upload limit. Transcribe it with "+
			"engine='telegram' or engine='whisper', or raise %s if the %s endpoint accepts "+
			"larger uploads.",
		float64(size)/1048576.0, float64(cfg.maxBytes)/1048576.0, cfg.label, cfg.maxMBEnv, cfg.label,
	)
}

// uploadName mirrors transcription._upload_name: Telegram voice notes carry
// 'oga' (Opus-in-Ogg), which Groq and OpenAI reject with 400
// unsupported_audio_format; the same bytes are named .ogg.
func uploadName(msg VoiceMessage) (filename, mime string) {
	ext := strings.TrimLeft(msg.Ext, ".")
	if ext == "" {
		ext = "ogg"
	}
	if strings.EqualFold(ext, "oga") {
		ext = "ogg"
	}
	mime = msg.MIME
	if mime == "" {
		mime = "audio/ogg"
	}
	return "voice." + ext, mime
}

// transcriptionPayload is the OpenAI-compatible response body subset.
type transcriptionPayload struct {
	Text     string `json:"text"`
	Language string `json:"language"`
}

// postAudio sends the multipart /audio/transcriptions request and decodes the
// JSON body. Every failure mirrors Python's f"{engine} request failed: {e}".
func postAudio(ctx context.Context, client *http.Client, cfg httpEngineConfig, data []byte, msg VoiceMessage, language string) (transcriptionPayload, error) {
	var payload transcriptionPayload
	fail := func(err error) (transcriptionPayload, error) {
		return payload, fmt.Errorf("%s request failed: %w", cfg.engine, err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", cfg.model); err != nil {
		return fail(err)
	}
	if err := writer.WriteField("response_format", cfg.responseFormat); err != nil {
		return fail(err)
	}
	if language != "" {
		if err := writer.WriteField("language", language); err != nil {
			return fail(err)
		}
	}
	filename, mime := uploadName(msg)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	header.Set("Content-Type", mime)
	part, err := writer.CreatePart(header)
	if err != nil {
		return fail(err)
	}
	if _, err := part.Write(data); err != nil {
		return fail(err)
	}
	if err := writer.Close(); err != nil {
		return fail(err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.url, &body)
	if err != nil {
		return fail(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if cfg.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return fail(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return fail(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return payload, fmt.Errorf("%s request failed: HTTP %d", cfg.engine, response.StatusCode)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fail(err)
	}
	return payload, nil
}

// ---------------------------------------------------------------------------
// Concurrency: one paid call per recording
// ---------------------------------------------------------------------------

var transcribeInflight = &inflightLocks{entries: map[string]*inflightLock{}}

// inflightLocks mirrors transcription._INFLIGHT_LOCKS: a ref-counted mutex
// per (chat, message, engine), dropped once the last waiter leaves so the
// registry does not grow with every message ever transcribed.
type inflightLocks struct {
	mu      sync.Mutex
	entries map[string]*inflightLock
}

type inflightLock struct {
	mu   sync.Mutex
	refs int
}

// lock returns the release function for key.
func (l *inflightLocks) lock(key string) func() {
	l.mu.Lock()
	entry := l.entries[key]
	if entry == nil {
		entry = &inflightLock{}
		l.entries[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		l.mu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(l.entries, key)
		}
		l.mu.Unlock()
	}
}

// defaultTranscribeCache returns the shared cache for dir, so concurrent
// calls that did not pass an explicit cache still see each other's writes
// (Python reads one SQLite database from every call).
func defaultTranscribeCache(dir string) *TranscribeCache {
	defaultCachesMu.Lock()
	defer defaultCachesMu.Unlock()
	cache := defaultCaches[dir]
	if cache == nil {
		cache = NewTranscribeCache(dir)
		defaultCaches[dir] = cache
	}
	return cache
}

var (
	defaultCachesMu sync.Mutex
	defaultCaches   = map[string]*TranscribeCache{}
)

// ---------------------------------------------------------------------------
// Auto-mode prefetch
// ---------------------------------------------------------------------------

// PrefetchOptions configures PrefetchTranscripts. TranscribeOptions carries
// the engine, clients, settings and env; Message is ignored (each entry of
// Messages supplies its own).
type PrefetchOptions struct {
	// Mode is TELEGRAM_TRANSCRIBE; only "auto" prefetches.
	Mode string
	// ChatID is the marked chat ID of every message in Messages.
	ChatID int64
	// Messages are the voice/video notes of one listing, in order.
	Messages []VoiceMessage
	TranscribeOptions
}

// PrefetchTranscripts mirrors transcription.prefetch_transcripts: fill the
// cache for cache-miss voice/video notes within the TranscribeBudget, in
// order, silently skipping engine failures. Only TELEGRAM_TRANSCRIBE=auto
// does any work; on-demand and off return immediately.
func PrefetchTranscripts(ctx context.Context, opts PrefetchOptions) {
	if opts.Mode != "auto" {
		return
	}
	engine, err := NormalizeEngine(opts.TranscribeOptions.Engine)
	if err != nil {
		return
	}
	budget := NewTranscribeBudget(opts.Settings.MaxVoices, opts.Settings.MaxSeconds)
	cache := opts.Cache
	if cache == nil {
		cache = defaultTranscribeCache(opts.Settings.CacheDir)
	}
	for _, msg := range opts.Messages {
		if msg.HasText {
			continue // already has text, nothing to fill
		}
		if _, ok, err := cache.Get(opts.ChatID, msg.MessageID, "", engine); err != nil {
			return
		} else if ok {
			continue
		}
		if !budget.CanAfford(msg.DurationSeconds) {
			continue
		}
		budget.Charge(msg.DurationSeconds)
		callOpts := opts.TranscribeOptions
		callOpts.ChatID = opts.ChatID
		callOpts.Message = msg
		callOpts.Cache = cache
		callOpts.Budget = nil // the loop charged the budget already
		// Shares the lock with Transcribe: a prefetch and an explicit call for
		// the same recording pay for it once between them. Python swallows
		// per-message engine failures the same way.
		_, _ = Transcribe(ctx, callOpts)
		if ctx.Err() != nil {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Render helpers shared by message formatting
// ---------------------------------------------------------------------------

// FormatDuration mirrors transcription.format_duration: "?:??" for an
// unknown duration, h:mm:ss past an hour, m:ss otherwise.
func FormatDuration(seconds *int) string {
	if seconds == nil {
		return "?:??"
	}
	value := *seconds
	hours := value / 3600
	rem := value % 3600
	minutes := rem / 60
	secs := rem % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%d:%02d", minutes, secs)
}

// VoiceAttachmentInfo is the transcript view of one voice/video note, as
// voice_attachment_info assembles it. TranscriptStatus is "ready",
// "pending", or "" when transcription is off or no chat ID is known.
type VoiceAttachmentInfo struct {
	DurationSeconds  *int
	Transcript       string
	TranscriptSource string
	TranscriptStatus string
}

// RenderVoiceText mirrors transcription.render_voice_text: never presented as
// a verbatim quote.
func RenderVoiceText(info VoiceAttachmentInfo) string {
	label := "voice " + FormatDuration(info.DurationSeconds)
	switch info.TranscriptStatus {
	case "ready":
		return fmt.Sprintf("%s | transcript (%s, not verbatim): %s", label, info.TranscriptSource, info.Transcript)
	case "pending":
		return label + " | transcript pending"
	default:
		return label
	}
}

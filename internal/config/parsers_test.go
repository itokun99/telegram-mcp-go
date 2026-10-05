package config

import (
	"reflect"
	"testing"
)

func TestExposedToolsDefaultAll(t *testing.T) {
	c := loadValid(t)
	if c.ExposedMode != "all" || len(c.ExposedAllowlist) != 0 {
		t.Fatalf("default exposure = %q %v, want all / empty", c.ExposedMode, c.ExposedAllowlist)
	}
}

func TestExposedToolsReadOnly(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "READ-ONLY"
	c := loadValidFrom(t, env)
	if c.ExposedMode != "read-only" {
		t.Fatalf("mode = %q, want read-only (case-insensitive)", c.ExposedMode)
	}
}

func TestExposedToolsReadOnlyPlusNames(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "read-only+ send_message ,reply_to_message , send_file"
	c := loadValidFrom(t, env)
	want := []string{"send_message", "reply_to_message", "send_file"}
	if !reflect.DeepEqual(c.ExposedAllowlist, want) {
		t.Fatalf("allowlist = %v, want %v", c.ExposedAllowlist, want)
	}
	if c.ExposedMode != "read-only" {
		t.Fatalf("mode = %q, want read-only", c.ExposedMode)
	}
}

func TestExposedToolsUnknownModeAborts(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "write"
	mustFail(t, env, "invalid TELEGRAM_EXPOSED_TOOLS")
}

func TestExposedToolsPlusOnAllAborts(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "all+send_message"
	mustFail(t, env, "only valid with read-only")
}

func TestExposedToolsEmptyAllowlistAborts(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "read-only+"
	mustFail(t, env, "must name at least one tool")
}

func TestExposedToolsSetButEmptyAborts(t *testing.T) {
	// os.getenv("TELEGRAM_EXPOSED_TOOLS", "all"): only unset -> "all";
	// set-but-empty is not an accepted mode and aborts.
	env := validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "   "
	mustFail(t, env, "invalid TELEGRAM_EXPOSED_TOOLS")
}

func TestValidateExposedToolsUnknownNameAborts(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "read-only+send_message,not_a_tool"
	c := loadValidFrom(t, env)
	// All names registered: no abort.
	if err := c.ValidateExposedTools([]string{"send_message", "not_a_tool"}); err != nil {
		t.Fatalf("all names registered must pass, got: %v", err)
	}
	// A typo'd name aborts.
	env = validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "read-only+send_messge"
	c = loadValidFrom(t, env)
	err := c.ValidateExposedTools([]string{"send_message"})
	if err == nil {
		t.Fatal("unknown tool name must abort startup")
	}
	if err.Error() == "" {
		t.Fatal("abort error must be non-empty")
	}
}

func TestFileExtensionsOverride(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_FILE_EXTENSIONS"] = "send_file: .pdf , png;upload_file:.jpg,.jpeg,.png,.webp"
	c := loadValidFrom(t, env)
	// The override replaces the tool's whole set (dot optional, case-insensitive).
	if !reflect.DeepEqual(c.ExtensionAllowlists["send_file"], []string{".pdf", ".png"}) {
		t.Fatalf("send_file = %v, want [.pdf .png]", c.ExtensionAllowlists["send_file"])
	}
	if !reflect.DeepEqual(c.ExtensionOverrides["upload_file"], []string{".jpeg", ".jpg", ".png", ".webp"}) {
		t.Fatalf("upload_file override = %v, want [.jpg .jpeg .png .webp]", c.ExtensionOverrides["upload_file"])
	}
	// A tool with a hardcoded default keeps it unless overridden.
	if !reflect.DeepEqual(c.ExtensionAllowlists["send_voice"], []string{".ogg", ".opus"}) {
		t.Fatalf("send_voice default lost: %v", c.ExtensionAllowlists["send_voice"])
	}
	if !reflect.DeepEqual(c.ExtensionAllowlists["send_sticker"], []string{".webp"}) {
		t.Fatalf("send_sticker default must survive other tools' overrides: %v", c.ExtensionAllowlists["send_sticker"])
	}
}

func TestFileExtensionsMalformedAborts(t *testing.T) {
	cases := []string{
		"send_file pdf",        // missing ':'
		"send_file:.bad*ext",   // bad extension token
		"send_file:.pdf,,png",  // empty extension entry
		"a:.pdf; b:.png; a:.x", // same tool twice
	}
	for _, tc := range cases {
		env := validEnv()
		env["TELEGRAM_FILE_EXTENSIONS"] = tc
		_, err := Load(NewInMemory(env))
		if err == nil {
			t.Fatalf("malformed entry %q must abort", tc)
		}
	}
}

func TestFileExtensionsCaseInsensitiveDotOptional(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_FILE_EXTENSIONS"] = "SEND_FILE:PDF,Jpeg"
	c := loadValidFrom(t, env)
	want := []string{".jpeg", ".pdf"}
	if !reflect.DeepEqual(c.ExtensionAllowlists["send_file"], want) {
		t.Fatalf("send_file = %v, want %v (tool+ext lower-cased, dot added)", c.ExtensionAllowlists["send_file"], want)
	}
}

func TestValidateFileExtensionsUnknownToolAborts(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_FILE_EXTENSIONS"] = "ghost_tool:.pdf"
	c := loadValidFrom(t, env)
	if err := c.ValidateFileExtensions([]string{"send_file"}); err == nil {
		t.Fatal("unknown tool in TELEGRAM_FILE_EXTENSIONS must abort")
	}
	if err := c.ValidateFileExtensions(nil); err == nil {
		// No registered tools at all -> unknown.
		t.Fatal("nil registered set with an override must abort")
	}
}

func TestParseAllowedRootsDriveLetters(t *testing.T) {
	// Each pair is (raw, Python-source-verified expectation from
	// runtime._parse_allowed_roots_env).
	cases := []struct {
		raw  string
		want []string
	}{
		{`C:\Users\x D:\Data;E:share`, []string{`C:\Users\x D:\Data`, `E:share`}},
		{"a:b:c", []string{"a:b:c"}},
		{"/x:y/z", []string{"/x:y/z"}},
		{` "/usr/share" , 'a b' , /tmp `, []string{"/usr/share", "a b", "/tmp"}},
		{"path1;path2,path3", []string{"path1", "path2", "path3"}},
		{"C:/one:C:/two", []string{"C:/one", "C:/two"}},
		{"abc:def", []string{"abc", "def"}},
		{"a b:c", []string{"a b:c"}},
		{`C\foo:D\bar`, []string{`C\foo`, `D\bar`}},
		{"a:b", []string{"a:b"}},
		{"9abc:def", []string{"9abc", "def"}},
		{"xabc:def", []string{"xabc", "def"}},
		{"abc9:def", []string{"abc9", "def"}},
		{"_abc:def", []string{"_abc", "def"}},
		{"abc_:def", []string{"abc_", "def"}},
		{"", nil},
	}
	for _, tc := range cases {
		got := parseAllowedRoots(tc.raw)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseAllowedRoots(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestAllowedRootsEndToEnd(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_ALLOWED_ROOTS"] = `C:\Data;/srv/telemedia`
	env["TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK"] = "1"
	env["TELEGRAM_SERVER_ROOTS_ONLY"] = "true"
	env["TELEGRAM_ROOTS_TIMEOUT_SECONDS"] = "25"
	c := loadValidFrom(t, env)
	want := []string{`C:\Data`, "/srv/telemedia"}
	if !reflect.DeepEqual(c.AllowedRoots, want) {
		t.Fatalf("AllowedRoots = %v, want %v", c.AllowedRoots, want)
	}
	if !c.AllowServerRootsFallback || !c.ServerRootsOnly {
		t.Fatalf("roots toggles = %v/%v, want both true", c.AllowServerRootsFallback, c.ServerRootsOnly)
	}
	if c.RootsRequestTimeout == nil || *c.RootsRequestTimeout != 25 {
		t.Fatalf("RootsRequestTimeout = %v, want 25", c.RootsRequestTimeout)
	}
}

func TestRootsTimeoutZeroMeansUnlimited(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_ROOTS_TIMEOUT_SECONDS"] = "0"
	c := loadValidFrom(t, env)
	if c.RootsRequestTimeout != nil {
		t.Fatalf("timeout 0 must mean wait-forever (nil), got %v", *c.RootsRequestTimeout)
	}
}

func TestToolTimeoutDefaultsAndUnlimited(t *testing.T) {
	c := loadValid(t)
	if c.ToolTimeoutSeconds != 55 || c.ToolTimeoutUnlimited {
		t.Fatalf("default = %v/unlimited=%v, want 55/false", c.ToolTimeoutSeconds, c.ToolTimeoutUnlimited)
	}

	env := validEnv()
	env["TELEGRAM_TOOL_TIMEOUT_SECONDS"] = "0"
	c = loadValidFrom(t, env)
	if !c.ToolTimeoutUnlimited {
		t.Fatal("timeout 0 must flag an unbounded operator session")
	}

	env = validEnv()
	env["TELEGRAM_TOOL_TIMEOUT_SECONDS"] = "garbage"
	c = loadValidFrom(t, env)
	if c.ToolTimeoutSeconds != 55 {
		t.Fatalf("non-numeric timeout must fall back to 55, got %v", c.ToolTimeoutSeconds)
	}
}

func TestFloodSleepThreshold(t *testing.T) {
	c := loadValid(t)
	if c.FloodSleepThreshold != 60 {
		t.Fatalf("default flood threshold = %d, want 60", c.FloodSleepThreshold)
	}
	env := validEnv()
	env["TELEGRAM_FLOOD_SLEEP_THRESHOLD"] = "-5"
	c = loadValidFrom(t, env)
	if c.FloodSleepThreshold != 0 {
		t.Fatalf("negative value clamps to 0, got %d", c.FloodSleepThreshold)
	}
	env["TELEGRAM_FLOOD_SLEEP_THRESHOLD"] = "nonsense"
	c = loadValidFrom(t, env)
	if c.FloodSleepThreshold != 60 {
		t.Fatalf("malformed value falls back to 60, got %d", c.FloodSleepThreshold)
	}
}

func TestChatAllowlistParse(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_ALLOWED_CHAT_IDS"] = "12345678,-100123456789,@MyChat, other_channel ,42"
	c := loadValidFrom(t, env)
	if !c.AllowedChats.Enabled() {
		t.Fatal("allowlist must be enabled")
	}
	a := c.AllowedChats
	if !a.AllowsChatID(12345678) || !a.AllowsChatID(-100123456789) {
		t.Fatalf("explicit IDs missing from set: %+v", a.IDs)
	}
	// Auto-indexed variants: -100.. indexes its bare channel ID.
	if !a.AllowsChatID(123456789) {
		t.Fatalf("-100123456789 must index bare channel 123456789: %+v", a.IDs)
	}
	// Positive bare ID indexes its supergroup (-1000000000000 - val) and group (-val).
	if !a.AllowsChatID(-1000000000000-42) || !a.AllowsChatID(-42) {
		t.Fatalf("42 must index -1000000000042 and -42: %+v", a.IDs)
	}
	if !a.AllowsUsername("mychat") || !a.AllowsUsername("@MyChat ") {
		t.Fatalf("usernames must match case-insensitively, IDs ok: %+v", a.Usernames)
	}
}

func TestChatAllowlistDisabled(t *testing.T) {
	c := loadValid(t)
	if c.AllowedChats != nil || c.AllowedChats.Enabled() {
		t.Fatal("unset TELEGRAM_ALLOWED_CHAT_IDS must leave the allowlist disabled")
	}
}

func TestTransportModes(t *testing.T) {
	c := loadValid(t)
	if c.Transport != "stdio" || c.Host != "127.0.0.1" || c.Port != 8765 {
		t.Fatalf("defaults = %q %q %d, want stdio/127.0.0.1/8765", c.Transport, c.Host, c.Port)
	}
	env := validEnv()
	env["MCP_TRANSPORT"] = "HTTP"
	env["MCP_HOST"] = "0.0.0.0"
	env["MCP_PORT"] = "9000"
	c = loadValidFrom(t, env)
	if c.Transport != "http" || c.Host != "0.0.0.0" || c.Port != 9000 {
		t.Fatalf("http transport = %+v", c)
	}
	env = validEnv()
	env["MCP_TRANSPORT"] = "grpc"
	mustFail(t, env, "invalid MCP_TRANSPORT")
}

func TestAllowedHostsAndOrigins(t *testing.T) {
	env := validEnv()
	env["MCP_ALLOWED_HOSTS"] = " a.com , mcp.example.com:* "
	env["MCP_ALLOWED_ORIGINS"] = "https://mcp.example.com"
	c := loadValidFrom(t, env)
	if !reflect.DeepEqual(c.AllowedHosts, []string{"a.com", "mcp.example.com:*"}) {
		t.Fatalf("AllowedHosts = %v", c.AllowedHosts)
	}
	if !reflect.DeepEqual(c.AllowedOrigins, []string{"https://mcp.example.com"}) {
		t.Fatalf("AllowedOrigins = %v", c.AllowedOrigins)
	}
}

func TestProxySocksGlobal(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_PROXY_TYPE"] = "socks5"
	env["TELEGRAM_PROXY_HOST"] = "127.0.0.1"
	env["TELEGRAM_PROXY_PORT"] = "1080"
	c := loadValidFrom(t, env)
	if c.Proxy.Type != "socks5" || c.Proxy.Host != "127.0.0.1" || c.Proxy.Port != 1080 {
		t.Fatalf("socks proxy = %+v", c.Proxy)
	}
	if c.Proxy.RDNS != true {
		t.Fatalf("RDNS must default to true when unset, got %v", c.Proxy.RDNS)
	}
}

func TestProxyPerLabelOverride(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_PROXY_TYPE"] = "http"
	env["TELEGRAM_PROXY_HOST"] = "global.example"
	env["TELEGRAM_PROXY_PORT"] = "8080"
	env["TELEGRAM_PROXY_TYPE_WORK"] = "socks4"
	env["TELEGRAM_PROXY_HOST_WORK"] = "work.example"
	env["TELEGRAM_PROXY_PORT_WORK"] = "3128"
	c := loadValidFrom(t, env)

	work, err := c.ResolveProxy("work")
	if err != nil {
		t.Fatalf("ResolveProxy(work) failed: %v", err)
	}
	if work.Type != "socks4" || work.Host != "work.example" {
		t.Fatalf("per-label override lost: %+v", work)
	}
	// Un-overridden sub-vars fall through to the global values.
	def, _ := c.ResolveProxy("default")
	if def.Host != "global.example" || def.Type != "http" {
		t.Fatalf("global fall-through lost: %+v", def)
	}
}

func TestProxyMtproxyRequiresSecret(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_PROXY_TYPE"] = "mtproxy"
	env["TELEGRAM_PROXY_HOST"] = "h"
	env["TELEGRAM_PROXY_PORT"] = "443"
	mustFail(t, env, "TELEGRAM_PROXY_SECRET is required for mtproxy")

	env["TELEGRAM_PROXY_SECRET"] = "abcd1234"
	c := loadValidFrom(t, env)
	if c.Proxy.Connection != "mtproxy" || c.Proxy.Secret != "abcd1234" {
		t.Fatalf("mtproxy = %+v", c.Proxy)
	}
}

func TestProxyInvalidTypeAborts(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_PROXY_TYPE"] = "quic"
	mustFail(t, env, "invalid TELEGRAM_PROXY_TYPE")
}

func TestProxyMissingHostPortAborts(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_PROXY_TYPE"] = "socks5"
	mustFail(t, env, "TELEGRAM_PROXY_HOST and TELEGRAM_PROXY_PORT are required")
}

func TestProxyBadPortAborts(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_PROXY_TYPE"] = "http"
	env["TELEGRAM_PROXY_HOST"] = "h"
	env["TELEGRAM_PROXY_PORT"] = "not-a-port"
	mustFail(t, env, "TELEGRAM_PROXY_PORT must be an integer")
}

func TestTranscriptionDefaultsAndValidation(t *testing.T) {
	c := loadValid(t)
	if c.TranscribeMode != "on-demand" || c.TranscribeEngine != "groq" {
		t.Fatalf("defaults = %q/%q, want on-demand/groq", c.TranscribeMode, c.TranscribeEngine)
	}
	env := validEnv()
	env["TELEGRAM_TRANSCRIBE"] = "auto"
	env["TELEGRAM_TRANSCRIBE_ENGINE"] = "whisper"
	c = loadValidFrom(t, env)
	if c.TranscribeMode != "auto" || c.TranscribeEngine != "whisper" {
		t.Fatalf("auto/whisper = %q/%q", c.TranscribeMode, c.TranscribeEngine)
	}
	env = validEnv()
	env["TELEGRAM_TRANSCRIBE"] = "maybe"
	mustFail(t, env, "invalid TELEGRAM_TRANSCRIBE")
	env = validEnv()
	env["TELEGRAM_TRANSCRIBE_ENGINE"] = "deepgram"
	mustFail(t, env, "invalid TELEGRAM_TRANSCRIBE_ENGINE")
}

func TestTranscribeSubSettingsDefaults(t *testing.T) {
	c := loadValid(t)
	s := c.Transcribe
	if s.TimeoutSeconds != 120 {
		t.Fatalf("timeout default = %f, want 120", s.TimeoutSeconds)
	}
	if s.OpenAIMaxMB != 25 || s.GroqMaxMB != 25 {
		t.Fatalf("max-mb defaults = %f/%f, want 25/25", s.OpenAIMaxMB, s.GroqMaxMB)
	}
	if s.WhisperModel != "small" || s.WhisperDevice != "auto" || s.WhisperComputeType != "default" {
		t.Fatalf("whisper defaults = %q/%q/%q", s.WhisperModel, s.WhisperDevice, s.WhisperComputeType)
	}
	if s.MaxVoices != 5 || s.MaxSeconds != 300 {
		t.Fatalf("budget defaults = %d/%d, want 5/300", s.MaxVoices, s.MaxSeconds)
	}
	if s.CacheDir != "data/transcripts" {
		t.Fatalf("cache dir default = %q, want data/transcripts", s.CacheDir)
	}
	if s.Language != "" {
		t.Fatalf("language default = %q, want empty (auto-detect)", s.Language)
	}
}

func TestTranscribeSubSettingsOverrides(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_TRANSCRIBE_LANGUAGE"] = "NL"
	env["TELEGRAM_TRANSCRIBE_TIMEOUT"] = "90"
	env["TELEGRAM_TRANSCRIBE_OPENAI_URL"] = "https://localhost:5092/v1"
	env["TELEGRAM_TRANSCRIBE_OPENAI_MODEL"] = "whisper-1"
	env["TELEGRAM_TRANSCRIBE_GROQ_MAX_MB"] = "50"
	env["GROQ_API_KEY"] = "gk_abc"
	env["TELEGRAM_TRANSCRIBE_WHISPER_MODEL"] = "large-v3"
	env["TELEGRAM_TRANSCRIBE_MAX_VOICES"] = "not-a-number"
	c := loadValidFrom(t, env)
	s := c.Transcribe
	if s.Language != "nl" {
		t.Fatalf("language = %q, want 'nl' (lower-cased)", s.Language)
	}
	if s.TimeoutSeconds != 90 {
		t.Fatalf("timeout = %f, want 90", s.TimeoutSeconds)
	}
	if s.OpenAIURL != "https://localhost:5092/v1" || s.OpenAIModel != "whisper-1" {
		t.Fatalf("openai = %q/%q", s.OpenAIURL, s.OpenAIModel)
	}
	if s.GroqMaxMB != 50 || s.GroqAPIKey != "gk_abc" {
		t.Fatalf("groq = %f/%q", s.GroqMaxMB, s.GroqAPIKey)
	}
	if s.WhisperModel != "large-v3" {
		t.Fatalf("whisper model = %q", s.WhisperModel)
	}
	// Mirrors the Python int(os.getenv(...)) fallback: bad value -> default.
	if s.MaxVoices != 5 {
		t.Fatalf("malformed MAX_VOICES must fall back to 5, got %d", s.MaxVoices)
	}
}

func TestToggles(t *testing.T) {
	c := loadValid(t)
	if !c.ContactFuzzy {
		t.Fatal("TELEGRAM_CONTACT_FUZZY must default to true")
	}
	if c.EventFeed {
		t.Fatal("TELEGRAM_EVENT_FEED must default to false")
	}
	env := validEnv()
	env["TELEGRAM_CONTACT_FUZZY"] = "no"
	env["TELEGRAM_EVENT_FEED"] = "on"
	env["TELEGRAM_EVENT_FEED_FILE"] = "/var/tmp/feed.jsonl"
	env["TELEGRAM_ALIASES_FILE"] = "/var/tmp/aliases.json"
	env["TELEGRAM_DEVICE_MODEL"] = "Telegram MCP"
	c = loadValidFrom(t, env)
	if c.ContactFuzzy {
		t.Fatal("CONTACT_FUZZY=no must parse to false")
	}
	if !c.EventFeed || c.EventFeedFile != "/var/tmp/feed.jsonl" {
		t.Fatalf("event feed = %v/%q", c.EventFeed, c.EventFeedFile)
	}
	if c.AliasesFile != "/var/tmp/aliases.json" || c.DeviceModel != "Telegram MCP" {
		t.Fatalf("aliases/device = %q/%q", c.AliasesFile, c.DeviceModel)
	}
}

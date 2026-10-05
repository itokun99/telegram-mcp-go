package kit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itokun99/telegram-mcp-go/internal/config"
)

func intPtr(value int) *int { return &value }

func testSettings(t *testing.T) config.TranscribeSettings {
	t.Helper()
	return config.TranscribeSettings{
		TimeoutSeconds: 5,
		GroqAPIKey:     "test-groq-key",
		GroqMaxMB:      25,
		OpenAIMaxMB:    25,
		WhisperModel:   "small",
		WhisperDevice:  "auto",
		MaxVoices:      5,
		MaxSeconds:     300,
		CacheDir:       t.TempDir(),
	}
}

// ---------------------------------------------------------------------------
// Fake Telegram surfaces and fake HTTP backend
// ---------------------------------------------------------------------------

type stubDownloader struct {
	data   []byte
	err    error
	onCall func()
	calls  atomic.Int32
}

func (s *stubDownloader) DownloadVoice(ctx context.Context, messageID int64) ([]byte, error) {
	s.calls.Add(1)
	if s.onCall != nil {
		s.onCall()
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.data, nil
}

type stubNative struct {
	results []NativeTranscription
	err     error
	calls   atomic.Int32
}

func (s *stubNative) TranscribeNative(ctx context.Context, messageID int64) (NativeTranscription, error) {
	index := int(s.calls.Add(1)) - 1
	if s.err != nil {
		return NativeTranscription{}, s.err
	}
	if index >= len(s.results) {
		index = len(s.results) - 1
	}
	return s.results[index], nil
}

type recordedRequest struct {
	path     string
	auth     string
	fields   map[string]string
	filename string
	mime     string
	body     []byte
}

func recordRequest(r *http.Request) (recordedRequest, error) {
	record := recordedRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), fields: map[string]string{}}
	reader, err := r.MultipartReader()
	if err != nil {
		return record, err
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return record, err
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return record, err
		}
		if part.FormName() == "file" {
			record.filename = part.FileName()
			record.mime = part.Header.Get("Content-Type")
			record.body = data
		} else {
			record.fields[part.FormName()] = string(data)
		}
	}
	return record, nil
}

type audioBackend struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	response string
	status   int
}

func newAudioBackend(t *testing.T, response string) *audioBackend {
	t.Helper()
	backend := &audioBackend{response: response, status: http.StatusOK}
	backend.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record, err := recordRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		backend.mu.Lock()
		backend.requests = append(backend.requests, record)
		status := backend.status
		response := backend.response
		backend.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(backend.server.Close)
	return backend
}

func (b *audioBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.requests)
}

func (b *audioBackend) last() recordedRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requests[len(b.requests)-1]
}

func (b *audioBackend) setStatus(status int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = status
}

// ---------------------------------------------------------------------------
// Engine resolution and budget
// ---------------------------------------------------------------------------

func TestNormalizeEngineDefaultsAndValidation(t *testing.T) {
	if engine, err := NormalizeEngine(""); err != nil || engine != EngineGroq {
		t.Fatalf("empty engine = %q, %v; want groq, nil", engine, err)
	}
	if engine, err := NormalizeEngine(" Telegram "); err != nil || engine != EngineTelegram {
		t.Fatalf("normalized engine = %q, %v; want telegram, nil", engine, err)
	}
	_, err := NormalizeEngine("bogus")
	want := "Invalid engine 'bogus'. Use one of: 'groq', 'openai', 'telegram', 'whisper'."
	if err == nil || err.Error() != want {
		t.Fatalf("invalid engine error = %v; want %q", err, want)
	}
}

func TestTranscribeBudgetMatchesPythonSemantics(t *testing.T) {
	byVoices := NewTranscribeBudget(2, 10_000)
	if !byVoices.CanAfford(intPtr(10)) {
		t.Fatal("can_afford(10) = false at start")
	}
	byVoices.Charge(intPtr(10))
	if !byVoices.CanAfford(intPtr(10)) {
		t.Fatal("can_afford(10) = false after one charge")
	}
	byVoices.Charge(intPtr(10))
	if byVoices.CanAfford(intPtr(1)) {
		t.Fatal("can_afford(1) = true after two charges; voice cap not enforced")
	}

	bySeconds := NewTranscribeBudget(10, 50)
	if !bySeconds.CanAfford(intPtr(40)) {
		t.Fatal("can_afford(40) = false under a 50s cap")
	}
	bySeconds.Charge(intPtr(40))
	if bySeconds.CanAfford(intPtr(20)) {
		t.Fatal("can_afford(20) = true after 40/50 charged")
	}

	unknown := NewTranscribeBudget(1, 10)
	unknown.Charge(nil)
	if unknown.UsedSeconds() != 0 {
		t.Fatalf("unknown duration charged %d seconds; want 0", unknown.UsedSeconds())
	}
	if unknown.CanAfford(nil) {
		t.Fatal("can_afford(nil) = true after the voice cap was reached")
	}

	defaults := NewTranscribeBudget(0, 0)
	if defaults.maxVoices != 5 || defaults.maxSeconds != 300 {
		t.Fatalf("default caps = %d/%d; want 5/300", defaults.maxVoices, defaults.maxSeconds)
	}
}

// ---------------------------------------------------------------------------
// HTTP engines
// ---------------------------------------------------------------------------

func TestTranscribeGroqHappyPathCacheHitAndRequestShape(t *testing.T) {
	backend := newAudioBackend(t, `{"text":"  hello world  ","language":"en"}`)
	settings := testSettings(t)
	downloader := &stubDownloader{data: []byte("audio-bytes")}
	opts := TranscribeOptions{
		Engine:     EngineGroq,
		ChatID:     42,
		Message:    VoiceMessage{MessageID: 7, DurationSeconds: intPtr(3), DeclaredSize: 11, Ext: "oga"},
		Downloader: downloader,
		Settings:   settings,
		GroqURL:    backend.server.URL,
	}

	result, err := Transcribe(context.Background(), opts)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if result.Text != "hello world" || result.Source != EngineGroq || result.Lang != "en" {
		t.Fatalf("result = %+v", result)
	}
	if result.Cached {
		t.Fatal("first call reported Cached=true")
	}
	if result.DurationSeconds == nil || *result.DurationSeconds != 3 {
		t.Fatalf("duration = %v; want the message duration", result.DurationSeconds)
	}
	if backend.count() != 1 || downloader.calls.Load() != 1 {
		t.Fatalf("backend calls = %d, downloads = %d; want 1/1", backend.count(), downloader.calls.Load())
	}
	request := backend.last()
	if request.path != "/audio/transcriptions" {
		t.Fatalf("path = %q", request.path)
	}
	if request.auth != "Bearer test-groq-key" {
		t.Fatalf("auth = %q", request.auth)
	}
	if request.fields["model"] != GroqModel || request.fields["response_format"] != "verbose_json" {
		t.Fatalf("fields = %v", request.fields)
	}
	if _, ok := request.fields["language"]; ok {
		t.Fatalf("language sent without a configured hint: %v", request.fields)
	}
	if request.filename != "voice.ogg" || request.mime != "audio/ogg" {
		t.Fatalf("file = %q (%s); want voice.ogg (audio/ogg)", request.filename, request.mime)
	}
	if string(request.body) != "audio-bytes" {
		t.Fatalf("body = %q", request.body)
	}

	cached, err := Transcribe(context.Background(), opts)
	if err != nil {
		t.Fatalf("cached Transcribe: %v", err)
	}
	if !cached.Cached || cached.Text != "hello world" || cached.Source != EngineGroq {
		t.Fatalf("cached result = %+v", cached)
	}
	if backend.count() != 1 || downloader.calls.Load() != 1 {
		t.Fatalf("cache hit paid again: backend = %d, downloads = %d", backend.count(), downloader.calls.Load())
	}
}

func TestTranscribeOpenAICompatibleEndpointAndLanguage(t *testing.T) {
	backend := newAudioBackend(t, `{"text":"hallo"}`)
	settings := testSettings(t)
	settings.Language = "nl"
	settings.OpenAIURL = backend.server.URL + "/v1"
	settings.OpenAIAPIKey = ""
	opts := TranscribeOptions{
		Engine:     EngineOpenAI,
		ChatID:     1,
		Message:    VoiceMessage{MessageID: 2, Ext: "mp3"},
		Downloader: &stubDownloader{data: []byte("mp3-bytes")},
		Settings:   settings,
	}

	result, err := Transcribe(context.Background(), opts)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if result.Text != "hallo" || result.Source != EngineOpenAI || result.Lang != "nl" {
		t.Fatalf("result = %+v", result)
	}
	request := backend.last()
	if request.path != "/v1/audio/transcriptions" {
		t.Fatalf("path = %q; want the appended /audio/transcriptions", request.path)
	}
	if request.fields["model"] != OpenAIDefaultModel || request.fields["response_format"] != "json" {
		t.Fatalf("fields = %v", request.fields)
	}
	if request.fields["language"] != "nl" {
		t.Fatalf("language = %q; want nl", request.fields["language"])
	}
	if request.auth != "" {
		t.Fatalf("auth = %q; want none for a keyless endpoint", request.auth)
	}
	if request.filename != "voice.mp3" {
		t.Fatalf("filename = %q", request.filename)
	}
}

func TestTranscribeConfigErrorMessagesMatchPython(t *testing.T) {
	settings := testSettings(t)
	settings.GroqAPIKey = ""
	downloader := &stubDownloader{data: []byte("audio")}

	_, err := Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineGroq, ChatID: 1, Message: VoiceMessage{MessageID: 1},
		Downloader: downloader, Settings: settings, GroqURL: "http://127.0.0.1:9",
	})
	wantGroq := "GROQ_API_KEY is not configured on this server. Use another engine or set GROQ_API_KEY."
	if err == nil || err.Error() != wantGroq {
		t.Fatalf("groq config error = %v; want %q", err, wantGroq)
	}

	settings.GroqAPIKey = "test-key"
	_, err = Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineOpenAI, ChatID: 1, Message: VoiceMessage{MessageID: 1},
		Downloader: downloader, Settings: settings,
	})
	wantOpenAI := "TELEGRAM_TRANSCRIBE_OPENAI_URL is not configured on this server. Set it to the base URL of an OpenAI-compatible API (e.g. https://api.openai.com/v1)."
	if err == nil || err.Error() != wantOpenAI {
		t.Fatalf("openai config error = %v; want %q", err, wantOpenAI)
	}
	if downloader.calls.Load() != 0 {
		t.Fatalf("downloads = %d; config errors must cost nothing", downloader.calls.Load())
	}
}

func TestTranscribeTooLargeDeclaredSize(t *testing.T) {
	backend := newAudioBackend(t, `{"text":"never"}`)
	settings := testSettings(t)
	settings.GroqMaxMB = 1
	downloader := &stubDownloader{data: []byte("audio")}
	_, err := Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineGroq, ChatID: 1,
		Message:    VoiceMessage{MessageID: 1, DeclaredSize: 2 * 1048576},
		Downloader: downloader, Settings: settings, GroqURL: backend.server.URL,
	})
	want := "recording is 2.0 MB, above the 1 MB Groq upload limit. Transcribe it with engine='telegram' or engine='whisper', or raise TELEGRAM_TRANSCRIBE_GROQ_MAX_MB if the Groq endpoint accepts larger uploads."
	if err == nil || err.Error() != want {
		t.Fatalf("too-large error = %v; want %q", err, want)
	}
	if downloader.calls.Load() != 0 {
		t.Fatalf("downloads = %d; the declared size must be refused before downloading", downloader.calls.Load())
	}
	if backend.count() != 0 {
		t.Fatalf("backend calls = %d; want 0", backend.count())
	}
}

func TestTranscribeTooLargeAfterDownload(t *testing.T) {
	backend := newAudioBackend(t, `{"text":"never"}`)
	settings := testSettings(t)
	settings.GroqMaxMB = 1
	downloader := &stubDownloader{data: make([]byte, 2*1048576)}
	_, err := Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineGroq, ChatID: 1,
		Message:    VoiceMessage{MessageID: 1},
		Downloader: downloader, Settings: settings, GroqURL: backend.server.URL,
	})
	want := "recording is 2.0 MB, above the 1 MB Groq upload limit. Transcribe it with engine='telegram' or engine='whisper', or raise TELEGRAM_TRANSCRIBE_GROQ_MAX_MB if the Groq endpoint accepts larger uploads."
	if err == nil || err.Error() != want {
		t.Fatalf("too-large error = %v; want %q", err, want)
	}
	if backend.count() != 0 {
		t.Fatalf("backend calls = %d; oversized bytes must not be uploaded", backend.count())
	}
}

func TestTranscribeDownloadAndBackendFailureMessages(t *testing.T) {
	backend := newAudioBackend(t, `{"text":"hi"}`)
	settings := testSettings(t)
	base := TranscribeOptions{
		Engine: EngineGroq, ChatID: 1,
		Message:  VoiceMessage{MessageID: 1},
		Settings: settings, GroqURL: backend.server.URL,
	}

	failing := base
	failing.Downloader = &stubDownloader{err: errors.New("boom")}
	if _, err := Transcribe(context.Background(), failing); err == nil || err.Error() != "download failed: boom" {
		t.Fatalf("download error = %v", err)
	}

	empty := base
	empty.Downloader = &stubDownloader{data: []byte{}}
	if _, err := Transcribe(context.Background(), empty); err == nil || err.Error() != "empty media download" {
		t.Fatalf("empty download error = %v", err)
	}

	backend.setStatus(http.StatusInternalServerError)
	ok := base
	ok.Downloader = &stubDownloader{data: []byte("audio")}
	if _, err := Transcribe(context.Background(), ok); err == nil || !strings.Contains(err.Error(), "groq request failed: HTTP 500") {
		t.Fatalf("HTTP failure error = %v", err)
	}

	backend.setStatus(http.StatusOK)
	backend.mu.Lock()
	backend.response = `{"text":"   "}`
	backend.mu.Unlock()
	if _, err := Transcribe(context.Background(), ok); err == nil || err.Error() != "empty transcript from groq" {
		t.Fatalf("empty transcript error = %v", err)
	}
}

func TestTranscribeBudgetExceeded(t *testing.T) {
	backend := newAudioBackend(t, `{"text":"never"}`)
	settings := testSettings(t)
	downloader := &stubDownloader{data: []byte("audio")}
	budget := NewTranscribeBudget(1, 300)
	budget.Charge(nil) // one recording already spent

	_, err := Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineGroq, ChatID: 1,
		Message:    VoiceMessage{MessageID: 99, DurationSeconds: intPtr(5)},
		Downloader: downloader, Settings: settings, GroqURL: backend.server.URL,
		Budget: budget,
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("error = %v; want ErrBudgetExceeded", err)
	}
	if err.Error() != "transcript pending" {
		t.Fatalf("budget error string = %q; want the Python render marker %q", err.Error(), "transcript pending")
	}
	if backend.count() != 0 || downloader.calls.Load() != 0 {
		t.Fatalf("budget exhaustion paid: backend = %d, downloads = %d", backend.count(), downloader.calls.Load())
	}

	spendable := NewTranscribeBudget(2, 300)
	spendable.Charge(intPtr(1))
	_, err = Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineGroq, ChatID: 1,
		Message:    VoiceMessage{MessageID: 100, DurationSeconds: intPtr(5)},
		Downloader: downloader, Settings: settings, GroqURL: backend.server.URL,
		Budget: spendable,
	})
	if err != nil {
		t.Fatalf("affordable call failed: %v", err)
	}
	if spendable.UsedVoices() != 2 || spendable.UsedSeconds() != 6 {
		t.Fatalf("budget = %d voices / %d seconds; want 2/6", spendable.UsedVoices(), spendable.UsedSeconds())
	}
}

// ---------------------------------------------------------------------------
// Whisper (KNOWN-GAP mapping) and native engine
// ---------------------------------------------------------------------------

func TestTranscribeWhisperMapsToHTTPEndpoint(t *testing.T) {
	backend := newAudioBackend(t, `{"text":"whisper text"}`)
	settings := testSettings(t)
	env := config.NewInMemory(map[string]string{
		"TELEGRAM_WHISPER_BASE_URL": backend.server.URL,
		"TELEGRAM_WHISPER_MODEL":    "medium",
		"TELEGRAM_WHISPER_DEVICE":   "cpu",
	})
	result, err := Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineWhisper, ChatID: 1,
		Message:    VoiceMessage{MessageID: 1},
		Downloader: &stubDownloader{data: []byte("audio")},
		Settings:   settings, Env: env,
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if result.Text != "whisper text" || result.Source != EngineWhisper {
		t.Fatalf("result = %+v", result)
	}
	request := backend.last()
	if request.path != "/audio/transcriptions" || request.fields["model"] != "medium" {
		t.Fatalf("whisper request = %+v", request)
	}
	if request.fields["response_format"] != "json" {
		t.Fatalf("response_format = %q; want json", request.fields["response_format"])
	}
}

func TestTranscribeWhisperConfigErrors(t *testing.T) {
	settings := testSettings(t)
	downloader := &stubDownloader{data: []byte("audio")}

	_, err := Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineWhisper, ChatID: 1, Message: VoiceMessage{MessageID: 1},
		Downloader: downloader, Settings: settings, Env: config.NewInMemory(nil),
	})
	wantMissing := "TELEGRAM_WHISPER_BASE_URL is not configured on this server. Set it to the base URL of an OpenAI-compatible whisper endpoint (e.g. http://localhost:8000/v1)."
	if err == nil || err.Error() != wantMissing {
		t.Fatalf("whisper base-url error = %v; want %q", err, wantMissing)
	}

	badDevice := config.NewInMemory(map[string]string{
		"TELEGRAM_WHISPER_BASE_URL": "http://127.0.0.1:9",
		"TELEGRAM_WHISPER_DEVICE":   "banana",
	})
	_, err = Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineWhisper, ChatID: 1, Message: VoiceMessage{MessageID: 1},
		Downloader: downloader, Settings: settings, Env: badDevice,
	})
	wantDevice := "Invalid TELEGRAM_WHISPER_DEVICE 'banana'. Expected one of: auto, cpu, cuda."
	if err == nil || err.Error() != wantDevice {
		t.Fatalf("whisper device error = %v; want %q", err, wantDevice)
	}
	if downloader.calls.Load() != 0 {
		t.Fatalf("downloads = %d; config errors must cost nothing", downloader.calls.Load())
	}
}

func TestTranscribeNativePollingAndCache(t *testing.T) {
	settings := testSettings(t)
	native := &stubNative{results: []NativeTranscription{{Pending: true}, {Text: "native text"}}}
	opts := TranscribeOptions{
		Engine: EngineTelegram, ChatID: 5,
		Message:            VoiceMessage{MessageID: 6, DurationSeconds: intPtr(83)},
		Native:             native,
		Settings:           settings,
		NativePollInterval: time.Nanosecond,
	}
	result, err := Transcribe(context.Background(), opts)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if result.Text != "native text" || result.Source != EngineTelegram || result.Pending {
		t.Fatalf("result = %+v", result)
	}
	if native.calls.Load() != 2 {
		t.Fatalf("native calls = %d; want one poll after pending", native.calls.Load())
	}

	cached, err := Transcribe(context.Background(), opts)
	if err != nil {
		t.Fatalf("cached Transcribe: %v", err)
	}
	if !cached.Cached || cached.Text != "native text" {
		t.Fatalf("cached result = %+v", cached)
	}
	if native.calls.Load() != 2 {
		t.Fatalf("native calls = %d after a cache hit; want 2", native.calls.Load())
	}
}

func TestTranscribeNativePremiumAndPending(t *testing.T) {
	settings := testSettings(t)
	premium := &stubNative{err: ErrPremiumRequired}
	_, err := Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineTelegram, ChatID: 1, Message: VoiceMessage{MessageID: 1},
		Native: premium, Settings: settings,
	})
	if !errors.Is(err, ErrPremiumRequired) {
		t.Fatalf("error = %v; want ErrPremiumRequired", err)
	}

	alwaysPending := &stubNative{results: []NativeTranscription{{Pending: true}}}
	result, err := Transcribe(context.Background(), TranscribeOptions{
		Engine: EngineTelegram, ChatID: 1, Message: VoiceMessage{MessageID: 2},
		Native: alwaysPending, Settings: settings,
		NativePollAttempts: 2, NativePollInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if !result.Pending || result.Text != "" {
		t.Fatalf("result = %+v; want Pending with no text", result)
	}
	if alwaysPending.calls.Load() != 3 {
		t.Fatalf("native calls = %d; want initial + 2 polls", alwaysPending.calls.Load())
	}
	if _, ok, _ := NewTranscribeCache(settings.CacheDir).Get(1, 2, EngineTelegram, ""); ok {
		t.Fatal("pending transcription was cached")
	}
}

func TestTranscribeSingleFlightSharesOnePaidCall(t *testing.T) {
	backend := newAudioBackend(t, `{"text":"shared","language":"en"}`)
	settings := testSettings(t)
	cache := NewTranscribeCache(settings.CacheDir)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	downloader := &stubDownloader{
		data: []byte("audio"),
		onCall: func() {
			once.Do(func() { close(entered) })
			<-release
		},
	}
	opts := TranscribeOptions{
		Engine: EngineGroq, ChatID: 1,
		Message:    VoiceMessage{MessageID: 2},
		Downloader: downloader, Settings: settings, GroqURL: backend.server.URL,
		Cache: cache,
	}

	type outcome struct {
		result TranscribeResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	call := func() {
		result, err := Transcribe(context.Background(), opts)
		outcomes <- outcome{result: result, err: err}
	}

	go call()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first call never reached the download")
	}
	go call()
	close(release)

	cachedCount := 0
	for i := 0; i < 2; i++ {
		select {
		case got := <-outcomes:
			if got.err != nil {
				t.Fatalf("Transcribe: %v", got.err)
			}
			if got.result.Text != "shared" {
				t.Fatalf("text = %q", got.result.Text)
			}
			if got.result.Cached {
				cachedCount++
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent Transcribe calls did not settle")
		}
	}
	if cachedCount != 1 {
		t.Fatalf("cached results = %d; want exactly one to have hit the cache", cachedCount)
	}
	if downloader.calls.Load() != 1 || backend.count() != 1 {
		t.Fatalf("downloads = %d, backend = %d; want one paid call", downloader.calls.Load(), backend.count())
	}
}

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

func TestTranscribeCachePersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	cache := NewTranscribeCache(dir)
	if err := cache.Save(1, 2, EngineGroq, "v1", intPtr(12), "en"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fresh := NewTranscribeCache(dir)
	hit, ok, err := fresh.Get(1, 2, EngineGroq, "")
	if err != nil || !ok {
		t.Fatalf("Get after reload: ok=%v err=%v", ok, err)
	}
	if hit.Text != "v1" || hit.Source != EngineGroq || hit.Lang != "en" {
		t.Fatalf("hit = %+v", hit)
	}
	if hit.DurationSeconds == nil || *hit.DurationSeconds != 12 {
		t.Fatalf("duration = %v", hit.DurationSeconds)
	}
	if hit.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not parsed")
	}

	file := filepath.Join(dir, transcribeCacheFileName)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("cache file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode = %o; want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("cache dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir mode = %o; want 700", dirInfo.Mode().Perm())
	}

	if err := cache.Save(1, 2, EngineGroq, "v2", nil, ""); err != nil {
		t.Fatalf("Save replace: %v", err)
	}
	replaced, ok, err := NewTranscribeCache(dir).Get(1, 2, EngineGroq, "")
	if err != nil || !ok || replaced.Text != "v2" {
		t.Fatalf("replaced = %+v ok=%v err=%v", replaced, ok, err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	var stored struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("parse cache file: %v", err)
	}
	if len(stored.Entries) != 1 {
		t.Fatalf("stored rows = %d; a replace must keep one row per (chat, message, source)", len(stored.Entries))
	}
}

func TestTranscribeCachePinsSourceAndPrefersDefaultEngine(t *testing.T) {
	dir := t.TempDir()
	cache := NewTranscribeCache(dir)
	if err := cache.Save(1, 2, EngineGroq, "groq text", nil, ""); err != nil {
		t.Fatalf("Save groq: %v", err)
	}
	if err := cache.Save(1, 2, EngineTelegram, "telegram text", nil, ""); err != nil {
		t.Fatalf("Save telegram: %v", err)
	}

	if hit, ok, _ := cache.Get(1, 2, EngineGroq, ""); !ok || hit.Text != "groq text" {
		t.Fatalf("pinned groq hit = %+v ok=%v", hit, ok)
	}
	if hit, ok, _ := cache.Get(1, 2, EngineTelegram, ""); !ok || hit.Text != "telegram text" {
		t.Fatalf("pinned telegram hit = %+v ok=%v", hit, ok)
	}
	if _, ok, _ := cache.Get(1, 2, EngineOpenAI, ""); ok {
		t.Fatal("pinned openai read returned another engine's row")
	}
	if hit, ok, _ := cache.Get(1, 2, "", EngineGroq); !ok || hit.Text != "groq text" {
		t.Fatalf("unpinned read = %+v ok=%v; the default engine's row must win", hit, ok)
	}
	if hit, ok, _ := cache.Get(1, 2, "", EngineTelegram); !ok || hit.Text != "telegram text" {
		t.Fatalf("unpinned read = %+v ok=%v", hit, ok)
	}
	if _, ok, _ := cache.Get(3, 2, "", EngineGroq); ok {
		t.Fatal("another message returned a row")
	}
}

// ---------------------------------------------------------------------------
// Auto-mode prefetch
// ---------------------------------------------------------------------------

func TestPrefetchHonorsModeTextAndBudget(t *testing.T) {
	backend := newAudioBackend(t, `{"text":"prefetched","language":"en"}`)
	settings := testSettings(t)
	messages := []VoiceMessage{
		{MessageID: 1, DurationSeconds: intPtr(5)},
		{MessageID: 2, DurationSeconds: intPtr(5)},
		{MessageID: 3, DurationSeconds: intPtr(5), HasText: true},
	}
	base := PrefetchOptions{
		Mode:     "on-demand",
		ChatID:   9,
		Messages: messages,
		TranscribeOptions: TranscribeOptions{
			Engine:     EngineGroq,
			Downloader: &stubDownloader{data: []byte("audio")},
			Settings:   settings,
			GroqURL:    backend.server.URL,
			Cache:      NewTranscribeCache(settings.CacheDir),
		},
	}
	PrefetchTranscripts(context.Background(), base)
	if backend.count() != 0 {
		t.Fatalf("backend calls = %d in on-demand mode; want 0", backend.count())
	}

	auto := base
	auto.Mode = "auto"
	auto.TranscribeOptions.Settings.MaxVoices = 1
	PrefetchTranscripts(context.Background(), auto)
	if backend.count() != 1 {
		t.Fatalf("backend calls = %d; the budget allows exactly one recording", backend.count())
	}
	if _, ok, _ := auto.Cache.Get(9, 1, EngineGroq, ""); !ok {
		t.Fatal("first message was not prefetched into the cache")
	}
	if _, ok, _ := auto.Cache.Get(9, 3, EngineGroq, ""); ok {
		t.Fatal("a message with existing text was prefetched")
	}
}

// ---------------------------------------------------------------------------
// Render helpers
// ---------------------------------------------------------------------------

func TestFormatDurationAndRenderVoiceText(t *testing.T) {
	if got := FormatDuration(nil); got != "?:??" {
		t.Fatalf("FormatDuration(nil) = %q", got)
	}
	if got := FormatDuration(intPtr(23)); got != "0:23" {
		t.Fatalf("FormatDuration(23) = %q", got)
	}
	if got := FormatDuration(intPtr(125)); got != "2:05" {
		t.Fatalf("FormatDuration(125) = %q", got)
	}
	if got := FormatDuration(intPtr(3725)); got != "1:02:05" {
		t.Fatalf("FormatDuration(3725) = %q", got)
	}

	pending := VoiceAttachmentInfo{DurationSeconds: intPtr(23), TranscriptStatus: "pending"}
	if got := RenderVoiceText(pending); got != "voice 0:23 | transcript pending" {
		t.Fatalf("pending render = %q", got)
	}
	ready := VoiceAttachmentInfo{DurationSeconds: intPtr(23), Transcript: "hi", TranscriptSource: EngineGroq, TranscriptStatus: "ready"}
	if got := RenderVoiceText(ready); got != "voice 0:23 | transcript (groq, not verbatim): hi" {
		t.Fatalf("ready render = %q", got)
	}
	idle := VoiceAttachmentInfo{DurationSeconds: intPtr(23)}
	if got := RenderVoiceText(idle); got != "voice 0:23" {
		t.Fatalf("idle render = %q", got)
	}
}

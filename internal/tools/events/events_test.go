package events

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

// fakeEntity is the minimal kit.Entity a test needs: an id, a peer kind and an
// optional handle.
type fakeEntity struct {
	id       int64
	kind     kit.PeerKind
	username string
}

func (e fakeEntity) BareID() int64          { return e.id }
func (e fakeEntity) PeerKind() kit.PeerKind { return e.kind }
func (e fakeEntity) Username() string       { return e.username }

// fakeSource is the fake gogram client seam: kit.EntitySource resolves an
// identifier to an Entity, so the wait tools exercise the real kit.Resolver
// path without a Telegram connection.
type fakeSource struct {
	mu        sync.Mutex
	entities  map[string]fakeEntity
	warmCalls int
	lookups   int
}

func newFakeSource(entities map[string]fakeEntity) *fakeSource {
	return &fakeSource{entities: entities}
}

func (s *fakeSource) Lookup(identifier any) (kit.Entity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	key := lookupKey(identifier)
	entity, ok := s.entities[key]
	if !ok {
		return nil, wrapNotFound(identifier)
	}
	return entity, nil
}

func (s *fakeSource) WarmEntities() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warmCalls++
	return nil
}

func (s *fakeSource) warmCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warmCalls
}

func lookupKey(identifier any) string {
	switch v := identifier.(type) {
	case string:
		return strings.TrimPrefix(v, "@")
	case int64:
		return formatInt(v)
	default:
		return ""
	}
}

func wrapNotFound(identifier any) error {
	return &notFound{identifier: identifier}
}

type notFound struct{ identifier any }

func (e *notFound) Error() string { return "entity not found: " + lookupKey(e.identifier) }

func (e *notFound) Unwrap() error { return kit.ErrEntityNotFound }

// recordingReporter captures the funnel's diagnostics so the tests never build
// the file-backed production logger.
type recordingReporter struct {
	mu       sync.Mutex
	errors   []string
	warnings []string
}

func (r *recordingReporter) Error(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, message)
}

func (r *recordingReporter) Warning(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, message)
}

// testClock is a manual burst clock in milliseconds, so debounce and settle
// assertions never depend on wall time.
type testClock struct{ millis atomic.Int64 }

func (c *testClock) seconds() float64 { return float64(c.millis.Load()) / 1000 }

func (c *testClock) advance(d time.Duration) { c.millis.Add(int64(d / time.Millisecond)) }

func (c *testClock) set(d time.Duration) { c.millis.Store(int64(d / time.Millisecond)) }

// testEnv wires a fresh module instance with offline seams: a manual clock, a
// recording reporter and a feed file inside t.TempDir.
type testEnv struct {
	clock    *testClock
	rep      *recordingReporter
	feedFile string
	source   *fakeSource
	t        *testing.T
}

func newTestEnv(t *testing.T, mutate ...func(*Deps)) *testEnv {
	t.Helper()
	clock := &testClock{}
	rep := &recordingReporter{}
	feedFile := filepath.Join(t.TempDir(), "incoming_feed.jsonl")
	deps := Deps{
		FeedFile:  feedFile,
		Monotonic: clock.seconds,
		Wall:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		Reporter:  rep,
	}
	for _, apply := range mutate {
		apply(&deps)
	}
	Configure(deps)
	t.Cleanup(func() { Configure(Deps{Reporter: rep}) })
	return &testEnv{clock: clock, rep: rep, feedFile: feedFile, t: t}
}

// deliver records one accepted incoming message from a human sender.
func (e *testEnv) deliver(chatID, messageID int64, name, username string) {
	e.t.Helper()
	msg := Incoming{
		ChatID:      chatID,
		MessageID:   messageID,
		Name:        name,
		Username:    username,
		HasUsername: username != "",
		Private:     true,
		SenderKnown: true,
	}
	OnIncoming(msg)
}

// decode unmarshals a tool result into a generic map for assertions.
func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("result is not a JSON object: %v (%s)", err, raw)
	}
	return out
}

// ---------------------------------------------------------------------------
// Registration parity
// ---------------------------------------------------------------------------

// inventoryNames are the events module's tools as listed in
// .omo/go-port/parity/inventory.json. The names must match exactly.
var inventoryNames = []string{
	"disable_incoming_feed",
	"enable_incoming_feed",
	"incoming_feed_status",
	"wait_for_new_message",
	"wait_for_settled_message",
}

// inventoryReadOnly mirrors the inventory's readonly flag per events tool.
var inventoryReadOnly = map[string]bool{
	"disable_incoming_feed":    false,
	"enable_incoming_feed":     false,
	"incoming_feed_status":     true,
	"wait_for_new_message":     true,
	"wait_for_settled_message": true,
}

func TestRegistersEveryInventoryToolName(t *testing.T) {
	reg := mcpserver.NewRegistry()
	Register(reg)
	srv, err := mcpserver.Build(reg, mcpserver.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	got := srv.ToolNames()
	want := slices.Clone(inventoryNames)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("served tool names = %v, want %v", got, want)
	}
	if len(got) != 5 {
		t.Fatalf("served %d tools, want 5", len(got))
	}
}

func TestDefaultRegistryCarriesTheFiveTools(t *testing.T) {
	names := mcpserver.RegisteredToolNames()
	for _, want := range inventoryNames {
		if !slices.Contains(names, want) {
			t.Errorf("tool %q not registered in the default registry: %v", want, names)
		}
	}
}

// TestSchemaDerivationAndReadOnlyHints drives the real MCP server so the
// inferred JSON schema is built for every tool and the readOnly hints match the
// inventory: TELEGRAM_EXPOSED_TOOLS=read-only keys on them.
func TestSchemaDerivationAndReadOnlyHints(t *testing.T) {
	reg := mcpserver.NewRegistry()
	Register(reg)
	srv, err := mcpserver.Build(reg, mcpserver.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.MCP.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "events-test", Version: "0.0.1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(res.Tools) != len(inventoryNames) {
		t.Fatalf("tools/list returned %d tools, want %d", len(res.Tools), len(inventoryNames))
	}

	schemas := map[string]map[string]any{}
	for _, tool := range res.Tools {
		if tool.Annotations == nil {
			t.Fatalf("tool %q has no annotations", tool.Name)
		}
		if got, want := tool.Annotations.ReadOnlyHint, inventoryReadOnly[tool.Name]; got != want {
			t.Errorf("tool %q readOnlyHint = %v, want %v", tool.Name, got, want)
		}
		if tool.InputSchema == nil {
			t.Fatalf("tool %q has no input schema", tool.Name)
		}
		fields := map[string]any{}
		encoded, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("tool %q is not encodable: %v", tool.Name, err)
		}
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatalf("tool %q is not an object: %v", tool.Name, err)
		}
		schemas[tool.Name] = fields
	}

	assertProperties(t, schemas, "wait_for_new_message", "timeout", "chat_id")
	assertProperties(t, schemas, "wait_for_settled_message", "settle_ms", "max_wait_ms", "chat_id")
	assertProperties(t, schemas, "enable_incoming_feed", "settle_ms")
	assertProperties(t, schemas, "disable_incoming_feed")
	assertProperties(t, schemas, "incoming_feed_status")
}

// assertProperties checks the schema exposes exactly the expected fields and
// that each carries the ported Python Args text as its description.
func assertProperties(t *testing.T, schemas map[string]map[string]any, tool string, fields ...string) {
	t.Helper()
	schema, ok := schemas[tool]
	if !ok {
		t.Fatalf("tool %q missing from the served schemas", tool)
	}
	raw, ok := schema["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("tool %q has no input schema object: %v", tool, schema)
	}
	props, _ := raw["properties"].(map[string]any)
	if len(fields) == 0 {
		// A no-argument tool derives {type: object, additionalProperties:
		// false} with no properties member at all.
		if len(props) != 0 {
			t.Errorf("tool %q exposes %d fields, want none (%v)", tool, len(props), keysOf(props))
		}
		return
	}
	if len(props) != len(fields) {
		t.Errorf("tool %q exposes %d fields, want %d (%v)", tool, len(props), len(fields), keysOf(props))
	}
	for _, field := range fields {
		entry, ok := props[field].(map[string]any)
		if !ok {
			t.Errorf("tool %q is missing field %q", tool, field)
			continue
		}
		if desc, _ := entry["description"].(string); strings.TrimSpace(desc) == "" {
			t.Errorf("tool %q field %q has no description (the Python Args text must be ported)", tool, field)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestReadOnlyModeKeepsTheThreeReadTools asserts the security boundary: the two
// feed toggles are pruned under read-only while the three read tools stay.
func TestReadOnlyModeKeepsTheThreeReadTools(t *testing.T) {
	reg := mcpserver.NewRegistry()
	Register(reg)
	srv, err := mcpserver.Build(reg, mcpserver.Options{ExposedMode: "read-only"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{"incoming_feed_status", "wait_for_new_message", "wait_for_settled_message"}
	slices.Sort(want)
	if got := srv.ToolNames(); !slices.Equal(got, want) {
		t.Fatalf("read-only served %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// wait_for_new_message
// ---------------------------------------------------------------------------

func TestWaitForNewMessageReturnsPendingChats(t *testing.T) {
	env := newTestEnv(t)
	env.clock.set(0)
	env.deliver(101, 7, "Ada", "ada")
	env.deliver(101, 8, "Ada", "ada")

	timeout := 0.05
	out, err := handleWaitForNewMessage(context.Background(), waitForNewMessageInput{Timeout: &timeout})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	got := decode(t, out)
	if got["event"] != true {
		t.Fatalf("event = %v, want true (%s)", got["event"], out)
	}
	chats, ok := got["pending_chats"].([]any)
	if !ok || len(chats) != 1 {
		t.Fatalf("pending_chats = %v, want one entry (%s)", got["pending_chats"], out)
	}
	chat := chats[0].(map[string]any)
	if chat["chat_id"].(float64) != 101 || chat["count"].(float64) != 2 || chat["last_message_id"].(float64) != 8 {
		t.Fatalf("pending chat = %v", chat)
	}
	if chat["username"] != "ada" {
		t.Fatalf("username = %v, want ada", chat["username"])
	}
}

// The pending view must not consume: wait_for_settled_message still sees the
// burst afterwards.
func TestWaitForNewMessageDoesNotConsume(t *testing.T) {
	env := newTestEnv(t)
	env.clock.set(0)
	env.deliver(101, 7, "Ada", "")

	timeout := 0.05
	if out, _ := handleWaitForNewMessage(context.Background(), waitForNewMessageInput{Timeout: &timeout}); !strings.Contains(out, `"event":true`) {
		t.Fatalf("wait_for_new_message = %s", out)
	}

	settle, maxWait := 0, 20
	out, err := handleWaitForSettledMessage(context.Background(), waitForSettledMessageInput{SettleMS: &settle, MaxWaitMS: &maxWait})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if got := decode(t, out); got["chat_id"].(float64) != 101 {
		t.Fatalf("settled burst = %s, want chat 101", out)
	}
}

func TestWaitForNewMessageTimesOut(t *testing.T) {
	newTestEnv(t)

	timeout := 0.01
	out, err := handleWaitForNewMessage(context.Background(), waitForNewMessageInput{Timeout: &timeout})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	got := decode(t, out)
	if got["event"] != false || got["reason"] != "timeout" {
		t.Fatalf("timeout result = %s", out)
	}
	if got["waiting_for"] != nil {
		t.Fatalf("waiting_for = %v, want null for an unfiltered wait", got["waiting_for"])
	}
}

// A chat_id the allowlist does not cover is denied with the privacy message
// verbatim, before any waiting happens.
func TestWaitForNewMessageDeniesUnlistedChat(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Allowlist = &allowOnly{ids: []int64{999}} })
	env.clock.set(0)
	env.deliver(101, 7, "Ada", "")

	timeout := 0.01
	out, err := handleWaitForNewMessage(context.Background(), waitForNewMessageInput{Timeout: &timeout, ChatID: int64(101)})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(out, "restricted by privacy policy") {
		t.Fatalf("denied wait = %s, want the privacy denial", out)
	}
	if !strings.Contains(out, "TELEGRAM_ALLOWED_CHAT_IDS") {
		t.Fatalf("denial must name the setting: %s", out)
	}
}

// allowOnly is a kit.ChatAllowlist that permits exactly the listed IDs.
type allowOnly struct{ ids []int64 }

func (a *allowOnly) AllowsChatID(id int64) bool { return slices.Contains(a.ids, id) }

func (a *allowOnly) AllowsUsername(string) bool { return false }

func (a *allowOnly) Enabled() bool { return len(a.ids) > 0 }

// ---------------------------------------------------------------------------
// wait_for_settled_message
// ---------------------------------------------------------------------------

// TestDebounceUsesQuietWindow exercises the settle logic on the tracker with a
// manual clock: a burst is invisible until the quiet window has elapsed, and a
// later message resets the window.
func TestDebounceUsesQuietWindow(t *testing.T) {
	env := newTestEnv(t)
	tr := active().tracker

	env.clock.set(0)
	env.deliver(101, 7, "Ada", "ada")
	env.clock.advance(400 * time.Millisecond)
	env.deliver(101, 5, "Ada", "ada") // out-of-order id: first_id must stay the min

	env.clock.advance(500 * time.Millisecond) // 0.9s quiet, window is 1s
	if _, _, settled := tr.scanSettled(env.clock.seconds(), 1, nil, nil); settled {
		t.Fatal("burst settled before the quiet window elapsed")
	}

	env.clock.advance(600 * time.Millisecond) // 1.5s quiet
	chatID, _, settled := tr.scanSettled(env.clock.seconds(), 1, nil, nil)
	if !settled || chatID != 101 {
		t.Fatalf("scanSettled = (%d, %v), want chat 101 settled", chatID, settled)
	}

	rec := tr.take(chatID)
	if rec == nil || rec.count != 2 || rec.firstID != 5 || rec.lastID != 7 {
		t.Fatalf("burst = %+v, want count 2 with ids 5..7", rec)
	}
	summary := newSummary(chatID, rec)
	if summary.BurstSeconds != 0.4 {
		t.Fatalf("burst_seconds = %v, want 0.4", summary.BurstSeconds)
	}
	if summary.Name != "Ada" || summary.Username == nil || *summary.Username != "ada" {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestWaitForSettledMessageConsumesBurst(t *testing.T) {
	env := newTestEnv(t)
	env.clock.set(0)
	env.deliver(101, 7, "Ada", "")
	env.clock.advance(1200 * time.Millisecond)

	settle, maxWait := 1000, 500
	out, err := handleWaitForSettledMessage(context.Background(), waitForSettledMessageInput{SettleMS: &settle, MaxWaitMS: &maxWait})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	got := decode(t, out)
	if got["event"] != true || got["chat_id"].(float64) != 101 || got["message_count"].(float64) != 1 {
		t.Fatalf("settled result = %s", out)
	}

	// Consumed: the next call finds nothing and times out.
	out, err = handleWaitForSettledMessage(context.Background(), waitForSettledMessageInput{SettleMS: &settle, MaxWaitMS: &maxWait})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if got := decode(t, out); got["event"] != false || got["reason"] != "timeout" {
		t.Fatalf("second settled call = %s, want a timeout", out)
	}
}

// With a target, an unrelated settled chat must not end the wait.
func TestWaitForSettledMessageHonoursTarget(t *testing.T) {
	env := newTestEnv(t)
	env.clock.set(0)
	env.deliver(101, 7, "Ada", "")
	env.clock.advance(1200 * time.Millisecond)

	settle, maxWait := 1000, 200
	out, err := handleWaitForSettledMessage(context.Background(), waitForSettledMessageInput{
		SettleMS: &settle, MaxWaitMS: &maxWait, ChatID: int64(202),
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	got := decode(t, out)
	if got["event"] != false {
		t.Fatalf("targeted wait = %s, want a timeout while only chat 101 is settled", out)
	}
	if got["waiting_for"].(float64) != 202 {
		t.Fatalf("waiting_for = %v, want 202", got["waiting_for"])
	}
}

// A chat_id given as a username goes through the injected entity source, the
// same path kit.Resolver serves in production.
func TestWaitForSettledMessageResolvesUsername(t *testing.T) {
	source := newFakeSource(map[string]fakeEntity{
		"bob": {id: 202, kind: kit.PeerUser, username: "bob"},
	})
	env := newTestEnv(t, func(d *Deps) { d.Resolver = kit.NewResolver(source) })
	env.clock.set(0)
	env.deliver(101, 7, "Ada", "")
	env.deliver(202, 9, "Bob", "bob")
	env.clock.advance(1200 * time.Millisecond)

	settle, maxWait := 1000, 500
	out, err := handleWaitForSettledMessage(context.Background(), waitForSettledMessageInput{
		SettleMS: &settle, MaxWaitMS: &maxWait, ChatID: "bob",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	got := decode(t, out)
	if got["chat_id"].(float64) != 202 {
		t.Fatalf("resolved wait = %s, want chat 202", out)
	}
	if source.lookups == 0 {
		t.Fatal("the injected entity source was never consulted")
	}
}

// Without a resolver a non-numeric chat_id is reported through the funnel
// instead of panicking or silently waiting for the wrong chat.
func TestWaitForNewMessageWithoutResolverReportsError(t *testing.T) {
	newTestEnv(t)

	timeout := 0.01
	out, err := handleWaitForNewMessage(context.Background(), waitForNewMessageInput{Timeout: &timeout, ChatID: "someone"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(out, "An error occurred (code:") {
		t.Fatalf("unresolvable chat_id = %s, want a funnel error string", out)
	}
}

// env0Errors returns the recorded funnel errors of the current instance.
func env0Errors(t *testing.T) []string {
	t.Helper()
	rep, _ := active().Reporter.(*recordingReporter)
	if rep == nil {
		t.Fatal("test reporter missing: the module bypassed Deps.Reporter")
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	return slices.Clone(rep.errors)
}

// The funnel must have reported the failure even though the returned string is
// the generic code: nothing is swallowed silently.
func TestUnresolvableChatIDReportsThroughFunnel(t *testing.T) {
	source := newFakeSource(map[string]fakeEntity{})
	newTestEnv(t, func(d *Deps) { d.Resolver = kit.NewResolver(source) })

	timeout := 0.01
	out, err := handleWaitForNewMessage(context.Background(), waitForNewMessageInput{Timeout: &timeout, ChatID: "ghost"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(out, "An error occurred (code:") {
		t.Fatalf("unknown username = %s, want a funnel error string", out)
	}
	if len(env0Errors(t)) == 0 {
		t.Fatal("the funnel did not report the failure")
	}
	if source.warmCount() == 0 {
		t.Fatal("resolution failure must warm the entity cache before giving up")
	}
}

// ---------------------------------------------------------------------------
// incoming_feed_status / enable / disable
// ---------------------------------------------------------------------------

func TestIncomingFeedStatusDefaults(t *testing.T) {
	env := newTestEnv(t)

	out, err := handleIncomingFeedStatus(context.Background(), struct{}{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	got := decode(t, out)
	if got["enabled"] != false {
		t.Fatalf("enabled = %v, want false by default (%s)", got["enabled"], out)
	}
	if got["feed_file"] != env.feedFile {
		t.Fatalf("feed_file = %v, want %q", got["feed_file"], env.feedFile)
	}
	if got["settle_ms"].(float64) != float64(defaultSettleMS) {
		t.Fatalf("settle_ms = %v, want %d", got["settle_ms"], defaultSettleMS)
	}
	if got["autostart_pending"] != false {
		t.Fatalf("autostart_pending = %v, want false without TELEGRAM_EVENT_FEED", got["autostart_pending"])
	}
	if !strings.HasPrefix(got["watch_command"].(string), "tail -n 0 -F ") {
		t.Fatalf("watch_command = %v", got["watch_command"])
	}
	oneChat := got["watch_command_for_one_chat"].(string)
	if !strings.Contains(oneChat, `grep --line-buffered '"chat_id": <ID>'`) {
		t.Fatalf("watch_command_for_one_chat = %v", oneChat)
	}
}

func TestFeedLifecycleEnableDisable(t *testing.T) {
	env := newTestEnv(t)

	settle := 250
	out, err := handleEnableIncomingFeed(context.Background(), enableIncomingFeedInput{SettleMS: &settle})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	got := decode(t, out)
	if got["enabled"] != true || got["settle_ms"].(float64) != 250 {
		t.Fatalf("enable result = %s", out)
	}
	t.Cleanup(func() { active().tracker.stopFeed() })

	info, err := os.Stat(env.feedFile)
	if err != nil {
		t.Fatalf("feed file was not created: %v", err)
	}
	// The feed holds private contact metadata.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("feed file mode = %o, want 600", perm)
	}

	// Idempotent with the same settle_ms: enabled stays on.
	if out, _ = handleEnableIncomingFeed(context.Background(), enableIncomingFeedInput{SettleMS: &settle}); !strings.Contains(out, `"enabled":true`) {
		t.Fatalf("re-enable = %s", out)
	}

	if out, err = handleDisableIncomingFeed(context.Background(), struct{}{}); err != nil || out != feedDisabledMessage {
		t.Fatalf("disable = %q (%v), want %q", out, err, feedDisabledMessage)
	}
	if out, _ = handleIncomingFeedStatus(context.Background(), struct{}{}); !strings.Contains(out, `"enabled":false`) {
		t.Fatalf("status after disable = %s", out)
	}
	// Disabling twice is a no-op with the documented message.
	if out, err = handleDisableIncomingFeed(context.Background(), struct{}{}); err != nil || out != feedNotEnabledMessage {
		t.Fatalf("second disable = %q (%v), want %q", out, err, feedNotEnabledMessage)
	}
}

// The running feed appends settled bursts as JSON lines.
func TestFeedAppendsSettledBursts(t *testing.T) {
	env := newTestEnv(t)

	settle := 0
	if _, err := handleEnableIncomingFeed(context.Background(), enableIncomingFeedInput{SettleMS: &settle}); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	t.Cleanup(func() { active().tracker.stopFeed() })

	env.clock.set(0)
	env.deliver(101, 7, "Ada", "ada")

	line := waitForFeedLine(t, env.feedFile, 2*time.Second)
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("feed line is not JSON: %v (%s)", err, line)
	}
	if got["chat_id"].(float64) != 101 || got["message_count"].(float64) != 1 {
		t.Fatalf("feed line = %s", line)
	}
	if _, hasEvent := got["event"]; hasEvent {
		t.Fatalf("feed line must not carry the event marker: %s", line)
	}
	if got["ts"] == nil || got["burst_seconds"] == nil {
		t.Fatalf("feed line is missing ts/burst_seconds: %s", line)
	}

	// The burst was consumed by the feed, so a settled wait finds nothing.
	waitMS, maxWaitMS := 0, 10
	if out, _ := handleWaitForSettledMessage(context.Background(), waitForSettledMessageInput{
		SettleMS: &waitMS, MaxWaitMS: &maxWaitMS,
	}); !strings.Contains(out, `"reason":"timeout"`) {
		t.Fatalf("feed did not consume the burst: %s", out)
	}
}

// waitForFeedLine waits for the consumer to publish the next feed line.
func waitForFeedLine(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if strings.TrimSpace(line) != "" {
					return line
				}
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no feed line appeared in %s within %s", path, timeout)
	return ""
}

// An explicit TELEGRAM_EVENT_FEED_FILE path is used verbatim and its parent is
// never created, so a typo fails loudly through the funnel.
func TestExplicitFeedFileMustExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent-dir", "feed.jsonl")
	newTestEnv(t, func(d *Deps) { d.FeedFile = missing })

	out, err := handleEnableIncomingFeed(context.Background(), enableIncomingFeedInput{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(out, "An error occurred (code:") {
		t.Fatalf("enable with a missing directory = %s, want a funnel error", out)
	}
	if _, statErr := os.Stat(filepath.Dir(missing)); statErr == nil {
		t.Fatal("the module created a directory for an explicit feed path")
	}
}

// TELEGRAM_EVENT_FEED starts the feed on the first incoming message, and only
// once: an explicit disable consumes the pending autostart.
func TestFeedAutostartsOnFirstIncomingMessage(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.EventFeed = true })

	status, err := handleIncomingFeedStatus(context.Background(), struct{}{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(status, `"autostart_pending":true`) {
		t.Fatalf("status = %s, want autostart_pending", status)
	}

	env.clock.set(0)
	env.deliver(101, 7, "Ada", "")
	t.Cleanup(func() { active().tracker.stopFeed() })

	deadline := time.Now().Add(2 * time.Second)
	for {
		if enabled, _, _ := active().tracker.feedStatus(active().Deps); enabled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the feed did not autostart on the first incoming message")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// An explicit disable consumes the autostart, so the next message must not
	// resurrect the feed the user turned off.
	if out, err := handleDisableIncomingFeed(context.Background(), struct{}{}); err != nil || out != feedDisabledMessage {
		t.Fatalf("disable = %q (%v)", out, err)
	}
	env.deliver(101, 8, "Ada", "")
	if enabled, _, _ := active().tracker.feedStatus(active().Deps); enabled {
		t.Fatal("the feed restarted after an explicit disable")
	}
	status, _ = handleIncomingFeedStatus(context.Background(), struct{}{})
	if !strings.Contains(status, `"autostart_pending":false`) {
		t.Fatalf("status after disable = %s, want autostart consumed", status)
	}
}

// ---------------------------------------------------------------------------
// Recorder filtering and JSON shapes
// ---------------------------------------------------------------------------

func TestOnIncomingIgnoresNonHumanAndNonPrivateMessages(t *testing.T) {
	env := newTestEnv(t)
	tr := active().tracker
	env.clock.set(0)

	cases := map[string]Incoming{
		"channel message": {ChatID: 101, MessageID: 1, Private: false, SenderKnown: true},
		"unknown sender":  {ChatID: 101, MessageID: 2, Private: true, SenderKnown: false},
		"bot sender":      {ChatID: 101, MessageID: 3, Private: true, SenderKnown: true, FromBot: true},
		"self message":    {ChatID: 101, MessageID: 4, Private: true, SenderKnown: true, FromSelf: true},
	}
	for _, msg := range cases {
		OnIncoming(msg)
	}
	if chats := tr.pendingChats(nil, nil); len(chats) != 0 {
		t.Fatalf("pending = %+v, want none of %v recorded", chats, keysOfInts(cases))
	}
}

func keysOfInts(m map[string]Incoming) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// The allowlist filters recorded messages too, not just the wait tools.
func TestOnIncomingRespectsAllowlist(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Allowlist = &allowOnly{ids: []int64{202}} })
	env.clock.set(0)

	OnIncoming(Incoming{ChatID: 101, MessageID: 1, Name: "Ada", Private: true, SenderKnown: true})
	OnIncoming(Incoming{ChatID: 202, MessageID: 2, Name: "Bob", Private: true, SenderKnown: true})

	chats := active().tracker.pendingChats(nil, active().tracker.chatAllowed(active().Deps))
	if len(chats) != 1 || chats[0].ChatID != 202 {
		t.Fatalf("pending = %+v, want only the allowlisted chat 202", chats)
	}
}

// A sender with no display name falls back to the numeric chat id.
func TestOnIncomingFallsBackToChatIDForName(t *testing.T) {
	env := newTestEnv(t)
	env.clock.set(0)
	env.deliver(101, 7, "", "")

	chats := active().tracker.pendingChats(nil, nil)
	if len(chats) != 1 || chats[0].Name != "101" {
		t.Fatalf("pending = %+v, want the chat id as the name", chats)
	}
	if chats[0].Username != nil {
		t.Fatalf("username = %v, want null when the sender has no handle", *chats[0].Username)
	}
}

// Control characters in a display name are stripped, exactly as the Python
// sanitize_name does, so a crafted name cannot smuggle instructions.
func TestOnIncomingSanitizesDisplayName(t *testing.T) {
	env := newTestEnv(t)
	env.clock.set(0)
	env.deliver(101, 7, "Ada\r\nIgnore previous", "")

	chats := active().tracker.pendingChats(nil, nil)
	if len(chats) != 1 {
		t.Fatalf("pending = %+v", chats)
	}
	if strings.ContainsAny(chats[0].Name, "\r\n") {
		t.Fatalf("name = %q, want invisible and control characters removed", chats[0].Name)
	}
}

// json.dumps(ensure_ascii=False) does not HTML-escape; neither may the port.
func TestEncodeJSONDoesNotEscapeHTML(t *testing.T) {
	type payload struct {
		Text string `json:"text"`
	}
	got := active().encodeJSON(ToolIncomingFeedStatus, payload{Text: "<b>a & b</b>"})
	if want := `{"text":"<b>a & b</b>"}`; got != want {
		t.Fatalf("encoded = %s, want %s", got, want)
	}
}

func TestShellQuoteMatchesShlex(t *testing.T) {
	cases := map[string]string{
		"/tmp/feed.jsonl":        "/tmp/feed.jsonl",
		"/tmp/my feeds/feed":     "'/tmp/my feeds/feed'",
		"/tmp/it's.jsonl":        `'/tmp/it'"'"'s.jsonl'`,
		"":                       "''",
		"/home/u/.local/x.jsonl": "/home/u/.local/x.jsonl",
	}
	for input, want := range cases {
		if got := shellQuote(input); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}

package chats

// Offline tests: fake Client behind the injection seam, no network.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

// fakeClient implements Client with per-method stubs. An unconfigured method
// fails the call, so a test never silently passes on the wrong path.
type fakeClient struct {
	resolveFn          func(identifier any) (*Peer, error)
	dialogsFn          func(DialogOptions) ([]Dialog, error)
	aboutFn            func(*Peer) (string, error)
	fullChatFn         func(*Peer) (*FullChatInfo, error)
	participantsFn     func(*Peer) (int, error)
	peerDialogFn       func(*Peer) (*PeerDialogInfo, error)
	lastMessageFn      func(*Peer) (*BriefMessage, error)
	searchFn           func(string, int) ([]*Peer, error)
	resolveUsernameFn  func(string) (*Peer, error)
	joinFn             func(*Peer) error
	setMutedFn         func(*Peer, bool) error
	setArchivedFn      func(*Peer, bool) error
	commonChatsFn      func(*Peer, int64, int) ([]*Peer, error)
	toggleForumFn      func(*Peer, bool, bool) error
	forumTopicsFn      func(*Peer, ForumTopicsOptions) (*ForumTopicsResult, error)
	createTopicFn      func(*Peer, string, *int64, *int64) (*TopicRef, error)
	editTopicFn        func(*Peer, int32, ForumTopicEdit) error
	deleteTopicFn      func(*Peer, int32) (int32, error)
	readParticipantsFn func(*Peer, int32) ([]ReadParticipant, error)
	exportLinkFn       func(*Peer, int32, bool) (*MessageLink, error)

	lastAccount       string
	dialogOptions     []DialogOptions
	joinCalls         []*Peer
	toggleForumCalls  []bool
	setMutedCalls     []bool
	setArchivedCalls  []bool
	editTopicCalls    []ForumTopicEdit
	deleteTopicCalls  []int32
	searchCalls       []string
	commonChatsLimits []int
	linkThreadCalls   []bool
}

func unconfigured(name string) error { return fmt.Errorf("fakeClient.%s not configured", name) }

func (f *fakeClient) Resolve(_ context.Context, identifier any) (*Peer, error) {
	if f.resolveFn == nil {
		return nil, unconfigured("Resolve")
	}
	return f.resolveFn(identifier)
}

func (f *fakeClient) Dialogs(_ context.Context, opts DialogOptions) ([]Dialog, error) {
	f.dialogOptions = append(f.dialogOptions, opts)
	if f.dialogsFn == nil {
		return nil, unconfigured("Dialogs")
	}
	return f.dialogsFn(opts)
}

func (f *fakeClient) About(_ context.Context, peer *Peer) (string, error) {
	if f.aboutFn == nil {
		return "", unconfigured("About")
	}
	return f.aboutFn(peer)
}

func (f *fakeClient) FullChat(_ context.Context, peer *Peer) (*FullChatInfo, error) {
	if f.fullChatFn == nil {
		return nil, unconfigured("FullChat")
	}
	return f.fullChatFn(peer)
}

func (f *fakeClient) ParticipantsCount(_ context.Context, peer *Peer) (int, error) {
	if f.participantsFn == nil {
		return 0, unconfigured("ParticipantsCount")
	}
	return f.participantsFn(peer)
}

func (f *fakeClient) PeerDialog(_ context.Context, peer *Peer) (*PeerDialogInfo, error) {
	if f.peerDialogFn == nil {
		return nil, unconfigured("PeerDialog")
	}
	return f.peerDialogFn(peer)
}

func (f *fakeClient) LastMessage(_ context.Context, peer *Peer) (*BriefMessage, error) {
	if f.lastMessageFn == nil {
		return nil, unconfigured("LastMessage")
	}
	return f.lastMessageFn(peer)
}

func (f *fakeClient) SearchChats(_ context.Context, query string, limit int) ([]*Peer, error) {
	f.searchCalls = append(f.searchCalls, query)
	if f.searchFn == nil {
		return nil, unconfigured("SearchChats")
	}
	return f.searchFn(query, limit)
}

func (f *fakeClient) ResolveUsername(_ context.Context, username string) (*Peer, error) {
	if f.resolveUsernameFn == nil {
		return nil, unconfigured("ResolveUsername")
	}
	return f.resolveUsernameFn(username)
}

func (f *fakeClient) Join(_ context.Context, peer *Peer) error {
	f.joinCalls = append(f.joinCalls, peer)
	if f.joinFn == nil {
		return unconfigured("Join")
	}
	return f.joinFn(peer)
}

func (f *fakeClient) SetMuted(_ context.Context, peer *Peer, muted bool) error {
	f.setMutedCalls = append(f.setMutedCalls, muted)
	if f.setMutedFn == nil {
		return unconfigured("SetMuted")
	}
	return f.setMutedFn(peer, muted)
}

func (f *fakeClient) SetArchived(_ context.Context, peer *Peer, archived bool) error {
	f.setArchivedCalls = append(f.setArchivedCalls, archived)
	if f.setArchivedFn == nil {
		return unconfigured("SetArchived")
	}
	return f.setArchivedFn(peer, archived)
}

func (f *fakeClient) CommonChats(_ context.Context, peer *Peer, maxID int64, limit int) ([]*Peer, error) {
	f.commonChatsLimits = append(f.commonChatsLimits, limit)
	if f.commonChatsFn == nil {
		return nil, unconfigured("CommonChats")
	}
	return f.commonChatsFn(peer, maxID, limit)
}

func (f *fakeClient) ToggleForum(_ context.Context, peer *Peer, enabled, tabs bool) error {
	f.toggleForumCalls = append(f.toggleForumCalls, tabs)
	if f.toggleForumFn == nil {
		return unconfigured("ToggleForum")
	}
	return f.toggleForumFn(peer, enabled, tabs)
}

func (f *fakeClient) ForumTopics(_ context.Context, peer *Peer, opts ForumTopicsOptions) (*ForumTopicsResult, error) {
	if f.forumTopicsFn == nil {
		return nil, unconfigured("ForumTopics")
	}
	return f.forumTopicsFn(peer, opts)
}

func (f *fakeClient) CreateForumTopic(_ context.Context, peer *Peer, title string, iconColor, iconEmojiID *int64) (*TopicRef, error) {
	if f.createTopicFn == nil {
		return nil, unconfigured("CreateForumTopic")
	}
	return f.createTopicFn(peer, title, iconColor, iconEmojiID)
}

func (f *fakeClient) EditForumTopic(_ context.Context, peer *Peer, topicID int32, edit ForumTopicEdit) error {
	f.editTopicCalls = append(f.editTopicCalls, edit)
	if f.editTopicFn == nil {
		return unconfigured("EditForumTopic")
	}
	return f.editTopicFn(peer, topicID, edit)
}

func (f *fakeClient) DeleteTopicHistory(_ context.Context, peer *Peer, topicID int32) (int32, error) {
	f.deleteTopicCalls = append(f.deleteTopicCalls, topicID)
	if f.deleteTopicFn == nil {
		return 0, unconfigured("DeleteTopicHistory")
	}
	return f.deleteTopicFn(peer, topicID)
}

func (f *fakeClient) ReadParticipants(_ context.Context, peer *Peer, messageID int32) ([]ReadParticipant, error) {
	if f.readParticipantsFn == nil {
		return nil, unconfigured("ReadParticipants")
	}
	return f.readParticipantsFn(peer, messageID)
}

func (f *fakeClient) ExportMessageLink(_ context.Context, peer *Peer, messageID int32, thread bool) (*MessageLink, error) {
	f.linkThreadCalls = append(f.linkThreadCalls, thread)
	if f.exportLinkFn == nil {
		return nil, unconfigured("ExportMessageLink")
	}
	return f.exportLinkFn(peer, messageID, thread)
}

var _ Client = (*fakeClient)(nil)

func installClient(t *testing.T, f *fakeClient) *fakeClient {
	t.Helper()
	clientProviderMu.Lock()
	prev := clientProvider
	clientProvider = func(_ context.Context, account string) (Client, error) {
		f.lastAccount = account
		return f, nil
	}
	clientProviderMu.Unlock()
	t.Cleanup(func() {
		clientProviderMu.Lock()
		clientProvider = prev
		clientProviderMu.Unlock()
	})
	return f
}

type fakeAllowlist struct {
	enabled   bool
	ids       map[int64]bool
	usernames map[string]bool
}

func (a *fakeAllowlist) AllowsChatID(id int64) bool { return a.ids[id] }
func (a *fakeAllowlist) AllowsUsername(handle string) bool {
	return a.usernames[strings.ToLower(strings.TrimPrefix(handle, "@"))]
}
func (a *fakeAllowlist) Enabled() bool { return a.enabled }

func installAllowlist(t *testing.T, a kit.ChatAllowlist) {
	t.Helper()
	allowlistMu.Lock()
	prev := chatAllowlist
	chatAllowlist = a
	allowlistMu.Unlock()
	t.Cleanup(func() {
		allowlistMu.Lock()
		chatAllowlist = prev
		allowlistMu.Unlock()
	})
}

func ptr[T any](v T) *T { return &v }

func userPeer(id int64, first, last string) *Peer {
	return &Peer{ID: id, Kind: kit.PeerUser, FirstName: first, LastName: last}
}

func supergroupPeer(id int64, title string, forum bool) *Peer {
	return &Peer{ID: id, Kind: kit.PeerSupergroup, Title: title, Forum: forum, Megagroup: true}
}

func parseObject(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("result is not a JSON object: %v\n%s", err, s)
	}
	return out
}

func parseArray(t *testing.T, s string) []any {
	t.Helper()
	var out []any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("result is not a JSON array: %v\n%s", err, s)
	}
	return out
}

func requireString(t *testing.T, got, want, label string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

// chatToolNames and chatReadOnly mirror the inventory entries and readonly
// flags of module "chats".
var chatToolNames = []string{
	"archive_chat", "create_forum_topic", "delete_forum_topic", "edit_forum_topic",
	"enable_forum_topics", "get_chat", "get_chats", "get_common_chats", "get_full_chat",
	"get_message_link", "get_message_read_by", "list_chats", "list_topics", "mute_chat",
	"resolve_username", "search_public_chats", "subscribe_public_channel",
	"unarchive_chat", "unmute_chat",
}

var chatReadOnly = map[string]bool{
	"archive_chat": false, "create_forum_topic": false, "delete_forum_topic": false,
	"edit_forum_topic": false, "enable_forum_topics": false, "get_chat": true,
	"get_chats": true, "get_common_chats": true, "get_full_chat": true,
	"get_message_link": true, "get_message_read_by": true, "list_chats": true,
	"list_topics": true, "mute_chat": false, "resolve_username": true,
	"search_public_chats": true, "subscribe_public_channel": false,
	"unarchive_chat": false, "unmute_chat": false,
}

// TestToolsRegisterAndAnnotate asserts every chats tool name is registered
// with the inventory's readonly flag; Build also infers every input schema,
// so a malformed struct tag panics here.
func TestToolsRegisterAndAnnotate(t *testing.T) {
	srv, err := mcpserver.Build(mcpserver.DefaultRegistry, mcpserver.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var listing bytes.Buffer
	if err := srv.DryRun(&listing); err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	var entries []struct {
		Name            string `json:"name"`
		ReadOnlyHint    bool   `json:"readOnlyHint"`
		OpenWorldHint   bool   `json:"openWorldHint"`
		DestructiveHint bool   `json:"destructiveHint"`
	}
	if err := json.Unmarshal(listing.Bytes(), &entries); err != nil {
		t.Fatalf("dry-run listing: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name)
		if want, ok := chatReadOnly[entry.Name]; !ok {
			t.Errorf("unexpected tool %q served", entry.Name)
		} else if entry.ReadOnlyHint != want {
			t.Errorf("%s readOnlyHint = %v, want %v", entry.Name, entry.ReadOnlyHint, want)
		}
		if !entry.OpenWorldHint {
			t.Errorf("%s openWorldHint = false, want true", entry.Name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(chatToolNames, ",") {
		t.Errorf("registered tools = %v, want %v", got, chatToolNames)
	}
}

func TestDefaultProviderReturnsFormattedError(t *testing.T) {
	SetClientProvider(nil)
	t.Cleanup(func() { SetClientProvider(nil) })
	got, err := handleGetChats(context.Background(), GetChatsInput{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(got, "code: CHAT-ERR-") {
		t.Errorf("default-provider result = %q, want a CHAT-ERR code", got)
	}
}

func TestAccountFromContextReachesProvider(t *testing.T) {
	f := installClient(t, &fakeClient{dialogsFn: func(DialogOptions) ([]Dialog, error) {
		return []Dialog{{Peer: userPeer(1, "A", "")}}, nil
	}})
	ctx := WithAccount(context.Background(), "work")
	if _, err := handleGetChats(ctx, GetChatsInput{}); err != nil {
		t.Fatal(err)
	}
	requireString(t, f.lastAccount, "work", "provider account")
}

func TestGetChatsPagination(t *testing.T) {
	f := installClient(t, &fakeClient{dialogsFn: func(DialogOptions) ([]Dialog, error) {
		return []Dialog{
			{Peer: userPeer(1, "One", "")},
			{Peer: userPeer(2, "Two", "")},
			{Peer: userPeer(3, "Three", "")},
		}, nil
	}})
	_ = f
	page1, err := handleGetChats(context.Background(), GetChatsInput{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	results := parseObject(t, page1)["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("page 1 has %d results, want 2", len(results))
	}
	page2, _ := handleGetChats(context.Background(), GetChatsInput{Page: 2, PageSize: 2})
	results = parseObject(t, page2)["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("page 2 has %d results, want 1", len(results))
	}
	outOfRange, _ := handleGetChats(context.Background(), GetChatsInput{Page: 9, PageSize: 2})
	requireString(t, outOfRange, "Page out of range.", "out-of-range page")
}

func TestGetChatsSanitizesAndFiltersAllowlist(t *testing.T) {
	installAllowlist(t, &fakeAllowlist{enabled: true, ids: map[int64]bool{2: true}})
	installClient(t, &fakeClient{dialogsFn: func(DialogOptions) ([]Dialog, error) {
		return []Dialog{
			{Peer: &Peer{ID: 1, Kind: kit.PeerUser, FirstName: "Blocked"}},
			{Peer: &Peer{ID: 2, Kind: kit.PeerUser, FirstName: "Evil\u200bName"}},
		}, nil
	}})
	got, err := handleGetChats(context.Background(), GetChatsInput{})
	if err != nil {
		t.Fatal(err)
	}
	results := parseObject(t, got)["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("allowlisted result count = %d, want 1", len(results))
	}
	record := results[0].(map[string]any)
	if record["chat_id"].(float64) != 2 {
		t.Errorf("chat_id = %v, want 2", record["chat_id"])
	}
	requireString(t, record["title"].(string), "EvilName", "sanitized title")
}

func TestSubscribePublicChannel(t *testing.T) {
	group := supergroupPeer(12345, "Forum Group", false)
	f := installClient(t, &fakeClient{
		resolveFn: func(any) (*Peer, error) { return group, nil },
		joinFn:    func(*Peer) error { return nil },
	})
	got, err := handleSubscribePublicChannel(context.Background(), SubscribePublicChannelInput{Channel: int64(12345)})
	if err != nil {
		t.Fatal(err)
	}
	requireString(t, got, "Subscribed to Forum Group.", "subscribe success")
	if len(f.joinCalls) != 1 || f.joinCalls[0] != group {
		t.Errorf("join calls = %v, want the resolved peer", f.joinCalls)
	}

	f.joinFn = func(*Peer) error { return fmt.Errorf("rpc: %w", ErrAlreadyParticipant) }
	got, _ = handleSubscribePublicChannel(context.Background(), SubscribePublicChannelInput{Channel: int64(12345)})
	requireString(t, got, "Already subscribed to Forum Group.", "already participant")

	f.joinFn = func(*Peer) error { return ErrChannelPrivate }
	got, _ = handleSubscribePublicChannel(context.Background(), SubscribePublicChannelInput{Channel: int64(12345)})
	requireString(t, got, "Cannot subscribe: this channel is private or requires an invite link.", "private channel")
}

func TestListTopicsFillsRecords(t *testing.T) {
	group := supergroupPeer(12345, "Forum Group", true)
	activity := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	installClient(t, &fakeClient{
		resolveFn: func(any) (*Peer, error) { return group, nil },
		forumTopicsFn: func(_ *Peer, opts ForumTopicsOptions) (*ForumTopicsResult, error) {
			if opts.Limit != 200 || opts.OffsetTopic != 0 || opts.Query != "" {
				t.Errorf("forum options = %+v, want defaults", opts)
			}
			return &ForumTopicsResult{
				Topics: []ForumTopic{
					{ID: 1, Title: "General", TotalMessages: ptr(10), UnreadCount: ptr(2), TopMessageID: 77},
					{ID: 2, Title: "", UnreadCount: ptr(0), Closed: true},
				},
				MessageDates: map[int32]time.Time{77: activity},
			}, nil
		},
	})
	got, err := handleListTopics(context.Background(), ListTopicsInput{ChatID: int64(12345)})
	if err != nil {
		t.Fatal(err)
	}
	results := parseObject(t, got)["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("topic count = %d, want 2", len(results))
	}
	first := results[0].(map[string]any)
	requireString(t, first["title"].(string), "General", "topic title")
	if first["total_messages"].(float64) != 10 || first["unread"].(float64) != 2 {
		t.Errorf("topic counters = %v", first)
	}
	if _, ok := first["last_activity"]; !ok {
		t.Errorf("last_activity missing from %v", first)
	}
	second := results[1].(map[string]any)
	requireString(t, second["title"].(string), "(no title)", "untitled topic")
	if _, ok := second["unread"]; ok {
		t.Errorf("zero unread must be omitted: %v", second)
	}
	if second["closed"] != true {
		t.Errorf("closed = %v, want true", second["closed"])
	}
}

func TestListTopicsRejectsNonForumChats(t *testing.T) {
	f := installClient(t, &fakeClient{resolveFn: func(any) (*Peer, error) {
		return &Peer{ID: 7, Kind: kit.PeerChannel, Title: "Broadcast", Forum: false}, nil
	}})
	got, _ := handleListTopics(context.Background(), ListTopicsInput{ChatID: int64(7)})
	requireString(t, got, "The specified chat is not a supergroup.", "broadcast channel")

	f.resolveFn = func(any) (*Peer, error) { return supergroupPeer(7, "Plain", false), nil }
	got, _ = handleListTopics(context.Background(), ListTopicsInput{ChatID: int64(7)})
	requireString(t, got, "The specified supergroup does not have forum topics enabled.", "forum disabled")

	f.resolveFn = func(any) (*Peer, error) { return supergroupPeer(7, "Plain", true), nil }
	f.forumTopicsFn = func(*Peer, ForumTopicsOptions) (*ForumTopicsResult, error) {
		return &ForumTopicsResult{}, nil
	}
	got, _ = handleListTopics(context.Background(), ListTopicsInput{ChatID: int64(7)})
	requireString(t, got, "No topics found for this chat.", "no topics")
}

func TestEnableForumTopics(t *testing.T) {
	group := supergroupPeer(7, "Plain", false)
	f := installClient(t, &fakeClient{
		resolveFn:     func(any) (*Peer, error) { return group, nil },
		toggleForumFn: func(*Peer, bool, bool) error { return nil },
	})
	got, err := handleEnableForumTopics(context.Background(), EnableForumTopicsInput{ChatID: int64(7)})
	if err != nil {
		t.Fatal(err)
	}
	requireString(t, got, "Forum topics enabled for Plain.", "enable result")
	if len(f.toggleForumCalls) != 1 || f.toggleForumCalls[0] != true {
		t.Errorf("toggle tabs = %v, want the Python default true", f.toggleForumCalls)
	}

	group.Forum = true
	got, _ = handleEnableForumTopics(context.Background(), EnableForumTopicsInput{ChatID: int64(7)})
	requireString(t, got, "Forum topics already enabled for Plain.", "already enabled")
}

func TestCreateForumTopic(t *testing.T) {
	group := supergroupPeer(12345, "Forum Group", true)
	installClient(t, &fakeClient{
		resolveFn: func(any) (*Peer, error) { return group, nil },
		createTopicFn: func(_ *Peer, title string, iconColor, iconEmojiID *int64) (*TopicRef, error) {
			requireString(t, title, "CleanTitle", "sanitized create title")
			if iconColor == nil || *iconColor != 5 || iconEmojiID != nil {
				t.Errorf("icon args = %v, %v", iconColor, iconEmojiID)
			}
			return &TopicRef{ID: ptr(int32(42))}, nil
		},
	})
	got, err := handleCreateForumTopic(context.Background(), CreateForumTopicInput{
		ChatID:    int64(12345),
		Title:     "Clean\u200bTitle",
		IconColor: ptr(int64(5)),
	})
	if err != nil {
		t.Fatal(err)
	}
	record := parseObject(t, got)["results"].([]any)[0].(map[string]any)
	if record["chat_id"].(float64) != -1000000000000-12345 {
		t.Errorf("chat_id = %v", record["chat_id"])
	}
	if record["topic_id"].(float64) != 42 {
		t.Errorf("topic_id = %v, want 42", record["topic_id"])
	}
	requireString(t, record["title"].(string), "CleanTitle", "record title")
}

func TestCreateForumTopicRequiresForum(t *testing.T) {
	installClient(t, &fakeClient{resolveFn: func(any) (*Peer, error) {
		return supergroupPeer(7, "Plain", false), nil
	}})
	got, _ := handleCreateForumTopic(context.Background(), CreateForumTopicInput{ChatID: int64(7), Title: "t"})
	want := "The specified supergroup does not have forum topics enabled. Use enable_forum_topics first."
	requireString(t, got, want, "forum disabled")
}

func TestEditForumTopic(t *testing.T) {
	group := supergroupPeer(9, "Forum", true)
	f := installClient(t, &fakeClient{
		resolveFn:   func(any) (*Peer, error) { return group, nil },
		editTopicFn: func(*Peer, int32, ForumTopicEdit) error { return nil },
	})
	empty, _ := handleEditForumTopic(context.Background(), EditForumTopicInput{ChatID: int64(9), TopicID: 4})
	requireString(t, empty, "Nothing to change: pass title, icon_emoji_id, closed or hidden.", "no changes")

	got, err := handleEditForumTopic(context.Background(), EditForumTopicInput{
		ChatID:      int64(9),
		TopicID:     4,
		Title:       ptr("New\u200bTitle"),
		IconEmojiID: ptr(int64(0)),
		Closed:      ptr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.editTopicCalls) != 1 {
		t.Fatalf("edit calls = %d, want 1", len(f.editTopicCalls))
	}
	call := f.editTopicCalls[0]
	requireString(t, *call.Title, "NewTitle", "sanitized edit title")
	if call.IconEmojiID == nil || *call.IconEmojiID != 0 || call.Closed == nil || !*call.Closed {
		t.Errorf("edit fields = %+v", call)
	}
	record := parseObject(t, got)["results"].([]any)[0].(map[string]any)
	if record["topic_id"].(float64) != 4 || record["closed"] != true {
		t.Errorf("record = %v", record)
	}
	if _, ok := record["hidden"]; ok {
		t.Errorf("unchanged fields must be omitted: %v", record)
	}
}

func TestDeleteForumTopicBatches(t *testing.T) {
	group := supergroupPeer(9, "Forum", true)
	offsets := []int32{7, 0}
	installClient(t, &fakeClient{
		resolveFn:     func(any) (*Peer, error) { return group, nil },
		deleteTopicFn: func(*Peer, int32) (int32, error) { o := offsets[0]; offsets = offsets[1:]; return o, nil },
	})
	got, err := handleDeleteForumTopic(context.Background(), DeleteForumTopicInput{ChatID: int64(9), TopicID: 4})
	if err != nil {
		t.Fatal(err)
	}
	record := parseObject(t, got)["results"].([]any)[0].(map[string]any)
	if record["batches"].(float64) != 2 || record["deleted"] != true {
		t.Errorf("record = %v", record)
	}
}

func TestDeleteForumTopicBounded(t *testing.T) {
	group := supergroupPeer(9, "Forum", true)
	f := installClient(t, &fakeClient{
		resolveFn:     func(any) (*Peer, error) { return group, nil },
		deleteTopicFn: func(*Peer, int32) (int32, error) { return 1, nil },
	})
	got, _ := handleDeleteForumTopic(context.Background(), DeleteForumTopicInput{ChatID: int64(9), TopicID: 4})
	want := fmt.Sprintf("Topic 4 is still being deleted after %d batches; call delete_forum_topic again to continue.", deleteTopicMaxBatches)
	requireString(t, got, want, "batch cap")
	if len(f.deleteTopicCalls) != deleteTopicMaxBatches {
		t.Errorf("delete calls = %d, want %d", len(f.deleteTopicCalls), deleteTopicMaxBatches)
	}
}

func listChatFixtures() []Dialog {
	return []Dialog{
		{Peer: userPeer(1, "Alice", "Smith"), UnreadCount: 3},
		{Peer: supergroupPeer(2, "Team", false), Muted: true},
		{Peer: &Peer{ID: 3, Kind: kit.PeerChannel, Title: "News"}, Archived: true},
	}
}

func TestListChatsFiltersAndRecords(t *testing.T) {
	installClient(t, &fakeClient{dialogsFn: func(DialogOptions) ([]Dialog, error) { return listChatFixtures(), nil }})
	got, err := handleListChats(context.Background(), ListChatsInput{})
	if err != nil {
		t.Fatal(err)
	}
	results := parseObject(t, got)["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("unfiltered count = %d, want 3", len(results))
	}
	first := results[0].(map[string]any)
	requireString(t, first["name"].(string), "Alice Smith", "user record name")
	requireString(t, first["type"].(string), "User", "user record type")
	if first["unread"].(float64) != 3 {
		t.Errorf("unread = %v", first["unread"])
	}
	second := results[1].(map[string]any)
	requireString(t, second["title"].(string), "Team", "group record title")
	if second["muted"] != true || second["archived"] != false {
		t.Errorf("group record = %v", second)
	}

	unreadOnly, _ := handleListChats(context.Background(), ListChatsInput{UnreadOnly: true})
	if n := len(parseObject(t, unreadOnly)["results"].([]any)); n != 1 {
		t.Errorf("unread_only count = %d, want 1", n)
	}
	unmuted, _ := handleListChats(context.Background(), ListChatsInput{UnmutedOnly: true})
	if n := len(parseObject(t, unmuted)["results"].([]any)); n != 2 {
		t.Errorf("unmuted_only count = %d, want 2", n)
	}
	archived, _ := handleListChats(context.Background(), ListChatsInput{Archived: ptr(true)})
	results = parseObject(t, archived)["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("archived count = %d, want 1", len(results))
	}
	requireString(t, results[0].(map[string]any)["title"].(string), "News", "archived record")

	byType, _ := handleListChats(context.Background(), ListChatsInput{ChatType: "channel"})
	if n := len(parseObject(t, byType)["results"].([]any)); n != 1 {
		t.Errorf("chat_type=channel count = %d, want 1", n)
	}

	noMatch, _ := handleListChats(context.Background(), ListChatsInput{ChatType: "bogus"})
	requireString(t, noMatch, "No chats found matching the criteria.", "no matches")
}

func TestListChatsWithAbout(t *testing.T) {
	f := installClient(t, &fakeClient{
		dialogsFn: func(DialogOptions) ([]Dialog, error) { return listChatFixtures()[:1], nil },
		aboutFn:   func(*Peer) (string, error) { return "About\u200bText", nil },
	})
	got, _ := handleListChats(context.Background(), ListChatsInput{WithAbout: true})
	record := parseObject(t, got)["results"].([]any)[0].(map[string]any)
	requireString(t, record["about"].(string), "AboutText", "about text")

	f.aboutFn = func(*Peer) (string, error) { return "", errors.New("flood") }
	got, _ = handleListChats(context.Background(), ListChatsInput{WithAbout: true})
	record = parseObject(t, got)["results"].([]any)[0].(map[string]any)
	requireString(t, record["about"].(string), "<error fetching description>", "about fallback")
}

func TestGetChatUserRecord(t *testing.T) {
	peer := userPeer(55, "Alice", "Smith")
	peer.Phone = "+1555"
	peer.Bot = false
	peer.Verified = true
	peer.HasPhoto = true
	peer.PhotoID = ptr(int64(9))
	when := time.Date(2026, 10, 5, 10, 30, 0, 0, time.UTC)
	installClient(t, &fakeClient{
		resolveFn: func(any) (*Peer, error) { return peer, nil },
		peerDialogFn: func(*Peer) (*PeerDialogInfo, error) {
			return &PeerDialogInfo{UnreadCount: 5, Archived: false}, nil
		},
		lastMessageFn: func(*Peer) (*BriefMessage, error) {
			return &BriefMessage{SenderName: "Bob", Date: when, Text: "hello\u200bworld"}, nil
		},
	})
	got, err := handleGetChat(context.Background(), GetChatInput{ChatID: int64(55)})
	if err != nil {
		t.Fatal(err)
	}
	record := parseObject(t, got)
	requireString(t, record["name"].(string), "Alice Smith", "name")
	requireString(t, record["type"].(string), "User", "type")
	requireString(t, record["phone"].(string), "+1555", "phone")
	if record["verified"] != true || record["bot"] != false {
		t.Errorf("flags = %v", record)
	}
	if record["unread"].(float64) != 5 || record["archived"] != false {
		t.Errorf("dialog state = %v", record)
	}
	if record["has_photo"] != true || record["current_avatar_id"].(float64) != 9 {
		t.Errorf("photo = %v", record)
	}
	last := record["last_message"].(map[string]any)
	requireString(t, last["sender"].(string), "Bob", "last sender")
	requireString(t, last["text"].(string), "helloworld", "last text")
}

func TestGetChatTitleRecordAndDegradedDialog(t *testing.T) {
	group := supergroupPeer(77, "Team", false)
	installClient(t, &fakeClient{
		resolveFn:      func(any) (*Peer, error) { return group, nil },
		participantsFn: func(*Peer) (int, error) { return 12, nil },
		peerDialogFn:   func(*Peer) (*PeerDialogInfo, error) { return nil, errors.New("boom") },
	})
	got, _ := handleGetChat(context.Background(), GetChatInput{ChatID: int64(77)})
	record := parseObject(t, got)
	requireString(t, record["title"].(string), "Team", "title")
	if record["participants"].(float64) != 12 {
		t.Errorf("participants = %v", record["participants"])
	}
	if _, ok := record["unread"]; ok {
		t.Errorf("dialog failure must leave unread out: %v", record)
	}
	if _, ok := record["last_message"]; ok {
		t.Errorf("dialog failure must skip last_message: %v", record)
	}
}

func TestSearchPublicChatsTypes(t *testing.T) {
	installClient(t, &fakeClient{searchFn: func(query string, limit int) ([]*Peer, error) {
		requireString(t, query, "news", "query")
		if limit != 20 {
			t.Errorf("limit = %d, want default 20", limit)
		}
		return []*Peer{
			&Peer{ID: 1, Kind: kit.PeerChannel, Title: "News"},
			&Peer{ID: 2, Kind: kit.PeerBasicGroup, Title: "Family"},
			userPeer(3, "Alice", ""),
		}, nil
	}})
	got, err := handleSearchPublicChats(context.Background(), SearchPublicChatsInput{Query: "news"})
	if err != nil {
		t.Fatal(err)
	}
	entities := parseArray(t, got)
	if len(entities) != 3 {
		t.Fatalf("entity count = %d, want 3", len(entities))
	}
	requireString(t, entities[0].(map[string]any)["type"].(string), "channel", "channel type")
	requireString(t, entities[1].(map[string]any)["type"].(string), "group", "group type")
	requireString(t, entities[2].(map[string]any)["type"].(string), "user", "user type")
}

func TestResolveUsername(t *testing.T) {
	installClient(t, &fakeClient{resolveUsernameFn: func(username string) (*Peer, error) {
		peer := supergroupPeer(100, "Public Group", false)
		peer.Handle = "public"
		return peer, nil
	}})
	got, err := handleResolveUsername(context.Background(), ResolveUsernameInput{Username: "@public"})
	if err != nil {
		t.Fatal(err)
	}
	record := parseObject(t, got)
	requireString(t, record["title"].(string), "Public Group", "title")
	if record["id"].(float64) != -1000000000000-100 {
		t.Errorf("id = %v", record["id"])
	}
	requireString(t, record["username"].(string), "public", "username")
}

func TestGetFullChat(t *testing.T) {
	installClient(t, &fakeClient{
		resolveFn: func(any) (*Peer, error) { return supergroupPeer(100, "Public Group", false), nil },
		fullChatFn: func(*Peer) (*FullChatInfo, error) {
			chat := supergroupPeer(100, "Public Group", false)
			chat.Handle = "public"
			return &FullChatInfo{
				Chat:              chat,
				About:             "About\u200b this",
				ParticipantsCount: ptr(42),
				LinkedChatID:      ptr(int64(55)),
			}, nil
		},
	})
	got, err := handleGetFullChat(context.Background(), GetFullChatInput{ChatID: int64(100)})
	if err != nil {
		t.Fatal(err)
	}
	record := parseObject(t, got)
	requireString(t, record["title"].(string), "Public Group", "title")
	requireString(t, record["about"].(string), "About this", "about")
	if record["participants_count"].(float64) != 42 || record["linked_chat_id"].(float64) != 55 {
		t.Errorf("counts = %v", record)
	}
	requireString(t, record["username"].(string), "public", "username")
}

func TestMuteAndUnmuteChat(t *testing.T) {
	f := installClient(t, &fakeClient{
		resolveFn:  func(any) (*Peer, error) { return userPeer(12, "A", ""), nil },
		setMutedFn: func(*Peer, bool) error { return nil },
	})
	got, err := handleMuteChat(context.Background(), MuteChatInput{ChatID: int64(12)})
	if err != nil {
		t.Fatal(err)
	}
	requireString(t, got, "Chat 12 muted.", "mute result")
	got, _ = handleUnmuteChat(context.Background(), UnmuteChatInput{ChatID: int64(12)})
	requireString(t, got, "Chat 12 unmuted.", "unmute result")
	if len(f.setMutedCalls) != 2 || f.setMutedCalls[0] != true || f.setMutedCalls[1] != false {
		t.Errorf("mute calls = %v", f.setMutedCalls)
	}
}

func TestArchiveAndUnarchiveChat(t *testing.T) {
	f := installClient(t, &fakeClient{
		resolveFn:     func(any) (*Peer, error) { return userPeer(12, "A", ""), nil },
		setArchivedFn: func(*Peer, bool) error { return nil },
	})
	got, err := handleArchiveChat(context.Background(), ArchiveChatInput{ChatID: int64(12)})
	if err != nil {
		t.Fatal(err)
	}
	requireString(t, got, "Chat 12 archived.", "archive result")
	got, _ = handleUnarchiveChat(context.Background(), UnarchiveChatInput{ChatID: int64(12)})
	requireString(t, got, "Chat 12 unarchived.", "unarchive result")
	if len(f.setArchivedCalls) != 2 || f.setArchivedCalls[0] != true || f.setArchivedCalls[1] != false {
		t.Errorf("archive calls = %v", f.setArchivedCalls)
	}
}

func TestGetCommonChats(t *testing.T) {
	chat := supergroupPeer(100, "Shared", false)
	chat.Handle = "shared"
	f := installClient(t, &fakeClient{
		resolveFn:     func(any) (*Peer, error) { return userPeer(12, "A", ""), nil },
		commonChatsFn: func(*Peer, int64, int) ([]*Peer, error) { return []*Peer{chat}, nil },
	})
	got, err := handleGetCommonChats(context.Background(), GetCommonChatsInput{UserID: int64(12), Limit: 150})
	if err != nil {
		t.Fatal(err)
	}
	want := "Chat ID: -1000000000100, Title: Shared, Type: Supergroup, Username: @shared"
	requireString(t, got, want, "common chat line")
	if len(f.commonChatsLimits) != 1 || f.commonChatsLimits[0] != 100 {
		t.Errorf("limit passed = %v, want clamped 100", f.commonChatsLimits)
	}

	f.commonChatsFn = func(*Peer, int64, int) ([]*Peer, error) { return nil, nil }
	got, _ = handleGetCommonChats(context.Background(), GetCommonChatsInput{UserID: int64(12)})
	requireString(t, got, "No common chats found with user 12.", "no common chats")
}

func TestGetMessageReadBy(t *testing.T) {
	readAt := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	f := installClient(t, &fakeClient{
		resolveFn: func(any) (*Peer, error) { return supergroupPeer(100, "G", false), nil },
		readParticipantsFn: func(*Peer, int32) ([]ReadParticipant, error) {
			return []ReadParticipant{{UserID: 20, ReadAt: &readAt}, {UserID: 21}}, nil
		},
	})
	got, err := handleGetMessageReadBy(context.Background(), GetMessageReadByInput{ChatID: int64(100), MessageID: 7})
	if err != nil {
		t.Fatal(err)
	}
	record := parseObject(t, got)
	requireString(t, record["chat_id"].(string), "100", "chat id string")
	if record["count"].(float64) != 2 {
		t.Errorf("count = %v", record["count"])
	}
	readers := record["read_by"].([]any)
	if readers[1].(map[string]any)["read_at"] != nil {
		t.Errorf("missing read_at must serialize as null: %v", readers[1])
	}

	f.readParticipantsFn = func(*Peer, int32) ([]ReadParticipant, error) { return nil, ErrMessageTooOld }
	got, _ = handleGetMessageReadBy(context.Background(), GetMessageReadByInput{ChatID: int64(100), MessageID: 7})
	requireString(t, got,
		"Read receipts unavailable for message 7 in chat 100: message is too old or read receipts are disabled.",
		"too old")

	f.readParticipantsFn = func(*Peer, int32) ([]ReadParticipant, error) { return nil, nil }
	got, _ = handleGetMessageReadBy(context.Background(), GetMessageReadByInput{ChatID: int64(100), MessageID: 7})
	requireString(t, got, "No read receipts available for message 7 in chat 100.", "no receipts")
}

func TestGetMessageLink(t *testing.T) {
	f := installClient(t, &fakeClient{
		resolveFn: func(any) (*Peer, error) { return supergroupPeer(100, "G", false), nil },
		exportLinkFn: func(*Peer, int32, bool) (*MessageLink, error) {
			return &MessageLink{Link: "https://t.me/c/1/7", HTML: "<a href=\"x\">7</a>"}, nil
		},
	})
	got, err := handleGetMessageLink(context.Background(), GetMessageLinkInput{ChatID: int64(100), MessageID: 7, Thread: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "Link: https://t.me/c/1/7\nHTML: <a href=\"x\">7</a>"
	requireString(t, got, want, "link output")
	if len(f.linkThreadCalls) != 1 || f.linkThreadCalls[0] != true {
		t.Errorf("thread flag = %v", f.linkThreadCalls)
	}

	f.resolveFn = func(any) (*Peer, error) { return userPeer(5, "A", ""), nil }
	got, _ = handleGetMessageLink(context.Background(), GetMessageLinkInput{ChatID: int64(5), MessageID: 7})
	requireString(t, got,
		"Cannot export message link for this entity type (User). Message links are only available for channels and supergroups.",
		"user entity")

	f.resolveFn = func(any) (*Peer, error) { return supergroupPeer(100, "G", false), nil }
	f.exportLinkFn = func(*Peer, int32, bool) (*MessageLink, error) { return &MessageLink{}, nil }
	got, _ = handleGetMessageLink(context.Background(), GetMessageLinkInput{ChatID: int64(100), MessageID: 7})
	requireString(t, got, "Could not export link for message 7 in chat 100.", "empty link")
}

func TestValidateChatIDMatchesPython(t *testing.T) {
	installClient(t, &fakeClient{})
	got, err := handleListTopics(context.Background(), ListTopicsInput{ChatID: 123.45})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Invalid chat_id") || !strings.Contains(got, "Type must be an integer or a string") {
		t.Errorf("validation result = %q", got)
	}

	f := installClient(t, &fakeClient{
		resolveFn:     func(any) (*Peer, error) { return userPeer(12, "A", ""), nil },
		setArchivedFn: func(*Peer, bool) error { return nil },
	})
	got, _ = handleArchiveChat(context.Background(), ArchiveChatInput{ChatID: "not-an-int"})
	// A non-numeric string is a valid identifier; resolution must be reached.
	if len(f.setArchivedCalls) != 1 {
		t.Errorf("non-numeric string id did not reach the client: %q", got)
	}
}

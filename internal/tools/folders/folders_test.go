package folders

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

// fakeClient serves the folders API from an in-memory folder list and records
// the mutations the tools attempt, so every body runs without a network or a
// real Telegram connection.
type fakeClient struct {
	filters    []DialogFilter
	peers      map[int64]InputPeer
	names      map[int64]PeerInfo
	limit      int
	premium    bool
	hasLimit   bool
	failFilter bool
	refused    map[string]bool
	hidden     map[int64]bool

	updated   []DialogFilter
	deleted   []int32
	reordered []int32
	updateErr error
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		peers:   map[int64]InputPeer{},
		names:   map[int64]PeerInfo{},
		refused: map[string]bool{},
		hidden:  map[int64]bool{},
	}
}

func (f *fakeClient) DialogFilters() ([]DialogFilter, error) {
	if f.failFilter {
		return nil, errors.New("telegram rpc failure")
	}
	out := make([]DialogFilter, len(f.filters))
	copy(out, f.filters)
	return out, nil
}

func (f *fakeClient) UpdateDialogFilter(id int32, filter *DialogFilter) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if filter == nil {
		f.deleted = append(f.deleted, id)
		return nil
	}
	stored := *filter
	stored.ID = id
	f.updated = append(f.updated, stored)
	return nil
}

func (f *fakeClient) UpdateDialogFilterOrder(order []int32) error {
	f.reordered = append(f.reordered, order...)
	return nil
}

func (f *fakeClient) ResolvePeer(identifier any) (InputPeer, error) {
	if f.refused[displayID(identifier)] {
		return InputPeer{}, errors.New("peer not found")
	}
	id := int64Of(identifier)
	if peer, ok := f.peers[id]; ok {
		return peer, nil
	}
	peer := InputPeer{MarkedID: id, Kind: "user"}
	f.peers[id] = peer
	return peer, nil
}

func (f *fakeClient) DescribePeer(peer InputPeer) (PeerInfo, error) {
	if f.hidden[peer.MarkedID] {
		return PeerInfo{}, errors.New("peer not found")
	}
	if info, ok := f.names[peer.MarkedID]; ok {
		return info, nil
	}
	return PeerInfo{MarkedID: peer.MarkedID, Name: "Chat", Type: "User"}, nil
}

func (f *fakeClient) FolderLimit() (int, bool, bool) { return f.limit, f.premium, f.hasLimit }

func (f *fakeClient) Lookup(any) (kit.Entity, error) { return nil, kit.ErrEntityNotFound }

func (f *fakeClient) WarmEntities() error { return nil }

func int64Of(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func install(t *testing.T, client Client) {
	t.Helper()
	previous := SetDeps(&Deps{
		Router:    kit.NewRouter([]string{"main"}),
		ClientFor: func(string) (Client, error) { return client, nil },
	})
	t.Cleanup(func() { SetDeps(previous) })
}

// call runs one handler and fails the test when it returns a protocol error
// instead of the formatted string every ported tool body produces.
func call[I any](t *testing.T, handler func(context.Context, I) (string, error), in I) string {
	t.Helper()
	text, err := handler(context.Background(), in)
	if err != nil {
		t.Fatalf("handler returned a protocol error: %v", err)
	}
	return text
}

func TestAllFolderToolsRegister(t *testing.T) {
	registered := map[string]bool{}
	for _, name := range mcpserver.RegisteredToolNames() {
		registered[name] = true
	}

	reads := []string{"list_folders", "get_folder"}
	writes := []string{"create_folder", "add_chat_to_folder", "remove_chat_from_folder", "delete_folder", "reorder_folders"}
	for _, name := range append(append([]string{}, reads...), writes...) {
		if !registered[name] {
			t.Errorf("tool %q is not registered", name)
		}
	}
	if got := len(reads) + len(writes); got != 7 {
		t.Fatalf("the folders inventory has 7 tools, listed %d", got)
	}

	srv, err := mcpserver.Build(mcpserver.DefaultRegistry, mcpserver.Options{ExposedMode: "read-only"})
	if err != nil {
		t.Fatalf("building the read-only surface: %v", err)
	}
	served := map[string]bool{}
	for _, name := range srv.ToolNames() {
		served[name] = true
	}
	for _, name := range reads {
		if !served[name] {
			t.Errorf("read tool %q was pruned from the read-only surface", name)
		}
	}
	for _, name := range writes {
		if served[name] {
			t.Errorf("write tool %q survived TELEGRAM_EXPOSED_TOOLS=read-only", name)
		}
	}
}

func TestListFoldersReportsEveryFolder(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{
		{ID: 2, Title: "Work", Emoticon: "💼", Groups: true, ExcludeArchived: true},
		{ID: 3, Title: "Shared", Shared: true, IncludePeers: []InputPeer{{MarkedID: -1001}}},
	}
	install(t, client)

	out := call(t, handleListFolders, listFoldersInput{})

	var payload struct {
		Count   int              `json:"count"`
		Folders []map[string]any `json:"folders"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("list_folders returned non-JSON %q: %v", out, err)
	}
	if payload.Count != 2 || len(payload.Folders) != 2 {
		t.Fatalf("expected 2 folders, got count=%d len=%d: %s", payload.Count, len(payload.Folders), out)
	}
	first := payload.Folders[0]
	if first["title"] != "Work" || first["emoticon"] != "💼" || first["groups"] != true {
		t.Errorf("first folder not rendered as expected: %v", first)
	}
	if first["included_peers_count"] != float64(0) || first["excluded_peers_count"] != float64(0) {
		t.Errorf("peer counts not reported: %v", first)
	}
	second := payload.Folders[1]
	if second["type"] != "shared" || second["included_peers_count"] != float64(1) {
		t.Errorf("shared folder not rendered as expected: %v", second)
	}
}

func TestListFoldersEmpty(t *testing.T) {
	install(t, newFakeClient())
	out := call(t, handleListFolders, listFoldersInput{})
	if out != "No folders found. Create one with create_folder tool." {
		t.Fatalf("unexpected empty-folder message: %q", out)
	}
}

func TestListFoldersFunnelsRPCFailure(t *testing.T) {
	client := newFakeClient()
	client.failFilter = true
	install(t, client)

	out := call(t, handleListFolders, listFoldersInput{})
	if !strings.Contains(out, "FOLDER-ERR-") {
		t.Fatalf("expected a funnel error code, got %q", out)
	}
	if strings.Contains(out, "rpc failure") {
		t.Errorf("the funnel leaked exception text: %q", out)
	}
}

func TestGetFolderResolvesPeers(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{
		ID:           5,
		Title:        "Team",
		IncludePeers: []InputPeer{{MarkedID: -1001234}},
		ExcludePeers: []InputPeer{{MarkedID: -1005678}},
		PinnedPeers:  []InputPeer{{MarkedID: -1005678}},
		ExcludeRead:  true,
	}}
	client.names[-1001234] = PeerInfo{MarkedID: -1001234, Name: "Design", Type: "Supergroup", Username: "design"}
	client.hidden[-1005678] = true
	install(t, client)

	out := call(t, handleGetFolder, getFolderInput{FolderID: 5})

	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("get_folder returned non-JSON %q: %v", out, err)
	}
	included := payload["included_chats"].([]any)
	first := included[0].(map[string]any)
	if first["name"] != "Design" || first["username"] != "design" || first["id"] != float64(-1001234) {
		t.Errorf("included chat not resolved: %v", first)
	}
	excluded := payload["excluded_chats"].([]any)
	if excluded[0].(map[string]any)["name"] != unknownChatName {
		t.Errorf("an unresolvable peer should degrade to the Unknown placeholder: %v", excluded[0])
	}
	if len(payload["pinned_chats"].([]any)) != 1 {
		t.Errorf("pinned chats not reported: %v", payload["pinned_chats"])
	}
	filters := payload["filters"].(map[string]any)
	if filters["exclude_read"] != true || filters["groups"] != false {
		t.Errorf("folder filter flags not reported: %v", filters)
	}
}

func TestGetFolderNotFound(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{ID: 2, Title: "Work"}}
	install(t, client)

	out := call(t, handleGetFolder, getFolderInput{FolderID: 9})
	if out != "Folder with ID 9 not found. Use list_folders to see available folders." {
		t.Fatalf("unexpected not-found message: %q", out)
	}
}

func TestCreateFolderAllocatesNextID(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{ID: 2}, {ID: 3}, {ID: 5}}
	client.peers[777] = InputPeer{MarkedID: 777, Kind: "channel"}
	install(t, client)

	out := call(t, handleCreateFolder, createFolderInput{
		Title:    "Reading",
		Emoticon: "📚",
		ChatIDs:  []any{float64(777)},
		Groups:   true,
	})

	var payload struct {
		Success            bool   `json:"success"`
		FolderID           int32  `json:"folder_id"`
		Title              string `json:"title"`
		IncludedChatsCount int    `json:"included_chats_count"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("create_folder returned non-JSON %q: %v", out, err)
	}
	if !payload.Success || payload.FolderID != 4 || payload.Title != "Reading" {
		t.Fatalf("expected the first free ID 4, got %+v", payload)
	}
	if payload.IncludedChatsCount != 1 {
		t.Errorf("expected the resolved chat to be included, got %d", payload.IncludedChatsCount)
	}
	if len(client.updated) != 1 {
		t.Fatalf("expected exactly one update, got %d", len(client.updated))
	}
	created := client.updated[0]
	if created.Title != "Reading" || !created.Groups || !created.ExcludeArchived {
		t.Errorf("created folder did not carry the requested flags: %+v", created)
	}
	if len(created.IncludePeers) != 1 || created.IncludePeers[0].MarkedID != 777 {
		t.Errorf("expected the resolved peer in include_peers: %+v", created.IncludePeers)
	}
	if len(created.ExcludePeers) != 0 || len(created.PinnedPeers) != 0 {
		t.Errorf("expected empty exclude/pinned lists: %+v", created)
	}
}

func TestCreateFolderExcludeArchivedDefault(t *testing.T) {
	client := newFakeClient()
	install(t, client)
	call(t, handleCreateFolder, createFolderInput{Title: "A"})
	if !client.updated[0].ExcludeArchived {
		t.Errorf("expected exclude_archived to default to true")
	}

	client = newFakeClient()
	install(t, client)
	explicit := false
	call(t, handleCreateFolder, createFolderInput{Title: "A", ExcludeArchived: &explicit})
	if client.updated[0].ExcludeArchived {
		t.Errorf("an explicit exclude_archived=false must be honoured")
	}
}

func TestCreateFolderEnforcesConfiguredLimit(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{ID: 2}, {ID: 3}}
	client.limit, client.premium, client.hasLimit = 2, false, true
	install(t, client)

	out := call(t, handleCreateFolder, createFolderInput{Title: "A"})
	if !strings.Contains(out, "folder limit of 2 for your regular account (2 folders)") {
		t.Fatalf("unexpected limit message: %q", out)
	}
	if len(client.updated) != 0 {
		t.Errorf("the limit check must run before the mutation")
	}

	premium := newFakeClient()
	premium.filters = []DialogFilter{{ID: 2}, {ID: 3}}
	premium.limit, premium.premium, premium.hasLimit = 4, true, true
	install(t, premium)
	if out := call(t, handleCreateFolder, createFolderInput{Title: "A"}); !strings.Contains(out, `"folder_id": 4`) {
		t.Fatalf("a premium account under its limit must be able to create: %q", out)
	}
}

func TestCreateFolderServerLimitRejection(t *testing.T) {
	client := newFakeClient()
	client.updateErr = errors.New("rpc error 400 DIALOG_FILTERS_TOO_MUCH: too much")
	install(t, client)

	out := call(t, handleCreateFolder, createFolderInput{Title: "A"})
	if !strings.Contains(out, "folder limit for your account") {
		t.Fatalf("expected the server limit message, got %q", out)
	}
}

func TestCreateFolderUnresolvableChat(t *testing.T) {
	client := newFakeClient()
	client.refused["ghost"] = true
	install(t, client)

	out := call(t, handleCreateFolder, createFolderInput{Title: "A", ChatIDs: []any{"ghost"}})
	if out != resolveFailed {
		t.Fatalf("unexpected resolve-failure message: %q", out)
	}
	if len(client.updated) != 0 {
		t.Errorf("an unresolvable chat_id must not create a folder")
	}
}

func TestAddChatToFolderIsIdempotent(t *testing.T) {
	peer := InputPeer{MarkedID: -100999, Kind: "channel"}
	client := newFakeClient()
	client.peers[-100999] = peer
	client.filters = []DialogFilter{{ID: 4, Title: "Team", IncludePeers: []InputPeer{peer}}}
	install(t, client)

	out := call(t, handleAddChatToFolder, addChatToFolderInput{FolderID: 4, ChatID: float64(-100999)})
	if out != "Chat -100999 is already in folder 4." {
		t.Fatalf("unexpected idempotent message: %q", out)
	}
	if len(client.updated) != 0 {
		t.Errorf("an already-included chat must not trigger an update")
	}
}

func TestAddChatToFolderAddsAndPins(t *testing.T) {
	client := newFakeClient()
	client.peers[55] = InputPeer{MarkedID: 55, Kind: "user"}
	client.filters = []DialogFilter{{
		ID:             4,
		Title:          "Team",
		ExcludeRead:    true,
		ExcludePeers:   []InputPeer{{MarkedID: 66}},
		Color:          3,
		TitleNoanimate: true,
	}}
	install(t, client)

	out := call(t, handleAddChatToFolder, addChatToFolderInput{FolderID: 4, ChatID: float64(55), Pinned: true})
	if out != "Chat 55 added to folder 4 (pinned)." {
		t.Fatalf("unexpected add message: %q", out)
	}
	if len(client.updated) != 1 {
		t.Fatalf("expected one update, got %d", len(client.updated))
	}
	updated := client.updated[0]
	if !containsPeer(updated.IncludePeers, 55) || !containsPeer(updated.PinnedPeers, 55) {
		t.Errorf("chat not added to both lists: %+v", updated)
	}
	if !updated.ExcludeRead || len(updated.ExcludePeers) != 1 || updated.Color != 3 || !updated.TitleNoanimate {
		t.Errorf("existing folder attributes were not preserved: %+v", updated)
	}
}

func TestAddChatToFolderUnpinnedSuffix(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{ID: 4, Title: "Team"}}
	install(t, client)

	out := call(t, handleAddChatToFolder, addChatToFolderInput{FolderID: 4, ChatID: float64(55)})
	if out != "Chat 55 added to folder 4." {
		t.Fatalf("unexpected add message: %q", out)
	}
}

func TestAddChatToFolderMissingFolder(t *testing.T) {
	install(t, newFakeClient())
	out := call(t, handleAddChatToFolder, addChatToFolderInput{FolderID: 9, ChatID: float64(55)})
	if out != "Folder with ID 9 not found. Use list_folders to see available folders." {
		t.Fatalf("unexpected not-found message: %q", out)
	}
}

func TestRemoveChatFromFolderDropsBothLists(t *testing.T) {
	peer := InputPeer{MarkedID: -100999, Kind: "channel"}
	client := newFakeClient()
	client.peers[-100999] = peer
	client.filters = []DialogFilter{{
		ID:           4,
		Title:        "Team",
		IncludePeers: []InputPeer{peer, {MarkedID: 12}},
		PinnedPeers:  []InputPeer{peer},
	}}
	install(t, client)

	out := call(t, handleRemoveChatFromFolder, removeChatFromFolderInput{FolderID: 4, ChatID: float64(-100999)})
	if out != "Chat -100999 removed from folder 4." {
		t.Fatalf("unexpected remove message: %q", out)
	}
	if len(client.updated) != 1 {
		t.Fatalf("expected one update, got %d", len(client.updated))
	}
	updated := client.updated[0]
	if containsPeer(updated.IncludePeers, -100999) || containsPeer(updated.PinnedPeers, -100999) {
		t.Errorf("chat still present after removal: %+v", updated)
	}
	if !containsPeer(updated.IncludePeers, 12) {
		t.Errorf("unrelated chats must be preserved: %+v", updated.IncludePeers)
	}
}

func TestRemoveChatFromFolderNotIncluded(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{ID: 4, Title: "Team", IncludePeers: []InputPeer{{MarkedID: 12}}}}
	install(t, client)

	out := call(t, handleRemoveChatFromFolder, removeChatFromFolderInput{FolderID: 4, ChatID: float64(55)})
	if out != "Chat 55 was not in folder 4." {
		t.Fatalf("unexpected no-op message: %q", out)
	}
	if len(client.updated) != 0 {
		t.Errorf("a no-op removal must not trigger an update")
	}
}

func TestDeleteFolderSystemFolder(t *testing.T) {
	install(t, newFakeClient())
	out := call(t, handleDeleteFolder, deleteFolderInput{FolderID: 1})
	if out != "Cannot delete system folder (ID 1). Only custom folders can be deleted." {
		t.Fatalf("unexpected system-folder message: %q", out)
	}
}

func TestDeleteFolderMissing(t *testing.T) {
	install(t, newFakeClient())
	out := call(t, handleDeleteFolder, deleteFolderInput{FolderID: 7})
	if out != "Folder with ID 7 not found (may already be deleted)." {
		t.Fatalf("unexpected missing-folder message: %q", out)
	}
}

func TestDeleteFolderSendsNilFilter(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{ID: 3, Title: "Old"}}
	install(t, client)

	out := call(t, handleDeleteFolder, deleteFolderInput{FolderID: 3})
	if out != "Folder 'Old' (ID 3) deleted. Chats are preserved." {
		t.Fatalf("unexpected delete message: %q", out)
	}
	if len(client.deleted) != 1 || client.deleted[0] != 3 {
		t.Errorf("expected a nil-filter update for folder 3, got %v", client.deleted)
	}
}

func TestReorderFoldersRejectsUnknownID(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{ID: 2}, {ID: 3}, {ID: 4}}
	install(t, client)

	out := call(t, handleReorderFolders, reorderFoldersInput{FolderIDs: []int32{2, 9}})
	if out != "Folder ID 9 not found. Use list_folders to see available folders." {
		t.Fatalf("unexpected unknown-id message: %q", out)
	}
	if len(client.reordered) != 0 {
		t.Errorf("an invalid order must not reach Telegram")
	}
}

func TestReorderFoldersRequiresEveryID(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{ID: 2}, {ID: 3}, {ID: 4}}
	install(t, client)

	out := call(t, handleReorderFolders, reorderFoldersInput{FolderIDs: []int32{2, 3}})
	if out != "All folder IDs must be included. Missing: {4}" {
		t.Fatalf("unexpected missing-ids message: %q", out)
	}
	if len(client.reordered) != 0 {
		t.Errorf("an incomplete order must not reach Telegram")
	}
}

func TestReorderFoldersAppliesOrder(t *testing.T) {
	client := newFakeClient()
	client.filters = []DialogFilter{{ID: 2}, {ID: 3}, {ID: 4}}
	install(t, client)

	out := call(t, handleReorderFolders, reorderFoldersInput{FolderIDs: []int32{4, 2, 3}})
	if out != "Folders reordered: [4, 2, 3]" {
		t.Fatalf("unexpected reorder message: %q", out)
	}
	if len(client.reordered) != 3 || client.reordered[0] != 4 {
		t.Errorf("order not forwarded: %v", client.reordered)
	}
}

func TestUnwiredModuleReportsMissingClient(t *testing.T) {
	previous := SetDeps(nil)
	t.Cleanup(func() { SetDeps(previous) })

	results := []string{
		call(t, handleListFolders, listFoldersInput{}),
		call(t, handleGetFolder, getFolderInput{FolderID: 2}),
		call(t, handleCreateFolder, createFolderInput{Title: "A"}),
		call(t, handleAddChatToFolder, addChatToFolderInput{FolderID: 2, ChatID: float64(1)}),
		call(t, handleRemoveChatFromFolder, removeChatFromFolderInput{FolderID: 2, ChatID: float64(1)}),
		call(t, handleDeleteFolder, deleteFolderInput{FolderID: 2}),
		call(t, handleReorderFolders, reorderFoldersInput{FolderIDs: []int32{2}}),
	}
	for i, out := range results {
		if !strings.Contains(out, "An error occurred") {
			t.Errorf("tool %d did not report the missing client: %q", i, out)
		}
	}
}

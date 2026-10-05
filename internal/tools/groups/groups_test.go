package groups

// Offline tests: a fake Client implements the gogram-facing interface, so no
// test opens a network connection. Every test asserts one observable behavior
// per tool, plus the module's registration surface (names and read-only
// flags) against the parity inventory.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

// expectedTools is the groups module's entry list of
// .omo/go-port/parity/inventory.json (25 names).
var expectedTools = []string{
	"ban_user", "create_channel", "create_group", "delete_chat_photo",
	"demote_admin", "edit_admin_rights", "edit_chat_about", "edit_chat_photo",
	"edit_chat_title", "export_chat_invite", "get_admins", "get_banned_users",
	"get_invite_link", "get_member_admin_status", "get_participants",
	"get_recent_actions", "import_chat_invite", "invite_to_group",
	"join_chat_by_link", "leave_chat", "promote_admin", "remove_user",
	"set_default_chat_permissions", "toggle_slow_mode", "unban_user",
}

// expectedReadOnly is the inventory's readonly=true subset.
var expectedReadOnly = []string{
	"get_admins", "get_banned_users", "get_member_admin_status",
	"get_participants", "get_recent_actions",
}

func TestGroupToolNamesRegistered(t *testing.T) {
	registered := mcpserver.RegisteredToolNames()
	for _, name := range expectedTools {
		if !slices.Contains(registered, name) {
			t.Errorf("tool %q is not registered in the process-wide registry", name)
		}
	}
}

func TestGroupRegistrationBuildsServer(t *testing.T) {
	// A private registry proves registration works without the process-wide
	// DefaultRegistry, and Build proves the SDK accepts every input schema.
	reg := mcpserver.NewRegistry()
	registerAll(reg.Register)
	srv, err := mcpserver.Build(reg, mcpserver.Options{Transport: "stdio"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	names := srv.ToolNames()
	want := append([]string(nil), expectedTools...)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("served tools = %v, want %v", names, want)
	}

	// read-only exposure serves exactly the inventory's readonly subset,
	// which pins every tool's ReadOnly flag.
	readOnlySrv, err := mcpserver.Build(reg, mcpserver.Options{Transport: "stdio", ExposedMode: "read-only"})
	if err != nil {
		t.Fatalf("Build(read-only): %v", err)
	}
	readOnly := append([]string(nil), expectedReadOnly...)
	slices.Sort(readOnly)
	if !slices.Equal(readOnlySrv.ToolNames(), readOnly) {
		t.Fatalf("read-only tools = %v, want %v", readOnlySrv.ToolNames(), readOnly)
	}
}

type fakeClient struct {
	resolvePeer         func(any) (telegram.InputPeer, error)
	getMe               func() (*telegram.UserObj, error)
	createChat          func([]telegram.InputUser, string, int32) (*telegram.MessagesInvitedUsers, error)
	addChatUser         func(int64, telegram.InputUser, int32) (*telegram.MessagesInvitedUsers, error)
	deleteChatUser      func(bool, int64, telegram.InputUser) (telegram.Updates, error)
	inviteToChannel     func(telegram.InputChannel, []telegram.InputUser) (*telegram.MessagesInvitedUsers, error)
	leaveChannel        func(telegram.InputChannel) (telegram.Updates, error)
	getChatMembers      func(any, ...*telegram.ParticipantOptions) ([]*telegram.Participant, int32, error)
	getFullChat         func(int64) (*telegram.MessagesChatFull, error)
	createChannel       func(string, ...*telegram.ChannelOptions) (*telegram.Channel, error)
	editTitle           func(telegram.InputChannel, string) (telegram.Updates, error)
	editChatTitle       func(int64, string) (telegram.Updates, error)
	editPhoto           func(telegram.InputChannel, telegram.InputChatPhoto) (telegram.Updates, error)
	editChatPhoto       func(int64, telegram.InputChatPhoto) (telegram.Updates, error)
	editChatAbout       func(telegram.InputPeer, string) (bool, error)
	uploadFile          func(any, ...*telegram.UploadOptions) (telegram.InputFile, error)
	editAdmin           func(telegram.InputChannel, telegram.InputUser, *telegram.ChatAdminRights, string) (telegram.Updates, error)
	editBanned          func(telegram.InputChannel, telegram.InputPeer, *telegram.ChatBannedRights) (telegram.Updates, error)
	getParticipant      func(telegram.InputChannel, telegram.InputPeer) (*telegram.ChannelsChannelParticipant, error)
	defaultBannedRights func(telegram.InputPeer, *telegram.ChatBannedRights) (telegram.Updates, error)
	toggleSlowMode      func(telegram.InputChannel, int32) (telegram.Updates, error)
	getParticipantsFn   func(telegram.InputChannel, telegram.ChannelParticipantsFilter, int32, int32, int64) (telegram.ChannelsChannelParticipants, error)
	exportChatInvite    func(*telegram.MessagesExportChatInviteParams) (telegram.ExportedChatInvite, error)
	checkChatInvite     func(string) (telegram.ChatInvite, error)
	importChatInvite    func(string) (telegram.MessagesChatInviteJoinResult, error)
	getAdminLog         func(*telegram.ChannelsGetAdminLogParams) (*telegram.ChannelsAdminLogResults, error)
}

func (f *fakeClient) ResolvePeer(id any) (telegram.InputPeer, error) {
	if f.resolvePeer == nil {
		return nil, fmt.Errorf("unexpected call: ResolvePeer")
	}
	return f.resolvePeer(id)
}

func (f *fakeClient) GetMe() (*telegram.UserObj, error) {
	if f.getMe == nil {
		return nil, fmt.Errorf("unexpected call: GetMe")
	}
	return f.getMe()
}

func (f *fakeClient) MessagesCreateChat(users []telegram.InputUser, title string, ttl int32) (*telegram.MessagesInvitedUsers, error) {
	if f.createChat == nil {
		return nil, fmt.Errorf("unexpected call: MessagesCreateChat")
	}
	return f.createChat(users, title, ttl)
}

func (f *fakeClient) MessagesAddChatUser(chatID int64, user telegram.InputUser, fwdLimit int32) (*telegram.MessagesInvitedUsers, error) {
	if f.addChatUser == nil {
		return nil, fmt.Errorf("unexpected call: MessagesAddChatUser")
	}
	return f.addChatUser(chatID, user, fwdLimit)
}

func (f *fakeClient) MessagesDeleteChatUser(revoke bool, chatID int64, user telegram.InputUser) (telegram.Updates, error) {
	if f.deleteChatUser == nil {
		return nil, fmt.Errorf("unexpected call: MessagesDeleteChatUser")
	}
	return f.deleteChatUser(revoke, chatID, user)
}

func (f *fakeClient) ChannelsInviteToChannel(channel telegram.InputChannel, users []telegram.InputUser) (*telegram.MessagesInvitedUsers, error) {
	if f.inviteToChannel == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsInviteToChannel")
	}
	return f.inviteToChannel(channel, users)
}

func (f *fakeClient) ChannelsLeaveChannel(channel telegram.InputChannel) (telegram.Updates, error) {
	if f.leaveChannel == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsLeaveChannel")
	}
	return f.leaveChannel(channel)
}

func (f *fakeClient) GetChatMembers(chatID any, opts ...*telegram.ParticipantOptions) ([]*telegram.Participant, int32, error) {
	if f.getChatMembers == nil {
		return nil, 0, fmt.Errorf("unexpected call: GetChatMembers")
	}
	return f.getChatMembers(chatID, opts...)
}

func (f *fakeClient) MessagesGetFullChat(chatID int64) (*telegram.MessagesChatFull, error) {
	if f.getFullChat == nil {
		return nil, fmt.Errorf("unexpected call: MessagesGetFullChat")
	}
	return f.getFullChat(chatID)
}

func (f *fakeClient) CreateChannel(title string, opts ...*telegram.ChannelOptions) (*telegram.Channel, error) {
	if f.createChannel == nil {
		return nil, fmt.Errorf("unexpected call: CreateChannel")
	}
	return f.createChannel(title, opts...)
}

func (f *fakeClient) ChannelsEditTitle(channel telegram.InputChannel, title string) (telegram.Updates, error) {
	if f.editTitle == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsEditTitle")
	}
	return f.editTitle(channel, title)
}

func (f *fakeClient) MessagesEditChatTitle(chatID int64, title string) (telegram.Updates, error) {
	if f.editChatTitle == nil {
		return nil, fmt.Errorf("unexpected call: MessagesEditChatTitle")
	}
	return f.editChatTitle(chatID, title)
}

func (f *fakeClient) ChannelsEditPhoto(channel telegram.InputChannel, photo telegram.InputChatPhoto) (telegram.Updates, error) {
	if f.editPhoto == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsEditPhoto")
	}
	return f.editPhoto(channel, photo)
}

func (f *fakeClient) MessagesEditChatPhoto(chatID int64, photo telegram.InputChatPhoto) (telegram.Updates, error) {
	if f.editChatPhoto == nil {
		return nil, fmt.Errorf("unexpected call: MessagesEditChatPhoto")
	}
	return f.editChatPhoto(chatID, photo)
}

func (f *fakeClient) MessagesEditChatAbout(peer telegram.InputPeer, about string) (bool, error) {
	if f.editChatAbout == nil {
		return false, fmt.Errorf("unexpected call: MessagesEditChatAbout")
	}
	return f.editChatAbout(peer, about)
}

func (f *fakeClient) UploadFile(src any, opts ...*telegram.UploadOptions) (telegram.InputFile, error) {
	if f.uploadFile == nil {
		return nil, fmt.Errorf("unexpected call: UploadFile")
	}
	return f.uploadFile(src, opts...)
}

func (f *fakeClient) ChannelsEditAdmin(channel telegram.InputChannel, user telegram.InputUser, rights *telegram.ChatAdminRights, rank string) (telegram.Updates, error) {
	if f.editAdmin == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsEditAdmin")
	}
	return f.editAdmin(channel, user, rights, rank)
}

func (f *fakeClient) ChannelsEditBanned(channel telegram.InputChannel, participant telegram.InputPeer, rights *telegram.ChatBannedRights) (telegram.Updates, error) {
	if f.editBanned == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsEditBanned")
	}
	return f.editBanned(channel, participant, rights)
}

func (f *fakeClient) ChannelsGetParticipant(channel telegram.InputChannel, participant telegram.InputPeer) (*telegram.ChannelsChannelParticipant, error) {
	if f.getParticipant == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsGetParticipant")
	}
	return f.getParticipant(channel, participant)
}

func (f *fakeClient) MessagesEditChatDefaultBannedRights(peer telegram.InputPeer, rights *telegram.ChatBannedRights) (telegram.Updates, error) {
	if f.defaultBannedRights == nil {
		return nil, fmt.Errorf("unexpected call: MessagesEditChatDefaultBannedRights")
	}
	return f.defaultBannedRights(peer, rights)
}

func (f *fakeClient) ChannelsToggleSlowMode(channel telegram.InputChannel, seconds int32) (telegram.Updates, error) {
	if f.toggleSlowMode == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsToggleSlowMode")
	}
	return f.toggleSlowMode(channel, seconds)
}

func (f *fakeClient) ChannelsGetParticipants(channel telegram.InputChannel, filter telegram.ChannelParticipantsFilter, offset, limit int32, hash int64) (telegram.ChannelsChannelParticipants, error) {
	if f.getParticipantsFn == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsGetParticipants")
	}
	return f.getParticipantsFn(channel, filter, offset, limit, hash)
}

func (f *fakeClient) MessagesExportChatInvite(params *telegram.MessagesExportChatInviteParams) (telegram.ExportedChatInvite, error) {
	if f.exportChatInvite == nil {
		return nil, fmt.Errorf("unexpected call: MessagesExportChatInvite")
	}
	return f.exportChatInvite(params)
}

func (f *fakeClient) MessagesCheckChatInvite(hash string) (telegram.ChatInvite, error) {
	if f.checkChatInvite == nil {
		return nil, fmt.Errorf("unexpected call: MessagesCheckChatInvite")
	}
	return f.checkChatInvite(hash)
}

func (f *fakeClient) MessagesImportChatInvite(hash string) (telegram.MessagesChatInviteJoinResult, error) {
	if f.importChatInvite == nil {
		return nil, fmt.Errorf("unexpected call: MessagesImportChatInvite")
	}
	return f.importChatInvite(hash)
}

func (f *fakeClient) ChannelsGetAdminLog(params *telegram.ChannelsGetAdminLogParams) (*telegram.ChannelsAdminLogResults, error) {
	if f.getAdminLog == nil {
		return nil, fmt.Errorf("unexpected call: ChannelsGetAdminLog")
	}
	return f.getAdminLog(params)
}

var _ Client = (*fakeClient)(nil)

// fakeRuntime installs cl as the process-wide client and restores the
// previous runtime when the test finishes.
func fakeRuntime(t *testing.T, cl Client) {
	t.Helper()
	prev := currentRuntime()
	SetRuntime(Runtime{Client: func(context.Context, string) (Client, error) { return cl, nil }})
	t.Cleanup(func() { SetRuntime(prev) })
}

// peerByID resolves the given chat IDs to channel peers and everything else
// to user peers.
func peerByID(channelIDs ...int64) func(any) (telegram.InputPeer, error) {
	return func(id any) (telegram.InputPeer, error) {
		number, isNumber := id.(int64)
		if !isNumber {
			return nil, fmt.Errorf("cannot resolve %v", id)
		}
		if slices.Contains(channelIDs, number) {
			return &telegram.InputPeerChannel{ChannelID: number}, nil
		}
		return &telegram.InputPeerUser{UserID: number}, nil
	}
}

func noError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
}

func participant(id int64, first, last, username string) *telegram.Participant {
	return &telegram.Participant{User: &telegram.UserObj{
		ID: id, FirstName: first, LastName: last, Username: username,
	}}
}

func TestCreateGroup(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID()
	cl.createChat = func(users []telegram.InputUser, title string, _ int32) (*telegram.MessagesInvitedUsers, error) {
		if title != "Crew" || len(users) != 2 {
			t.Errorf("createChat title=%q users=%d", title, len(users))
		}
		return &telegram.MessagesInvitedUsers{Updates: &telegram.UpdatesObj{
			Chats: []telegram.Chat{&telegram.Channel{ID: 123, Megagroup: true}},
		}}, nil
	}
	got, err := createGroup(context.Background(), createGroupInput{Title: "Crew", UserIDs: []any{int64(1), int64(2)}})
	noError(t, err)
	if got != "Group created with ID: -1000000000123" {
		t.Fatalf("got %q", got)
	}

	got, err = createGroup(context.Background(), createGroupInput{Title: "Crew"})
	noError(t, err)
	if got != "Error: No valid users provided" {
		t.Fatalf("got %q", got)
	}
}

func TestInviteToGroup(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.inviteToChannel = func(channel telegram.InputChannel, users []telegram.InputUser) (*telegram.MessagesInvitedUsers, error) {
		if len(users) != 2 {
			t.Errorf("invited %d users", len(users))
		}
		return &telegram.MessagesInvitedUsers{Updates: &telegram.UpdatesObj{
			Users: []telegram.User{&telegram.UserObj{ID: 1}, &telegram.UserObj{ID: 2}},
		}}, nil
	}
	got, err := inviteToGroup(context.Background(), inviteToGroupInput{GroupID: int64(5), UserIDs: []any{int64(1), int64(2)}})
	noError(t, err)
	if got != "Successfully invited 2 users to 5" {
		t.Fatalf("got %q", got)
	}
}

func TestLeaveChat(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.leaveChannel = func(telegram.InputChannel) (telegram.Updates, error) { return nil, nil }
	got, err := leaveChat(context.Background(), leaveChatInput{ChatID: int64(5)})
	noError(t, err)
	if got != "Left channel/supergroup 5 (ID: 5)." {
		t.Fatalf("got %q", got)
	}
}

func TestGetParticipants(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.getChatMembers = func(_ any, opts ...*telegram.ParticipantOptions) ([]*telegram.Participant, int32, error) {
		if len(opts) != 1 || opts[0].Limit != 2 {
			t.Errorf("GetChatMembers opts = %+v", opts)
		}
		return []*telegram.Participant{
			participant(1, "Ada", "Lovelace", "ada"),
			participant(2, "Alan", "Turing", ""),
		}, 2, nil
	}
	got, err := getParticipants(context.Background(), getParticipantsInput{ChatID: int64(5), PageSize: intPtr(2)})
	noError(t, err)
	if !strings.Contains(got, `"name":"Ada Lovelace"`) || !strings.Contains(got, "Page 1 (showing 2 participants)") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "[empty]") {
		t.Fatalf("empty usernames must be omitted, got %q", got)
	}

	got, err = getParticipants(context.Background(), getParticipantsInput{ChatID: int64(5), PageSize: intPtr(1001)})
	noError(t, err)
	if got != "Error: page_size cannot exceed 1000 participants per request." {
		t.Fatalf("got %q", got)
	}
}

func TestCreateChannel(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.createChannel = func(title string, opts ...*telegram.ChannelOptions) (*telegram.Channel, error) {
		if title != "News" || len(opts) != 1 || opts[0].About != "all the news" {
			t.Errorf("createChannel title=%q opts=%+v", title, opts)
		}
		return &telegram.Channel{ID: 77}, nil
	}
	got, err := createChannel(context.Background(), createChannelInput{Title: "News", About: "all the news"})
	noError(t, err)
	if got != "Channel 'News' created with ID: 77" {
		t.Fatalf("got %q", got)
	}
}

func TestEditChatTitle(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.editTitle = func(_ telegram.InputChannel, title string) (telegram.Updates, error) {
		if title != "New Name" {
			t.Errorf("title = %q", title)
		}
		return nil, nil
	}
	got, err := editChatTitle(context.Background(), editChatTitleInput{ChatID: int64(5), Title: "New Name"})
	noError(t, err)
	if got != "Chat 5 title updated to 'New Name'." {
		t.Fatalf("got %q", got)
	}
}

func TestEditChatPhoto(t *testing.T) {
	cl := &fakeClient{}
	prev := currentRuntime()
	SetRuntime(Runtime{
		Client: func(context.Context, string) (Client, error) { return cl, nil },
		FileResolver: func(_ context.Context, toolName, rawPath string) (string, error) {
			if toolName != "edit_chat_photo" || rawPath != "pic.png" {
				t.Errorf("resolver got tool=%q path=%q", toolName, rawPath)
			}
			return "/roots/pic.png", nil
		},
	})
	t.Cleanup(func() { SetRuntime(prev) })
	cl.resolvePeer = peerByID(5)
	cl.uploadFile = func(src any, _ ...*telegram.UploadOptions) (telegram.InputFile, error) {
		if src != "/roots/pic.png" {
			t.Errorf("upload src = %v", src)
		}
		return nil, nil
	}
	cl.editPhoto = func(_ telegram.InputChannel, photo telegram.InputChatPhoto) (telegram.Updates, error) {
		if _, isUploaded := photo.(*telegram.InputChatUploadedPhoto); !isUploaded {
			t.Errorf("photo = %T", photo)
		}
		return nil, nil
	}
	got, err := editChatPhoto(context.Background(), editChatPhotoInput{ChatID: int64(5), FilePath: "pic.png"})
	noError(t, err)
	if got != "Chat 5 photo updated from /roots/pic.png." {
		t.Fatalf("got %q", got)
	}
}

func TestEditChatAbout(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.editChatAbout = func(_ telegram.InputPeer, about string) (bool, error) {
		if about != "hello" {
			t.Errorf("about = %q", about)
		}
		return true, nil
	}
	got, err := editChatAbout(context.Background(), editChatAboutInput{ChatID: int64(5), About: "hello"})
	noError(t, err)
	if got != "Chat 5 description updated." {
		t.Fatalf("got %q", got)
	}

	cl.editChatAbout = func(telegram.InputPeer, string) (bool, error) {
		return false, fmt.Errorf("rpc error: CHAT_ABOUT_NOT_MODIFIED")
	}
	got, err = editChatAbout(context.Background(), editChatAboutInput{ChatID: int64(5), About: "hello"})
	noError(t, err)
	if got != "Chat 5 description is already set to the requested value." {
		t.Fatalf("got %q", got)
	}
}

func TestDeleteChatPhoto(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.editPhoto = func(_ telegram.InputChannel, photo telegram.InputChatPhoto) (telegram.Updates, error) {
		if _, isEmpty := photo.(*telegram.InputChatPhotoEmpty); !isEmpty {
			t.Errorf("photo = %T", photo)
		}
		return nil, nil
	}
	got, err := deleteChatPhoto(context.Background(), deleteChatPhotoInput{ChatID: int64(5)})
	noError(t, err)
	if got != "Chat 5 photo deleted." {
		t.Fatalf("got %q", got)
	}
}

func TestPromoteAdmin(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	var captured *telegram.ChatAdminRights
	cl.editAdmin = func(_ telegram.InputChannel, _ telegram.InputUser, rights *telegram.ChatAdminRights, rank string) (telegram.Updates, error) {
		captured, _ = rights, rank
		if rank != "Admin" {
			t.Errorf("rank = %q", rank)
		}
		return nil, nil
	}
	got, err := promoteAdmin(context.Background(), promoteAdminInput{GroupID: int64(5), UserID: int64(9)})
	noError(t, err)
	if got != "Successfully promoted user 9 to admin in 5" {
		t.Fatalf("got %q", got)
	}
	if captured == nil || !captured.ChangeInfo || !captured.BanUsers || captured.AddAdmins {
		t.Fatalf("default rights = %+v", captured)
	}
}

func TestDemoteAdmin(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.editAdmin = func(_ telegram.InputChannel, _ telegram.InputUser, rights *telegram.ChatAdminRights, rank string) (telegram.Updates, error) {
		if rank != "" || *rights != (telegram.ChatAdminRights{}) {
			t.Errorf("demote rights=%+v rank=%q", rights, rank)
		}
		return nil, nil
	}
	got, err := demoteAdmin(context.Background(), demoteAdminInput{GroupID: int64(5), UserID: int64(9)})
	noError(t, err)
	if got != "Successfully demoted user 9 from admin in 5" {
		t.Fatalf("got %q", got)
	}
}

func TestBanUser(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.editBanned = func(_ telegram.InputChannel, _ telegram.InputPeer, rights *telegram.ChatBannedRights) (telegram.Updates, error) {
		if !rights.ViewMessages || !rights.SendMessages || rights.UntilDate != 0 {
			t.Errorf("ban rights = %+v", rights)
		}
		return nil, nil
	}
	got, err := banUser(context.Background(), banUserInput{ChatID: int64(5), UserID: int64(9)})
	noError(t, err)
	if got != "User 9 banned from chat 5 (ID: 5)." {
		t.Fatalf("got %q", got)
	}

	// FloodWait must reach the funnel's explicit do-not-retry prose.
	cl.editBanned = func(telegram.InputChannel, telegram.InputPeer, *telegram.ChatBannedRights) (telegram.Updates, error) {
		return nil, fmt.Errorf("rpc error: FLOOD_WAIT_42")
	}
	got, err = banUser(context.Background(), banUserInput{ChatID: int64(5), UserID: int64(9)})
	noError(t, err)
	if !strings.Contains(got, "Do NOT retry immediately") || !strings.Contains(got, "42 seconds") {
		t.Fatalf("got %q", got)
	}
}

func TestUnbanUser(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.editBanned = func(_ telegram.InputChannel, _ telegram.InputPeer, rights *telegram.ChatBannedRights) (telegram.Updates, error) {
		if *rights != (telegram.ChatBannedRights{}) {
			t.Errorf("unban rights = %+v", rights)
		}
		return nil, nil
	}
	got, err := unbanUser(context.Background(), unbanUserInput{ChatID: int64(5), UserID: int64(9)})
	noError(t, err)
	if got != "User 9 unbanned from chat 5 (ID: 5)." {
		t.Fatalf("got %q", got)
	}
}

func TestRemoveUser(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.getParticipant = func(telegram.InputChannel, telegram.InputPeer) (*telegram.ChannelsChannelParticipant, error) {
		return &telegram.ChannelsChannelParticipant{Participant: &telegram.ChannelParticipantObj{UserID: 9}}, nil
	}
	bans, unbans := 0, 0
	cl.editBanned = func(_ telegram.InputChannel, _ telegram.InputPeer, rights *telegram.ChatBannedRights) (telegram.Updates, error) {
		if rights.ViewMessages {
			bans++
			return nil, nil
		}
		unbans++
		return nil, nil
	}
	previousDelay := removeUserUnbanDelay
	removeUserUnbanDelay = 0
	t.Cleanup(func() { removeUserUnbanDelay = previousDelay })

	got, err := removeUser(context.Background(), removeUserInput{ChatID: int64(5), UserID: int64(9)})
	noError(t, err)
	if got != "User 9 removed from chat 5 (ID: 5). No ban left in place." {
		t.Fatalf("got %q", got)
	}
	if bans != 1 || unbans != 1 {
		t.Fatalf("bans=%d unbans=%d, want 1/1", bans, unbans)
	}

	cl.editBanned = func(_ telegram.InputChannel, _ telegram.InputPeer, rights *telegram.ChatBannedRights) (telegram.Updates, error) {
		if rights.ViewMessages {
			return nil, nil
		}
		return nil, fmt.Errorf("rpc error: CHAT_ADMIN_REQUIRED")
	}
	got, err = removeUser(context.Background(), removeUserInput{ChatID: int64(5), UserID: int64(9)})
	noError(t, err)
	if !strings.Contains(got, "currently BANNED") || !strings.Contains(got, "unban_user") {
		t.Fatalf("got %q", got)
	}
}

func TestSetDefaultChatPermissions(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.defaultBannedRights = func(_ telegram.InputPeer, rights *telegram.ChatBannedRights) (telegram.Updates, error) {
		// Defaults come from the Python signature: send_messages allowed,
		// change_info restricted, so the flags invert.
		if rights.SendMessages || !rights.ChangeInfo || !rights.PinMessages {
			t.Errorf("rights = %+v", rights)
		}
		return nil, nil
	}
	got, err := setDefaultChatPermissions(context.Background(), setDefaultChatPermissionsInput{ChatID: int64(5)})
	noError(t, err)
	if got != "Default permissions for chat 5 updated." {
		t.Fatalf("got %q", got)
	}
}

func TestToggleSlowMode(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.toggleSlowMode = func(_ telegram.InputChannel, seconds int32) (telegram.Updates, error) {
		if seconds != 60 {
			t.Errorf("seconds = %d", seconds)
		}
		return nil, nil
	}
	got, err := toggleSlowMode(context.Background(), toggleSlowModeInput{ChatID: int64(5), Seconds: 60})
	noError(t, err)
	if got != "Slow mode enabled for chat 5 (interval: 60s)." {
		t.Fatalf("got %q", got)
	}

	cl.resolvePeer = func(any) (telegram.InputPeer, error) { return &telegram.InputPeerChat{ChatID: 5}, nil }
	got, err = toggleSlowMode(context.Background(), toggleSlowModeInput{ChatID: int64(5)})
	noError(t, err)
	if got != "Error: slow mode is only supported for supergroups." {
		t.Fatalf("got %q", got)
	}
}

func TestEditAdminRights(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.editAdmin = func(_ telegram.InputChannel, _ telegram.InputUser, rights *telegram.ChatAdminRights, rank string) (telegram.Updates, error) {
		if !rights.BanUsers || rank != "Mod" {
			t.Errorf("rights=%+v rank=%q", rights, rank)
		}
		return nil, nil
	}
	got, err := editAdminRights(context.Background(), editAdminRightsInput{
		ChatID: int64(5), UserID: int64(9), Rank: "Mod", BanUsers: true,
	})
	noError(t, err)
	if got != "Admin rights updated for user 9 in chat 5." {
		t.Fatalf("got %q", got)
	}
}

func TestGetAdmins(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.getChatMembers = func(_ any, opts ...*telegram.ParticipantOptions) ([]*telegram.Participant, int32, error) {
		if len(opts) != 1 {
			t.Fatalf("opts = %+v", opts)
		}
		if _, isAdmins := opts[0].Filter.(*telegram.ChannelParticipantsAdmins); !isAdmins {
			t.Errorf("filter = %T", opts[0].Filter)
		}
		return []*telegram.Participant{participant(1, "Ada", "", "ada")}, 1, nil
	}
	got, err := getAdmins(context.Background(), getAdminsInput{ChatID: int64(5)})
	noError(t, err)
	if !strings.Contains(got, `"username":"ada"`) {
		t.Fatalf("got %q", got)
	}

	cl.getChatMembers = func(any, ...*telegram.ParticipantOptions) ([]*telegram.Participant, int32, error) {
		return nil, 0, nil
	}
	got, err = getAdmins(context.Background(), getAdminsInput{ChatID: int64(5)})
	noError(t, err)
	if got != "No admins found." {
		t.Fatalf("got %q", got)
	}
}

func TestGetMemberAdminStatus(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.getParticipant = func(telegram.InputChannel, telegram.InputPeer) (*telegram.ChannelsChannelParticipant, error) {
		return &telegram.ChannelsChannelParticipant{Participant: &telegram.ChannelParticipantAdmin{
			UserID:      9,
			Rank:        "Boss",
			AdminRights: &telegram.ChatAdminRights{BanUsers: true},
		}}, nil
	}
	got, err := getMemberAdminStatus(context.Background(), getMemberAdminStatusInput{ChatID: int64(5), UserID: int64(9)})
	noError(t, err)
	for _, want := range []string{`"role":"admin"`, `"rank":"Boss"`, `"ban_users":true`, `"pin_messages":false`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %q", want, got)
		}
	}
}

func TestGetBannedUsers(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.getChatMembers = func(_ any, opts ...*telegram.ParticipantOptions) ([]*telegram.Participant, int32, error) {
		if len(opts) != 1 {
			t.Fatalf("opts = %+v", opts)
		}
		if _, isKicked := opts[0].Filter.(*telegram.ChannelParticipantsKicked); !isKicked {
			t.Errorf("filter = %T", opts[0].Filter)
		}
		return []*telegram.Participant{participant(4, "Eve", "", "")}, 1, nil
	}
	got, err := getBannedUsers(context.Background(), getBannedUsersInput{ChatID: int64(5)})
	noError(t, err)
	if !strings.Contains(got, `"id":4`) {
		t.Fatalf("got %q", got)
	}
}

func TestGetInviteLink(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.exportChatInvite = func(params *telegram.MessagesExportChatInviteParams) (telegram.ExportedChatInvite, error) {
		if params.Peer == nil {
			t.Error("peer is nil")
		}
		return &telegram.ChatInviteExported{Link: "https://t.me/+abc"}, nil
	}
	got, err := getInviteLink(context.Background(), getInviteLinkInput{ChatID: int64(5)})
	noError(t, err)
	if got != "https://t.me/+abc" {
		t.Fatalf("got %q", got)
	}
}

func TestJoinChatByLink(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.checkChatInvite = func(hash string) (telegram.ChatInvite, error) {
		return nil, fmt.Errorf("not a member yet")
	}
	cl.importChatInvite = func(hash string) (telegram.MessagesChatInviteJoinResult, error) {
		if hash != "abc" {
			t.Errorf("hash = %q", hash)
		}
		return &telegram.MessagesChatInviteJoinResultOk{Updates: &telegram.UpdatesObj{
			Chats: []telegram.Chat{&telegram.ChatObj{Title: "Cool Chat"}},
		}}, nil
	}
	got, err := joinChatByLink(context.Background(), joinChatByLinkInput{Link: "https://t.me/+abc"})
	noError(t, err)
	if got != "Successfully joined chat: Cool Chat" {
		t.Fatalf("got %q", got)
	}
}

func TestJoinChatByLinkAlreadyMember(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.checkChatInvite = func(string) (telegram.ChatInvite, error) {
		return &telegram.ChatInviteAlready{Chat: &telegram.ChatObj{Title: "Home"}}, nil
	}
	got, err := joinChatByLink(context.Background(), joinChatByLinkInput{Link: "+abc"})
	noError(t, err)
	if got != "You are already a member of this chat: Home" {
		t.Fatalf("got %q", got)
	}
}

func TestExportChatInvite(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.exportChatInvite = func(*telegram.MessagesExportChatInviteParams) (telegram.ExportedChatInvite, error) {
		return &telegram.ChatInviteExported{Link: "https://t.me/+xyz"}, nil
	}
	got, err := exportChatInvite(context.Background(), exportChatInviteInput{ChatID: int64(5)})
	noError(t, err)
	if got != "https://t.me/+xyz" {
		t.Fatalf("got %q", got)
	}
}

func TestImportChatInvite(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.checkChatInvite = func(string) (telegram.ChatInvite, error) {
		return nil, fmt.Errorf("not a member yet")
	}
	cl.importChatInvite = func(hash string) (telegram.MessagesChatInviteJoinResult, error) {
		if hash != "expired" {
			t.Errorf("hash = %q", hash)
		}
		return nil, fmt.Errorf("rpc error: INVITE_HASH_EXPIRED")
	}
	got, err := importChatInvite(context.Background(), importChatInviteInput{Hash: "+expired"})
	noError(t, err)
	if got != "The invite hash has expired and is no longer valid." {
		t.Fatalf("got %q", got)
	}
}

func TestGetRecentActions(t *testing.T) {
	cl := &fakeClient{}
	fakeRuntime(t, cl)
	cl.resolvePeer = peerByID(5)
	cl.getAdminLog = func(params *telegram.ChannelsGetAdminLogParams) (*telegram.ChannelsAdminLogResults, error) {
		if params.Limit != 20 {
			t.Errorf("limit = %d", params.Limit)
		}
		return &telegram.ChannelsAdminLogResults{Events: []*telegram.ChannelAdminLogEvent{
			{ID: 7, Date: 1700000000, UserID: 9},
		}}, nil
	}
	got, err := getRecentActions(context.Background(), getRecentActionsInput{ChatID: int64(5)})
	noError(t, err)
	if !strings.Contains(got, `"ID": 7`) {
		t.Fatalf("got %q", got)
	}

	cl.getAdminLog = func(*telegram.ChannelsGetAdminLogParams) (*telegram.ChannelsAdminLogResults, error) {
		return &telegram.ChannelsAdminLogResults{}, nil
	}
	got, err = getRecentActions(context.Background(), getRecentActionsInput{ChatID: int64(5)})
	noError(t, err)
	if got != "No recent admin actions found." {
		t.Fatalf("got %q", got)
	}
}

func intPtr(value int) *int { return &value }

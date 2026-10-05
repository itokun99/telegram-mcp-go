package messages

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/config"
	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

var inventoryToolNames = []string{
	"clear_draft",
	"create_poll",
	"delete_chat_history",
	"delete_message",
	"delete_messages_bulk",
	"delete_scheduled_message",
	"edit_message",
	"export_unread_messages",
	"forward_message",
	"forward_messages",
	"get_drafts",
	"get_history",
	"get_message_context",
	"get_message_reactions",
	"get_messages",
	"get_pinned_messages",
	"get_scheduled_messages",
	"get_send_as",
	"list_inline_buttons",
	"list_messages",
	"mark_as_read",
	"pin_message",
	"press_inline_button",
	"remove_reaction",
	"reply_to_message",
	"save_draft",
	"search_global",
	"search_messages",
	"send_message",
	"send_reaction",
	"send_scheduled_message",
	"transcribe_voice",
	"unpin_all_messages",
	"unpin_message",
}

func TestEveryInventoryToolRegisters(t *testing.T) {
	registered := map[string]bool{}
	for _, name := range mcpserver.DefaultRegistry.Names() {
		registered[name] = true
	}
	for _, want := range inventoryToolNames {
		if !registered[want] {
			t.Errorf("tool %q is not registered", want)
		}
	}
	if len(inventoryToolNames) != 34 {
		t.Fatalf("inventory list has %d names, want 34", len(inventoryToolNames))
	}
}

// ---------------------------------------------------------------------------
// fake gogram client
// ---------------------------------------------------------------------------

type fakeClient struct {
	resolveEntity        func(any) (any, error)
	resolveInputPeer     func(any) (telegram.InputPeer, error)
	peerID               func(telegram.Peer) int64
	warmDialogs          func() error
	getMe                func() (*telegram.UserObj, error)
	getMessages          func(any, *telegram.SearchOption) ([]telegram.NewMessage, error)
	getMessageByID       func(any, int32) (*telegram.NewMessage, error)
	getMediaGroup        func(any, int32) ([]telegram.NewMessage, error)
	getDialogs           func(int32) ([]telegram.TLDialog, error)
	getScheduledHistory  func(telegram.InputPeer) ([]*telegram.MessageObj, error)
	getAllDrafts         func() (telegram.Updates, error)
	getBotCallbackAnswer func(*telegram.MessagesGetBotCallbackAnswerParams) (*telegram.MessagesBotCallbackAnswer, error)
	getReactionsList     func(*telegram.MessagesGetMessageReactionsListParams) (*telegram.MessagesMessageReactionsList, error)
	getSendAs            func(telegram.InputPeer) (*telegram.ChannelsSendAsPeers, error)
	sendMessage          func(any, string, *telegram.SendOptions) (*telegram.NewMessage, error)
	editMessage          func(any, int32, string, *telegram.SendOptions) (*telegram.NewMessage, error)
	sendRich             func(any, *telegram.RichBuilder, *telegram.SendOptions) (*telegram.NewMessage, error)
	editRich             func(any, int32, *telegram.RichBuilder, *telegram.SendOptions) (*telegram.NewMessage, error)
	sendPoll             func(any, string, []string, *telegram.PollOptions) (*telegram.NewMessage, error)
	deleteMessages       func(any, []int32, bool) (*telegram.MessagesAffectedMessages, error)
	deleteHistory        func(*telegram.MessagesDeleteHistoryParams) (*telegram.MessagesAffectedHistory, error)
	deleteScheduled      func(telegram.InputPeer, []int32) error
	forwardMessages      func(*telegram.MessagesForwardMessagesParams) (telegram.Updates, error)
	pinMessage           func(any, int32) error
	unpinMessage         func(any, int32) error
	unpinAllMessages     func(telegram.InputPeer) error
	readHistory          func(any) error
	saveDraft            func(*telegram.MessagesSaveDraftParams) error
	sendReaction         func(any, int32, telegram.Reaction, bool) error
	removeReaction       func(any, int32) error
	downloadVoice        func(*telegram.NewMessage) ([]byte, error)
	transcribeAudio      func(any, int32) (*telegram.MessagesTranscribedAudio, error)
}

func (f *fakeClient) ResolveEntity(identifier any) (any, error) {
	if f.resolveEntity != nil {
		return f.resolveEntity(identifier)
	}
	return &telegram.UserObj{ID: 7, FirstName: "Tester", Username: "tester"}, nil
}

func (f *fakeClient) ResolveInputPeer(identifier any) (telegram.InputPeer, error) {
	if f.resolveInputPeer != nil {
		return f.resolveInputPeer(identifier)
	}
	return &telegram.InputPeerUser{UserID: 7, AccessHash: 1}, nil
}

func (f *fakeClient) PeerID(peer telegram.Peer) int64 {
	if f.peerID != nil {
		return f.peerID(peer)
	}
	return peerMarkedID(peer)
}

func (f *fakeClient) WarmDialogs() error {
	if f.warmDialogs != nil {
		return f.warmDialogs()
	}
	return nil
}

func (f *fakeClient) GetMe() (*telegram.UserObj, error) {
	if f.getMe != nil {
		return f.getMe()
	}
	return &telegram.UserObj{}, nil
}

func (f *fakeClient) GetMessages(peer any, opt *telegram.SearchOption) ([]telegram.NewMessage, error) {
	if f.getMessages != nil {
		return f.getMessages(peer, opt)
	}
	return nil, nil
}

func (f *fakeClient) GetMessageByID(peer any, id int32) (*telegram.NewMessage, error) {
	if f.getMessageByID != nil {
		return f.getMessageByID(peer, id)
	}
	return nil, nil
}

func (f *fakeClient) GetMediaGroup(peer any, id int32) ([]telegram.NewMessage, error) {
	if f.getMediaGroup != nil {
		return f.getMediaGroup(peer, id)
	}
	return nil, nil
}

func (f *fakeClient) GetDialogs(limit int32) ([]telegram.TLDialog, error) {
	if f.getDialogs != nil {
		return f.getDialogs(limit)
	}
	return nil, nil
}

func (f *fakeClient) GetScheduledHistory(peer telegram.InputPeer) ([]*telegram.MessageObj, error) {
	if f.getScheduledHistory != nil {
		return f.getScheduledHistory(peer)
	}
	return nil, nil
}

func (f *fakeClient) GetAllDrafts() (telegram.Updates, error) {
	if f.getAllDrafts != nil {
		return f.getAllDrafts()
	}
	return &telegram.UpdatesObj{}, nil
}

func (f *fakeClient) GetBotCallbackAnswer(params *telegram.MessagesGetBotCallbackAnswerParams) (*telegram.MessagesBotCallbackAnswer, error) {
	if f.getBotCallbackAnswer != nil {
		return f.getBotCallbackAnswer(params)
	}
	return &telegram.MessagesBotCallbackAnswer{}, nil
}

func (f *fakeClient) GetMessageReactionsList(params *telegram.MessagesGetMessageReactionsListParams) (*telegram.MessagesMessageReactionsList, error) {
	if f.getReactionsList != nil {
		return f.getReactionsList(params)
	}
	return &telegram.MessagesMessageReactionsList{}, nil
}

func (f *fakeClient) GetSendAs(peer telegram.InputPeer) (*telegram.ChannelsSendAsPeers, error) {
	if f.getSendAs != nil {
		return f.getSendAs(peer)
	}
	return &telegram.ChannelsSendAsPeers{}, nil
}

func (f *fakeClient) SendMessage(peer any, body string, opts *telegram.SendOptions) (*telegram.NewMessage, error) {
	if f.sendMessage != nil {
		return f.sendMessage(peer, body, opts)
	}
	return &telegram.NewMessage{ID: 1, Message: &telegram.MessageObj{}}, nil
}

func (f *fakeClient) EditMessage(peer any, id int32, body string, opts *telegram.SendOptions) (*telegram.NewMessage, error) {
	if f.editMessage != nil {
		return f.editMessage(peer, id, body, opts)
	}
	return &telegram.NewMessage{ID: id, Message: &telegram.MessageObj{}}, nil
}

func (f *fakeClient) SendRich(peer any, builder *telegram.RichBuilder, opts *telegram.SendOptions) (*telegram.NewMessage, error) {
	if f.sendRich != nil {
		return f.sendRich(peer, builder, opts)
	}
	return &telegram.NewMessage{ID: 1, Message: &telegram.MessageObj{}}, nil
}

func (f *fakeClient) EditRich(peer any, id int32, builder *telegram.RichBuilder, opts *telegram.SendOptions) (*telegram.NewMessage, error) {
	if f.editRich != nil {
		return f.editRich(peer, id, builder, opts)
	}
	return &telegram.NewMessage{ID: id, Message: &telegram.MessageObj{}}, nil
}

func (f *fakeClient) SendPoll(peer any, question string, options []string, opts *telegram.PollOptions) (*telegram.NewMessage, error) {
	if f.sendPoll != nil {
		return f.sendPoll(peer, question, options, opts)
	}
	return &telegram.NewMessage{ID: 1, Message: &telegram.MessageObj{}}, nil
}

func (f *fakeClient) DeleteMessages(peer any, ids []int32, revoke bool) (*telegram.MessagesAffectedMessages, error) {
	if f.deleteMessages != nil {
		return f.deleteMessages(peer, ids, revoke)
	}
	return &telegram.MessagesAffectedMessages{}, nil
}

func (f *fakeClient) DeleteHistory(params *telegram.MessagesDeleteHistoryParams) (*telegram.MessagesAffectedHistory, error) {
	if f.deleteHistory != nil {
		return f.deleteHistory(params)
	}
	return &telegram.MessagesAffectedHistory{}, nil
}

func (f *fakeClient) DeleteScheduledMessages(peer telegram.InputPeer, ids []int32) error {
	if f.deleteScheduled != nil {
		return f.deleteScheduled(peer, ids)
	}
	return nil
}

func (f *fakeClient) ForwardMessages(params *telegram.MessagesForwardMessagesParams) (telegram.Updates, error) {
	if f.forwardMessages != nil {
		return f.forwardMessages(params)
	}
	return &telegram.UpdatesObj{}, nil
}

func (f *fakeClient) PinMessage(peer any, id int32) error {
	if f.pinMessage != nil {
		return f.pinMessage(peer, id)
	}
	return nil
}

func (f *fakeClient) UnpinMessage(peer any, id int32) error {
	if f.unpinMessage != nil {
		return f.unpinMessage(peer, id)
	}
	return nil
}

func (f *fakeClient) UnpinAllMessages(peer telegram.InputPeer) error {
	if f.unpinAllMessages != nil {
		return f.unpinAllMessages(peer)
	}
	return nil
}

func (f *fakeClient) ReadHistory(peer any) error {
	if f.readHistory != nil {
		return f.readHistory(peer)
	}
	return nil
}

func (f *fakeClient) SaveDraft(params *telegram.MessagesSaveDraftParams) error {
	if f.saveDraft != nil {
		return f.saveDraft(params)
	}
	return nil
}

func (f *fakeClient) SendReaction(peer any, id int32, reaction telegram.Reaction, big bool) error {
	if f.sendReaction != nil {
		return f.sendReaction(peer, id, reaction, big)
	}
	return nil
}

func (f *fakeClient) RemoveReaction(peer any, id int32) error {
	if f.removeReaction != nil {
		return f.removeReaction(peer, id)
	}
	return nil
}

func (f *fakeClient) DownloadVoice(msg *telegram.NewMessage) ([]byte, error) {
	if f.downloadVoice != nil {
		return f.downloadVoice(msg)
	}
	return []byte("audio"), nil
}

func (f *fakeClient) TranscribeAudio(peer any, id int32) (*telegram.MessagesTranscribedAudio, error) {
	if f.transcribeAudio != nil {
		return f.transcribeAudio(peer, id)
	}
	return &telegram.MessagesTranscribedAudio{Text: "native text"}, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func configure(t *testing.T, cl Client, rt Runtime) {
	t.Helper()
	rt.Clients = func(string) (Client, error) { return cl, nil }
	Configure(rt)
	t.Cleanup(func() { Configure(Runtime{}) })
}

func sampleMessage(id int32, body string) telegram.NewMessage {
	return telegram.NewMessage{
		ID: id,
		Message: &telegram.MessageObj{
			ID:      id,
			Message: body,
			Date:    1700000000,
			PeerID:  &telegram.PeerUser{UserID: 1},
			FromID:  &telegram.PeerUser{UserID: 11},
		},
		Sender: &telegram.UserObj{ID: 11, FirstName: "Alice"},
	}
}

func mustContain(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("result %q does not contain %q", got, want)
	}
}

type fakeAllowlist struct {
	enabled bool
	ids     map[int64]bool
}

func (f fakeAllowlist) AllowsChatID(id int64) bool { return f.ids[id] }
func (f fakeAllowlist) AllowsUsername(string) bool { return false }
func (f fakeAllowlist) Enabled() bool              { return f.enabled }

// ---------------------------------------------------------------------------
// per-tool behavior
// ---------------------------------------------------------------------------

func TestGetMessagesFormatsAndPaginates(t *testing.T) {
	var seen *telegram.SearchOption
	cl := &fakeClient{getMessages: func(peer any, opt *telegram.SearchOption) ([]telegram.NewMessage, error) {
		seen = opt
		return []telegram.NewMessage{sampleMessage(5, "hello world")}, nil
	}}
	configure(t, cl, Runtime{Mode: "off"})
	out, err := handleGetMessages(context.Background(), getMessagesInput{ChatID: int64(1), Page: 3, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "ID: 5")
	mustContain(t, out, "Message: hello world")
	if seen == nil || seen.AddOffset != 20 || seen.Limit != 10 {
		t.Fatalf("search option = %+v, want AddOffset 20 Limit 10", seen)
	}
}

func TestGetMessagesEmpty(t *testing.T) {
	configure(t, &fakeClient{}, Runtime{Mode: "off"})
	out, _ := handleGetMessages(context.Background(), getMessagesInput{ChatID: int64(1)})
	if out != "No messages found for this page." {
		t.Fatalf("got %q", out)
	}
}

func TestAllowlistDeniesChat(t *testing.T) {
	configure(t, &fakeClient{}, Runtime{
		Mode:      "off",
		Allowlist: fakeAllowlist{enabled: true, ids: map[int64]bool{99: true}},
	})
	out, _ := handleGetMessages(context.Background(), getMessagesInput{ChatID: int64(1)})
	mustContain(t, out, "restricted by privacy policy")
}

func TestSendMessageAndScheduled(t *testing.T) {
	var sent string
	cl := &fakeClient{sendMessage: func(peer any, body string, opts *telegram.SendOptions) (*telegram.NewMessage, error) {
		sent = body
		return &telegram.NewMessage{ID: 9, Message: &telegram.MessageObj{}}, nil
	}}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleSendMessage(context.Background(), sendMessageInput{ChatID: int64(1), Message: "**hi** there", ParseMode: "md"})
	if out != "Message sent successfully." || sent != "hi there" {
		t.Fatalf("send_message: out=%q sent=%q", out, sent)
	}
	out, _ = handleSendScheduledMessage(context.Background(), sendScheduledMessageInput{
		ChatID: int64(1), Message: "later", ScheduleDate: "2030-01-02T15:04:05Z",
	})
	mustContain(t, out, "Scheduled message 9 for 2030-01-02T15:04:05+00:00")
	bad, _ := handleSendScheduledMessage(context.Background(), sendScheduledMessageInput{
		ChatID: int64(1), Message: "x", ScheduleDate: "not-a-date",
	})
	mustContain(t, bad, "schedule_date could not be parsed")
}

func TestScheduledMessagesListAndDelete(t *testing.T) {
	cl := &fakeClient{
		getScheduledHistory: func(peer telegram.InputPeer) ([]*telegram.MessageObj, error) {
			return []*telegram.MessageObj{{ID: 4, Message: "pending", Date: 1700000000}}, nil
		},
	}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleGetScheduledMessages(context.Background(), getScheduledMessagesInput{ChatID: int64(1)})
	mustContain(t, out, "Scheduled messages in chat 1 (1):")
	mustContain(t, out, "ID: 4")
	mustContain(t, out, "Text: pending")

	empty, _ := handleDeleteScheduledMessage(context.Background(), deleteScheduledMessageInput{ChatID: int64(1)})
	if empty != "message_ids must be a non-empty list." {
		t.Fatalf("got %q", empty)
	}
	done, _ := handleDeleteScheduledMessage(context.Background(), deleteScheduledMessageInput{
		ChatID: int64(1), MessageIDs: []int64{4, 5},
	})
	if done != "Deleted 2 scheduled message(s) from chat 1." {
		t.Fatalf("got %q", done)
	}
}

func TestListInlineButtonsAndPress(t *testing.T) {
	markup := &telegram.ReplyInlineMarkup{Rows: []*telegram.KeyboardButtonRow{
		{Buttons: []telegram.KeyboardButton{
			&telegram.KeyboardButtonCallback{Text: "Confirm", Data: []byte{1, 2}},
			&telegram.KeyboardButtonURL{Text: "Docs", URL: "https://example.test"},
		}},
	}}
	message := sampleMessage(6, "buttons")
	message.Message.ReplyMarkup = markup
	cl := &fakeClient{
		getMessageByID: func(peer any, id int32) (*telegram.NewMessage, error) {
			return &message, nil
		},
		getBotCallbackAnswer: func(params *telegram.MessagesGetBotCallbackAnswerParams) (*telegram.MessagesBotCallbackAnswer, error) {
			if string(params.Data) != "\x01\x02" {
				t.Fatalf("callback data = %v", params.Data)
			}
			return &telegram.MessagesBotCallbackAnswer{Message: "confirmed"}, nil
		},
	}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleListInlineButtons(context.Background(), listInlineButtonsInput{ChatID: int64(1), MessageID: int64(6)})
	mustContain(t, out, `"has_callback":true`)
	mustContain(t, out, `"url":"https://example.test"`)

	missing, _ := handlePressInlineButton(context.Background(), pressInlineButtonInput{ChatID: int64(1)})
	mustContain(t, missing, "Provide button_text or button_index")
	pressed, _ := handlePressInlineButton(context.Background(), pressInlineButtonInput{
		ChatID: int64(1), MessageID: int64(6), ButtonText: "confirm",
	})
	mustContain(t, pressed, "confirmed")
}

func TestListMessagesFiltersAndValidates(t *testing.T) {
	cl := &fakeClient{getMessages: func(peer any, opt *telegram.SearchOption) ([]telegram.NewMessage, error) {
		return []telegram.NewMessage{sampleMessage(5, "hello world")}, nil
	}}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleListMessages(context.Background(), listMessagesInput{ChatID: int64(1)})
	mustContain(t, out, `"text":"hello world"`)
	bad, _ := handleListMessages(context.Background(), listMessagesInput{ChatID: int64(1), FromDate: "05/10/2026"})
	if bad != "Invalid from_date format. Use YYYY-MM-DD." {
		t.Fatalf("got %q", bad)
	}
}

func TestTranscribeVoicePaths(t *testing.T) {
	configure(t, &fakeClient{}, Runtime{Mode: "off"})
	off, _ := handleTranscribeVoice(context.Background(), transcribeVoiceInput{ChatID: int64(1), MessageID: 1})
	mustContain(t, off, "transcription_disabled")

	voice := sampleMessage(3, "")
	voice.Message.Media = &telegram.MessageMediaDocument{Document: &telegram.DocumentObj{
		Size: 10, MimeType: "audio/ogg",
		Attributes: []telegram.DocumentAttribute{
			&telegram.DocumentAttributeAudio{Voice: true, Duration: 5},
		},
	}}
	cl := &fakeClient{
		getMessageByID: func(peer any, id int32) (*telegram.NewMessage, error) { return &voice, nil },
	}
	configure(t, cl, Runtime{Mode: "on-demand", Engine: "telegram", Settings: config.TranscribeSettings{}})
	notPremium, _ := handleTranscribeVoice(context.Background(), transcribeVoiceInput{
		ChatID: int64(1), MessageID: 3, Engine: "telegram",
	})
	mustContain(t, notPremium, "telegram_premium_required")

	configure(t, &fakeClient{}, Runtime{Mode: "on-demand"})
	invalid, _ := handleTranscribeVoice(context.Background(), transcribeVoiceInput{
		ChatID: int64(1), MessageID: 3, Engine: "bogus",
	})
	mustContain(t, invalid, "Invalid engine 'bogus'")

	plain := sampleMessage(4, "text")
	configure(t, &fakeClient{getMessageByID: func(peer any, id int32) (*telegram.NewMessage, error) {
		return &plain, nil
	}}, Runtime{Mode: "on-demand"})
	noVoice, _ := handleTranscribeVoice(context.Background(), transcribeVoiceInput{ChatID: int64(1), MessageID: 4, Engine: "telegram"})
	mustContain(t, noVoice, "has no voice message or video note to transcribe")
}

func TestGetMessageContext(t *testing.T) {
	cl := &fakeClient{
		getMessages: func(peer any, opt *telegram.SearchOption) ([]telegram.NewMessage, error) {
			return []telegram.NewMessage{sampleMessage(4, "before")}, nil
		},
		getMessageByID: func(peer any, id int32) (*telegram.NewMessage, error) {
			if id == 5 {
				message := sampleMessage(5, "target")
				return &message, nil
			}
			return nil, nil
		},
	}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleGetMessageContext(context.Background(), getMessageContextInput{ChatID: int64(1), MessageID: 5})
	mustContain(t, out, `"is_target":true`)
	mustContain(t, out, `"target_message_id":5`)
}

func TestGetSendAs(t *testing.T) {
	cl := &fakeClient{getSendAs: func(peer telegram.InputPeer) (*telegram.ChannelsSendAsPeers, error) {
		return &telegram.ChannelsSendAsPeers{
			Peers: []*telegram.SendAsPeer{
				{Peer: &telegram.PeerChannel{ChannelID: 3}, PremiumRequired: true},
			},
			Chats: []telegram.Chat{&telegram.Channel{ID: 3, Title: "My Channel"}},
		}, nil
	}}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleGetSendAs(context.Background(), getSendAsInput{ChatID: int64(1)})
	mustContain(t, out, `"premium_required":true`)
	mustContain(t, out, "My Channel")
}

func TestForwardSingleAndBatch(t *testing.T) {
	var forwarded *telegram.MessagesForwardMessagesParams
	cl := &fakeClient{
		forwardMessages: func(params *telegram.MessagesForwardMessagesParams) (telegram.Updates, error) {
			forwarded = params
			return &telegram.UpdatesObj{}, nil
		},
	}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleForwardMessage(context.Background(), forwardMessageInput{
		FromChatID: int64(1), MessageID: int64(5), ToChatID: int64(2),
	})
	if out != "Message 5 forwarded from 1 to 2." {
		t.Fatalf("got %q", out)
	}
	if forwarded == nil || len(forwarded.ID) != 1 || forwarded.ID[0] != 5 {
		t.Fatalf("forwarded params = %+v", forwarded)
	}
	if got, _ := handleForwardMessages(context.Background(), forwardMessagesInput{FromChatID: int64(1), ToChatID: int64(2)}); got != "Error: message_ids must contain at least one id." {
		t.Fatalf("got %q", got)
	}
	out, _ = handleForwardMessages(context.Background(), forwardMessagesInput{
		FromChatID: int64(1), ToChatID: int64(2), MessageIDs: []int64{5, 6},
	})
	mustContain(t, out, "2 messages forwarded from 1 to 2.")
}

func TestEditDeletePinUnpinReadTools(t *testing.T) {
	cl := &fakeClient{
		deleteHistory: func(params *telegram.MessagesDeleteHistoryParams) (*telegram.MessagesAffectedHistory, error) {
			return &telegram.MessagesAffectedHistory{PtsCount: 3, Offset: 2}, nil
		},
		deleteMessages: func(peer any, ids []int32, revoke bool) (*telegram.MessagesAffectedMessages, error) {
			return &telegram.MessagesAffectedMessages{PtsCount: int32(len(ids))}, nil
		},
	}
	configure(t, cl, Runtime{Mode: "off"})
	if out, _ := handleEditMessage(context.Background(), editMessageInput{ChatID: int64(1), MessageID: 5, NewText: "edited"}); out != "Message 5 edited." {
		t.Fatalf("edit_message: %q", out)
	}
	if out, _ := handleDeleteMessage(context.Background(), deleteMessageInput{ChatID: int64(1), MessageID: 5}); out != "Message 5 deleted." {
		t.Fatalf("delete_message: %q", out)
	}
	out, _ := handleDeleteChatHistory(context.Background(), deleteChatHistoryInput{ChatID: int64(1)})
	mustContain(t, out, "history cleared for you: 3 messages deleted (offset=2).")
	out, _ = handleDeleteMessagesBulk(context.Background(), deleteMessagesBulkInput{ChatID: int64(1), MessageIDs: []int64{1, 2}})
	if out != "Deleted 2 of 2 messages from chat 1." {
		t.Fatalf("delete_messages_bulk: %q", out)
	}
	if out, _ := handlePinMessage(context.Background(), pinMessageInput{ChatID: int64(1), MessageID: 5}); out != "Message 5 pinned in chat 1." {
		t.Fatalf("pin_message: %q", out)
	}
	if out, _ := handleUnpinMessage(context.Background(), pinMessageInput{ChatID: int64(1), MessageID: 5}); out != "Message 5 unpinned in chat 1." {
		t.Fatalf("unpin_message: %q", out)
	}
	if out, _ := handleUnpinAllMessages(context.Background(), unpinAllMessagesInput{ChatID: int64(1)}); out != "All messages unpinned in chat 1." {
		t.Fatalf("unpin_all_messages: %q", out)
	}
	if out, _ := handleMarkAsRead(context.Background(), markAsReadInput{ChatID: int64(1)}); out != "Marked all messages as read in chat 1." {
		t.Fatalf("mark_as_read: %q", out)
	}
	if out, _ := handleReplyToMessage(context.Background(), replyToMessageInput{ChatID: int64(1), MessageID: 5, Text: "re"}); out != "Replied to message 5 in chat 1." {
		t.Fatalf("reply_to_message: %q", out)
	}
}

func TestSearchToolsReturnRecords(t *testing.T) {
	cl := &fakeClient{getMessages: func(peer any, opt *telegram.SearchOption) ([]telegram.NewMessage, error) {
		return []telegram.NewMessage{sampleMessage(5, "match")}, nil
	}}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleSearchMessages(context.Background(), searchMessagesInput{ChatID: int64(1), Query: "match"})
	mustContain(t, out, `"text":"match"`)
	global, _ := handleSearchGlobal(context.Background(), searchGlobalInput{Query: "match"})
	mustContain(t, global, `"chat_name"`)
	history, _ := handleGetHistory(context.Background(), getHistoryInput{ChatID: int64(1)})
	mustContain(t, history, `"id":5`)
	configure(t, &fakeClient{}, Runtime{Mode: "off"})
	pinned, _ := handleGetPinnedMessages(context.Background(), getPinnedMessagesInput{ChatID: int64(1)})
	if pinned != "No pinned messages found in this chat." {
		t.Fatalf("get_pinned_messages: %q", pinned)
	}
}

func TestCreatePollValidationAndSend(t *testing.T) {
	cl := &fakeClient{sendPoll: func(peer any, question string, options []string, opts *telegram.PollOptions) (*telegram.NewMessage, error) {
		if question != "Best?" || len(options) != 2 || !opts.PublicVoters {
			t.Fatalf("poll args: %q %v %+v", question, options, opts)
		}
		return &telegram.NewMessage{}, nil
	}}
	configure(t, cl, Runtime{Mode: "off"})
	if out, _ := handleCreatePoll(context.Background(), createPollInput{ChatID: int64(1), Question: " "}); out != "Error: Poll question cannot be empty." {
		t.Fatalf("create_poll empty: %q", out)
	}
	if out, _ := handleCreatePoll(context.Background(), createPollInput{ChatID: int64(1), Question: "Q", Options: []any{"only"}}); out != "Error: Poll must have at least 2 options." {
		t.Fatalf("create_poll one option: %q", out)
	}
	if out, _ := handleCreatePoll(context.Background(), createPollInput{ChatID: int64(1), Question: "Q", Options: []any{"a", "a"}}); out != "Error: Poll options must be unique." {
		t.Fatalf("create_poll duplicates: %q", out)
	}
	out, _ := handleCreatePoll(context.Background(), createPollInput{ChatID: int64(1), Question: "Best?", Options: "a, b"})
	if out != "Poll created successfully in chat 1." {
		t.Fatalf("create_poll: %q", out)
	}
}

func TestReactions(t *testing.T) {
	var emoji string
	cl := &fakeClient{
		sendReaction: func(peer any, id int32, reaction telegram.Reaction, big bool) error {
			if value, ok := reaction.(*telegram.ReactionEmoji); ok {
				emoji = value.Emoticon
			}
			return nil
		},
		getMessageByID: func(peer any, id int32) (*telegram.NewMessage, error) {
			message := sampleMessage(5, "reacted")
			message.Message.Reactions = &telegram.MessageReactions{Results: []*telegram.ReactionCount{
				{Reaction: &telegram.ReactionEmoji{Emoticon: "👍"}, Count: 2},
			}}
			return &message, nil
		},
		getReactionsList: func(params *telegram.MessagesGetMessageReactionsListParams) (*telegram.MessagesMessageReactionsList, error) {
			return &telegram.MessagesMessageReactionsList{Reactions: []*telegram.MessagePeerReaction{
				{PeerID: &telegram.PeerUser{UserID: 42}, Reaction: &telegram.ReactionEmoji{Emoticon: "👍"}, Date: 1700000000},
			}}, nil
		},
	}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleSendReaction(context.Background(), sendReactionInput{ChatID: int64(1), MessageID: 5, Emoji: "👍"})
	mustContain(t, out, "Reaction '👍' sent to message 5 in chat 1.")
	if emoji != "👍" {
		t.Fatalf("emoji = %q", emoji)
	}
	invalid, _ := handleSendReaction(context.Background(), sendReactionInput{ChatID: int64(1), MessageID: 5, Emoji: "custom:0"})
	if invalid != "Invalid custom reaction. Use custom:<positive document ID>." {
		t.Fatalf("custom reaction: %q", invalid)
	}
	if out, _ := handleRemoveReaction(context.Background(), removeReactionInput{ChatID: int64(1), MessageID: 5}); out != "Reaction removed from message 5 in chat 1." {
		t.Fatalf("remove_reaction: %q", out)
	}
	list, _ := handleGetMessageReactions(context.Background(), getMessageReactionsInput{ChatID: int64(1), MessageID: 5})
	mustContain(t, list, `"emoji": "👍"`)
	mustContain(t, list, `"count": 1`)
}

func TestGetMessageReactionsWhenEmpty(t *testing.T) {
	cl := &fakeClient{getMessageByID: func(peer any, id int32) (*telegram.NewMessage, error) {
		message := sampleMessage(5, "no reactions")
		return &message, nil
	}}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleGetMessageReactions(context.Background(), getMessageReactionsInput{ChatID: int64(1), MessageID: 5})
	mustContain(t, out, `"count": 0`)
}

func TestSaveAndClearDraft(t *testing.T) {
	var params *telegram.MessagesSaveDraftParams
	cl := &fakeClient{saveDraft: func(p *telegram.MessagesSaveDraftParams) error {
		params = p
		return nil
	}}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleSaveDraft(context.Background(), saveDraftInput{ChatID: int64(1), Message: "draft text"})
	mustContain(t, out, "Draft saved to chat 1.")
	if params == nil || params.Message != "draft text" {
		t.Fatalf("draft params = %+v", params)
	}
	if out, _ := handleClearDraft(context.Background(), clearDraftInput{ChatID: int64(1)}); out != "Draft cleared from chat 1." {
		t.Fatalf("clear_draft: %q", out)
	}
}

func TestGetDrafts(t *testing.T) {
	cl := &fakeClient{getAllDrafts: func() (telegram.Updates, error) {
		return &telegram.UpdatesObj{Updates: []telegram.Update{
			&telegram.UpdateDraftMessage{
				Peer:  &telegram.PeerUser{UserID: 9},
				Draft: &telegram.DraftMessageObj{Message: "unsent", Date: 1700000000},
			},
		}}, nil
	}}
	configure(t, cl, Runtime{Mode: "off"})
	out, _ := handleGetDrafts(context.Background(), getDraftsInput{})
	mustContain(t, out, `"count": 1`)
	mustContain(t, out, "unsent")
}

func TestExportUnreadMessagesWritesFile(t *testing.T) {
	cl := &fakeClient{
		getDialogs: func(limit int32) ([]telegram.TLDialog, error) {
			return []telegram.TLDialog{{
				Dialog: &telegram.DialogObj{UnreadCount: 1},
				Peer:   &telegram.PeerUser{UserID: 7},
			}}, nil
		},
		getMessages: func(peer any, opt *telegram.SearchOption) ([]telegram.NewMessage, error) {
			return []telegram.NewMessage{sampleMessage(5, "unread text")}, nil
		},
	}
	configure(t, cl, Runtime{Mode: "off"})
	path := filepath.Join(t.TempDir(), "export", "unread.json")
	out, _ := handleExportUnreadMessages(context.Background(), exportUnreadMessagesInput{
		ChatIDs: []any{int64(7)}, OutputPath: path,
	})
	mustContain(t, out, `"status": "ok"`)
	mustContain(t, out, `"messages_exported": 1`)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("export file: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("export json: %v", err)
	}
	if _, ok := parsed["chats"]; !ok {
		t.Fatalf("export missing chats key: %s", string(data))
	}
}

func TestFormatDateChip(t *testing.T) {
	if conflict := chipConflict("html"); conflict != "format_date needs plain-text messages (leave parse_mode unset)." {
		t.Fatalf("chip conflict: %q", conflict)
	}
	chip, errText := dateEntity("Meet on 13/09 17:00 ok", "13/09 17:00")
	if errText != "" {
		t.Fatalf("date entity error: %s", errText)
	}
	formatted, ok := chip.(*telegram.MessageEntityFormattedDate)
	if !ok || !formatted.ShortTime || formatted.Length != 11 {
		t.Fatalf("date entity = %+v", chip)
	}
	if _, errText := dateEntity("Meet on 32/13", "32/13"); errText != "format_date '32/13' is not a valid date." {
		t.Fatalf("invalid date error: %q", errText)
	}
}

func TestRegisteredNamesAreSortedUnique(t *testing.T) {
	names := mcpserver.DefaultRegistry.Names()
	if !sort.StringsAreSorted(names) {
		t.Fatalf("registry names are not sorted: %v", names)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Fatalf("duplicate registered tool %q", name)
		}
		seen[name] = true
	}
}

func TestSortHelpers(t *testing.T) {
	ids := []int64{5, 1, 3}
	sortInt64(ids)
	if ids[0] != 1 || ids[2] != 5 {
		t.Fatalf("sortInt64 = %v", ids)
	}
	kit.SetDefaultReporter(kit.NewErrorLogger("", os.Stderr))
}

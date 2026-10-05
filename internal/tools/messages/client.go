package messages

// This file holds the client-facing contract of the messages tools plus the
// gogram-backed implementation. Tool bodies program against Client, never
// against a live *telegram.Client: offline tests inject a fake, and the boot
// layer wires NewGogramClient over a connected session.

import (
	"bytes"
	"context"
	"fmt"
	"os"

	telegram "github.com/amarnathcjd/gogram/telegram"
)

// Client is the Telegram access the messages tools need. It mirrors the
// Telethon client calls the Python tools make; the gogram-backed
// implementation lives in this package (NewGogramClient) and tests inject
// fakes, so every tool runs offline.
type Client interface {
	// ResolveEntity resolves an identifier (marked/bare ID, @username, handle,
	// phone number, "me"/"self") to a full gogram entity (*telegram.UserObj,
	// *telegram.Channel, *telegram.ChatObj).
	ResolveEntity(identifier any) (any, error)
	// ResolveInputPeer resolves an identifier to an InputPeer for RPCs that
	// take one directly.
	ResolveInputPeer(identifier any) (telegram.InputPeer, error)
	// PeerID returns the marked peer ID of a gogram Peer.
	PeerID(peer telegram.Peer) int64
	// WarmDialogs populates the entity cache from the dialog list.
	WarmDialogs() error
	// GetMe returns the connected user (Premium checks).
	GetMe() (*telegram.UserObj, error)

	// GetMessages fetches messages: a listing (opt.Limit/AddOffset/Query/
	// MinID/MaxID/MinDate/MaxDate), a search, or one or more IDs (opt.IDs).
	GetMessages(peer any, opt *telegram.SearchOption) ([]telegram.NewMessage, error)
	// GetMessageByID fetches a single message; nil means not found.
	GetMessageByID(peer any, id int32) (*telegram.NewMessage, error)
	// GetMediaGroup returns the album siblings of a message.
	GetMediaGroup(peer any, id int32) ([]telegram.NewMessage, error)
	// GetDialogs returns dialogs with their unread state.
	GetDialogs(limit int32) ([]telegram.TLDialog, error)
	// GetScheduledHistory lists the pending scheduled messages of a chat.
	GetScheduledHistory(peer telegram.InputPeer) ([]*telegram.MessageObj, error)
	// GetAllDrafts returns the Updates payload of messages.getAllDrafts.
	GetAllDrafts() (telegram.Updates, error)
	// GetBotCallbackAnswer presses an inline callback button.
	GetBotCallbackAnswer(params *telegram.MessagesGetBotCallbackAnswerParams) (*telegram.MessagesBotCallbackAnswer, error)
	// GetMessageReactionsList lists who reacted to a message.
	GetMessageReactionsList(params *telegram.MessagesGetMessageReactionsListParams) (*telegram.MessagesMessageReactionsList, error)
	// GetSendAs lists the allowed send-as peers of a destination.
	GetSendAs(peer telegram.InputPeer) (*telegram.ChannelsSendAsPeers, error)

	// SendMessage sends text (entities pre-parsed by the caller).
	SendMessage(peer any, text string, opts *telegram.SendOptions) (*telegram.NewMessage, error)
	// EditMessage edits a message the account sent.
	EditMessage(peer any, id int32, text string, opts *telegram.SendOptions) (*telegram.NewMessage, error)
	// SendRich sends a server-parsed rich message (Premium accounts only).
	SendRich(peer any, builder *telegram.RichBuilder, opts *telegram.SendOptions) (*telegram.NewMessage, error)
	// EditRich edits a message with server-parsed rich content.
	EditRich(peer any, id int32, builder *telegram.RichBuilder, opts *telegram.SendOptions) (*telegram.NewMessage, error)
	// SendPoll sends a native poll.
	SendPoll(peer any, question string, options []string, opts *telegram.PollOptions) (*telegram.NewMessage, error)
	// DeleteMessages deletes messages; revoke selects "for both parties".
	DeleteMessages(peer any, ids []int32, revoke bool) (*telegram.MessagesAffectedMessages, error)
	// DeleteHistory clears a chat's history.
	DeleteHistory(params *telegram.MessagesDeleteHistoryParams) (*telegram.MessagesAffectedHistory, error)
	// DeleteScheduledMessages deletes pending scheduled messages.
	DeleteScheduledMessages(peer telegram.InputPeer, ids []int32) error
	// ForwardMessages forwards messages (raw request, so the caller can read
	// the UpdateMessageID correlation back).
	ForwardMessages(params *telegram.MessagesForwardMessagesParams) (telegram.Updates, error)
	// PinMessage pins a message in a chat.
	PinMessage(peer any, id int32) error
	// UnpinMessage unpins a message in a chat.
	UnpinMessage(peer any, id int32) error
	// UnpinAllMessages unpins every pinned message of a chat.
	UnpinAllMessages(peer telegram.InputPeer) error
	// ReadHistory marks a chat read up to the latest message.
	ReadHistory(peer any) error
	// SaveDraft saves (or clears) a draft.
	SaveDraft(params *telegram.MessagesSaveDraftParams) error
	// SendReaction sends one reaction; big requests the animation.
	SendReaction(peer any, id int32, reaction telegram.Reaction, big bool) error
	// RemoveReaction removes the account's own reaction.
	RemoveReaction(peer any, id int32) error

	// DownloadVoice fetches the audio bytes of a voice message / video note.
	DownloadVoice(msg *telegram.NewMessage) ([]byte, error)
	// TranscribeAudio runs Telegram's native transcription of a voice note.
	TranscribeAudio(peer any, id int32) (*telegram.MessagesTranscribedAudio, error)
}

// gogramClient adapts a connected *telegram.Client to Client.
type gogramClient struct {
	client *telegram.Client
}

// NewGogramClient returns the Client backed by a connected gogram client.
func NewGogramClient(client *telegram.Client) Client { return &gogramClient{client: client} }

// ResolveEntity resolves the identifier and loads the full entity object, so
// kit.WrapEntity can classify it.
func (g *gogramClient) ResolveEntity(identifier any) (any, error) {
	peer, err := g.client.ResolvePeer(identifier)
	if err != nil {
		return nil, err
	}
	switch p := peer.(type) {
	case *telegram.InputPeerSelf:
		return g.client.GetMe()
	case *telegram.InputPeerUser:
		return g.client.GetUser(p.UserID)
	case *telegram.InputPeerUserFromMessage:
		return g.client.GetUser(p.UserID)
	case *telegram.InputPeerChat:
		return g.client.GetChat(p.ChatID)
	case *telegram.InputPeerChannel:
		return g.client.GetChannel(p.ChannelID)
	case *telegram.InputPeerChannelFromMessage:
		return g.client.GetChannel(p.ChannelID)
	default:
		return nil, fmt.Errorf("could not resolve entity for %v", identifier)
	}
}

func (g *gogramClient) ResolveInputPeer(identifier any) (telegram.InputPeer, error) {
	return g.client.ResolvePeer(identifier)
}

func (g *gogramClient) PeerID(peer telegram.Peer) int64 { return g.client.GetPeerID(peer) }

func (g *gogramClient) WarmDialogs() error {
	_, err := g.client.GetDialogs(&telegram.DialogOptions{Limit: 200})
	return err
}

func (g *gogramClient) GetMe() (*telegram.UserObj, error) { return g.client.GetMe() }

func (g *gogramClient) GetMessages(peer any, opt *telegram.SearchOption) ([]telegram.NewMessage, error) {
	if opt == nil {
		return g.client.GetMessages(peer)
	}
	return g.client.GetMessages(peer, opt)
}

func (g *gogramClient) GetMessageByID(peer any, id int32) (*telegram.NewMessage, error) {
	return g.client.GetMessageByID(peer, id)
}

func (g *gogramClient) GetMediaGroup(peer any, id int32) ([]telegram.NewMessage, error) {
	return g.client.GetMediaGroup(peer, id)
}

func (g *gogramClient) GetDialogs(limit int32) ([]telegram.TLDialog, error) {
	return g.client.GetDialogs(&telegram.DialogOptions{Limit: limit})
}

func (g *gogramClient) GetScheduledHistory(peer telegram.InputPeer) ([]*telegram.MessageObj, error) {
	result, err := g.client.MessagesGetScheduledHistory(peer, 0)
	if err != nil {
		return nil, err
	}
	container, ok := result.(*telegram.MessagesMessagesObj)
	if !ok {
		return nil, nil
	}
	out := make([]*telegram.MessageObj, 0, len(container.Messages))
	for _, message := range container.Messages {
		if obj, ok := message.(*telegram.MessageObj); ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

func (g *gogramClient) GetAllDrafts() (telegram.Updates, error) {
	return g.client.MessagesGetAllDrafts()
}

func (g *gogramClient) GetBotCallbackAnswer(params *telegram.MessagesGetBotCallbackAnswerParams) (*telegram.MessagesBotCallbackAnswer, error) {
	return g.client.MessagesGetBotCallbackAnswer(params)
}

func (g *gogramClient) GetMessageReactionsList(params *telegram.MessagesGetMessageReactionsListParams) (*telegram.MessagesMessageReactionsList, error) {
	return g.client.MessagesGetMessageReactionsList(params)
}

func (g *gogramClient) GetSendAs(peer telegram.InputPeer) (*telegram.ChannelsSendAsPeers, error) {
	return g.client.ChannelsGetSendAs(false, false, peer)
}

func (g *gogramClient) SendMessage(peer any, text string, opts *telegram.SendOptions) (*telegram.NewMessage, error) {
	if opts == nil {
		return g.client.SendMessage(peer, text)
	}
	return g.client.SendMessage(peer, text, opts)
}

func (g *gogramClient) EditMessage(peer any, id int32, text string, opts *telegram.SendOptions) (*telegram.NewMessage, error) {
	if opts == nil {
		return g.client.EditMessage(peer, id, text)
	}
	return g.client.EditMessage(peer, id, text, opts)
}

func (g *gogramClient) SendRich(peer any, builder *telegram.RichBuilder, opts *telegram.SendOptions) (*telegram.NewMessage, error) {
	if opts == nil {
		return g.client.SendRich(peer, builder)
	}
	return g.client.SendRich(peer, builder, opts)
}

func (g *gogramClient) EditRich(peer any, id int32, builder *telegram.RichBuilder, opts *telegram.SendOptions) (*telegram.NewMessage, error) {
	if opts == nil {
		return g.client.EditRich(peer, id, builder)
	}
	return g.client.EditRich(peer, id, builder, opts)
}

func (g *gogramClient) SendPoll(peer any, question string, options []string, opts *telegram.PollOptions) (*telegram.NewMessage, error) {
	if opts == nil {
		return g.client.SendPoll(peer, question, options)
	}
	return g.client.SendPoll(peer, question, options, opts)
}

func (g *gogramClient) DeleteMessages(peer any, ids []int32, revoke bool) (*telegram.MessagesAffectedMessages, error) {
	return g.client.DeleteMessages(peer, ids, !revoke)
}

func (g *gogramClient) DeleteHistory(params *telegram.MessagesDeleteHistoryParams) (*telegram.MessagesAffectedHistory, error) {
	return g.client.MessagesDeleteHistory(params)
}

func (g *gogramClient) DeleteScheduledMessages(peer telegram.InputPeer, ids []int32) error {
	_, err := g.client.MessagesDeleteScheduledMessages(peer, ids)
	return err
}

func (g *gogramClient) ForwardMessages(params *telegram.MessagesForwardMessagesParams) (telegram.Updates, error) {
	return g.client.MessagesForwardMessages(params)
}

func (g *gogramClient) PinMessage(peer any, id int32) error {
	_, err := g.client.PinMessage(peer, id)
	return err
}

func (g *gogramClient) UnpinMessage(peer any, id int32) error {
	_, err := g.client.UnpinMessage(peer, id)
	return err
}

func (g *gogramClient) UnpinAllMessages(peer telegram.InputPeer) error {
	_, err := g.client.MessagesUnpinAllMessages(peer, 0, nil)
	return err
}

func (g *gogramClient) ReadHistory(peer any) error {
	_, err := g.client.SendReadAck(peer)
	return err
}

func (g *gogramClient) SaveDraft(params *telegram.MessagesSaveDraftParams) error {
	_, err := g.client.MessagesSaveDraft(params)
	return err
}

func (g *gogramClient) SendReaction(peer any, id int32, reaction telegram.Reaction, big bool) error {
	return g.client.SendReaction(peer, id, []telegram.Reaction{reaction}, big)
}

func (g *gogramClient) RemoveReaction(peer any, id int32) error {
	return g.client.SendReaction(peer, id, []telegram.Reaction{})
}

func (g *gogramClient) DownloadVoice(msg *telegram.NewMessage) ([]byte, error) {
	var buffer bytes.Buffer
	path, err := g.client.DownloadMedia(msg.Media(), &telegram.DownloadOptions{
		Buffer: &buffer,
		Ctx:    context.Background(),
	})
	if err != nil {
		return nil, err
	}
	if buffer.Len() > 0 {
		return buffer.Bytes(), nil
	}
	if path != "" {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		return data, nil
	}
	return nil, fmt.Errorf("voice download produced no data")
}

func (g *gogramClient) TranscribeAudio(peer any, id int32) (*telegram.MessagesTranscribedAudio, error) {
	inputPeer, err := g.client.ResolvePeer(peer)
	if err != nil {
		return nil, err
	}
	return g.client.MessagesTranscribeAudio(inputPeer, id)
}

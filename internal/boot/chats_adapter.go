package boot

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/tools/chats"
)

// chatsAdapter implements chats.Client over the account's lazily connected
// gogram client. Every method resolves the client first, so a call before the
// connection exists reports the session error through the tool funnel.
type chatsAdapter struct {
	sess *sessionManager
}

var _ chats.Client = (*chatsAdapter)(nil)

func newChatsAdapter(sess *sessionManager) chats.Client {
	return &chatsAdapter{sess: sess}
}

// Resolve mirrors resolve_entity: a marked/bare ID, @username, handle, phone
// number or "me"/"self" becomes a Peer.
func (a *chatsAdapter) Resolve(ctx context.Context, identifier any) (*chats.Peer, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	peer, err := cl.ResolvePeer(identifier)
	if err != nil {
		return nil, err
	}
	return a.peerFromInputPeer(ctx, cl, peer)
}

// Dialogs lists the account's dialogs. A nil Archived option returns the main
// list followed by the archived folder, matching the Python dialog.archived
// view of get_dialogs(); a non-nil option fetches exactly that folder.
func (a *chatsAdapter) Dialogs(ctx context.Context, opts chats.DialogOptions) ([]chats.Dialog, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}

	var raw []telegram.TLDialog
	if opts.Archived == nil {
		main, err := fetchDialogs(cl, 0, opts.Limit)
		if err != nil {
			return nil, botMethodError(cl, err)
		}
		archived, err := fetchDialogs(cl, 1, remainingLimit(opts.Limit, len(main)))
		if err != nil {
			return nil, botMethodError(cl, err)
		}
		raw = append(main, archived...)
	} else {
		folder := int32(0)
		if *opts.Archived {
			folder = 1
		}
		raw, err = fetchDialogs(cl, folder, opts.Limit)
		if err != nil {
			return nil, botMethodError(cl, err)
		}
	}

	out := make([]chats.Dialog, 0, len(raw))
	for _, dialog := range raw {
		obj, ok := dialog.Dialog.(*telegram.DialogObj)
		if !ok {
			continue
		}
		peer, err := a.peerFromInput(ctx, cl, dialog.Peer)
		if err != nil {
			return nil, err
		}
		out = append(out, chats.Dialog{
			Peer:           peer,
			Archived:       obj.FolderID == 1,
			UnreadCount:    int(obj.UnreadCount),
			UnreadMark:     obj.UnreadMark,
			Muted:          dialogMuted(obj),
			UnreadMentions: int(obj.UnreadMentionsCount),
		})
	}
	return out, nil
}

// fetchDialogs reads one folder; limit <= 0 means every dialog (gogram
// paginates internally).
func fetchDialogs(cl *telegram.Client, folder int32, limit int) ([]telegram.TLDialog, error) {
	return cl.GetDialogs(&telegram.DialogOptions{Limit: int32(limit), FolderID: folder})
}

func remainingLimit(limit, fetched int) int {
	if limit <= 0 {
		return 0
	}
	return max(limit-fetched, 0)
}

// dialogMuted reports the call-time mute state, like list_chats comparing
// notify_settings.mute_until against now.
func dialogMuted(dialog *telegram.DialogObj) bool {
	return dialog.NotifySettings != nil && int64(dialog.NotifySettings.MuteUntil) > time.Now().Unix()
}

// botMethodError maps Telegram's bot restriction onto the sentinel the chat
// tools special-case.
func botMethodError(cl *telegram.Client, err error) error {
	if cl.MatchRPCError(err, "BOT_METHOD_INVALID") {
		return chats.ErrBotMethodInvalid
	}
	return err
}

// About fetches one peer's description: users.getFullUser,
// channels.getFullChannel or messages.getFullChat by peer kind.
func (a *chatsAdapter) About(ctx context.Context, peer *chats.Peer) (string, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return "", err
	}
	switch peer.Kind {
	case kit.PeerUser:
		user, err := a.inputUser(ctx, cl, peer)
		if err != nil {
			return "", err
		}
		full, err := cl.UsersGetFullUser(user)
		if err != nil {
			return "", err
		}
		if full == nil || full.FullUser == nil {
			return "", nil
		}
		return full.FullUser.About, nil
	case kit.PeerBasicGroup:
		full, err := cl.MessagesGetFullChat(peer.ID)
		if err != nil {
			return "", err
		}
		if obj, ok := full.FullChat.(*telegram.ChatFullObj); ok {
			return obj.About, nil
		}
		return "", nil
	default:
		channel, err := a.inputChannel(ctx, cl, peer)
		if err != nil {
			return "", err
		}
		full, err := cl.ChannelsGetFullChannel(channel)
		if err != nil {
			return "", err
		}
		if obj, ok := full.FullChat.(*telegram.ChannelFull); ok {
			return obj.About, nil
		}
		return "", nil
	}
}

// FullChat fetches the full info of a group or channel.
func (a *chatsAdapter) FullChat(ctx context.Context, peer *chats.Peer) (*chats.FullChatInfo, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	info := &chats.FullChatInfo{}
	switch peer.Kind {
	case kit.PeerBasicGroup:
		full, err := cl.MessagesGetFullChat(peer.ID)
		if err != nil {
			return nil, err
		}
		if obj, ok := full.FullChat.(*telegram.ChatFullObj); ok {
			info.About = obj.About
			if participants, ok := obj.Participants.(*telegram.ChatParticipantsObj); ok {
				count := len(participants.Participants)
				info.ParticipantsCount = &count
			}
		}
		info.Chat = firstPeer(full.Chats)
		return info, nil
	case kit.PeerUser:
		return nil, fmt.Errorf("get_full_chat: %v is not a group or channel", peer.ID)
	default:
		channel, err := a.inputChannel(ctx, cl, peer)
		if err != nil {
			return nil, err
		}
		full, err := cl.ChannelsGetFullChannel(channel)
		if err != nil {
			return nil, err
		}
		if obj, ok := full.FullChat.(*telegram.ChannelFull); ok {
			info.About = obj.About
			if obj.ParticipantsCount > 0 {
				count := int(obj.ParticipantsCount)
				info.ParticipantsCount = &count
			}
			if obj.LinkedChatID != 0 {
				linked := obj.LinkedChatID
				info.LinkedChatID = &linked
			}
		}
		info.Chat = firstPeer(full.Chats)
		return info, nil
	}
}

// ParticipantsCount mirrors get_participants(peer, limit=0).total.
func (a *chatsAdapter) ParticipantsCount(ctx context.Context, peer *chats.Peer) (int, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return 0, err
	}
	switch peer.Kind {
	case kit.PeerUser:
		return 0, fmt.Errorf("participants are only available for groups and channels")
	case kit.PeerBasicGroup:
		full, err := cl.MessagesGetFullChat(peer.ID)
		if err != nil {
			return 0, err
		}
		if obj, ok := full.FullChat.(*telegram.ChatFullObj); ok {
			if participants, ok := obj.Participants.(*telegram.ChatParticipantsObj); ok {
				return len(participants.Participants), nil
			}
		}
		return 0, nil
	default:
		_, total, err := cl.GetChatMembers(peer.ID, &telegram.ParticipantOptions{Limit: 1})
		if err != nil {
			return 0, err
		}
		return int(total), nil
	}
}

// PeerDialog resolves exactly one peer's dialog state
// (messages.getPeerDialogs); nil when the peer has no dialog.
func (a *chatsAdapter) PeerDialog(ctx context.Context, peer *chats.Peer) (*chats.PeerDialogInfo, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	inputPeer, err := a.inputPeer(ctx, cl, peer)
	if err != nil {
		return nil, err
	}
	resp, err := cl.MessagesGetPeerDialogs([]telegram.InputDialogPeer{&telegram.InputDialogPeerObj{Peer: inputPeer}})
	if err != nil {
		return nil, err
	}
	for _, dialog := range resp.Dialogs {
		obj, ok := dialog.(*telegram.DialogObj)
		if !ok {
			continue
		}
		return &chats.PeerDialogInfo{
			UnreadCount: int(obj.UnreadCount),
			Archived:    obj.FolderID == 1,
		}, nil
	}
	return nil, nil
}

// LastMessage mirrors get_messages(peer, limit=1) first element; nil when the
// peer has no messages.
func (a *chatsAdapter) LastMessage(ctx context.Context, peer *chats.Peer) (*chats.BriefMessage, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	messages, err := cl.GetMessages(peer.ID, &telegram.SearchOption{Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}
	message := messages[0]
	return &chats.BriefMessage{
		SenderName: messageSenderName(message),
		Date:       time.Unix(int64(message.Date()), 0),
		Text:       message.Text(),
	}, nil
}

// messageSenderName resolves the sender's display name (user name, sender
// chat title, or "" when unknown).
func messageSenderName(message telegram.NewMessage) string {
	if sender, err := message.GetSender(); err == nil && sender != nil {
		name := sender.FirstName
		if sender.LastName != "" {
			if name != "" {
				name += " "
			}
			name += sender.LastName
		}
		return name
	}
	if chat := message.GetSenderChat(); chat != nil {
		return chat.Title
	}
	return ""
}

// SearchChats mirrors contacts.search, the request search_public_chats runs.
func (a *chatsAdapter) SearchChats(ctx context.Context, query string, limit int) ([]*chats.Peer, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	found, err := cl.ContactsSearch(false, false, query, int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]*chats.Peer, 0, len(found.Results))
	for _, result := range found.Results {
		peer, err := a.peerFromInput(ctx, cl, result)
		if err != nil {
			continue
		}
		out = append(out, peer)
	}
	return out, nil
}

// ResolveUsername mirrors contacts.resolveUsername.
func (a *chatsAdapter) ResolveUsername(ctx context.Context, username string) (*chats.Peer, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	entity, err := cl.ResolveUsername(username)
	if err != nil {
		return nil, err
	}
	return peerFromEntity(entity)
}

// Join mirrors channels.joinChannel, reporting the two sentinel failures the
// subscribe tool special-cases.
func (a *chatsAdapter) Join(ctx context.Context, peer *chats.Peer) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	if peer.Kind == kit.PeerBasicGroup {
		return fmt.Errorf("subscribe_public_channel: %v is a basic group, not a channel", peer.ID)
	}
	channel, err := a.inputChannel(ctx, cl, peer)
	if err != nil {
		return err
	}
	if _, err := cl.ChannelsJoinChannel(channel); err != nil {
		switch {
		case cl.MatchRPCError(err, "USER_ALREADY_PARTICIPANT"):
			return fmt.Errorf("%w: %v", chats.ErrAlreadyParticipant, err)
		case cl.MatchRPCError(err, "CHANNEL_PRIVATE"):
			return fmt.Errorf("%w: %v", chats.ErrChannelPrivate, err)
		default:
			return err
		}
	}
	return nil
}

// SetMuted sets the peer's notification mute state
// (account.updateNotifySettings): muted uses mute_until = 2^31-1, unmuted 0.
func (a *chatsAdapter) SetMuted(ctx context.Context, peer *chats.Peer, muted bool) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	inputPeer, err := a.inputPeer(ctx, cl, peer)
	if err != nil {
		return err
	}
	muteUntil := int32(0)
	if muted {
		muteUntil = 2147483647
	}
	_, err = cl.AccountUpdateNotifySettings(
		&telegram.InputNotifyPeerObj{Peer: inputPeer},
		&telegram.InputPeerNotifySettings{MuteUntil: muteUntil},
	)
	return err
}

// SetArchived moves the peer in or out of the archive folder
// (folders.editPeerFolders, folder 1 / folder 0).
func (a *chatsAdapter) SetArchived(ctx context.Context, peer *chats.Peer, archived bool) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	inputPeer, err := a.inputPeer(ctx, cl, peer)
	if err != nil {
		return err
	}
	folder := int32(0)
	if archived {
		folder = 1
	}
	_, err = cl.FoldersEditPeerFolders([]*telegram.InputFolderPeer{{Peer: inputPeer, FolderID: folder}})
	return err
}

// CommonChats mirrors messages.getCommonChats.
func (a *chatsAdapter) CommonChats(ctx context.Context, peer *chats.Peer, maxID int64, limit int) ([]*chats.Peer, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	if peer.Kind != kit.PeerUser {
		return nil, fmt.Errorf("common chats are only available for users")
	}
	user, err := a.inputUser(ctx, cl, peer)
	if err != nil {
		return nil, err
	}
	common, err := cl.MessagesGetCommonChats(user, maxID, int32(limit))
	if err != nil {
		return nil, err
	}
	chatsList, ok := common.(*telegram.MessagesChatsObj)
	if !ok {
		return nil, nil
	}
	out := make([]*chats.Peer, 0)
	for _, chat := range chatsList.Chats {
		if built, err := peerFromEntity(chat); err == nil {
			out = append(out, built)
		}
	}
	return out, nil
}

// ToggleForum mirrors channels.toggleForum.
func (a *chatsAdapter) ToggleForum(ctx context.Context, peer *chats.Peer, enabled, tabs bool) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	channel, err := a.inputChannel(ctx, cl, peer)
	if err != nil {
		return err
	}
	_, err = cl.ChannelsToggleForum(channel, enabled, tabs)
	return err
}

// ForumTopics mirrors the channels.getForumTopics request: the topics plus
// the dates of the messages the request returned alongside them.
func (a *chatsAdapter) ForumTopics(ctx context.Context, peer *chats.Peer, opts chats.ForumTopicsOptions) (*chats.ForumTopicsResult, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	inputPeer, err := a.inputPeer(ctx, cl, peer)
	if err != nil {
		return nil, err
	}
	params := &telegram.MessagesGetForumTopicsParams{
		Peer:        inputPeer,
		Q:           opts.Query,
		OffsetTopic: int32(opts.OffsetTopic),
		Limit:       int32(opts.Limit),
	}
	result, err := cl.MessagesGetForumTopics(params)
	if err != nil {
		return nil, err
	}
	out := &chats.ForumTopicsResult{MessageDates: map[int32]time.Time{}}
	for _, topic := range result.Topics {
		obj, ok := topic.(*telegram.ForumTopicObj)
		if !ok {
			continue
		}
		entry := chats.ForumTopic{
			ID:           obj.ID,
			Title:        obj.Title,
			Closed:       obj.Closed,
			Hidden:       obj.Hidden,
			TopMessageID: obj.TopMessage,
		}
		if obj.UnreadCount > 0 {
			unread := int(obj.UnreadCount)
			entry.UnreadCount = &unread
		}
		out.Topics = append(out.Topics, entry)
	}
	for _, message := range result.Messages {
		if obj, ok := message.(*telegram.MessageObj); ok {
			out.MessageDates[obj.ID] = time.Unix(int64(obj.Date), 0)
		}
	}
	return out, nil
}

// CreateForumTopic mirrors messages.createForumTopic (the implementation
// draws the random_id) and extracts the created topic ID from the Updates
// payload best-effort, like the Python tool.
func (a *chatsAdapter) CreateForumTopic(ctx context.Context, peer *chats.Peer, title string, iconColor, iconEmojiID *int64) (*chats.TopicRef, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	inputPeer, err := a.inputPeer(ctx, cl, peer)
	if err != nil {
		return nil, err
	}
	params := &telegram.MessagesCreateForumTopicParams{
		Peer:     inputPeer,
		Title:    title,
		RandomID: rand.Int63(),
	}
	if iconColor != nil {
		params.IconColor = int32(*iconColor)
	}
	if iconEmojiID != nil {
		params.IconEmojiID = *iconEmojiID
	}
	updates, err := cl.MessagesCreateForumTopic(params)
	if err != nil {
		return nil, err
	}
	return &chats.TopicRef{ID: topicIDFromUpdates(updates)}, nil
}

// topicIDFromUpdates walks an Updates payload for the topic the creation
// produced: the first message's reply header names the topic it belongs to.
func topicIDFromUpdates(updates telegram.Updates) *int32 {
	var list []telegram.Update
	switch u := updates.(type) {
	case *telegram.UpdatesObj:
		list = u.Updates
	case *telegram.UpdatesCombined:
		list = u.Updates
	}
	for _, update := range list {
		var message telegram.Message
		switch v := update.(type) {
		case *telegram.UpdateNewChannelMessage:
			message = v.Message
		case *telegram.UpdateNewMessage:
			message = v.Message
		}
		obj, ok := message.(*telegram.MessageObj)
		if !ok || obj.ReplyTo == nil {
			continue
		}
		if reply, ok := obj.ReplyTo.(*telegram.MessageReplyHeaderObj); ok {
			if reply.ReplyToTopID != 0 {
				topicID := reply.ReplyToTopID
				return &topicID
			}
			if reply.ReplyToMsgID != 0 {
				topicID := reply.ReplyToMsgID
				return &topicID
			}
		}
	}
	return nil
}

// EditForumTopic mirrors messages.editForumTopic. Nil edit fields are left
// unchanged. NOTE: gogram's generated request only sets a bool flag when the
// value is true, so Closed=false / Hidden=false cannot be expressed through
// this SDK version; those two requests are dropped (documented SDK gap).
func (a *chatsAdapter) EditForumTopic(ctx context.Context, peer *chats.Peer, topicID int32, edit chats.ForumTopicEdit) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	inputPeer, err := a.inputPeer(ctx, cl, peer)
	if err != nil {
		return err
	}
	params := &telegram.MessagesEditForumTopicParams{Peer: inputPeer, TopicID: topicID}
	if edit.Title != nil {
		params.Title = *edit.Title
	}
	if edit.IconEmojiID != nil {
		params.IconEmojiID = *edit.IconEmojiID
	}
	if edit.Closed != nil {
		params.Closed = *edit.Closed
	}
	if edit.Hidden != nil {
		params.Hidden = *edit.Hidden
	}
	_, err = cl.MessagesEditForumTopic(params)
	return err
}

// DeleteTopicHistory mirrors one messages.deleteTopicHistory request and
// returns the affected-history offset (0 when the deletion is complete).
func (a *chatsAdapter) DeleteTopicHistory(ctx context.Context, peer *chats.Peer, topicID int32) (int32, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return 0, err
	}
	inputPeer, err := a.inputPeer(ctx, cl, peer)
	if err != nil {
		return 0, err
	}
	affected, err := cl.MessagesDeleteTopicHistory(inputPeer, topicID)
	if err != nil {
		return 0, err
	}
	if affected == nil {
		return 0, nil
	}
	return affected.Offset, nil
}

// ReadParticipants mirrors messages.getMessageReadParticipants, mapping the
// RPC failures the tool has dedicated messages for.
func (a *chatsAdapter) ReadParticipants(ctx context.Context, peer *chats.Peer, messageID int32) ([]chats.ReadParticipant, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	inputPeer, err := a.inputPeer(ctx, cl, peer)
	if err != nil {
		return nil, err
	}
	dates, err := cl.MessagesGetMessageReadParticipants(inputPeer, messageID)
	if err != nil {
		switch {
		case cl.MatchRPCError(err, "MSG_TOO_OLD"):
			return nil, fmt.Errorf("%w: %v", chats.ErrMessageTooOld, err)
		case cl.MatchRPCError(err, "CHAT_ADMIN_REQUIRED"):
			return nil, fmt.Errorf("%w: %v", chats.ErrAdminRequired, err)
		case cl.MatchRPCError(err, "USER_NOT_PARTICIPANT"):
			return nil, fmt.Errorf("%w: %v", chats.ErrUserNotParticipant, err)
		case cl.MatchRPCError(err, "PEER_ID_INVALID"):
			return nil, fmt.Errorf("%w: %v", chats.ErrPeerIDInvalid, err)
		default:
			return nil, err
		}
	}
	out := make([]chats.ReadParticipant, 0, len(dates))
	for _, entry := range dates {
		if entry == nil {
			continue
		}
		readAt := time.Unix(int64(entry.Date), 0)
		out = append(out, chats.ReadParticipant{UserID: entry.UserID, ReadAt: &readAt})
	}
	return out, nil
}

// ExportMessageLink mirrors channels.exportMessageLink.
func (a *chatsAdapter) ExportMessageLink(ctx context.Context, peer *chats.Peer, messageID int32, thread bool) (*chats.MessageLink, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	channel, err := a.inputChannel(ctx, cl, peer)
	if err != nil {
		return nil, err
	}
	link, err := cl.ChannelsExportMessageLink(false, thread, channel, messageID)
	if err != nil {
		return nil, err
	}
	return &chats.MessageLink{Link: link.Link, HTML: link.Html}, nil
}

// inputPeer builds the InputPeer for a peer from its cached access hash.
func (a *chatsAdapter) inputPeer(ctx context.Context, cl *telegram.Client, peer *chats.Peer) (telegram.InputPeer, error) {
	switch peer.Kind {
	case kit.PeerUser:
		user, err := cl.GetUser(peer.ID)
		if err != nil {
			return nil, err
		}
		return &telegram.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, nil
	case kit.PeerBasicGroup:
		return &telegram.InputPeerChat{ChatID: peer.ID}, nil
	default:
		channel, err := cl.GetChannel(peer.ID)
		if err != nil {
			return nil, err
		}
		return &telegram.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}, nil
	}
}

// inputChannel builds the InputChannel for a channel-family peer.
func (a *chatsAdapter) inputChannel(ctx context.Context, cl *telegram.Client, peer *chats.Peer) (telegram.InputChannel, error) {
	if peer.Kind != kit.PeerChannel && peer.Kind != kit.PeerSupergroup && peer.Kind != kit.PeerGroup {
		return nil, fmt.Errorf("%v is not a channel", peer.ID)
	}
	channel, err := cl.GetChannel(peer.ID)
	if err != nil {
		return nil, err
	}
	return &telegram.InputChannelObj{ChannelID: channel.ID, AccessHash: channel.AccessHash}, nil
}

// inputUser builds the InputUser for a user peer.
func (a *chatsAdapter) inputUser(ctx context.Context, cl *telegram.Client, peer *chats.Peer) (telegram.InputUser, error) {
	if peer.Kind != kit.PeerUser {
		return nil, fmt.Errorf("%v is not a user", peer.ID)
	}
	user, err := cl.GetUser(peer.ID)
	if err != nil {
		return nil, err
	}
	return &telegram.InputUserObj{UserID: user.ID, AccessHash: user.AccessHash}, nil
}

// peerFromInput loads the entity behind a Peer reference and adapts it.
func (a *chatsAdapter) peerFromInput(ctx context.Context, cl *telegram.Client, peer telegram.Peer) (*chats.Peer, error) {
	switch p := peer.(type) {
	case *telegram.PeerUser:
		user, err := cl.GetUser(p.UserID)
		if err != nil {
			return nil, err
		}
		return peerFromEntity(user)
	case *telegram.PeerChat:
		chat, err := cl.GetChat(p.ChatID)
		if err != nil {
			return nil, err
		}
		return peerFromEntity(chat)
	case *telegram.PeerChannel:
		channel, err := cl.GetChannel(p.ChannelID)
		if err != nil {
			return nil, err
		}
		return peerFromEntity(channel)
	default:
		return nil, fmt.Errorf("unsupported peer type %T", peer)
	}
}

// peerFromInputPeer loads the entity behind an InputPeer and adapts it.
func (a *chatsAdapter) peerFromInputPeer(ctx context.Context, cl *telegram.Client, peer telegram.InputPeer) (*chats.Peer, error) {
	switch p := peer.(type) {
	case *telegram.InputPeerSelf:
		me, err := cl.GetMe()
		if err != nil {
			return nil, err
		}
		return peerFromEntity(me)
	case *telegram.InputPeerUser:
		user, err := cl.GetUser(p.UserID)
		if err != nil {
			return nil, err
		}
		return peerFromEntity(user)
	case *telegram.InputPeerUserFromMessage:
		user, err := cl.GetUser(p.UserID)
		if err != nil {
			return nil, err
		}
		return peerFromEntity(user)
	case *telegram.InputPeerChat:
		chat, err := cl.GetChat(p.ChatID)
		if err != nil {
			return nil, err
		}
		return peerFromEntity(chat)
	case *telegram.InputPeerChannel:
		channel, err := cl.GetChannel(p.ChannelID)
		if err != nil {
			return nil, err
		}
		return peerFromEntity(channel)
	case *telegram.InputPeerChannelFromMessage:
		channel, err := cl.GetChannel(p.ChannelID)
		if err != nil {
			return nil, err
		}
		return peerFromEntity(channel)
	default:
		return nil, fmt.Errorf("unsupported peer type %T", peer)
	}
}

func peerFromEntity(entity any) (*chats.Peer, error) {
	switch e := entity.(type) {
	case *telegram.UserObj:
		peer := &chats.Peer{
			ID:        e.ID,
			Kind:      kit.PeerUser,
			Handle:    e.Username,
			FirstName: e.FirstName,
			LastName:  e.LastName,
			Phone:     e.Phone,
			Bot:       e.Bot,
			Verified:  e.Verified,
		}
		if photo, ok := e.Photo.(*telegram.UserProfilePhotoObj); ok {
			id := photo.PhotoID
			peer.HasPhoto = true
			peer.PhotoID = &id
		}
		return peer, nil
	case *telegram.UserEmpty:
		return &chats.Peer{ID: e.ID, Kind: kit.PeerUser}, nil
	case *telegram.ChatObj:
		peer := &chats.Peer{
			ID:    e.ID,
			Kind:  kit.PeerBasicGroup,
			Title: e.Title,
		}
		if photo, ok := e.Photo.(*telegram.ChatPhotoObj); ok {
			id := photo.PhotoID
			peer.HasPhoto = true
			peer.PhotoID = &id
		}
		return peer, nil
	case *telegram.ChatEmpty:
		return &chats.Peer{ID: e.ID, Kind: kit.PeerBasicGroup}, nil
	case *telegram.Channel:
		peer := &chats.Peer{
			ID:        e.ID,
			Kind:      channelPeerKind(e),
			Handle:    e.Username,
			Title:     e.Title,
			Forum:     e.Forum,
			Megagroup: e.Megagroup,
		}
		if photo, ok := e.Photo.(*telegram.ChatPhotoObj); ok {
			id := photo.PhotoID
			peer.HasPhoto = true
			peer.PhotoID = &id
		}
		return peer, nil
	case *telegram.ChannelForbidden:
		return &chats.Peer{ID: e.ID, Kind: channelPeerKind2(e.Megagroup, e.Broadcast), Title: e.Title}, nil
	default:
		return nil, fmt.Errorf("unsupported entity type %T", entity)
	}
}

// channelPeerKind classifies a channel entity the way runtime.get_entity_type
// does: megagroup -> Supergroup, broadcast -> Channel, neither -> Group.
func channelPeerKind(channel *telegram.Channel) kit.PeerKind {
	return channelPeerKind2(channel.Megagroup, channel.Broadcast)
}

func channelPeerKind2(megagroup, broadcast bool) kit.PeerKind {
	switch {
	case megagroup:
		return kit.PeerSupergroup
	case broadcast:
		return kit.PeerChannel
	default:
		return kit.PeerGroup
	}
}

// firstPeer adapts the first chat of a full-chat payload.
func firstPeer(chats []telegram.Chat) *chats.Peer {
	for _, chat := range chats {
		if peer, err := peerFromEntity(chat); err == nil {
			return peer
		}
	}
	return nil
}

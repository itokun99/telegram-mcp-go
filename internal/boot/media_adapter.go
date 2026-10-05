package boot

import (
	"bytes"
	"context"
	"fmt"
	"os"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/tools/media"
)

// mediaAdapter implements media.Client over the account's lazily connected
// gogram client. It embeds the shared entity source, which covers the
// interface's kit.EntitySource half and feeds kit.Resolver.
type mediaAdapter struct {
	*entitySource
}

var _ media.Client = (*mediaAdapter)(nil)
var _ kit.PhotoClient = (*mediaAdapter)(nil)

func newMediaAdapter(sess *sessionManager, src *entitySource) media.Client {
	return &mediaAdapter{entitySource: src}
}

// MessageMedia reports the media facts of one message: existence, the
// description get_media_info prints, and Telegram's declared size.
func (a *mediaAdapter) MessageMedia(entity kit.Entity, messageID int32) (media.MediaInfo, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return media.MediaInfo{}, err
	}
	peer, err := a.inputPeer(entity)
	if err != nil {
		return media.MediaInfo{}, err
	}
	message, err := cl.GetMessageByID(peer, messageID)
	if err != nil {
		return media.MediaInfo{}, err
	}
	if message == nil || !message.IsMedia() {
		return media.MediaInfo{HasMedia: false}, nil
	}
	info := media.MediaInfo{HasMedia: true, Description: cl.Stringify(message.Media())}
	if size, known := mediaSize(message.Media()); known {
		info.DeclaredSize = size
		info.HasDeclaredSize = true
	}
	return info, nil
}

// mediaSize reads Telegram's declared size of a document or photo media.
func mediaSize(media telegram.MessageMedia) (int64, bool) {
	switch m := media.(type) {
	case *telegram.MessageMediaDocument:
		if doc, ok := m.Document.(*telegram.DocumentObj); ok && doc.Size > 0 {
			return doc.Size, true
		}
	case *telegram.MessageMediaPhoto:
		if photo, ok := m.Photo.(*telegram.PhotoObj); ok {
			var largest int32
			for _, size := range photo.Sizes {
				if s, ok := size.(*telegram.PhotoSizeObj); ok && s.Size > largest {
					largest = s.Size
				}
			}
			if largest > 0 {
				return int64(largest), true
			}
		}
	}
	return 0, false
}

// DownloadMessageMedia downloads one message's media into destPath, bounded
// by maxBytes. The declared size is checked before the transfer and the
// landed file after it; a file that exceeds the limit is removed again.
func (a *mediaAdapter) DownloadMessageMedia(entity kit.Entity, messageID int32, destPath string, maxBytes int64) (string, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return "", err
	}
	peer, err := a.inputPeer(entity)
	if err != nil {
		return "", err
	}
	message, err := cl.GetMessageByID(peer, messageID)
	if err != nil {
		return "", err
	}
	if message == nil || !message.IsMedia() {
		return "", fmt.Errorf("%w: message %d has no media", media.ErrNoMedia, messageID)
	}
	if maxBytes > 0 {
		if size, known := mediaSize(message.Media()); known && size > maxBytes {
			return "", fmt.Errorf("%w: media declares %d bytes, limit is %d", media.ErrMediaTooLarge, size, maxBytes)
		}
	}
	written, err := cl.DownloadMedia(message.Media(), &telegram.DownloadOptions{FileName: destPath})
	if err != nil {
		return "", err
	}
	if maxBytes > 0 {
		if info, statErr := os.Stat(written); statErr == nil && info.Size() > maxBytes {
			_ = os.Remove(written)
			return "", fmt.Errorf("%w: media streamed %d bytes, limit is %d", media.ErrMediaTooLarge, info.Size(), maxBytes)
		}
	}
	return written, nil
}

// SendFiles sends one file or one media group (album) of 2-10 paths.
func (a *mediaAdapter) SendFiles(entity kit.Entity, filePaths []string, opts media.SendOptions) error {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return err
	}
	peer, err := a.inputPeer(entity)
	if err != nil {
		return err
	}
	mediaOpts := &telegram.MediaOptions{}
	if opts.Caption != "" {
		mediaOpts.Caption = opts.Caption
	}
	if opts.TopicID != nil {
		mediaOpts.TopicID = *opts.TopicID
	}
	if opts.Schedule != nil {
		mediaOpts.ScheduleDate = int32(opts.Schedule.Unix())
	}
	if opts.VoiceNote {
		mediaOpts.Attributes = []telegram.DocumentAttribute{&telegram.DocumentAttributeAudio{Voice: true}}
	}
	if len(filePaths) == 1 {
		_, err = cl.SendMedia(peer, filePaths[0], mediaOpts)
		return err
	}
	_, err = cl.SendAlbum(peer, filePaths, mediaOpts)
	return err
}

// SendGifDocument cannot be implemented with gogram v1.7.71: sending by
// document ID requires the access hash and file reference, and the SDK
// exposes no lookup for a bare ID (its cache keeps no document index, and the
// messages.searchGifs request that produces the IDs does not exist in the
// SDK's TL layer). The error is the honest gap report.
func (a *mediaAdapter) SendGifDocument(entity kit.Entity, gifID int64, topicID *int32) error {
	return fmt.Errorf("send_gif is not available: gogram v1.7.71 cannot resolve document ID %d to a sendable reference (no messages.searchGifs request, no document lookup)", gifID)
}

// UploadFile uploads a local file and reports its Telegram metadata.
func (a *mediaAdapter) UploadFile(filePath string) (media.UploadedFile, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return media.UploadedFile{}, err
	}
	uploaded, err := cl.UploadFile(filePath)
	if err != nil {
		return media.UploadedFile{}, err
	}
	obj, ok := uploaded.(*telegram.InputFileObj)
	if !ok {
		return media.UploadedFile{}, nil
	}
	return media.UploadedFile{Name: obj.Name, MD5Checksum: obj.Md5Checksum}, nil
}

// StickerSetTitles returns the titles of every sticker set the account is a
// member of (messages.getAllStickers, hash=0).
func (a *mediaAdapter) StickerSetTitles() ([]string, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	resp, err := cl.MessagesGetAllStickers(0)
	if err != nil {
		return nil, err
	}
	obj, ok := resp.(*telegram.MessagesAllStickersObj)
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(obj.Sets))
	for _, set := range obj.Sets {
		if set != nil {
			out = append(out, set.Title)
		}
	}
	return out, nil
}

// SearchGifIDs cannot be implemented with gogram v1.7.71: the SDK's TL layer
// (layer 227) contains no messages.searchGifs request, and no other request
// exposes Telegram's GIF search index. The error is the honest gap report.
func (a *mediaAdapter) SearchGifIDs(query string, limit int) ([]int64, error) {
	return nil, fmt.Errorf("get_gif_search is not available: gogram v1.7.71 exposes no messages.searchGifs request (query %q)", query)
}

// ListUserPhotos returns a user's profile photos in profile order.
func (a *mediaAdapter) ListUserPhotos(user any, limit int32) ([]telegram.Photo, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	peer, err := a.inputPeer(user)
	if err != nil {
		return nil, err
	}
	inputUser, ok := peer.(*telegram.InputPeerUser)
	if !ok {
		return nil, fmt.Errorf("profile photos are only available for users")
	}
	resp, err := cl.PhotosGetUserPhotos(&telegram.InputUserObj{UserID: inputUser.UserID, AccessHash: inputUser.AccessHash}, 0, 0, limit)
	if err != nil {
		return nil, err
	}
	switch photos := resp.(type) {
	case *telegram.PhotosPhotosObj:
		return photos.Photos, nil
	case *telegram.PhotosPhotosSlice:
		return photos.Photos, nil
	default:
		return nil, nil
	}
}

// ListChatAvatarMessages returns the chat-photo service messages of a chat.
func (a *mediaAdapter) ListChatAvatarMessages(chatID any, limit int32) ([]kit.PhotoMessage, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	peer, err := a.inputPeer(chatID)
	if err != nil {
		return nil, err
	}
	messages, err := cl.GetHistory(peer, &telegram.HistoryOption{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]kit.PhotoMessage, 0)
	for _, message := range messages {
		action, ok := message.Action.(*telegram.MessageActionChatEditPhoto)
		if !ok || action.Photo == nil {
			continue
		}
		photo, ok := action.Photo.(*telegram.PhotoObj)
		if !ok {
			continue
		}
		out = append(out, kit.PhotoMessage{
			ID:    int32(photo.ID),
			Date:  message.Date(),
			Photo: photo,
		})
	}
	return out, nil
}

// ListPhotoMessages returns the chat's photo messages, newest first.
func (a *mediaAdapter) ListPhotoMessages(chatID any, limit int32) ([]kit.PhotoMessage, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	peer, err := a.inputPeer(chatID)
	if err != nil {
		return nil, err
	}
	messages, err := cl.GetMessages(peer, &telegram.SearchOption{
		Limit:  limit,
		Filter: &telegram.InputMessagesFilterPhotoVideo{},
	})
	if err != nil {
		return nil, err
	}
	out := make([]kit.PhotoMessage, 0, len(messages))
	for _, message := range messages {
		photo := message.Photo()
		if photo == nil {
			continue
		}
		out = append(out, kit.PhotoMessage{
			ID:      message.ID,
			Date:    message.Date(),
			Caption: message.Text(),
			Photo:   photo,
		})
	}
	return out, nil
}

// DownloadMedia returns the media bytes; a nil thumbnail asks for the
// full-size version.
func (a *mediaAdapter) DownloadMedia(media any, thumbnail telegram.PhotoSize) ([]byte, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	opts := &telegram.DownloadOptions{Buffer: &buffer}
	if thumbnail != nil {
		opts.ThumbOnly = true
		opts.ThumbSize = thumbnail
	}
	if _, err := cl.DownloadMedia(media, opts); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// inputPeer resolves a tool-level identifier (a kit.Entity from the resolver,
// or any gogram-resolvable reference) to the InputPeer the RPCs take.
func (a *mediaAdapter) inputPeer(identifier any) (telegram.InputPeer, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	entity, ok := identifier.(kit.Entity)
	if !ok {
		return cl.ResolvePeer(identifier)
	}
	switch entity.PeerKind() {
	case kit.PeerUser:
		if user, err := cl.GetUser(entity.BareID()); err == nil {
			return &telegram.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, nil
		}
	case kit.PeerBasicGroup:
		if chat, err := cl.GetChat(entity.BareID()); err == nil {
			return &telegram.InputPeerChat{ChatID: chat.ID}, nil
		}
	default:
		if channel, err := cl.GetChannel(entity.BareID()); err == nil {
			return &telegram.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}, nil
		}
	}
	return cl.ResolvePeer(kit.GetMarkedID(entity))
}

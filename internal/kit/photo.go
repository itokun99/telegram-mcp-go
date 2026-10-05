package kit

// This file ports photo_source.py: resolving peer photos from every source
// Telegram offers them through, and the decision table that picks between the
// native user avatar history, the chat-photo service messages and photos
// posted in the chat.
//
// The Telegram access itself sits behind the PhotoClient interface, which the
// session layer implements over a live gogram client and tests fake. The table
// below - which source lists which identifiers, which reference is "current",
// and which size a thumbnail download asks for - is ported unchanged.

import (
	"fmt"
	"strings"
	"time"

	telegram "github.com/amarnathcjd/gogram/telegram"
)

// PhotoSource is the source selector accepted by the photo tools.
type PhotoSource string

const (
	// AvatarSource is the peer's profile pictures, in profile order.
	AvatarSource PhotoSource = "avatars"
	// MessageSource is the photos posted in the chat, newest first.
	MessageSource PhotoSource = "messages"
	// ThumbnailTargetPixels is the widest size a thumbnail download accepts.
	ThumbnailTargetPixels = 320
	// PhotoSourceHint is the text the photo tools return for an unknown
	// source, kept verbatim from media.py.
	PhotoSourceHint = "Unknown photo source. Expected one of: avatars, messages."
)

// PhotoSources lists the accepted sources in the order the error message names
// them.
var PhotoSources = []PhotoSource{AvatarSource, MessageSource}

// UnknownPhotoSourceError ports UnknownPhotoSource.
type UnknownPhotoSourceError struct {
	Requested string
}

func (e *UnknownPhotoSourceError) Error() string {
	accepted := make([]string, 0, len(PhotoSources))
	for _, source := range PhotoSources {
		accepted = append(accepted, string(source))
	}
	return fmt.Sprintf(
		"Unknown photo source '%s'. Expected one of: %s.",
		e.Requested, strings.Join(accepted, ", "),
	)
}

// ValidatePhotoSource ports validate_source: an empty value defaults to
// avatars, surrounding space and casing are ignored, and anything else is an
// *UnknownPhotoSourceError.
func ValidatePhotoSource(source string) (PhotoSource, error) {
	normalised := PhotoSource(strings.ToLower(strings.TrimSpace(source)))
	if normalised == "" {
		return AvatarSource, nil
	}
	for _, accepted := range PhotoSources {
		if normalised == accepted {
			return normalised, nil
		}
	}
	return "", &UnknownPhotoSourceError{Requested: source}
}

// PhotoMessage is one message row the photo sources draw on: a posted photo
// message (identifier is the message id, caption is the message text) or a
// chat-photo service message (identifier is the photo id, the date is the
// service message's).
type PhotoMessage struct {
	ID      int32
	Date    int32
	Caption string
	Photo   telegram.Photo
}

// PhotoClient is the Telegram access the photo sources need. The session layer
// implements it over a live gogram client; tests inject a fake, so the decision
// table below is exercised without a network.
type PhotoClient interface {
	// ListUserPhotos returns a user's profile photos in profile order.
	ListUserPhotos(user any, limit int32) ([]telegram.Photo, error)
	// ListChatAvatarMessages returns chat-photo service messages; rows
	// without a photo change action are dropped by the implementation.
	ListChatAvatarMessages(chatID any, limit int32) ([]PhotoMessage, error)
	// ListPhotoMessages returns the chat's photo messages, newest first.
	ListPhotoMessages(chatID any, limit int32) ([]PhotoMessage, error)
	// DownloadMedia returns the media bytes; a nil thumbnail asks for the
	// full-size version.
	DownloadMedia(media any, thumbnail telegram.PhotoSize) ([]byte, error)
}

// PhotoReference ports PhotoReference: one retrievable photo plus the
// identifier used to open it again.
type PhotoReference struct {
	Identifier int64
	Photo      telegram.Photo
	IsCurrent  bool
	TakenAt    *time.Time
	Caption    string
}

// PhotoDescription is the JSON shape of Describe, with the Python key order
// and the caption present only when there is one.
type PhotoDescription struct {
	ID        int64      `json:"id"`
	Date      *time.Time `json:"date"`
	IsCurrent bool       `json:"is_current"`
	Caption   string     `json:"caption,omitempty"`
}

// Describe ports PhotoReference.describe.
func (r PhotoReference) Describe() PhotoDescription {
	return PhotoDescription{
		ID:        r.Identifier,
		Date:      r.TakenAt,
		IsCurrent: r.IsCurrent,
		Caption:   r.Caption,
	}
}

// ListPhotoReferences ports list_photo_references: the newest limit photos of
// entity from the requested source.
func ListPhotoReferences(
	client PhotoClient,
	entity any,
	source PhotoSource,
	limit int,
) ([]PhotoReference, error) {
	if source != AvatarSource && source != MessageSource {
		return nil, &UnknownPhotoSourceError{Requested: string(source)}
	}

	switch {
	case source == MessageSource:
		return listMessagePhotoReferences(client, entity, limit)
	case PeerSupportsNativeAvatarHistory(entity):
		return listUserAvatarReferences(client, entity, limit)
	default:
		return listChatAvatarReferences(client, entity, limit)
	}
}

// FindPhotoReference ports find_photo_reference: one photo by identifier, or
// the current avatar when the identifier is nil. A nil result with a nil error
// means the peer has no such photo.
func FindPhotoReference(
	client PhotoClient,
	entity any,
	source PhotoSource,
	identifier *int64,
	searchDepth int,
) (*PhotoReference, error) {
	references, err := ListPhotoReferences(client, entity, source, searchDepth)
	if err != nil {
		return nil, err
	}
	if len(references) == 0 {
		return nil, nil
	}
	if identifier == nil {
		for position := range references {
			if references[position].IsCurrent {
				return &references[position], nil
			}
		}
		return &references[0], nil
	}
	for position := range references {
		if references[position].Identifier == *identifier {
			return &references[position], nil
		}
	}
	return nil, nil
}

// DownloadPhotoBytes ports download_photo_bytes: one referenced photo straight
// to memory, never to disk. A thumbnail request that cannot be served - no
// size within the target, or an empty download - falls back to the full-size
// fetch, as it does in Python.
func DownloadPhotoBytes(
	client PhotoClient,
	reference PhotoReference,
	thumbnail bool,
) ([]byte, error) {
	if thumbnail {
		if size, ok := ThumbnailSize(reference.Photo); ok {
			downloaded, err := client.DownloadMedia(reference.Photo, size)
			if err == nil && len(downloaded) > 0 {
				return downloaded, nil
			}
		}
	}
	return client.DownloadMedia(reference.Photo, nil)
}

// PeerSupportsSource ports peer_supports_source: whether the peer can serve
// the source at all, used to explain empty results. The empty and forbidden TL
// constructors are counted with their full counterparts - they are the same
// peer with flags set - while nil and unknown types are not.
func PeerSupportsSource(entity any, source PhotoSource) bool {
	if source != MessageSource {
		return true
	}
	switch entity.(type) {
	case *telegram.UserObj, *telegram.UserEmpty,
		*telegram.ChatObj, *telegram.ChatEmpty, *telegram.ChatForbidden,
		*telegram.Channel, *telegram.ChannelForbidden:
		return true
	default:
		return false
	}
}

// PeerSupportsNativeAvatarHistory ports _peer_supports_native_avatar_history:
// only users expose their avatar history through the dedicated photos API;
// every other peer has to be read from chat-photo service messages.
func PeerSupportsNativeAvatarHistory(entity any) bool {
	_, isUser := entity.(*telegram.UserObj)
	return isUser
}

// CurrentPhotoID ports _current_photo_id: the photo id the entity currently
// shows as its avatar, or false when it has none.
func CurrentPhotoID(entity any) (int64, bool) {
	var profile any
	switch typed := entity.(type) {
	case *telegram.UserObj:
		profile = typed.Photo
	case *telegram.ChatObj:
		profile = typed.Photo
	case *telegram.Channel:
		profile = typed.Photo
	default:
		return 0, false
	}

	switch photo := profile.(type) {
	case *telegram.UserProfilePhotoObj:
		return photo.PhotoID, true
	case *telegram.ChatPhotoObj:
		return photo.PhotoID, true
	default:
		return 0, false
	}
}

// ThumbnailSizeIndex ports _thumbnail_size_index: the index of the last
// available size wider than zero and no wider than ThumbnailTargetPixels.
func ThumbnailSizeIndex(photo telegram.Photo) (int, bool) {
	sizes := photoSizes(photo)
	chosen := -1
	for position, size := range sizes {
		width := photoSizeWidth(size)
		if width > 0 && width <= ThumbnailTargetPixels {
			chosen = position
		}
	}
	if chosen < 0 {
		return 0, false
	}
	return chosen, true
}

// ThumbnailSize is ThumbnailSizeIndex returning the size itself, which is what
// the download options take.
func ThumbnailSize(photo telegram.Photo) (telegram.PhotoSize, bool) {
	sizes := photoSizes(photo)
	position, ok := ThumbnailSizeIndex(photo)
	if !ok || position >= len(sizes) {
		return nil, false
	}
	return sizes[position], true
}

func listUserAvatarReferences(
	client PhotoClient,
	entity any,
	limit int,
) ([]PhotoReference, error) {
	photos, err := client.ListUserPhotos(entity, int32(limit))
	if err != nil {
		return nil, err
	}
	currentID, hasCurrent := CurrentPhotoID(entity)

	references := make([]PhotoReference, 0, len(photos))
	for _, photo := range photos {
		identifier, ok := photoID(photo)
		if !ok {
			continue
		}
		references = append(references, PhotoReference{
			Identifier: identifier,
			Photo:      photo,
			IsCurrent:  hasCurrent && identifier == currentID,
			TakenAt:    photoDate(photo),
		})
	}
	return references, nil
}

func listChatAvatarReferences(
	client PhotoClient,
	entity any,
	limit int,
) ([]PhotoReference, error) {
	messages, err := client.ListChatAvatarMessages(entity, int32(limit))
	if err != nil {
		return nil, err
	}
	currentID, hasCurrent := CurrentPhotoID(entity)

	references := make([]PhotoReference, 0, len(messages))
	for _, message := range messages {
		identifier, ok := photoID(message.Photo)
		if !ok {
			continue
		}
		references = append(references, PhotoReference{
			Identifier: identifier,
			Photo:      message.Photo,
			IsCurrent:  hasCurrent && identifier == currentID,
			TakenAt:    unixSeconds(message.Date),
		})
	}
	return references, nil
}

func listMessagePhotoReferences(
	client PhotoClient,
	entity any,
	limit int,
) ([]PhotoReference, error) {
	messages, err := client.ListPhotoMessages(entity, int32(limit))
	if err != nil {
		return nil, err
	}

	references := make([]PhotoReference, 0, len(messages))
	for _, message := range messages {
		if _, ok := photoID(message.Photo); !ok {
			continue
		}
		references = append(references, PhotoReference{
			Identifier: int64(message.ID),
			Photo:      message.Photo,
			IsCurrent:  false,
			TakenAt:    unixSeconds(message.Date),
			Caption:    message.Caption,
		})
	}
	return references, nil
}

func photoSizes(photo telegram.Photo) []telegram.PhotoSize {
	if sized, ok := photo.(*telegram.PhotoObj); ok {
		return sized.Sizes
	}
	return nil
}

func photoSizeWidth(size telegram.PhotoSize) int32 {
	switch typed := size.(type) {
	case *telegram.PhotoSizeObj:
		return typed.W
	case *telegram.PhotoCachedSize:
		return typed.W
	default:
		return 0
	}
}

func photoID(photo telegram.Photo) (int64, bool) {
	switch typed := photo.(type) {
	case *telegram.PhotoObj:
		return typed.ID, true
	case *telegram.PhotoEmpty:
		return typed.ID, true
	default:
		return 0, false
	}
}

func photoDate(photo telegram.Photo) *time.Time {
	if typed, ok := photo.(*telegram.PhotoObj); ok {
		return unixSeconds(typed.Date)
	}
	return nil
}

func unixSeconds(seconds int32) *time.Time {
	stamp := time.Unix(int64(seconds), 0).UTC()
	return &stamp
}

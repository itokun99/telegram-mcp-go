package kit

// The photo-source decision table, driven through a fake PhotoClient: no
// network and no gogram client are involved.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	telegram "github.com/amarnathcjd/gogram/telegram"
)

type fakePhotoClient struct {
	userPhotos         []telegram.Photo
	chatAvatarMessages []PhotoMessage
	photoMessages      []PhotoMessage
	branches           []string
	limits             []int32
	requestedThumbs    []telegram.PhotoSize
}

func (f *fakePhotoClient) ListUserPhotos(_ any, limit int32) ([]telegram.Photo, error) {
	f.branches = append(f.branches, "user-avatars")
	f.limits = append(f.limits, limit)
	return f.userPhotos, nil
}

func (f *fakePhotoClient) ListChatAvatarMessages(_ any, limit int32) ([]PhotoMessage, error) {
	f.branches = append(f.branches, "chat-avatars")
	f.limits = append(f.limits, limit)
	return f.chatAvatarMessages, nil
}

func (f *fakePhotoClient) ListPhotoMessages(_ any, limit int32) ([]PhotoMessage, error) {
	f.branches = append(f.branches, "messages")
	f.limits = append(f.limits, limit)
	return f.photoMessages, nil
}

func (f *fakePhotoClient) DownloadMedia(_ any, thumbnail telegram.PhotoSize) ([]byte, error) {
	f.requestedThumbs = append(f.requestedThumbs, thumbnail)
	if thumbnail != nil {
		return []byte("thumbnail-bytes"), nil
	}
	return []byte("full-bytes"), nil
}

func (f *fakePhotoClient) onlyBranch(t *testing.T) string {
	t.Helper()
	if len(f.branches) != 1 {
		t.Fatalf("branches = %v, want exactly one", f.branches)
	}
	return f.branches[0]
}

func sizedPhoto(id int64, widths ...int32) *telegram.PhotoObj {
	photo := &telegram.PhotoObj{ID: id, Date: 1_700_000_000}
	for _, width := range widths {
		photo.Sizes = append(photo.Sizes, &telegram.PhotoSizeObj{Type: "x", W: width})
	}
	return photo
}

func userWithPhoto(id int64, current int64) *telegram.UserObj {
	return &telegram.UserObj{ID: id, Photo: &telegram.UserProfilePhotoObj{PhotoID: current}}
}

func channelWithPhoto(id int64, current int64) *telegram.Channel {
	return &telegram.Channel{ID: id, Title: "t", Photo: &telegram.ChatPhotoObj{PhotoID: current}}
}

func avatarServiceMessage(photoID int64, unix int32) PhotoMessage {
	var photo telegram.Photo
	if photoID != 0 {
		photo = &telegram.PhotoObj{ID: photoID}
	}
	return PhotoMessage{ID: int32(100 + photoID), Date: unix, Photo: photo}
}

func photoMessage(id int32, photoID int64, caption string, unix int32) PhotoMessage {
	return PhotoMessage{ID: id, Date: unix, Caption: caption, Photo: &telegram.PhotoObj{ID: photoID}}
}

func identifiers(references []PhotoReference) []int64 {
	listed := make([]int64, 0, len(references))
	for _, reference := range references {
		listed = append(listed, reference.Identifier)
	}
	return listed
}

func TestPhotoSourceValidation(t *testing.T) {
	if source, err := ValidatePhotoSource(""); err != nil || source != AvatarSource {
		t.Errorf("empty source = %q, %v; want avatars", source, err)
	}
	if source, err := ValidatePhotoSource("  MESSAGES "); err != nil || source != MessageSource {
		t.Errorf("padded source = %q, %v; want messages", source, err)
	}

	_, err := ValidatePhotoSource("stories")
	var unknown *UnknownPhotoSourceError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want *UnknownPhotoSourceError", err)
	}
	if got := err.Error(); got != "Unknown photo source 'stories'. Expected one of: avatars, messages." {
		t.Errorf("message = %q", got)
	}
}

func TestPhotoListRejectsAnUnknownSource(t *testing.T) {
	client := &fakePhotoClient{}
	_, err := ListPhotoReferences(client, userWithPhoto(7, 0), PhotoSource("stories"), 10)
	var unknown *UnknownPhotoSourceError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want *UnknownPhotoSourceError", err)
	}
	if len(client.branches) != 0 {
		t.Errorf("branches = %v, want no Telegram access", client.branches)
	}
}

func TestPhotoUserAvatarsUseTheNativeHistory(t *testing.T) {
	client := &fakePhotoClient{userPhotos: []telegram.Photo{sizedPhoto(200), sizedPhoto(100)}}

	references, err := ListPhotoReferences(client, userWithPhoto(7, 200), AvatarSource, 10)
	if err != nil {
		t.Fatalf("ListPhotoReferences: %v", err)
	}

	if branch := client.onlyBranch(t); branch != "user-avatars" {
		t.Errorf("branch = %q, want user-avatars", branch)
	}
	if client.limits[0] != 10 {
		t.Errorf("limit = %d, want 10", client.limits[0])
	}
	if got := fmt.Sprint(identifiers(references)); got != "[200 100]" {
		t.Errorf("identifiers = %s, want [200 100]", got)
	}
	if !references[0].IsCurrent || references[1].IsCurrent {
		t.Errorf("is_current = %v, %v; want true, false", references[0].IsCurrent, references[1].IsCurrent)
	}
}

func TestPhotoChannelAvatarsFallBackToServiceMessages(t *testing.T) {
	client := &fakePhotoClient{chatAvatarMessages: []PhotoMessage{
		avatarServiceMessage(55, 1_700_000_100),
		avatarServiceMessage(44, 1_700_000_200),
	}}

	references, err := ListPhotoReferences(client, channelWithPhoto(9, 55), AvatarSource, 10)
	if err != nil {
		t.Fatalf("ListPhotoReferences: %v", err)
	}

	if branch := client.onlyBranch(t); branch != "chat-avatars" {
		t.Errorf("branch = %q, want chat-avatars", branch)
	}
	if got := fmt.Sprint(identifiers(references)); got != "[55 44]" {
		t.Errorf("identifiers = %s, want [55 44]", got)
	}
	if !references[0].IsCurrent || references[1].IsCurrent {
		t.Errorf("is_current = %v, %v; want true, false", references[0].IsCurrent, references[1].IsCurrent)
	}
	if want := time.Unix(1_700_000_100, 0).UTC(); references[0].TakenAt == nil || !references[0].TakenAt.Equal(want) {
		t.Errorf("taken_at = %v, want %v", references[0].TakenAt, want)
	}
}

func TestPhotoServiceMessagesWithoutAPhotoAreSkipped(t *testing.T) {
	client := &fakePhotoClient{chatAvatarMessages: []PhotoMessage{
		avatarServiceMessage(0, 1_700_000_100),
		avatarServiceMessage(44, 1_700_000_200),
	}}

	references, err := ListPhotoReferences(client, channelWithPhoto(9, 0), AvatarSource, 10)
	if err != nil {
		t.Fatalf("ListPhotoReferences: %v", err)
	}
	if got := fmt.Sprint(identifiers(references)); got != "[44]" {
		t.Errorf("identifiers = %s, want [44]", got)
	}
	if references[0].IsCurrent {
		t.Error("no reference should be current when the entity has no avatar id")
	}
}

func TestPhotoMessagePhotosAreAddressedByMessageIDAndKeepCaptions(t *testing.T) {
	client := &fakePhotoClient{photoMessages: []PhotoMessage{
		photoMessage(9001, 1, "on the balcony", 1_700_000_300),
	}}

	references, err := ListPhotoReferences(client, channelWithPhoto(9, 0), MessageSource, 10)
	if err != nil {
		t.Fatalf("ListPhotoReferences: %v", err)
	}

	if branch := client.onlyBranch(t); branch != "messages" {
		t.Errorf("branch = %q, want messages", branch)
	}
	if references[0].Identifier != 9001 {
		t.Errorf("identifier = %d, want the message id 9001", references[0].Identifier)
	}
	if references[0].Describe().Caption != "on the balcony" {
		t.Errorf("caption = %q, want %q", references[0].Describe().Caption, "on the balcony")
	}
	if references[0].IsCurrent {
		t.Error("a message photo is never the current avatar")
	}
}

func TestPhotoMessageSourceAppliesToUsersWithoutTouchingAvatarHistory(t *testing.T) {
	client := &fakePhotoClient{photoMessages: []PhotoMessage{photoMessage(5, 1, "", 1_700_000_300)}}

	references, err := ListPhotoReferences(client, userWithPhoto(7, 0), MessageSource, 10)
	if err != nil {
		t.Fatalf("ListPhotoReferences: %v", err)
	}
	if branch := client.onlyBranch(t); branch != "messages" {
		t.Errorf("branch = %q, want messages", branch)
	}
	if got := fmt.Sprint(identifiers(references)); got != "[5]" {
		t.Errorf("identifiers = %s, want [5]", got)
	}
}

func TestPhotoFindReferenceBranches(t *testing.T) {
	user := userWithPhoto(7, 300)
	client := &fakePhotoClient{userPhotos: []telegram.Photo{sizedPhoto(200), sizedPhoto(300)}}
	wanted := int64(200)

	found, err := FindPhotoReference(client, user, AvatarSource, &wanted, 20)
	if err != nil {
		t.Fatalf("FindPhotoReference by id: %v", err)
	}
	if found == nil || found.Identifier != 200 {
		t.Fatalf("found = %v, want identifier 200", found)
	}

	client = &fakePhotoClient{userPhotos: []telegram.Photo{sizedPhoto(200), sizedPhoto(300)}}
	found, err = FindPhotoReference(client, user, AvatarSource, nil, 20)
	if err != nil {
		t.Fatalf("FindPhotoReference current: %v", err)
	}
	if found == nil || found.Identifier != 300 {
		t.Fatalf("found = %v, want the current avatar 300", found)
	}

	client = &fakePhotoClient{userPhotos: []telegram.Photo{sizedPhoto(200)}}
	missing := int64(999)
	found, err = FindPhotoReference(client, user, AvatarSource, &missing, 20)
	if err != nil || found != nil {
		t.Errorf("missing id: found = %v, err = %v; want nil, nil", found, err)
	}

	empty := &fakePhotoClient{}
	found, err = FindPhotoReference(empty, channelWithPhoto(9, 0), AvatarSource, nil, 20)
	if err != nil || found != nil {
		t.Errorf("empty history: found = %v, err = %v; want nil, nil", found, err)
	}

	noCurrent := userWithPhoto(7, 0)
	found, err = FindPhotoReference(
		&fakePhotoClient{userPhotos: []telegram.Photo{sizedPhoto(200), sizedPhoto(100)}},
		noCurrent, AvatarSource, nil, 20,
	)
	if err != nil {
		t.Fatalf("FindPhotoReference without a current avatar: %v", err)
	}
	if found == nil || found.Identifier != 200 {
		t.Errorf("found = %v, want the first reference 200", found)
	}
}

func TestPhotoDownloadChoosesTheLargestSizeWithinTarget(t *testing.T) {
	reference := PhotoReference{Identifier: 1, Photo: sizedPhoto(1, 90, 320, 1280), IsCurrent: true}

	client := &fakePhotoClient{}
	data, err := DownloadPhotoBytes(client, reference, true)
	if err != nil {
		t.Fatalf("DownloadPhotoBytes: %v", err)
	}
	if string(data) != "thumbnail-bytes" {
		t.Errorf("data = %q, want the thumbnail download", data)
	}
	if len(client.requestedThumbs) != 1 || client.requestedThumbs[0] == nil {
		t.Fatalf("requested thumbs = %v, want one thumbnail size", client.requestedThumbs)
	}
	if width := client.requestedThumbs[0].(*telegram.PhotoSizeObj).W; width != 320 {
		t.Errorf("thumbnail width = %d, want 320", width)
	}
}

func TestPhotoFullDownloadAsksForNoThumbnail(t *testing.T) {
	reference := PhotoReference{Identifier: 1, Photo: sizedPhoto(1, 90), IsCurrent: true}

	client := &fakePhotoClient{}
	data, err := DownloadPhotoBytes(client, reference, false)
	if err != nil {
		t.Fatalf("DownloadPhotoBytes: %v", err)
	}
	if string(data) != "full-bytes" {
		t.Errorf("data = %q, want the full-size download", data)
	}
	if len(client.requestedThumbs) != 1 || client.requestedThumbs[0] != nil {
		t.Errorf("requested thumbs = %v, want one nil size", client.requestedThumbs)
	}
}

func TestPhotoThumbnailFallsBackToFullDownloadWhenNoSizeFits(t *testing.T) {
	reference := PhotoReference{Identifier: 1, Photo: sizedPhoto(1, 1280), IsCurrent: true}

	client := &fakePhotoClient{}
	data, err := DownloadPhotoBytes(client, reference, true)
	if err != nil {
		t.Fatalf("DownloadPhotoBytes: %v", err)
	}
	if string(data) != "full-bytes" {
		t.Errorf("data = %q, want the full-size fallback", data)
	}
	if len(client.requestedThumbs) != 1 || client.requestedThumbs[0] != nil {
		t.Errorf("requested thumbs = %v, want one nil size", client.requestedThumbs)
	}
}

func TestPhotoThumbnailSkipsZeroWidthSizes(t *testing.T) {
	photo := &telegram.PhotoObj{ID: 1, Sizes: []telegram.PhotoSize{
		&telegram.PhotoStrippedSize{Type: "i"},
		&telegram.PhotoSizeObj{Type: "x", W: 0},
		&telegram.PhotoSizeObj{Type: "x", W: 160},
	}}
	position, ok := ThumbnailSizeIndex(photo)
	if !ok || position != 2 {
		t.Errorf("index = %d, %v; want 2, true", position, ok)
	}
	if _, ok := ThumbnailSizeIndex(&telegram.PhotoEmpty{ID: 1}); ok {
		t.Error("an empty photo has no sizes to choose from")
	}
}

func TestPhotoDescribeOmitsCaptionAndCarriesDate(t *testing.T) {
	taken := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	described := PhotoReference{
		Identifier: 5,
		Photo:      sizedPhoto(5),
		TakenAt:    &taken,
	}.Describe()

	if described.ID != 5 || described.IsCurrent || described.Caption != "" {
		t.Errorf("described = %+v, want id 5, not current, no caption", described)
	}
	if described.Date == nil || !described.Date.Equal(taken) {
		t.Errorf("date = %v, want %v", described.Date, taken)
	}
	if described.Caption != "" {
		t.Error("an empty caption must stay empty so the JSON field is omitted")
	}
}

func TestPhotoPeerCapabilityChecks(t *testing.T) {
	cases := []struct {
		name     string
		entity   any
		supports bool
	}{
		{"user", &telegram.UserObj{ID: 1}, true},
		{"basic group", &telegram.ChatObj{ID: 2}, true},
		{"channel", &telegram.Channel{ID: 3}, true},
		{"empty user", &telegram.UserEmpty{ID: 4}, true},
		{"forbidden channel", &telegram.ChannelForbidden{ID: 5}, true},
		{"nil", nil, false},
		{"not an entity", "chat", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PeerSupportsSource(tc.entity, MessageSource); got != tc.supports {
				t.Errorf("messages supported = %v, want %v", got, tc.supports)
			}
			if !PeerSupportsSource(tc.entity, AvatarSource) {
				t.Error("the avatars source is always nominally supported")
			}
		})
	}

	if !PeerSupportsNativeAvatarHistory(&telegram.UserObj{ID: 1}) {
		t.Error("users expose a native avatar history")
	}
	for _, notAUser := range []any{&telegram.ChatObj{ID: 2}, &telegram.Channel{ID: 3}, &telegram.UserEmpty{ID: 4}, nil} {
		if PeerSupportsNativeAvatarHistory(notAUser) {
			t.Errorf("%T should not use the native avatar history", notAUser)
		}
	}
}

func TestPhotoCurrentPhotoIDReadsBothProfileShapes(t *testing.T) {
	if id, ok := CurrentPhotoID(userWithPhoto(7, 200)); !ok || id != 200 {
		t.Errorf("user photo id = %d, %v; want 200, true", id, ok)
	}
	if id, ok := CurrentPhotoID(channelWithPhoto(9, 55)); !ok || id != 55 {
		t.Errorf("channel photo id = %d, %v; want 55, true", id, ok)
	}
	if _, ok := CurrentPhotoID(&telegram.Channel{ID: 9, Photo: &telegram.ChatPhotoEmpty{}}); ok {
		t.Error("a channel with no photo has no current photo id")
	}
	if _, ok := CurrentPhotoID(&telegram.ChatObj{ID: 2}); ok {
		t.Error("a basic group with no photo has no current photo id")
	}
	if _, ok := CurrentPhotoID(nil); ok {
		t.Error("nil has no current photo id")
	}
}

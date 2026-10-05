package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	telegram "github.com/amarnathcjd/gogram/telegram"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/kit/paths"
	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

// mediaToolNames is the media module of .omo/go-port/parity/inventory.json.
var mediaToolNames = []string{
	"download_media",
	"get_gif_search",
	"get_media_info",
	"get_photo_sheet",
	"get_sticker_sets",
	"list_photos",
	"open_photo",
	"send_album",
	"send_file",
	"send_gif",
	"send_sticker",
	"send_voice",
	"upload_file",
}

func TestMediaToolsRegistered(t *testing.T) {
	expected := slices.Clone(mediaToolNames)
	slices.Sort(expected)
	registered := mcpserver.RegisteredToolNames()
	if !slices.Equal(registered, expected) {
		t.Fatalf("registered tools = %v, want %v", registered, expected)
	}
}

func TestMediaToolAnnotationsMatchInventory(t *testing.T) {
	srv, err := mcpserver.Build(mcpserver.DefaultRegistry, mcpserver.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var listing bytes.Buffer
	if err := srv.DryRun(&listing); err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	var tools []struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		ReadOnlyHint bool   `json:"readOnlyHint"`
		OpenWorld    bool   `json:"openWorldHint"`
	}
	if err := json.Unmarshal(listing.Bytes(), &tools); err != nil {
		t.Fatalf("unmarshal dry-run listing: %v", err)
	}
	readonly := map[string]bool{
		"download_media": false, "get_gif_search": true, "get_media_info": true,
		"get_photo_sheet": true, "get_sticker_sets": true, "list_photos": true,
		"open_photo": true, "send_album": false, "send_file": false,
		"send_gif": false, "send_sticker": false, "send_voice": false,
		"upload_file": false,
	}
	if len(tools) != len(readonly) {
		t.Fatalf("served %d tools, want %d", len(tools), len(readonly))
	}
	for _, tool := range tools {
		want, ok := readonly[tool.Name]
		if !ok {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		if tool.ReadOnlyHint != want {
			t.Errorf("%s readOnlyHint = %v, want %v", tool.Name, tool.ReadOnlyHint, want)
		}
		if !tool.OpenWorld {
			t.Errorf("%s openWorldHint = false, want true", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
	}
}

type sentCall struct {
	paths []string
	opts  SendOptions
}

type gifCall struct {
	gifID   int64
	topicID *int32
}

type fakeClient struct {
	entity             kit.Entity
	mediaInfo          MediaInfo
	downloadData       []byte
	downloadErr        error
	uploaded           UploadedFile
	titles             []string
	gifIDs             []int64
	chatAvatarMessages []kit.PhotoMessage
	photoMessages      []kit.PhotoMessage
	photoData          []byte
	sent               []sentCall
	gifs               []gifCall
}

func (f *fakeClient) Lookup(any) (kit.Entity, error) { return f.entity, nil }
func (f *fakeClient) WarmEntities() error            { return nil }
func (f *fakeClient) ListUserPhotos(any, int32) ([]telegram.Photo, error) {
	return nil, nil
}
func (f *fakeClient) ListChatAvatarMessages(any, int32) ([]kit.PhotoMessage, error) {
	return f.chatAvatarMessages, nil
}
func (f *fakeClient) ListPhotoMessages(any, int32) ([]kit.PhotoMessage, error) {
	return f.photoMessages, nil
}
func (f *fakeClient) DownloadMedia(any, telegram.PhotoSize) ([]byte, error) {
	return f.photoData, nil
}
func (f *fakeClient) MessageMedia(kit.Entity, int32) (MediaInfo, error) { return f.mediaInfo, nil }
func (f *fakeClient) DownloadMessageMedia(_ kit.Entity, _ int32, destPath string, _ int64) (string, error) {
	if f.downloadErr != nil {
		return "", f.downloadErr
	}
	if err := os.WriteFile(destPath, f.downloadData, 0o644); err != nil {
		return "", err
	}
	return destPath, nil
}
func (f *fakeClient) SendFiles(_ kit.Entity, filePaths []string, opts SendOptions) error {
	f.sent = append(f.sent, sentCall{paths: filePaths, opts: opts})
	return nil
}
func (f *fakeClient) SendGifDocument(_ kit.Entity, gifID int64, topicID *int32) error {
	f.gifs = append(f.gifs, gifCall{gifID: gifID, topicID: topicID})
	return nil
}
func (f *fakeClient) UploadFile(string) (UploadedFile, error)   { return f.uploaded, nil }
func (f *fakeClient) StickerSetTitles() ([]string, error)       { return f.titles, nil }
func (f *fakeClient) SearchGifIDs(string, int) ([]int64, error) { return f.gifIDs, nil }

type harness struct {
	root   string
	client *fakeClient
	deps   *Deps
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	client := &fakeClient{}
	deps := &Deps{
		ClientFor: func(string) (Client, error) { return client, nil },
		Paths:     paths.New(paths.Settings{ServerRoots: []string{root}, ServerRootsOnly: true}),
	}
	previous := SetDeps(deps)
	t.Cleanup(func() { SetDeps(previous) })
	return &harness{root: root, client: client, deps: deps}
}

func (h *harness) file(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(h.root, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func (h *harness) user(t *testing.T) kit.Entity {
	t.Helper()
	entity, err := kit.WrapEntity(&telegram.UserObj{ID: 55, Username: "bob"})
	if err != nil {
		t.Fatalf("WrapEntity: %v", err)
	}
	h.client.entity = entity
	return entity
}

func TestSendFileSendsAndReports(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	path := h.file(t, "report.bin", []byte("data"))
	topic := int32(7)

	text, err := handleSendFile(context.Background(), sendFileInput{
		ChatID: int64(7), FilePath: path, Caption: "hi", TopicID: &topic,
	})
	if err != nil {
		t.Fatalf("handleSendFile: %v", err)
	}
	want := fmt.Sprintf("File sent to chat 7 from %s.", path)
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if len(h.client.sent) != 1 {
		t.Fatalf("sent calls = %d, want 1", len(h.client.sent))
	}
	call := h.client.sent[0]
	if len(call.paths) != 1 || call.paths[0] != path {
		t.Errorf("paths = %v, want [%s]", call.paths, path)
	}
	if call.opts.Caption != "hi" || call.opts.TopicID == nil || *call.opts.TopicID != 7 {
		t.Errorf("opts = %+v, want caption hi and topic 7", call.opts)
	}
	if call.opts.Schedule != nil {
		t.Errorf("schedule = %v, want nil", call.opts.Schedule)
	}
}

func TestSendFileSchedules(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	path := h.file(t, "later.bin", []byte("data"))
	when := "2030-05-01T14:30:00"

	text, err := handleSendFile(context.Background(), sendFileInput{
		ChatID: "bob", FilePath: path, ScheduleDate: when,
	})
	if err != nil {
		t.Fatalf("handleSendFile: %v", err)
	}
	if !strings.HasPrefix(text, "File from "+path+" scheduled for ") {
		t.Fatalf("text = %q, want a scheduled reply", text)
	}
	if !strings.Contains(text, "in chat bob.") {
		t.Fatalf("text = %q, want chat id bob", text)
	}
	if len(h.client.sent) != 1 || h.client.sent[0].opts.Schedule == nil {
		t.Fatalf("schedule not passed through: %+v", h.client.sent)
	}
	if got := h.client.sent[0].opts.Schedule.UTC().Format("2006-01-02T15:04:05"); got != when {
		t.Fatalf("scheduled instant = %s, want %s", got, when)
	}
}

func TestSendFileRejectsMissingPath(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	missing := filepath.Join(h.root, "nope.bin")

	text, err := handleSendFile(context.Background(), sendFileInput{ChatID: int64(7), FilePath: missing})
	if err != nil {
		t.Fatalf("handleSendFile: %v", err)
	}
	if want := "File not found: " + missing; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if len(h.client.sent) != 0 {
		t.Fatalf("send happened despite a bad path: %+v", h.client.sent)
	}
}

func TestSendFileListSendsAlbum(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	first := h.file(t, "a.bin", []byte("a"))
	second := h.file(t, "b.bin", []byte("b"))

	text, err := handleSendFile(context.Background(), sendFileInput{
		ChatID: int64(7), FilePath: []any{first, second}, Caption: "album",
	})
	if err != nil {
		t.Fatalf("handleSendFile: %v", err)
	}
	if want := "Album sent to chat 7 with 2 files."; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if len(h.client.sent) != 1 || len(h.client.sent[0].paths) != 2 {
		t.Fatalf("album sent = %+v, want 2 paths", h.client.sent)
	}
}

func TestSendAlbumValidatesCount(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	only := h.file(t, "one.bin", []byte("1"))

	text, err := handleSendAlbum(context.Background(), sendAlbumInput{ChatID: int64(7), FilePaths: []string{only}})
	if err != nil {
		t.Fatalf("handleSendAlbum: %v", err)
	}
	if want := "Albums must contain between 2 and 10 files."; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestDownloadMediaWritesThroughGate(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.client.mediaInfo = MediaInfo{HasMedia: true, Description: "Document(...)"}
	h.client.downloadData = []byte("binary")

	text, err := handleDownloadMedia(context.Background(), downloadMediaInput{ChatID: int64(7), MessageID: 3})
	if err != nil {
		t.Fatalf("handleDownloadMedia: %v", err)
	}
	if !strings.HasPrefix(text, "Media downloaded to ") {
		t.Fatalf("text = %q, want a download reply", text)
	}
	written := strings.TrimSuffix(strings.TrimPrefix(text, "Media downloaded to "), ".")
	if !strings.Contains(written, filepath.Join("downloads", "telegram_7_3_")) {
		t.Fatalf("written path %q, want the downloads subdir default", written)
	}
	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("reading download: %v", err)
	}
	if string(data) != "binary" {
		t.Fatalf("downloaded bytes = %q, want %q", data, "binary")
	}
}

func TestDownloadMediaNoMedia(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.client.mediaInfo = MediaInfo{HasMedia: false}

	text, err := handleDownloadMedia(context.Background(), downloadMediaInput{ChatID: int64(7), MessageID: 3})
	if err != nil {
		t.Fatalf("handleDownloadMedia: %v", err)
	}
	if want := "No media found in the specified message."; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestDownloadMediaDeclaredOversize(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.client.mediaInfo = MediaInfo{HasMedia: true, DeclaredSize: 300 << 20, HasDeclaredSize: true}

	text, err := handleDownloadMedia(context.Background(), downloadMediaInput{ChatID: int64(7), MessageID: 3})
	if err != nil {
		t.Fatalf("handleDownloadMedia: %v", err)
	}
	want := fmt.Sprintf("Media is too large for download_media (limit: %d bytes).", 200<<20)
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestDownloadMediaStreamedOversize(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.client.mediaInfo = MediaInfo{HasMedia: true}
	h.client.downloadErr = ErrMediaTooLarge

	text, err := handleDownloadMedia(context.Background(), downloadMediaInput{ChatID: int64(7), MessageID: 3})
	if err != nil {
		t.Fatalf("handleDownloadMedia: %v", err)
	}
	want := fmt.Sprintf("Media is too large for download_media (limit: %d bytes).", 200<<20)
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestSendVoiceRequiresOggOrOpus(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.deps.Paths = paths.New(paths.Settings{
		ServerRoots:         []string{h.root},
		ServerRootsOnly:     true,
		ExtensionAllowlists: map[string][]string{"send_voice": {".txt"}},
	})
	path := h.file(t, "note.txt", []byte("not audio"))

	text, err := handleSendVoice(context.Background(), sendVoiceInput{ChatID: int64(7), FilePath: path})
	if err != nil {
		t.Fatalf("handleSendVoice: %v", err)
	}
	if want := "Voice file must be .ogg or .opus format."; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if len(h.client.sent) != 0 {
		t.Fatalf("sent despite bad format: %+v", h.client.sent)
	}
}

func TestSendVoiceSendsNote(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	path := h.file(t, "note.ogg", []byte("ogg"))

	text, err := handleSendVoice(context.Background(), sendVoiceInput{ChatID: int64(7), FilePath: path})
	if err != nil {
		t.Fatalf("handleSendVoice: %v", err)
	}
	if want := fmt.Sprintf("Voice message sent to chat 7 from %s.", path); text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if len(h.client.sent) != 1 || !h.client.sent[0].opts.VoiceNote {
		t.Fatalf("voice note flag not passed: %+v", h.client.sent)
	}
}

func TestUploadFileReportsMetadata(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	path := h.file(t, "doc.bin", []byte("abc"))
	h.client.uploaded = UploadedFile{Name: "up.bin", Size: 3, MD5Checksum: "abc123"}

	text, err := handleUploadFile(context.Background(), uploadFileInput{FilePath: path})
	if err != nil {
		t.Fatalf("handleUploadFile: %v", err)
	}
	for _, fragment := range []string{
		`"path": ` + strconv.Quote(path),
		`"name": "up.bin"`,
		`"size": 3`,
		`"md5_checksum": "abc123"`,
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("payload %s missing %s", text, fragment)
		}
	}
}

func TestUploadFileNullChecksum(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	path := h.file(t, "doc.bin", []byte("abc"))
	h.client.uploaded = UploadedFile{}

	text, err := handleUploadFile(context.Background(), uploadFileInput{FilePath: path})
	if err != nil {
		t.Fatalf("handleUploadFile: %v", err)
	}
	if !strings.Contains(text, `"md5_checksum": null`) {
		t.Fatalf("payload = %s, want a null checksum", text)
	}
	if !strings.Contains(text, `"name": "doc.bin"`) {
		t.Fatalf("payload = %s, want the local base name fallback", text)
	}
}

func TestGetMediaInfo(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.client.mediaInfo = MediaInfo{HasMedia: true, Description: "Document(id=1)"}

	text, err := handleGetMediaInfo(context.Background(), getMediaInfoInput{ChatID: int64(7), MessageID: 3})
	if err != nil {
		t.Fatalf("handleGetMediaInfo: %v", err)
	}
	if text != "Document(id=1)" {
		t.Fatalf("text = %q, want the media description", text)
	}
}

func TestGetStickerSetsSanitizesTitles(t *testing.T) {
	h := newHarness(t)
	h.client.titles = []string{"Cool\x07Set"}

	text, err := handleGetStickerSets(context.Background(), getStickerSetsInput{})
	if err != nil {
		t.Fatalf("handleGetStickerSets: %v", err)
	}
	var titles []string
	if err := json.Unmarshal([]byte(text), &titles); err != nil {
		t.Fatalf("unmarshal %q: %v", text, err)
	}
	if len(titles) != 1 || titles[0] != "CoolSet" {
		t.Fatalf("titles = %v, want [CoolSet]", titles)
	}
}

func TestSendStickerEnforcesWebp(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	path := h.file(t, "pic.png", []byte("png"))

	text, err := handleSendSticker(context.Background(), sendStickerInput{ChatID: int64(7), FilePath: path})
	if err != nil {
		t.Fatalf("handleSendSticker: %v", err)
	}
	if want := "File extension is not allowed for send_sticker. Allowed: .webp."; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestSendStickerSendsWebp(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	path := h.file(t, "sticker.webp", []byte("webp"))

	text, err := handleSendSticker(context.Background(), sendStickerInput{ChatID: int64(7), FilePath: path})
	if err != nil {
		t.Fatalf("handleSendSticker: %v", err)
	}
	if want := fmt.Sprintf("Sticker sent to chat 7 from %s.", path); text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestGetGifSearch(t *testing.T) {
	h := newHarness(t)
	h.client.gifIDs = []int64{11, 22}

	text, err := handleGetGifSearch(context.Background(), getGifSearchInput{Query: "cats"})
	if err != nil {
		t.Fatalf("handleGetGifSearch: %v", err)
	}
	if !strings.Contains(text, "11") || !strings.Contains(text, "22") {
		t.Fatalf("text = %q, want both gif ids", text)
	}
}

func TestGetGifSearchEmpty(t *testing.T) {
	newHarness(t)

	text, err := handleGetGifSearch(context.Background(), getGifSearchInput{Query: "none"})
	if err != nil {
		t.Fatalf("handleGetGifSearch: %v", err)
	}
	if text != "[]" {
		t.Fatalf("text = %q, want []", text)
	}
}

func TestSendGifByDocumentID(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	topic := int32(3)

	text, err := handleSendGif(context.Background(), sendGifInput{ChatID: int64(7), GifID: 99, TopicID: &topic})
	if err != nil {
		t.Fatalf("handleSendGif: %v", err)
	}
	if text != "GIF sent to chat 7." {
		t.Fatalf("text = %q, want the gif reply", text)
	}
	if len(h.client.gifs) != 1 || h.client.gifs[0].gifID != 99 {
		t.Fatalf("gif calls = %+v, want id 99", h.client.gifs)
	}
}

func TestListPhotosIndexesMessages(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.client.photoMessages = []kit.PhotoMessage{{
		ID: 42, Date: 1700000000, Caption: "nice\x00photo", Photo: &telegram.PhotoEmpty{ID: 700},
	}}

	text, err := handleListPhotos(context.Background(), listPhotosInput{ChatID: int64(7), Source: "messages"})
	if err != nil {
		t.Fatalf("handleListPhotos: %v", err)
	}
	for _, fragment := range []string{
		`"chat_id": 55`, `"type": "User"`, `"source": "messages"`,
		`"count": 1`, `"id": 42`, `"caption": "nicephoto"`,
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("payload %s missing %s", text, fragment)
		}
	}
}

func TestListPhotosRejectsUnknownSource(t *testing.T) {
	h := newHarness(t)
	h.user(t)

	text, err := handleListPhotos(context.Background(), listPhotosInput{ChatID: int64(7), Source: "elsewhere"})
	if err != nil {
		t.Fatalf("handleListPhotos: %v", err)
	}
	if text != kit.PhotoSourceHint {
		t.Fatalf("text = %q, want %q", text, kit.PhotoSourceHint)
	}
}

func TestOpenPhotoReturnsImage(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.client.chatAvatarMessages = []kit.PhotoMessage{{ID: 1, Photo: &telegram.PhotoEmpty{ID: 700}}}
	h.client.photoData = []byte("jpegbytes")

	blocks, err := handleOpenPhoto(context.Background(), openPhotoInput{ChatID: int64(7)})
	if err != nil {
		t.Fatalf("handleOpenPhoto: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	image, ok := blocks[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("block = %T, want *mcp.ImageContent", blocks[0])
	}
	if string(image.Data) != "jpegbytes" || image.MIMEType != "image/jpeg" {
		t.Fatalf("image = %q %s, want jpeg bytes", image.Data, image.MIMEType)
	}
}

func TestOpenPhotoSavePathKeepsCopy(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.client.chatAvatarMessages = []kit.PhotoMessage{{ID: 1, Photo: &telegram.PhotoEmpty{ID: 700}}}
	h.client.photoData = []byte("jpegbytes")
	savePath := filepath.Join(h.root, "copies", "keep.jpg")

	blocks, err := handleOpenPhoto(context.Background(), openPhotoInput{ChatID: int64(7), SavePath: savePath})
	if err != nil {
		t.Fatalf("handleOpenPhoto: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	data, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("reading saved copy: %v", err)
	}
	if string(data) != "jpegbytes" {
		t.Fatalf("saved bytes = %q, want jpegbytes", data)
	}
}

func TestOpenPhotoReportsMissingReference(t *testing.T) {
	h := newHarness(t)
	h.user(t)

	blocks, err := handleOpenPhoto(context.Background(), openPhotoInput{ChatID: int64(7)})
	if err != nil {
		t.Fatalf("handleOpenPhoto: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	text, ok := blocks[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("block = %T, want *mcp.TextContent", blocks[0])
	}
	if want := "No avatars photo found for chat 7."; text.Text != want {
		t.Fatalf("text = %q, want %q", text.Text, want)
	}
}

func TestGetPhotoSheetBuildsCollage(t *testing.T) {
	h := newHarness(t)
	h.user(t)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatalf("encoding fixture png: %v", err)
	}
	h.client.chatAvatarMessages = []kit.PhotoMessage{{ID: 1, Photo: &telegram.PhotoEmpty{ID: 700}}}
	h.client.photoData = encoded.Bytes()

	blocks, err := handleGetPhotoSheet(context.Background(), getPhotoSheetInput{ChatID: int64(7)})
	if err != nil {
		t.Fatalf("handleGetPhotoSheet: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	text, ok := blocks[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("block 0 = %T, want *mcp.TextContent", blocks[0])
	}
	if !strings.Contains(text.Text, "1 avatars photo(s) for 55") {
		t.Fatalf("summary = %q, want the tile count and marked id", text.Text)
	}
	sheet, ok := blocks[1].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("block 1 = %T, want *mcp.ImageContent", blocks[1])
	}
	if sheet.MIMEType != "image/png" || len(sheet.Data) == 0 {
		t.Fatalf("sheet = %s with %d bytes, want a png", sheet.MIMEType, len(sheet.Data))
	}
}

func TestReadOnlyFanOutAcrossAccounts(t *testing.T) {
	h := newHarness(t)
	entity, err := kit.WrapEntity(&telegram.UserObj{ID: 55})
	if err != nil {
		t.Fatalf("WrapEntity: %v", err)
	}
	alpha := &fakeClient{entity: entity, mediaInfo: MediaInfo{HasMedia: true, Description: "alpha-media"}}
	beta := &fakeClient{entity: entity, mediaInfo: MediaInfo{HasMedia: true, Description: "beta-media"}}
	h.deps.Router = kit.NewRouter([]string{"alpha", "beta"})
	h.deps.ClientFor = func(label string) (Client, error) {
		switch label {
		case "alpha":
			return alpha, nil
		case "beta":
			return beta, nil
		}
		return nil, fmt.Errorf("unexpected label %q", label)
	}

	text, err := handleGetMediaInfo(context.Background(), getMediaInfoInput{ChatID: int64(7), MessageID: 3})
	if err != nil {
		t.Fatalf("handleGetMediaInfo: %v", err)
	}
	want := "[alpha]\nalpha-media\n\n[beta]\nbeta-media"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestWriteRequiresAccountInMultiAccountMode(t *testing.T) {
	h := newHarness(t)
	h.user(t)
	h.deps.Router = kit.NewRouter([]string{"alpha", "beta"})

	text, err := handleSendGif(context.Background(), sendGifInput{ChatID: int64(7), GifID: 99})
	if err != nil {
		t.Fatalf("handleSendGif: %v", err)
	}
	want := "Error: 'account' is required. Available accounts: alpha, beta"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if len(h.client.gifs) != 0 {
		t.Fatalf("send happened without an account: %+v", h.client.gifs)
	}
}

package profile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/kit/paths"
	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

// inventoryToolNames are the profile-module tool names of
// .omo/go-port/parity/inventory.json, in the inventory's order.
var inventoryToolNames = []string{
	"delete_profile_photo",
	"get_bot_info",
	"get_full_user",
	"get_me",
	"get_privacy_settings",
	"get_user_photos",
	"get_user_status",
	"set_bot_commands",
	"set_privacy_settings",
	"set_profile_photo",
	"update_profile",
}

// inventoryReadOnly mirrors the inventory's readonly flag per tool.
var inventoryReadOnly = map[string]bool{
	"get_me": true, "get_privacy_settings": true, "get_full_user": true,
	"get_bot_info": true, "get_user_photos": true, "get_user_status": true,
	"update_profile": false, "set_profile_photo": false, "delete_profile_photo": false,
	"set_privacy_settings": false, "set_bot_commands": false,
}

// fakeEntity is a resolved peer.
type fakeEntity struct {
	id       int64
	kind     kit.PeerKind
	username string
}

func (e fakeEntity) BareID() int64          { return e.id }
func (e fakeEntity) PeerKind() kit.PeerKind { return e.kind }
func (e fakeEntity) Username() string       { return e.username }

// fakeClient records every call and returns canned data, so every tool body is
// exercised without a network.
type fakeClient struct {
	me   *User
	full *FullUser

	myPhotos  []int64
	userPhoto []int64
	privacy   []PrivacyRule
	status    string

	// errs holds per-operation failures; a non-nil entry fails that call.
	errs map[string]error

	// recorded calls
	updated         [3]*string
	uploadedPath    string
	deletedPhotos   []int64
	privacyKey      PrivacyKey
	privacyRules    []PrivacyRule
	setCommands     []BotCommand
	userPhotosLimit int32
	myPhotosLimit   int32
	statusArg       any
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		me:   &User{ID: 42, FirstName: "Ada", LastName: "Lovelace", Username: "ada", Phone: "+100"},
		errs: map[string]error{},
	}
}

func (c *fakeClient) GetMe(context.Context) (*User, error) {
	if err := c.errs["get_me"]; err != nil {
		return nil, err
	}
	return c.me, nil
}

func (c *fakeClient) UpdateProfile(_ context.Context, firstName, lastName, about *string) error {
	if err := c.errs["update_profile"]; err != nil {
		return err
	}
	c.updated = [3]*string{firstName, lastName, about}
	return nil
}

func (c *fakeClient) UploadProfilePhoto(_ context.Context, path string) error {
	if err := c.errs["set_profile_photo"]; err != nil {
		return err
	}
	c.uploadedPath = path
	return nil
}

func (c *fakeClient) MyProfilePhotoIDs(_ context.Context, limit int32) ([]int64, error) {
	if err := c.errs["my_photos"]; err != nil {
		return nil, err
	}
	c.myPhotosLimit = limit
	return c.myPhotos, nil
}

func (c *fakeClient) DeleteProfilePhotos(_ context.Context, ids []int64) error {
	if err := c.errs["delete_photos"]; err != nil {
		return err
	}
	c.deletedPhotos = ids
	return nil
}

func (c *fakeClient) UserPhotos(_ context.Context, _ any, limit int32) ([]int64, error) {
	if err := c.errs["user_photos"]; err != nil {
		return nil, err
	}
	c.userPhotosLimit = limit
	return c.userPhoto, nil
}

func (c *fakeClient) GetPrivacy(_ context.Context, key PrivacyKey) ([]PrivacyRule, error) {
	if err := c.errs["get_privacy"]; err != nil {
		return nil, err
	}
	c.privacyKey = key
	return c.privacy, nil
}

func (c *fakeClient) SetPrivacy(_ context.Context, key PrivacyKey, rules []PrivacyRule) error {
	if err := c.errs["set_privacy"]; err != nil {
		return err
	}
	c.privacyKey = key
	c.privacyRules = rules
	return nil
}

func (c *fakeClient) FullUser(context.Context, any) (*User, *FullUser, error) {
	if err := c.errs["full_user"]; err != nil {
		return nil, nil, err
	}
	return c.me, c.full, nil
}

func (c *fakeClient) UserStatus(_ context.Context, user any) (string, error) {
	if err := c.errs["user_status"]; err != nil {
		return "", err
	}
	c.statusArg = user
	return c.status, nil
}

func (c *fakeClient) SetBotCommands(_ context.Context, _ any, commands []BotCommand) error {
	if err := c.errs["set_bot_commands"]; err != nil {
		return err
	}
	c.setCommands = commands
	return nil
}

// recordingReporter captures the funnel's diagnostics.
type recordingReporter struct {
	errors   []string
	warnings []string
}

func (r *recordingReporter) Error(message string)   { r.errors = append(r.errors, message) }
func (r *recordingReporter) Warning(message string) { r.warnings = append(r.warnings, message) }

// harness wires one fake client into a Deps on a call context.
type harness struct {
	deps       *Deps
	client     *fakeClient
	reporter   *recordingReporter
	resolved   []any
	resolveErr error
}

func newHarness(client *fakeClient) *harness {
	h := &harness{client: client, reporter: &recordingReporter{}}
	entity := fakeEntity{id: 7, kind: kit.PeerUser, username: "target"}
	h.deps = &Deps{
		Router: kit.NewRouter([]string{"default"}),
		Client: func(string) (Client, error) { return client, nil },
		Resolve: func(_ context.Context, _ string, identifier any) (kit.Entity, error) {
			h.resolved = append(h.resolved, identifier)
			if h.resolveErr != nil {
				return nil, h.resolveErr
			}
			return entity, nil
		},
		Reporter: h.reporter,
	}
	return h
}

func (h *harness) ctx() context.Context { return WithDeps(context.Background(), h.deps) }

// assertFunneled asserts result is the funnel's generic message and that the
// funnel actually reported a line.
func (h *harness) assertFunneled(t *testing.T, result, functionName string) {
	t.Helper()
	const marker = "An error occurred (code: "
	if !strings.HasPrefix(result, marker) {
		t.Fatalf("%s result %q is not a funnel error", functionName, result)
	}
	if len(h.reporter.errors) == 0 {
		t.Fatalf("%s: funnel reported no error line", functionName)
	}
}

// TestAllInventoryToolNamesRegister asserts every profile-module tool of the
// parity inventory registers under its exact name.
func TestAllInventoryToolNamesRegister(t *testing.T) {
	registered := mcpserver.RegisteredToolNames()
	for _, name := range inventoryToolNames {
		if !slices.Contains(registered, name) {
			t.Errorf("tool %q is not registered", name)
		}
	}
}

// TestProfileRegistryHoldsExactlyTheInventory asserts a private registry built
// from this module's registrations contains exactly the 11 inventory names,
// no more and no less.
func TestProfileRegistryHoldsExactlyTheInventory(t *testing.T) {
	reg := profileRegistry()
	got := slices.Clone(reg.Names())
	slices.Sort(got)
	want := slices.Clone(inventoryToolNames)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("registered profile tools = %v, want %v", got, want)
	}
}

// TestAnnotationsMatchInventory asserts the readOnlyHint of every tool matches
// the inventory (TELEGRAM_EXPOSED_TOOLS=read-only prunes on it) and that every
// tool is open-world, like the Python annotations.
func TestAnnotationsMatchInventory(t *testing.T) {
	for name, entry := range dryRunEntries(t) {
		want, known := inventoryReadOnly[name]
		if !known {
			t.Errorf("served tool %q is not in the inventory", name)
			continue
		}
		if entry.ReadOnlyHint != want {
			t.Errorf("%s readOnlyHint = %v, want %v", name, entry.ReadOnlyHint, want)
		}
		if !entry.OpenWorldHint {
			t.Errorf("%s openWorldHint = false, want true", name)
		}
		if entry.Description == "" {
			t.Errorf("%s has no description", name)
		}
	}
}

// TestReadOnlyModeServesExactlyTheReadTools asserts TELEGRAM_EXPOSED_TOOLS=
// read-only keeps the six read tools and drops the five writes.
func TestReadOnlyModeServesExactlyTheReadTools(t *testing.T) {
	srv, err := mcpserver.Build(profileRegistry(), mcpserver.Options{ExposedMode: "read-only"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := srv.ToolNames()
	var want []string
	for _, name := range inventoryToolNames {
		if inventoryReadOnly[name] {
			want = append(want, name)
		}
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("read-only served %v, want %v", got, want)
	}
}

// dryRunEntry mirrors mcpserver's dry-run tool record.
type dryRunEntry struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

// dryRunEntries builds this module's server and reads its served tool surface.
func dryRunEntries(t *testing.T) map[string]dryRunEntry {
	t.Helper()
	srv, err := mcpserver.Build(profileRegistry(), mcpserver.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var buf bytes.Buffer
	if err := srv.DryRun(&buf); err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	var entries []dryRunEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("dry-run output is not valid JSON: %v", err)
	}
	out := make(map[string]dryRunEntry, len(entries))
	for _, entry := range entries {
		out[entry.Name] = entry
	}
	return out
}

// TestGetMe asserts get_me returns runtime.format_entity's shape for the
// authorized user, indented like json.dumps(indent=2).
func TestGetMe(t *testing.T) {
	h := newHarness(newFakeClient())

	got := getMe(h.ctx(), h.client, getMeInput{})
	want := "{\n  \"id\": 42,\n  \"name\": \"Ada Lovelace\",\n  \"type\": \"user\",\n  \"username\": \"ada\",\n  \"phone\": \"+100\"\n}"
	if got != want {
		t.Fatalf("get_me = %q, want %q", got, want)
	}
}

// TestGetMeFunnelsError asserts a failing client yields the funnel message and
// leaks neither the exception text nor the identity.
func TestGetMeFunnelsError(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.errs["get_me"] = errors.New("boom secret detail")

	h.assertFunneled(t, getMe(h.ctx(), h.client, getMeInput{}), "get_me")
}

// TestUpdateProfile asserts the three optional fields reach the client and the
// success string matches Python's.
func TestUpdateProfile(t *testing.T) {
	h := newHarness(newFakeClient())
	first, last, about := "Ada", "L", "bio"

	got := updateProfile(h.ctx(), h.client, updateProfileInput{FirstName: &first, LastName: &last, About: &about})
	if got != "Profile updated." {
		t.Fatalf("update_profile = %q", got)
	}
	if h.client.updated[0] == nil || *h.client.updated[0] != first ||
		h.client.updated[1] == nil || *h.client.updated[1] != last ||
		h.client.updated[2] == nil || *h.client.updated[2] != about {
		t.Fatalf("update fields = %v", h.client.updated)
	}
}

// TestUpdateProfileKeepsUnsetFields asserts an omitted field stays nil so the
// client keeps the current value (Telethon's unset-means-unchanged).
func TestUpdateProfileKeepsUnsetFields(t *testing.T) {
	h := newHarness(newFakeClient())
	first := "Ada"

	if got := updateProfile(h.ctx(), h.client, updateProfileInput{FirstName: &first}); got != "Profile updated." {
		t.Fatalf("update_profile = %q", got)
	}
	if h.client.updated[1] != nil || h.client.updated[2] != nil {
		t.Fatalf("unset fields were sent as values: %v", h.client.updated)
	}
}

// TestSetProfilePhotoGatesPath asserts the path gate runs before the upload and
// the resolved path reaches the client.
func TestSetProfilePhotoGatesPath(t *testing.T) {
	root := t.TempDir()
	photo := filepath.Join(root, "avatar.png")
	if err := os.WriteFile(photo, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The gate resolves symlinks before the containment check, so on macOS the
	// /var temp root comes back as its /private/var realpath.
	resolved, err := filepath.EvalSymlinks(photo)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(newFakeClient())
	h.deps.Gate = paths.New(paths.Settings{ServerRoots: []string{root}})

	got := setProfilePhoto(h.ctx(), h.client, setProfilePhotoInput{FilePath: photo})
	if got != "Profile photo updated from "+resolved+"." {
		t.Fatalf("set_profile_photo = %q, want the resolved path %q", got, resolved)
	}
	if h.client.uploadedPath != resolved {
		t.Fatalf("uploaded %q, want %q", h.client.uploadedPath, resolved)
	}
}

// TestSetProfilePhotoRejectsPathOutsideRoots asserts a path outside the roots
// is refused by the gate and no upload happens.
func TestSetProfilePhotoRejectsPathOutsideRoots(t *testing.T) {
	h := newHarness(newFakeClient())
	h.deps.Gate = paths.New(paths.Settings{ServerRoots: []string{t.TempDir()}})

	got := setProfilePhoto(h.ctx(), h.client, setProfilePhotoInput{FilePath: "/etc/hosts"})
	if got != "Path is outside allowed roots." {
		t.Fatalf("set_profile_photo = %q", got)
	}
	if h.client.uploadedPath != "" {
		t.Fatalf("uploaded %q despite a rejected path", h.client.uploadedPath)
	}
}

// TestSetProfilePhotoRejectsTraversal asserts the traversal contract string.
func TestSetProfilePhotoRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	h := newHarness(newFakeClient())
	h.deps.Gate = paths.New(paths.Settings{ServerRoots: []string{root}})

	got := setProfilePhoto(h.ctx(), h.client, setProfilePhotoInput{FilePath: root + "/../escape.png"})
	if got != "Path traversal is not allowed." {
		t.Fatalf("set_profile_photo = %q", got)
	}
}

// TestSetProfilePhotoDisabledWithoutRoots asserts the fail-closed message when
// no roots are configured.
func TestSetProfilePhotoDisabledWithoutRoots(t *testing.T) {
	h := newHarness(newFakeClient())
	h.deps.Gate = paths.New(paths.Settings{})

	got := setProfilePhoto(h.ctx(), h.client, setProfilePhotoInput{FilePath: "/tmp/whatever.png"})
	if !strings.Contains(got, setProfilePhotoToolName+" is disabled") {
		t.Fatalf("set_profile_photo = %q", got)
	}
	if h.client.uploadedPath != "" {
		t.Fatalf("uploaded %q with file tools disabled", h.client.uploadedPath)
	}
}

// TestDeleteProfilePhotoNoPhoto asserts the "nothing to delete" branch.
func TestDeleteProfilePhotoNoPhoto(t *testing.T) {
	h := newHarness(newFakeClient())

	if got := deleteProfilePhoto(h.ctx(), h.client, deleteProfilePhotoInput{}); got != "No profile photo to delete." {
		t.Fatalf("delete_profile_photo = %q", got)
	}
	if h.client.deletedPhotos != nil {
		t.Fatalf("deleted %v with no photo present", h.client.deletedPhotos)
	}
}

// TestDeleteProfilePhotoDeletesCurrent asserts only the current photo is
// deleted and the requested limit is the Python 1.
func TestDeleteProfilePhotoDeletesCurrent(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.myPhotos = []int64{555, 444}

	if got := deleteProfilePhoto(h.ctx(), h.client, deleteProfilePhotoInput{}); got != "Profile photo deleted." {
		t.Fatalf("delete_profile_photo = %q", got)
	}
	if !slices.Equal(h.client.deletedPhotos, []int64{555}) {
		t.Fatalf("deleted %v, want [555]", h.client.deletedPhotos)
	}
	if h.client.myPhotosLimit != 1 {
		t.Fatalf("photo limit = %d, want 1", h.client.myPhotosLimit)
	}
}

// TestGetPrivacySettings asserts the last-seen key is queried and the rules are
// serialized.
func TestGetPrivacySettings(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.privacy = []PrivacyRule{{Kind: PrivacyAllowAll}, {Kind: PrivacyDisallowUsers, Users: []int64{9}}}

	got := getPrivacySettings(h.ctx(), h.client, getPrivacySettingsInput{})
	if h.client.privacyKey != PrivacyKeyStatus {
		t.Fatalf("privacy key = %q, want status", h.client.privacyKey)
	}
	var decoded privacySettingsResult
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("get_privacy_settings returned invalid JSON %q: %v", got, err)
	}
	if decoded.Key != "status" || len(decoded.Rules) != 2 {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.Rules[0].Kind != PrivacyAllowAll || len(decoded.Rules[0].Users) != 0 {
		t.Fatalf("allow-all rule = %+v", decoded.Rules[0])
	}
	if decoded.Rules[1].Kind != PrivacyDisallowUsers || !slices.Equal(decoded.Rules[1].Users, []int64{9}) {
		t.Fatalf("disallow-users rule = %+v", decoded.Rules[1])
	}
}

// TestSetPrivacySettingsDefaultsToAllowAll asserts an empty allow list produces
// allow_all, matching the Python default.
func TestSetPrivacySettingsDefaultsToAllowAll(t *testing.T) {
	h := newHarness(newFakeClient())

	got := setPrivacySettings(h.ctx(), h.client, setPrivacySettingsInput{Key: "status"})
	if got != "Privacy settings for status updated successfully." {
		t.Fatalf("set_privacy_settings = %q", got)
	}
	if len(h.client.privacyRules) != 1 || h.client.privacyRules[0].Kind != PrivacyAllowAll {
		t.Fatalf("rules = %+v, want one allow_all", h.client.privacyRules)
	}
	if h.client.privacyKey != PrivacyKeyStatus {
		t.Fatalf("privacy key = %q", h.client.privacyKey)
	}
}

// TestSetPrivacySettingsAllowAndDis asserts both user lists resolve into the
// two rules, allow first.
func TestSetPrivacySettingsAllowAndDis(t *testing.T) {
	h := newHarness(newFakeClient())

	got := setPrivacySettings(h.ctx(), h.client, setPrivacySettingsInput{
		Key:           "phone",
		AllowUsers:    []any{"@target"},
		DisallowUsers: []any{12345},
	})
	if got != "Privacy settings for phone updated successfully." {
		t.Fatalf("set_privacy_settings = %q", got)
	}
	if len(h.client.privacyRules) != 2 {
		t.Fatalf("rules = %+v, want 2", h.client.privacyRules)
	}
	if h.client.privacyRules[0].Kind != PrivacyAllowUsers || !slices.Equal(h.client.privacyRules[0].Users, []int64{7}) {
		t.Fatalf("allow rule = %+v", h.client.privacyRules[0])
	}
	if h.client.privacyRules[1].Kind != PrivacyDisallowUsers || !slices.Equal(h.client.privacyRules[1].Users, []int64{7}) {
		t.Fatalf("disallow rule = %+v", h.client.privacyRules[1])
	}
	if h.client.privacyKey != PrivacyKeyPhone {
		t.Fatalf("privacy key = %q, want phone", h.client.privacyKey)
	}
}

// TestSetPrivacySettingsUnsupportedKey asserts the unsupported-key early
// return, naming every supported key in order.
func TestSetPrivacySettingsUnsupportedKey(t *testing.T) {
	h := newHarness(newFakeClient())

	got := setPrivacySettings(h.ctx(), h.client, setPrivacySettingsInput{Key: "last_seen"})
	want := "Error: Unsupported privacy key 'last_seen'. Supported keys: status, phone, profile_photo"
	if got != want {
		t.Fatalf("set_privacy_settings = %q, want %q", got, want)
	}
	if h.client.privacyRules != nil {
		t.Fatalf("rules were sent for an unsupported key: %+v", h.client.privacyRules)
	}
}

// TestSetPrivacySettingsRejectsInvalidID asserts an out-of-range ID is refused
// before any Telegram call, with the validation message verbatim.
func TestSetPrivacySettingsRejectsInvalidID(t *testing.T) {
	h := newHarness(newFakeClient())

	got := setPrivacySettings(h.ctx(), h.client, setPrivacySettingsInput{
		Key:        "status",
		AllowUsers: []any{"99999999999999999999999"},
	})
	if !strings.HasPrefix(got, "Invalid allow_users:") || !strings.Contains(got, "out of the valid integer range") {
		t.Fatalf("set_privacy_settings = %q", got)
	}
	if h.client.privacyRules != nil {
		t.Fatalf("rules were sent for an invalid ID: %+v", h.client.privacyRules)
	}
}

// TestGetFullUser asserts the full payload: the personal-channel link, the ISO
// birthday, the sanitized bio, trust flags present only when set, and the
// collectible usernames minus the primary one.
func TestGetFullUser(t *testing.T) {
	h := newHarness(newFakeClient())
	avatar := int64(9001)
	h.client.me = &User{
		ID: 7, FirstName: "Ada", LastName: "Lovelace", Username: "ada",
		Phone: "+100", LangCode: "en", Scam: true, Premium: true,
		Usernames: []string{"ada", "ada2"}, AvatarPhotoID: &avatar,
		RestrictionReasons: []string{"reported"},
	}
	h.client.full = &FullUser{
		About: "hello", CommonChatsCount: 3,
		PersonalChannelID: 500, PersonalChannelUsername: "chan",
		BirthdayDay: 10, BirthdayMonth: 12, BirthdayYear: 1815, HasBirthday: true,
		PrivateForwardName: "Ada",
	}

	got := getFullUser(h.ctx(), h.client, getFullUserInput{Username: "ada"})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("get_full_user returned invalid JSON %q: %v", got, err)
	}
	if decoded["bio"] != "hello" || decoded["premium"] != true {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded["personal_channel"] != "https://t.me/chan" {
		t.Fatalf("personal_channel = %v", decoded["personal_channel"])
	}
	if decoded["birthday"] != "1815-12-10" {
		t.Fatalf("birthday = %v", decoded["birthday"])
	}
	if decoded["current_avatar_id"] != float64(9001) {
		t.Fatalf("current_avatar_id = %v", decoded["current_avatar_id"])
	}
	if decoded["private_forward_name"] != "Ada" || decoded["language"] != "en" {
		t.Fatalf("decoded = %+v", decoded)
	}
	usernames, _ := decoded["additional_usernames"].([]any)
	if len(usernames) != 1 || usernames[0] != "ada2" {
		t.Fatalf("additional_usernames = %v", decoded["additional_usernames"])
	}
	trust, _ := decoded["trust"].(map[string]any)
	if trust["scam"] != true {
		t.Fatalf("trust = %v", trust)
	}
	if _, present := trust["fake"]; present {
		t.Fatalf("unset trust flag present: %v", trust)
	}
	reasons, _ := trust["restriction_reasons"].([]any)
	if len(reasons) != 1 || reasons[0] != "reported" {
		t.Fatalf("restriction_reasons = %v", trust["restriction_reasons"])
	}
	relationship, _ := decoded["relationship"].(map[string]any)
	if len(relationship) != 0 {
		t.Fatalf("relationship = %v, want empty", relationship)
	}
	if decoded["common_chats_count"] != float64(3) {
		t.Fatalf("common_chats_count = %v", decoded["common_chats_count"])
	}
	if decoded["bot"] != false || decoded["verified"] != false {
		t.Fatalf("flags = %+v", decoded)
	}
}

// TestGetFullUserSanitizesInvisibleCharacters asserts invisible codepoints are
// stripped from the bio and a year-less birthday renders the vCard form.
func TestGetFullUserSanitizesInvisibleCharacters(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.me = &User{ID: 7, FirstName: "Ada"}
	h.client.full = &FullUser{
		About:       "hello\u200bworld",
		BirthdayDay: 3, BirthdayMonth: 4, HasBirthday: true,
	}

	got := getFullUser(h.ctx(), h.client, getFullUserInput{Username: 7})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("invalid JSON %q: %v", got, err)
	}
	if bio, _ := decoded["bio"].(string); strings.ContainsRune(bio, '\u200b') {
		t.Fatalf("bio kept an invisible codepoint: %q", bio)
	}
	if decoded["birthday"] != "--04-03" {
		t.Fatalf("birthday = %v, want --04-03", decoded["birthday"])
	}
	if decoded["pinned_message_id"] != nil || decoded["gifts_count"] != nil {
		t.Fatalf("absent optional fields are not null: %+v", decoded)
	}
}

// TestGetFullUserBusinessProfile asserts only the present business sections
// appear, with sanitized strings.
func TestGetFullUserBusinessProfile(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.me = &User{ID: 7, FirstName: "Ada"}
	h.client.full = &FullUser{
		BusinessLocationAddress: "1 Main St", HasBusinessLocation: true,
		BusinessTimezoneID: 42, HasBusinessHours: true,
		BusinessIntroTitle: "Shop", BusinessIntroDescription: "Buy", HasBusinessIntro: true,
	}

	got := getFullUser(h.ctx(), h.client, getFullUserInput{Username: 7})
	var decoded struct {
		Business businessProfile `json:"business"`
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("invalid JSON %q: %v", got, err)
	}
	if decoded.Business.Location == nil || *decoded.Business.Location != "1 Main St" {
		t.Fatalf("location = %v", decoded.Business.Location)
	}
	if decoded.Business.Timezone == nil || *decoded.Business.Timezone != 42 {
		t.Fatalf("timezone = %v", decoded.Business.Timezone)
	}
	if decoded.Business.IntroTitle == nil || *decoded.Business.IntroTitle != "Shop" {
		t.Fatalf("intro_title = %v", decoded.Business.IntroTitle)
	}
	if decoded.Business.IntroDescription == nil || *decoded.Business.IntroDescription != "Buy" {
		t.Fatalf("intro_description = %v", decoded.Business.IntroDescription)
	}
}

// TestGetFullUserFunnelsResolverError asserts an unresolvable reference is
// funneled rather than returned raw.
func TestGetFullUserFunnelsResolverError(t *testing.T) {
	h := newHarness(newFakeClient())
	h.resolveErr = errors.New("no such user 12345")

	h.assertFunneled(t, getFullUser(h.ctx(), h.client, getFullUserInput{Username: 12345}), "get_full_user")
}

// TestGetBotInfo asserts the bot payload: the resolved entity's marked ID and
// username, sanitized names, and the about text.
func TestGetBotInfo(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.me = &User{ID: 7, FirstName: "Helper", LastName: "Bot", Username: "helper", Bot: true, Verified: true}
	h.client.full = &FullUser{About: "I help"}

	got := getBotInfo(h.ctx(), h.client, getBotInfoInput{BotUsername: "helper"})
	var decoded botInfoResult
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("get_bot_info returned invalid JSON %q: %v", got, err)
	}
	// The ID and username come from the resolved entity, not from the client's
	// own view of the user: Python reads entity.id / entity.username.
	if decoded.BotInfo.ID != 7 || decoded.BotInfo.Username != "target" {
		t.Fatalf("bot_info = %+v", decoded.BotInfo)
	}
	if decoded.BotInfo.FirstName != "Helper" || decoded.BotInfo.LastName != "Bot" {
		t.Fatalf("names = %+v", decoded.BotInfo)
	}
	if !decoded.BotInfo.IsBot || !decoded.BotInfo.Verified {
		t.Fatalf("flags = %+v", decoded.BotInfo)
	}
	if decoded.BotInfo.About == nil || *decoded.BotInfo.About != "I help" {
		t.Fatalf("about = %v", decoded.BotInfo.About)
	}
}

// TestGetBotInfoSanitizesEmptyFieldsAsPythonDoes asserts the Python parity of
// the two sanitize calls: an absent last name and an empty about both render as
// the sanitizer's "[empty]" marker rather than being dropped, because
// sanitize_name/sanitize_user_content return "[empty]" for empty input.
func TestGetBotInfoSanitizesEmptyFieldsAsPythonDoes(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.me = &User{ID: 7, FirstName: "Helper", Bot: true}
	h.client.full = &FullUser{}

	got := getBotInfo(h.ctx(), h.client, getBotInfoInput{BotUsername: "helper"})
	if !strings.Contains(got, `"last_name": "[empty]"`) {
		t.Fatalf("absent last_name was not sanitized like Python's: %q", got)
	}
	if !strings.Contains(got, `"about": "[empty]"`) {
		t.Fatalf("empty about was not sanitized like Python's: %q", got)
	}
}

// TestSetBotCommandsRefusesUserAccount asserts the non-bot early return and
// that no command list is sent.
func TestSetBotCommandsRefusesUserAccount(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.me = &User{ID: 1, Bot: false}

	got := setBotCommands(h.ctx(), h.client, setBotCommandsInput{
		BotUsername: "helper",
		Commands:    []BotCommand{{Command: "start", Description: "Begin"}},
	})
	if got != botOnlyMessage {
		t.Fatalf("set_bot_commands = %q", got)
	}
	if h.client.setCommands != nil {
		t.Fatalf("commands were sent by a non-bot account: %+v", h.client.setCommands)
	}
}

// TestSetBotCommandsSetsCommands asserts a bot account forwards the command
// list and the success string names the bot.
func TestSetBotCommandsSetsCommands(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.me = &User{ID: 1, Bot: true}
	commands := []BotCommand{{Command: "start", Description: "Begin"}, {Command: "help", Description: "Help"}}

	got := setBotCommands(h.ctx(), h.client, setBotCommandsInput{BotUsername: "helper", Commands: commands})
	if got != "Bot commands set for helper." {
		t.Fatalf("set_bot_commands = %q", got)
	}
	if !slices.Equal(h.client.setCommands, commands) {
		t.Fatalf("commands = %+v", h.client.setCommands)
	}
}

// TestGetUserPhotos asserts the photo IDs come back indented and the limit is
// forwarded.
func TestGetUserPhotos(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.userPhoto = []int64{11, 22}

	got := getUserPhotos(h.ctx(), h.client, getUserPhotosInput{UserID: "target", Limit: 5})
	if got != "[\n  11,\n  22\n]" {
		t.Fatalf("get_user_photos = %q", got)
	}
	if h.client.userPhotosLimit != 5 {
		t.Fatalf("limit = %d, want 5", h.client.userPhotosLimit)
	}
}

// TestGetUserPhotosDefaultsLimit asserts the Python default of 10 applies when
// the caller omits the limit, and an empty result is [] rather than null.
func TestGetUserPhotosDefaultsLimit(t *testing.T) {
	h := newHarness(newFakeClient())

	got := getUserPhotos(h.ctx(), h.client, getUserPhotosInput{UserID: 7})
	if got != "[]" {
		t.Fatalf("get_user_photos = %q, want []", got)
	}
	if h.client.userPhotosLimit != defaultUserPhotoLimit {
		t.Fatalf("limit = %d, want %d", h.client.userPhotosLimit, defaultUserPhotoLimit)
	}
}

// TestGetUserStatus asserts the status text is returned verbatim and the
// resolved entity reached the client.
func TestGetUserStatus(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.status = "UserStatusOnline"

	got := getUserStatus(h.ctx(), h.client, getUserStatusInput{UserID: "@target"})
	if got != "UserStatusOnline" {
		t.Fatalf("get_user_status = %q", got)
	}
	entity, ok := h.client.statusArg.(kit.Entity)
	if !ok || entity.BareID() != 7 {
		t.Fatalf("client received %v", h.client.statusArg)
	}
}

// TestGetUserStatusRejectsInvalidID asserts @validate_id's message comes back
// verbatim and the client is never called.
func TestGetUserStatusRejectsInvalidID(t *testing.T) {
	h := newHarness(newFakeClient())

	got := getUserStatus(h.ctx(), h.client, getUserStatusInput{UserID: 3.5})
	if !strings.HasPrefix(got, "Invalid user_id:") {
		t.Fatalf("get_user_status = %q", got)
	}
	if h.client.statusArg != nil {
		t.Fatalf("client was called for an invalid ID: %v", h.client.statusArg)
	}
}

// TestDepsConsumingBodiesWithoutDepsReturnAFunnelError asserts every body that
// reads Deps (the path gate, the entity resolver) yields the funnel's message
// on a bare context instead of panicking. The bodies that need only the client
// (get_me, update_profile, delete_profile_photo, get_privacy_settings) do not
// consult Deps at all - matching the Python tools, none of which resolve an
// identifier - so they are covered through dispatch instead.
func TestDepsConsumingBodiesWithoutDepsReturnAFunnelError(t *testing.T) {
	ctx := context.Background()
	client := newFakeClient()
	client.me = &User{ID: 1, Bot: true}

	for name, got := range map[string]string{
		"set_profile_photo":    setProfilePhoto(ctx, client, setProfilePhotoInput{FilePath: "x.png"}),
		"set_privacy_settings": setPrivacySettings(ctx, client, setPrivacySettingsInput{Key: "status"}),
		"get_full_user":        getFullUser(ctx, client, getFullUserInput{Username: "a"}),
		"get_bot_info":         getBotInfo(ctx, client, getBotInfoInput{BotUsername: "a"}),
		"set_bot_commands":     setBotCommands(ctx, client, setBotCommandsInput{BotUsername: "a"}),
		"get_user_photos":      getUserPhotos(ctx, client, getUserPhotosInput{UserID: "a"}),
		"get_user_status":      getUserStatus(ctx, client, getUserStatusInput{UserID: "a"}),
	} {
		if !strings.HasPrefix(got, "An error occurred (code: ") {
			t.Errorf("%s without deps = %q, want a funnel error", name, got)
		}
	}
}

// TestFloodWaitIsNotRetryable asserts a FloodWait error surfaces the "do NOT
// retry immediately" prose and is reported as a warning, not an error.
func TestFloodWaitIsNotRetryable(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.errs["get_me"] = &kit.FloodWaitError{Seconds: 42}

	got := getMe(h.ctx(), h.client, getMeInput{})
	if !strings.Contains(got, "Do NOT retry immediately") || !strings.Contains(got, "42 seconds") {
		t.Fatalf("get_me flood wait = %q", got)
	}
	if len(h.reporter.warnings) != 1 {
		t.Fatalf("flood wait warnings = %v, want exactly one", h.reporter.warnings)
	}
	if len(h.reporter.errors) != 0 {
		t.Fatalf("flood wait must not be persisted as an error: %v", h.reporter.errors)
	}
}

// TestParsePrivacyKey asserts the supported-key mapping.
func TestParsePrivacyKey(t *testing.T) {
	for _, key := range []string{"status", "phone", "profile_photo"} {
		if _, ok := ParsePrivacyKey(key); !ok {
			t.Errorf("ParsePrivacyKey(%q) = false, want true", key)
		}
	}
	if _, ok := ParsePrivacyKey("phone_number"); ok {
		t.Error("ParsePrivacyKey(\"phone_number\") = true, want false")
	}
}

// TestResolveRuleUsersSkipsUnresolvable asserts a reference that does not
// resolve is skipped, not fatal, matching the Python warning-and-continue.
func TestResolveRuleUsersSkipsUnresolvable(t *testing.T) {
	h := newHarness(newFakeClient())
	h.resolveErr = errors.New("unknown peer")

	got := setPrivacySettings(h.ctx(), h.client, setPrivacySettingsInput{
		Key:        "status",
		AllowUsers: []any{"nobody"},
	})
	if got != "Privacy settings for status updated successfully." {
		t.Fatalf("set_privacy_settings = %q", got)
	}
	if len(h.client.privacyRules) != 0 {
		t.Fatalf("rules = %+v, want none (no user resolved)", h.client.privacyRules)
	}
}

// TestDispatchRoutesThroughTheRouter asserts the registered call path (not just
// the body) resolves Deps, resolves the client for the account label, runs the
// body and returns its text with no protocol error.
func TestDispatchRoutesThroughTheRouter(t *testing.T) {
	h := newHarness(newFakeClient())
	h.client.status = "UserStatusRecently"

	got := dispatch(h.ctx(), "get_user_status", true, getUserStatus, getUserStatusInput{UserID: "@target"})
	if got != "UserStatusRecently" {
		t.Fatalf("dispatch result = %q", got)
	}
	if h.client.statusArg == nil {
		t.Fatal("dispatch did not reach the client")
	}
}

// TestDispatchFunnelsClientFailures asserts an account that cannot be built is
// funneled into the result text, never a protocol error.
func TestDispatchFunnelsClientFailures(t *testing.T) {
	reporter := &recordingReporter{}
	deps := &Deps{
		Router:   kit.NewRouter([]string{"default"}),
		Client:   func(string) (Client, error) { return nil, errors.New("not connected") },
		Reporter: reporter,
	}
	ctx := WithDeps(context.Background(), deps)

	got := dispatch(ctx, "get_me", true, getMe, getMeInput{})
	if !strings.HasPrefix(got, "An error occurred (code: ") {
		t.Fatalf("dispatch result = %q", got)
	}
	if strings.Contains(got, "not connected") {
		t.Fatalf("dispatch leaked the client error text: %q", got)
	}
	if len(reporter.errors) == 0 {
		t.Fatal("routing failure was not reported")
	}
}

// TestDispatchWithoutDepsIsFunneled asserts a context carrying no Deps yields
// the funnel's text rather than a panic.
func TestDispatchWithoutDepsIsFunneled(t *testing.T) {
	kit.SetDefaultReporter(&recordingReporter{})
	t.Cleanup(func() { kit.SetDefaultReporter(nil) })

	got := dispatch(context.Background(), "get_me", true, getMe, getMeInput{})
	if !strings.HasPrefix(got, "An error occurred (code: ") {
		t.Fatalf("dispatch result = %q", got)
	}
}

// TestDispatchMultiAccountWriteRequiresAccount asserts the read-only fan-out
// contract still applies on the registered path: a write in multi-account mode
// is refused with the account hint instead of hitting Telegram.
func TestDispatchMultiAccountWriteRequiresAccount(t *testing.T) {
	h := newHarness(newFakeClient())
	h.deps.Router = kit.NewRouter([]string{"work", "personal"})

	got := dispatch(h.ctx(), "update_profile", false, updateProfile, updateProfileInput{})
	if !strings.Contains(got, "'account' is required") {
		t.Fatalf("dispatch result = %q", got)
	}
	if h.client.updated != [3]*string{} {
		t.Fatalf("a write ran without an account label: %v", h.client.updated)
	}
}

// profileRegistry collects this module's tools into a private registry by
// re-running the module's register functions against a swapped default
// registry, restoring the original afterwards.
func profileRegistry() *mcpserver.Registry {
	original := mcpserver.DefaultRegistry
	reg := mcpserver.NewRegistry()
	mcpserver.DefaultRegistry = reg
	defer func() { mcpserver.DefaultRegistry = original }()

	registerGetMe()
	registerUpdateProfile()
	registerSetProfilePhoto()
	registerDeleteProfilePhoto()
	registerGetPrivacySettings()
	registerSetPrivacySettings()
	registerGetFullUser()
	registerGetBotInfo()
	registerSetBotCommands()
	registerGetUserPhotos()
	registerGetUserStatus()
	return reg
}

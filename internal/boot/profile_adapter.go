package boot

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/tools/profile"
)

// profileAdapter implements profile.Client over the account's lazily
// connected gogram client. It keeps a small photo cache because the delete
// tool receives bare photo IDs while Telegram's deletePhotos needs the access
// hash and file reference that only the listing calls return.
type profileAdapter struct {
	sess *sessionManager

	mu     sync.Mutex
	photos map[int64]telegram.InputPhoto
}

var _ profile.Client = (*profileAdapter)(nil)

func newProfileAdapter(sess *sessionManager) profile.Client {
	return &profileAdapter{sess: sess, photos: map[int64]telegram.InputPhoto{}}
}

// GetMe returns the authorized account's own user record.
func (a *profileAdapter) GetMe(ctx context.Context) (*profile.User, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	me, err := cl.GetMe()
	if err != nil {
		return nil, err
	}
	return profileUser(me), nil
}

// UpdateProfile changes the account's name and bio. A nil field keeps its
// current value: gogram's generated request only sets a flag for a non-empty
// value, so both nil and "" mean "leave unchanged".
func (a *profileAdapter) UpdateProfile(ctx context.Context, firstName, lastName, about *string) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	_, err = cl.AccountUpdateProfile(deref(firstName), deref(lastName), deref(about))
	return err
}

// UploadProfilePhoto uploads path as the account's new profile photo.
func (a *profileAdapter) UploadProfilePhoto(ctx context.Context, path string) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	uploaded, err := cl.UploadFile(path)
	if err != nil {
		return err
	}
	_, err = cl.PhotosUploadProfilePhoto(&telegram.PhotosUploadProfilePhotoParams{File: uploaded})
	return err
}

// MyProfilePhotoIDs returns the account's own profile photos, newest first,
// as bare photo IDs; the listing also feeds the delete cache.
func (a *profileAdapter) MyProfilePhotoIDs(ctx context.Context, limit int32) ([]int64, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := cl.PhotosGetUserPhotos(&telegram.InputUserSelf{}, 0, 0, limit)
	if err != nil {
		return nil, err
	}
	return a.rememberPhotos(resp), nil
}

// DeleteProfilePhotos deletes profile photos by bare ID, resolving the
// access hashes from the cached listing (refreshed once when an ID is
// unknown).
func (a *profileAdapter) DeleteProfilePhotos(ctx context.Context, ids []int64) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	input := make([]telegram.InputPhoto, 0, len(ids))
	for _, id := range ids {
		photo, ok := a.cachedPhoto(id)
		if !ok {
			if _, err := a.MyProfilePhotoIDs(ctx, 0); err != nil {
				return err
			}
			photo, ok = a.cachedPhoto(id)
		}
		if !ok {
			return fmt.Errorf("delete_profile_photo: photo %d is not among the account's profile photos", id)
		}
		input = append(input, photo)
	}
	_, err = cl.PhotosDeletePhotos(input)
	return err
}

// UserPhotos returns another user's profile photo IDs, in profile order.
func (a *profileAdapter) UserPhotos(ctx context.Context, user any, limit int32) ([]int64, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	inputUser, err := a.inputUser(ctx, cl, user)
	if err != nil {
		return nil, err
	}
	resp, err := cl.PhotosGetUserPhotos(inputUser, 0, 0, limit)
	if err != nil {
		return nil, err
	}
	return a.rememberPhotos(resp), nil
}

// GetPrivacy returns the privacy rules of one key.
func (a *profileAdapter) GetPrivacy(ctx context.Context, key profile.PrivacyKey) ([]profile.PrivacyRule, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := cl.AccountGetPrivacy(inputPrivacyKey(key))
	if err != nil {
		return nil, err
	}
	out := make([]profile.PrivacyRule, 0, len(rules.Rules))
	for _, rule := range rules.Rules {
		switch r := rule.(type) {
		case *telegram.PrivacyValueAllowAll:
			out = append(out, profile.PrivacyRule{Kind: profile.PrivacyAllowAll})
		case *telegram.PrivacyValueAllowContacts:
			out = append(out, profile.PrivacyRule{Kind: profile.PrivacyAllowAll})
		case *telegram.PrivacyValueDisallowAll:
			out = append(out, profile.PrivacyRule{Kind: profile.PrivacyDisallowAll})
		case *telegram.PrivacyValueDisallowContacts:
			out = append(out, profile.PrivacyRule{Kind: profile.PrivacyDisallowAll})
		case *telegram.PrivacyValueAllowUsers:
			out = append(out, profile.PrivacyRule{Kind: profile.PrivacyAllowUsers, Users: r.Users})
		case *telegram.PrivacyValueDisallowUsers:
			out = append(out, profile.PrivacyRule{Kind: profile.PrivacyDisallowUsers, Users: r.Users})
		}
	}
	return out, nil
}

// SetPrivacy replaces the privacy rules of one key.
func (a *profileAdapter) SetPrivacy(ctx context.Context, key profile.PrivacyKey, rules []profile.PrivacyRule) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	input := make([]telegram.InputPrivacyRule, 0, len(rules))
	for _, rule := range rules {
		switch rule.Kind {
		case profile.PrivacyAllowAll:
			input = append(input, &telegram.InputPrivacyValueAllowAll{})
		case profile.PrivacyDisallowAll:
			input = append(input, &telegram.InputPrivacyValueDisallowAll{})
		case profile.PrivacyAllowUsers:
			users, err := a.inputUsers(ctx, cl, rule.Users)
			if err != nil {
				return err
			}
			input = append(input, &telegram.InputPrivacyValueAllowUsers{Users: users})
		case profile.PrivacyDisallowUsers:
			users, err := a.inputUsers(ctx, cl, rule.Users)
			if err != nil {
				return err
			}
			input = append(input, &telegram.InputPrivacyValueDisallowUsers{Users: users})
		}
	}
	_, err = cl.AccountSetPrivacy(inputPrivacyKey(key), input)
	return err
}

// FullUser returns a user's full profile: the user record plus the extended
// profile.
func (a *profileAdapter) FullUser(ctx context.Context, user any) (*profile.User, *profile.FullUser, error) {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return nil, nil, err
	}
	inputUser, err := a.inputUser(ctx, cl, user)
	if err != nil {
		return nil, nil, err
	}
	resp, err := cl.UsersGetFullUser(inputUser)
	if err != nil {
		return nil, nil, err
	}
	record := (*profile.User)(nil)
	for _, candidate := range resp.Users {
		if obj, ok := candidate.(*telegram.UserObj); ok {
			record = profileUser(obj)
			break
		}
	}
	if record == nil {
		record = &profile.User{}
	}
	full := &profile.FullUser{}
	if resp.FullUser != nil {
		*full = fullUser(cl, resp.FullUser)
	}
	return record, full, nil
}

// fullUser projects gogram's UserFull onto the profile module's view.
func fullUser(cl *telegram.Client, source *telegram.UserFull) profile.FullUser {
	full := profile.FullUser{
		About:              source.About,
		CommonChatsCount:   source.CommonChatsCount,
		PrivateForwardName: source.PrivateForwardName,
	}
	if source.PersonalChannelID != 0 {
		full.PersonalChannelID = source.PersonalChannelID
		if channel, err := cl.GetChannel(source.PersonalChannelID); err == nil {
			full.PersonalChannelUsername = channel.Username
		}
	}
	if birthday := source.Birthday; birthday != nil {
		full.HasBirthday = true
		full.BirthdayDay = birthday.Day
		full.BirthdayMonth = birthday.Month
		full.BirthdayYear = birthday.Year
	}
	if location := source.BusinessLocation; location != nil {
		full.HasBusinessLocation = true
		full.BusinessLocationAddress = location.Address
	}
	if hours := source.BusinessWorkHours; hours != nil {
		full.HasBusinessHours = true
		full.BusinessTimezoneID = timezoneID(hours)
	}
	if intro := source.BusinessIntro; intro != nil {
		full.HasBusinessIntro = true
		full.BusinessIntroTitle = intro.Title
		full.BusinessIntroDescription = intro.Description
	}
	if source.PinnedMsgID != 0 {
		pinned := source.PinnedMsgID
		full.PinnedMessageID = &pinned
	}
	if source.StargiftsCount != 0 {
		gifts := source.StargiftsCount
		full.GiftsCount = &gifts
	}
	return full
}

// timezoneID reads the business timezone string, which the profile view
// carries as an ID.
func timezoneID(hours *telegram.BusinessWorkHours) int32 {
	var id int32
	if _, err := fmt.Sscanf(hours.TimezoneID, "%d", &id); err != nil {
		return 0
	}
	return id
}

// UserStatus returns a user's online status as display text (the concrete
// gogram status type name).
func (a *profileAdapter) UserStatus(ctx context.Context, user any) (string, error) {
	raw, err := a.rawEntity(user)
	if err != nil {
		return "", err
	}
	obj, ok := raw.(*telegram.UserObj)
	if !ok {
		return "", fmt.Errorf("user status is only available for users")
	}
	if obj.Status == nil {
		return "", nil
	}
	return reflect.TypeOf(obj.Status).Elem().Name(), nil
}

// SetBotCommands replaces a bot's command list for the default scope in
// English.
func (a *profileAdapter) SetBotCommands(ctx context.Context, bot any, commands []profile.BotCommand) error {
	cl, err := a.sess.client(ctx)
	if err != nil {
		return err
	}
	if _, err := a.inputUser(ctx, cl, bot); err != nil {
		return err
	}
	cmds := make([]*telegram.BotCommand, 0, len(commands))
	for _, command := range commands {
		cmds = append(cmds, &telegram.BotCommand{Command: command.Command, Description: command.Description})
	}
	_, err = cl.BotsSetBotCommands(&telegram.BotCommandScopeDefault{}, "en", cmds)
	return err
}

// inputUser resolves a tool-level user reference to the InputUser the
// profile RPCs take.
func (a *profileAdapter) inputUser(ctx context.Context, cl *telegram.Client, user any) (telegram.InputUser, error) {
	entity, ok := user.(kit.Entity)
	if !ok {
		raw, err := a.rawEntity(user)
		if err != nil {
			return nil, err
		}
		obj, ok := raw.(*telegram.UserObj)
		if !ok {
			return nil, fmt.Errorf("not a user: %T", raw)
		}
		return &telegram.InputUserObj{UserID: obj.ID, AccessHash: obj.AccessHash}, nil
	}
	if entity.PeerKind() != kit.PeerUser {
		return nil, fmt.Errorf("not a user")
	}
	obj, err := cl.GetUser(entity.BareID())
	if err != nil {
		return nil, err
	}
	return &telegram.InputUserObj{UserID: obj.ID, AccessHash: obj.AccessHash}, nil
}

// inputUsers resolves bare user IDs to InputUsers.
func (a *profileAdapter) inputUsers(ctx context.Context, cl *telegram.Client, ids []int64) ([]telegram.InputUser, error) {
	out := make([]telegram.InputUser, 0, len(ids))
	for _, id := range ids {
		obj, err := cl.GetUser(id)
		if err != nil {
			return nil, err
		}
		out = append(out, &telegram.InputUserObj{UserID: obj.ID, AccessHash: obj.AccessHash})
	}
	return out, nil
}

// rawEntity resolves through the shared entity source.
func (a *profileAdapter) rawEntity(identifier any) (any, error) {
	src := &entitySource{sess: a.sess}
	return src.raw(identifier)
}

// rememberPhotos caches the InputPhoto of every listed photo and returns the
// bare IDs.
func (a *profileAdapter) rememberPhotos(resp telegram.PhotosPhotos) []int64 {
	var photos []telegram.Photo
	switch page := resp.(type) {
	case *telegram.PhotosPhotosObj:
		photos = page.Photos
	case *telegram.PhotosPhotosSlice:
		photos = page.Photos
	}
	out := make([]int64, 0, len(photos))
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, photo := range photos {
		obj, ok := photo.(*telegram.PhotoObj)
		if !ok {
			continue
		}
		out = append(out, obj.ID)
		a.photos[obj.ID] = &telegram.InputPhotoObj{ID: obj.ID, AccessHash: obj.AccessHash, FileReference: obj.FileReference}
	}
	return out
}

func (a *profileAdapter) cachedPhoto(id int64) (telegram.InputPhoto, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	photo, ok := a.photos[id]
	return photo, ok
}

// inputPrivacyKey maps the module's simplified key onto gogram's enum.
func inputPrivacyKey(key profile.PrivacyKey) telegram.InputPrivacyKey {
	switch key {
	case profile.PrivacyKeyPhone:
		return telegram.InputPrivacyKeyPhoneNumber
	case profile.PrivacyKeyProfilePhoto:
		return telegram.InputPrivacyKeyProfilePhoto
	default:
		return telegram.InputPrivacyKeyStatusTimestamp
	}
}

// profileUser projects a gogram user onto the profile module's record.
func profileUser(user *telegram.UserObj) *profile.User {
	record := &profile.User{
		ID:                 user.ID,
		Username:           user.Username,
		FirstName:          user.FirstName,
		LastName:           user.LastName,
		Phone:              user.Phone,
		LangCode:           user.LangCode,
		Bot:                user.Bot,
		Verified:           user.Verified,
		Premium:            user.Premium,
		Scam:               user.Scam,
		Fake:               user.Fake,
		Restricted:         user.Restricted,
		Deleted:            user.Deleted,
		Support:            user.Support,
		Contact:            user.Contact,
		MutualContact:      user.MutualContact,
		CloseFriend:        user.CloseFriend,
		Self:               user.Self,
		RestrictionReasons: restrictionReasons(user),
	}
	for _, extra := range user.Usernames {
		if extra != nil && extra.Username != "" {
			record.Usernames = append(record.Usernames, extra.Username)
		}
	}
	if user.Status != nil {
		record.Status = reflect.TypeOf(user.Status).Elem().Name()
	}
	if photo, ok := user.Photo.(*telegram.UserProfilePhotoObj); ok {
		id := photo.PhotoID
		record.AvatarPhotoID = &id
	}
	return record
}

// restrictionReasons flattens the restriction reason records.
func restrictionReasons(user *telegram.UserObj) []string {
	out := make([]string, 0, len(user.RestrictionReason))
	for _, reason := range user.RestrictionReason {
		if reason != nil {
			out = append(out, reason.Reason)
		}
	}
	return out
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

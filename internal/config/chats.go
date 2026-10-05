package config

import (
	"strconv"
	"strings"
)

// ChatAllowlist is the parsed TELEGRAM_ALLOWED_CHAT_IDS set: integer chat
// IDs (with their marked/bare variants auto-indexed) and lower-cased
// usernames. A nil *ChatAllowlist means the allowlist is disabled and
// every chat is allowed (mirrors runtime.ALLOWED_CHAT_IDS = None).
type ChatAllowlist struct {
	// IDs holds integer IDs plus the auto-indexed variants.
	IDs map[int64]bool
	// Usernames holds lower-cased handles without the leading @.
	Usernames map[string]bool
}

// parseAllowedChatIDs mirrors runtime._parse_allowed_chat_ids.
//
// Tokens are comma-separated integers or @usernames/handles. A marked
// supergroup ID (-100XXXXXXXXXX) also indexes its bare channel ID; a
// positive bare ID also indexes its supergroup (-1000000000000 - val)
// and group (-val) variants; a negative basic-group ID also indexes its
// positive form. Returns nil for unset/blank input (allowlist disabled).
func parseAllowedChatIDs(raw string) *ChatAllowlist {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var ids map[int64]bool
	var usernames map[string]bool
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		val, err := strconv.ParseInt(token, 10, 64)
		if err != nil {
			handle := strings.ToLower(strings.TrimSpace(strings.TrimLeft(token, "@")))
			if handle != "" {
				if usernames == nil {
					usernames = map[string]bool{}
				}
				usernames[handle] = true
			}
			continue
		}
		if ids == nil {
			ids = map[int64]bool{}
		}
		ids[val] = true
		s := strconv.FormatInt(val, 10)
		switch {
		case strings.HasPrefix(s, "-100") && len(s) > 4:
			if channelID, err := strconv.ParseInt(s[4:], 10, 64); err == nil {
				ids[channelID] = true
			}
		case val > 0:
			ids[-1000000000000-val] = true
			ids[-val] = true
		case val < 0:
			ids[-val] = true
		}
	}
	if ids == nil && usernames == nil {
		return nil
	}
	return &ChatAllowlist{IDs: ids, Usernames: usernames}
}

// AllowsChatID reports whether the bare integer ID is allowlisted.
func (a *ChatAllowlist) AllowsChatID(id int64) bool {
	return a != nil && a.IDs[id]
}

// AllowsUsername reports whether the handle (with or without @, any case)
// is allowlisted.
func (a *ChatAllowlist) AllowsUsername(handle string) bool {
	if a == nil {
		return false
	}
	return a.Usernames[strings.ToLower(strings.TrimLeft(strings.TrimSpace(handle), "@"))]
}

// Enabled reports whether chat access control is active at all.
func (a *ChatAllowlist) Enabled() bool { return a != nil }

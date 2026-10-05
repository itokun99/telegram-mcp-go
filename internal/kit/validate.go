package kit

// This file ports the validation and privacy-allowlist half of runtime.py:
// validate_id's per-parameter validation and the TELEGRAM_ALLOWED_CHAT_IDS
// enforcement that resolves usernames before denying.

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// chatParamNames mirrors CHAT_PARAM_NAMES: the parameter names that, beyond
// ID validation, go through the privacy allowlist.
var chatParamNames = map[string]bool{
	"chat_id":      true,
	"from_chat_id": true,
	"to_chat_id":   true,
	"channel":      true,
}

// IsChatParam reports whether name is gated by the chat allowlist.
func IsChatParam(name string) bool { return chatParamNames[name] }

// ValidateID mirrors the per-value validation of runtime.validate_id.
//
// Integers and integer strings are normalized to int64 and range-checked;
// non-numeric strings (usernames, handles, phone numbers and free-text
// contact aliases) pass through unchanged — only type and range are validated
// here, reference resolution belongs to the resolver/alias layer. A slice of
// values is validated element-wise. nil yields (nil, nil).
func ValidateID(name string, value any) (any, *ValidationError) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case int32:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case uint:
		if uint64(v) > math.MaxInt64 {
			return nil, invalidRange(name, value)
		}
		return int64(v), nil
	case uint64:
		if v > math.MaxInt64 {
			return nil, invalidRange(name, value)
		}
		return int64(v), nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			return parsed, nil
		}
		if errors.Is(err, strconv.ErrRange) {
			return nil, invalidRange(name, value)
		}
		return v, nil
	}

	if items, ok := sliceItems(value); ok {
		out := make([]any, 0, len(items))
		for _, item := range items {
			validated, err := ValidateID(name, item)
			if err != nil {
				return nil, err
			}
			out = append(out, validated)
		}
		return out, nil
	}
	return nil, invalidType(name, value)
}

// sliceItems unwraps a slice or array value; []byte is not an ID list.
func sliceItems(value any) ([]any, bool) {
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
		return nil, false
	}
	items := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		items[i] = rv.Index(i).Interface()
	}
	return items, true
}

func invalidRange(name string, value any) *ValidationError {
	return &ValidationError{
		Message: fmt.Sprintf("Invalid %s: %v. ID is out of the valid integer range.", name, value),
	}
}

func invalidType(name string, value any) *ValidationError {
	return &ValidationError{
		Message: fmt.Sprintf("Invalid %s: %v. Type must be an integer or a string.", name, value),
	}
}

// ChatGate enforces the TELEGRAM_ALLOWED_CHAT_IDS privacy allowlist for the
// chat parameters of a tool, mirroring the decorator half of
// runtime.validate_id.
type ChatGate struct {
	// Allowlist is the parsed TELEGRAM_ALLOWED_CHAT_IDS; nil disables the gate.
	Allowlist ChatAllowlist
	// ResolveEntity resolves a string identifier that is not literally on the
	// allowlist, so an allowlisted @username (or an ID whose marked/bare form
	// is allowlisted) still passes. Optional; when nil, every miss is denied.
	ResolveEntity func(identifier any) (Entity, error)
}

// Enabled reports whether the allowlist is active.
func (g *ChatGate) Enabled() bool {
	return g != nil && g.Allowlist != nil && g.Allowlist.Enabled()
}

// Validate validates one parameter value and, for chat parameters under an
// active allowlist, enforces it: a value not on the list is resolved
// (usernames included) before the call is denied.
//
// It returns the validated value, or an error whose type is *ValidationError
// or *ChatAccessDeniedError and whose message LogAndFormatError returns
// verbatim.
func (g *ChatGate) Validate(name string, value any) (any, error) {
	validated, err := ValidateID(name, value)
	if err != nil {
		return nil, err
	}
	if validated == nil || !g.Enabled() || !IsChatParam(name) {
		return validated, nil
	}
	for _, item := range gateItems(validated) {
		if ChatAllowed(g.Allowlist, item, nil) {
			continue
		}
		if identifier, ok := item.(string); ok && g.ResolveEntity != nil {
			if entity, resolveErr := g.ResolveEntity(identifier); resolveErr == nil &&
				ChatAllowed(g.Allowlist, identifier, entity) {
				continue
			}
		}
		return nil, &ChatAccessDeniedError{Message: CheckChatAccess(g.Allowlist, item, nil)}
	}
	return validated, nil
}

func gateItems(validated any) []any {
	if items, ok := validated.([]any); ok {
		return items
	}
	return []any{validated}
}

// ChatAllowed mirrors runtime.is_chat_allowed: a nil or disabled allowlist
// permits everything; integers are checked as parsed (the parser already
// indexed marked/bare variants); strings are checked as integers and then as
// handles; an entity is checked by marked ID, bare ID and username.
func ChatAllowed(allowlist ChatAllowlist, identifier any, entity Entity) bool {
	if allowlist == nil || !allowlist.Enabled() {
		return true
	}
	switch v := identifier.(type) {
	case int64:
		if allowlist.AllowsChatID(v) {
			return true
		}
	case int:
		if allowlist.AllowsChatID(int64(v)) {
			return true
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			if allowlist.AllowsChatID(parsed) {
				return true
			}
		} else if handle := strings.TrimLeft(trimmed, "@"); handle != "" && allowlist.AllowsUsername(handle) {
			return true
		}
	}
	if entity != nil {
		if allowlist.AllowsChatID(GetMarkedID(entity)) {
			return true
		}
		if allowlist.AllowsChatID(entity.BareID()) {
			return true
		}
		if username := entity.Username(); username != "" && allowlist.AllowsUsername(username) {
			return true
		}
	}
	return false
}

// CheckChatAccess mirrors runtime.check_chat_access: it returns the denial
// message for identifier, or "" when the chat is allowed.
func CheckChatAccess(allowlist ChatAllowlist, identifier any, entity Entity) string {
	if ChatAllowed(allowlist, identifier, entity) {
		return ""
	}
	return fmt.Sprintf(
		"Access to chat '%v' is restricted by privacy policy (TELEGRAM_ALLOWED_CHAT_IDS).",
		identifier,
	)
}

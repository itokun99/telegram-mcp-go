package kit

import (
	"errors"
	"strings"
	"testing"

	"github.com/itokun99/telegram-mcp-go/internal/config"
)

var _ ChatAllowlist = (*config.ChatAllowlist)(nil)

type fakeAllowlist struct {
	ids       map[int64]bool
	usernames map[string]bool
	enabled   bool
}

func newFakeAllowlist(ids []int64, usernames ...string) *fakeAllowlist {
	allowlist := &fakeAllowlist{ids: map[int64]bool{}, usernames: map[string]bool{}, enabled: true}
	for _, id := range ids {
		allowlist.ids[id] = true
	}
	for _, username := range usernames {
		allowlist.usernames[strings.ToLower(strings.TrimLeft(username, "@"))] = true
	}
	return allowlist
}

func (f *fakeAllowlist) AllowsChatID(id int64) bool { return f != nil && f.ids[id] }

func (f *fakeAllowlist) AllowsUsername(handle string) bool {
	if f == nil {
		return false
	}
	return f.usernames[strings.ToLower(strings.TrimLeft(handle, "@"))]
}

func (f *fakeAllowlist) Enabled() bool { return f != nil && f.enabled }

func TestValidateIDAcceptsIntegersAndStrings(t *testing.T) {
	cases := []struct {
		value any
		want  any
	}{
		{12345, int64(12345)},
		{int64(-100123456), int64(-100123456)},
		{"12345", int64(12345)},
		{"@test_user", "@test_user"},
		{"test_user_long_enough", "test_user_long_enough"},
	}
	for _, tc := range cases {
		got, err := ValidateID("user_id", tc.value)
		if err != nil {
			t.Errorf("ValidateID(%v) error: %v", tc.value, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ValidateID(%v) = %#v, want %#v", tc.value, got, tc.want)
		}
	}
}

func TestValidateIDNormalizesLists(t *testing.T) {
	got, err := ValidateID("user_ids", []any{123, "456", "@test_user"})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{int64(123), int64(456), "@test_user"}
	if len(got.([]any)) != 3 {
		t.Fatalf("got %#v", got)
	}
	for i := range want {
		if got.([]any)[i] != want[i] {
			t.Errorf("item %d = %#v, want %#v", i, got.([]any)[i], want[i])
		}
	}
}

func TestValidateIDTypeErrors(t *testing.T) {
	_, err := ValidateID("user_id", 123.45)
	if err == nil || err.Message != "Invalid user_id: 123.45. Type must be an integer or a string." {
		t.Errorf("float error = %v", err)
	}

	_, err = ValidateID("user_ids", []any{123, "456", 123.45})
	if err == nil || err.Message != "Invalid user_ids: 123.45. Type must be an integer or a string." {
		t.Errorf("list item error = %v", err)
	}
}

func TestValidateIDRangeErrors(t *testing.T) {
	_, err := ValidateID("user_id", uint64(1<<64-1))
	want := "Invalid user_id: 18446744073709551615. ID is out of the valid integer range."
	if err == nil || err.Message != want {
		t.Errorf("uint64 range error = %v", err)
	}

	_, err = ValidateID("user_id", "99999999999999999999999")
	want = "Invalid user_id: 99999999999999999999999. ID is out of the valid integer range."
	if err == nil || err.Message != want {
		t.Errorf("string range error = %v", err)
	}
}

func TestValidateIDNil(t *testing.T) {
	got, err := ValidateID("user_id", nil)
	if got != nil || err != nil {
		t.Errorf("nil = %#v, %v", got, err)
	}
}

func TestChatGateDeniesUnlistedChatID(t *testing.T) {
	gate := &ChatGate{Allowlist: newFakeAllowlist([]int64{12345678, -100123456789})}
	_, err := gate.Validate("chat_id", int64(99999999))
	var denied *ChatAccessDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected ChatAccessDeniedError, got %v", err)
	}
	want := "Access to chat '99999999' is restricted by privacy policy (TELEGRAM_ALLOWED_CHAT_IDS)."
	if denied.Message != want {
		t.Errorf("message = %q, want %q", denied.Message, want)
	}
}

func TestChatGateAllowsListedForms(t *testing.T) {
	gate := &ChatGate{Allowlist: newFakeAllowlist([]int64{12345678, -100123456789, 123456789})}
	for _, value := range []any{int64(12345678), "12345678", int64(-100123456789), int64(123456789)} {
		if _, err := gate.Validate("chat_id", value); err != nil {
			t.Errorf("allowed value %v denied: %v", value, err)
		}
	}
}

func TestChatGateIgnoresNonChatParams(t *testing.T) {
	gate := &ChatGate{Allowlist: newFakeAllowlist([]int64{12345678})}
	if _, err := gate.Validate("user_id", int64(99999999)); err != nil {
		t.Errorf("user_id must not be gated: %v", err)
	}
}

func TestChatGateDisabledAllowlist(t *testing.T) {
	gate := &ChatGate{}
	if gate.Enabled() {
		t.Error("nil allowlist must be disabled")
	}
	if _, err := gate.Validate("chat_id", int64(99999999)); err != nil {
		t.Errorf("disabled allowlist denied a chat: %v", err)
	}
}

func TestChatGateResolvesUsernamesBeforeDenial(t *testing.T) {
	allowlist := newFakeAllowlist([]int64{12345}, "allowed_handle", "resolve_me")
	resolved := 0
	gate := &ChatGate{
		Allowlist: allowlist,
		ResolveEntity: func(identifier any) (Entity, error) {
			resolved++
			return fakeEntity{id: 777, kind: PeerUser, username: "resolve_me"}, nil
		},
	}

	if _, err := gate.Validate("chat_id", "@allowed_handle"); err != nil {
		t.Errorf("listed handle denied: %v", err)
	}
	if _, err := gate.Validate("chat_id", "@ghost"); err != nil {
		t.Errorf("entity-resolved username denied: %v", err)
	}
	if resolved == 0 {
		t.Error("resolver was never consulted for an unlisted username")
	}

	gate.ResolveEntity = func(identifier any) (Entity, error) {
		return nil, errors.New("not found")
	}
	_, err := gate.Validate("chat_id", "@ghost")
	var denied *ChatAccessDeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Message, "'@ghost'") {
		t.Errorf("failed resolution must deny with the original reference: %v", err)
	}

	gate.ResolveEntity = nil
	if _, err := gate.Validate("chat_id", "@ghost"); err == nil {
		t.Error("nil resolver must deny unlisted strings")
	}
}

func TestChatGateNeverResolvesNumericIDs(t *testing.T) {
	resolved := 0
	gate := &ChatGate{
		Allowlist: newFakeAllowlist([]int64{12345}),
		ResolveEntity: func(identifier any) (Entity, error) {
			resolved++
			return fakeEntity{id: 777, kind: PeerUser}, nil
		},
	}
	if _, err := gate.Validate("chat_id", int64(777)); err == nil {
		t.Error("unlisted numeric id must be denied without resolution")
	}
	if resolved != 0 {
		t.Errorf("numeric ids must not be resolved (%d resolver calls)", resolved)
	}
}

func TestChatGateEnsuresBatchParams(t *testing.T) {
	gate := &ChatGate{Allowlist: newFakeAllowlist([]int64{-100111111111})}
	if _, err := gate.Validate("to_chat_id", []any{int64(-100111111111), int64(-100999999999)}); err == nil {
		t.Error("unlisted batch member must deny the call")
	}
	if _, err := gate.Validate("from_chat_id", int64(-100111111111)); err != nil {
		t.Errorf("listed batch member denied: %v", err)
	}
}

func TestChatGateWithConfigAllowlistVariants(t *testing.T) {
	allowlist := &config.ChatAllowlist{IDs: map[int64]bool{
		12345678:                  true,
		-1000000000000 - 12345678: true,
		-12345678:                 true,
		-100123456789:             true,
		123456789:                 true,
	}}
	gate := &ChatGate{Allowlist: allowlist}
	for _, value := range []any{int64(12345678), "12345678", int64(-100123456789), int64(123456789)} {
		if _, err := gate.Validate("chat_id", value); err != nil {
			t.Errorf("variant %v denied: %v", value, err)
		}
	}
	if _, err := gate.Validate("chat_id", int64(99999999)); err == nil {
		t.Error("unlisted id allowed")
	}
}

func TestIsChatParam(t *testing.T) {
	for _, name := range []string{"chat_id", "from_chat_id", "to_chat_id", "channel"} {
		if !IsChatParam(name) {
			t.Errorf("%s must be gated", name)
		}
	}
	for _, name := range []string{"user_id", "message_id", "limit", ""} {
		if IsChatParam(name) {
			t.Errorf("%s must not be gated", name)
		}
	}
}

func TestChatAllowedWithEntity(t *testing.T) {
	byBareID := newFakeAllowlist([]int64{111})
	if !ChatAllowed(byBareID, nil, fakeEntity{id: 111, kind: PeerUser}) {
		t.Error("bare id should match")
	}

	byMarked := newFakeAllowlist([]int64{-1000000012345})
	if !ChatAllowed(byMarked, nil, fakeEntity{id: 12345, kind: PeerChannel}) {
		t.Error("marked channel id should match")
	}

	byUsername := newFakeAllowlist(nil, "allowed_ch")
	if !ChatAllowed(byUsername, nil, fakeEntity{id: 999999, kind: PeerChannel, username: "allowed_ch"}) {
		t.Error("entity username should match")
	}

	if ChatAllowed(newFakeAllowlist([]int64{12345}), nil, fakeEntity{id: 999999, kind: PeerUser}) {
		t.Error("unlisted entity must not match")
	}
	if !ChatAllowed(nil, int64(999999), nil) {
		t.Error("nil allowlist must allow everything")
	}
}

func TestCheckChatAccess(t *testing.T) {
	allowlist := newFakeAllowlist([]int64{12345})
	if got := CheckChatAccess(allowlist, int64(12345), nil); got != "" {
		t.Errorf("allowed chat returned %q", got)
	}
	want := "Access to chat '99999' is restricted by privacy policy (TELEGRAM_ALLOWED_CHAT_IDS)."
	if got := CheckChatAccess(allowlist, int64(99999), nil); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

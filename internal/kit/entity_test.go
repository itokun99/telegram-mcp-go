package kit

import (
	"reflect"
	"testing"

	telegram "github.com/amarnathcjd/gogram/telegram"
)

type fakeEntity struct {
	id       int64
	kind     PeerKind
	username string
}

func (f fakeEntity) BareID() int64      { return f.id }
func (f fakeEntity) PeerKind() PeerKind { return f.kind }
func (f fakeEntity) Username() string   { return f.username }

func TestWrapEntityGogramTypes(t *testing.T) {
	cases := []struct {
		name         string
		input        any
		wantType     string
		wantBare     int64
		wantMarked   int64
		wantFilter   EntityFilter
		wantUsername string
	}{
		{"user", &telegram.UserObj{ID: 1, Username: "jdoe"}, "User", 1, 1, "user", "jdoe"},
		{"empty user", &telegram.UserEmpty{ID: 5}, "User", 5, 5, "user", ""},
		{"basic group", &telegram.ChatObj{ID: 2, Title: "Group"}, "Group (Basic)", 2, -2, "group", ""},
		{"empty group", &telegram.ChatEmpty{ID: 3}, "Group (Basic)", 3, -3, "group", ""},
		{"forbidden group", &telegram.ChatForbidden{ID: 4, Title: "Gone"}, "Group (Basic)", 4, -4, "group", ""},
		{"supergroup", &telegram.Channel{ID: 6, Megagroup: true, Username: "super"}, "Supergroup", 6, -1000000000006, "group", "super"},
		{"broadcast channel", &telegram.Channel{ID: 7, Broadcast: true}, "Channel", 7, -1000000000007, "channel", ""},
		{"flagless channel", &telegram.Channel{ID: 8}, "Group", 8, -1000000000008, "group", ""},
		{"forbidden channel", &telegram.ChannelForbidden{ID: 9, Broadcast: true}, "Channel", 9, -1000000000009, "channel", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GetEntityType(tc.input); got != tc.wantType {
				t.Errorf("type = %q, want %q", got, tc.wantType)
			}
			if got := GetMarkedID(tc.input); got != tc.wantMarked {
				t.Errorf("marked id = %d, want %d", got, tc.wantMarked)
			}
			if got := GetEntityFilterType(tc.input); got != tc.wantFilter {
				t.Errorf("filter = %q, want %q", got, tc.wantFilter)
			}
			entity, err := WrapEntity(tc.input)
			if err != nil {
				t.Fatalf("WrapEntity: %v", err)
			}
			if entity.BareID() != tc.wantBare {
				t.Errorf("bare id = %d, want %d", entity.BareID(), tc.wantBare)
			}
			if entity.Username() != tc.wantUsername {
				t.Errorf("username = %q, want %q", entity.Username(), tc.wantUsername)
			}
		})
	}
}

func TestGetMarkedIDFloor(t *testing.T) {
	channel := &telegram.Channel{ID: 123, Broadcast: true}
	if got := GetMarkedID(channel); got != -1000000000123 {
		t.Errorf("marked id = %d, want %d", got, int64(-1000000000123))
	}
}

func TestMarkedIDCandidates(t *testing.T) {
	for _, identifier := range []any{123, int64(123)} {
		got := MarkedIDCandidates(identifier)
		want := []int64{-1000000000123, -123}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("candidates(%v) = %v, want %v", identifier, got, want)
		}
	}
	for _, identifier := range []any{0, -123, "123", uint64(123)} {
		if got := MarkedIDCandidates(identifier); got != nil {
			t.Errorf("candidates(%v) = %v, want nil", identifier, got)
		}
	}
}

func TestWrapEntityPassthroughAndErrors(t *testing.T) {
	original := fakeEntity{id: 42, kind: PeerUser}
	wrapped, err := WrapEntity(original)
	if err != nil || wrapped.BareID() != 42 {
		t.Errorf("Entity passthrough = %v, %v", wrapped, err)
	}
	if _, err := WrapEntity(nil); err == nil {
		t.Error("nil must not wrap")
	}
	if _, err := WrapEntity("not an entity"); err == nil {
		t.Error("unknown type must not wrap")
	}
}

func TestEntityHelpersUnknownValues(t *testing.T) {
	if got := GetEntityType(nil); got != "Unknown" {
		t.Errorf("GetEntityType(nil) = %q", got)
	}
	if got := GetMarkedID("x"); got != 0 {
		t.Errorf("GetMarkedID(\"x\") = %d", got)
	}
	if got := GetEntityFilterType(42); got != "" {
		t.Errorf("GetEntityFilterType(42) = %q", got)
	}
}

func TestEntityHelpersAcceptKitEntities(t *testing.T) {
	supergroup := fakeEntity{id: 12345, kind: PeerSupergroup, username: "sg"}
	if got := GetEntityType(supergroup); got != "Supergroup" {
		t.Errorf("type = %q", got)
	}
	if got := GetMarkedID(supergroup); got != -1000000012345 {
		t.Errorf("marked = %d", got)
	}
	if got := GetEntityFilterType(supergroup); got != "group" {
		t.Errorf("filter = %q", got)
	}
}

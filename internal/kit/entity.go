package kit

// This file ports get_entity_type / get_marked_id / get_entity_filter_type
// and adapts gogram TL objects to the Entity interface so tools never import
// gogram entity types directly.

import (
	"fmt"

	telegram "github.com/amarnathcjd/gogram/telegram"
)

// PeerKind is the normalized, human-readable chat/entity class, with the
// exact string values of runtime.get_entity_type: User, Group (Basic),
// Supergroup, Channel and Group (a Channel carrying neither flag).
type PeerKind string

const (
	PeerUser       PeerKind = "User"
	PeerBasicGroup PeerKind = "Group (Basic)"
	PeerSupergroup PeerKind = "Supergroup"
	PeerChannel    PeerKind = "Channel"
	PeerGroup      PeerKind = "Group"
)

type gogramEntity struct {
	id       int64
	kind     PeerKind
	username string
}

func (e *gogramEntity) BareID() int64      { return e.id }
func (e *gogramEntity) PeerKind() PeerKind { return e.kind }
func (e *gogramEntity) Username() string   { return e.username }

// WrapEntity returns v as an Entity: an Entity passes through, a gogram
// pointer to User/Chat/Channel (including the empty and forbidden variants)
// is classified, anything else is an error.
func WrapEntity(v any) (Entity, error) {
	switch t := v.(type) {
	case nil:
		return nil, fmt.Errorf("kit: cannot wrap nil entity")
	case Entity:
		return t, nil
	case *telegram.Channel:
		return &gogramEntity{id: t.ID, kind: channelKind(t.Megagroup, t.Broadcast), username: t.Username}, nil
	case *telegram.ChannelForbidden:
		return &gogramEntity{id: t.ID, kind: channelKind(t.Megagroup, t.Broadcast)}, nil
	case *telegram.UserObj:
		return &gogramEntity{id: t.ID, kind: PeerUser, username: t.Username}, nil
	case *telegram.UserEmpty:
		return &gogramEntity{id: t.ID, kind: PeerUser}, nil
	case *telegram.ChatObj:
		return &gogramEntity{id: t.ID, kind: PeerBasicGroup}, nil
	case *telegram.ChatEmpty:
		return &gogramEntity{id: t.ID, kind: PeerBasicGroup}, nil
	case *telegram.ChatForbidden:
		return &gogramEntity{id: t.ID, kind: PeerBasicGroup}, nil
	default:
		return nil, fmt.Errorf("kit: unsupported entity type %T", v)
	}
}

// channelKind classifies a Channel by its flags: megagroup -> Supergroup,
// broadcast -> Channel, neither -> Group (the runtime.py fallback).
func channelKind(megagroup, broadcast bool) PeerKind {
	switch {
	case megagroup:
		return PeerSupergroup
	case broadcast:
		return PeerChannel
	default:
		return PeerGroup
	}
}

// GetEntityType ports get_entity_type. It accepts an Entity or a gogram
// entity pointer and returns the normalized kind; unknown values yield
// "Unknown".
func GetEntityType(v any) string {
	entity, err := WrapEntity(v)
	if err != nil {
		return "Unknown"
	}
	return string(entity.PeerKind())
}

// GetMarkedID ports get_marked_id: -1000000000000 - id for channel-family
// entities (Channel, Supergroup and flagless Group), -id for basic groups,
// and the bare id for users. Non-entities yield 0.
func GetMarkedID(v any) int64 {
	entity, err := WrapEntity(v)
	if err != nil {
		return 0
	}
	return markedID(entity)
}

func markedID(entity Entity) int64 {
	switch entity.PeerKind() {
	case PeerSupergroup, PeerChannel, PeerGroup:
		return -1000000000000 - entity.BareID()
	case PeerBasicGroup:
		return -entity.BareID()
	default:
		return entity.BareID()
	}
}

// EntityFilter is a list_chats-compatible filter type.
type EntityFilter string

const (
	FilterUser    EntityFilter = "user"
	FilterGroup   EntityFilter = "group"
	FilterChannel EntityFilter = "channel"
)

// GetEntityFilterType ports get_entity_filter_type: user/group/channel, or
// "" when no filter applies.
func GetEntityFilterType(v any) EntityFilter {
	entity, err := WrapEntity(v)
	if err != nil {
		return ""
	}
	switch entity.PeerKind() {
	case PeerUser:
		return FilterUser
	case PeerSupergroup, PeerGroup, PeerBasicGroup:
		return FilterGroup
	case PeerChannel:
		return FilterChannel
	default:
		return ""
	}
}

// MarkedIDCandidates ports _marked_id_candidates: the marked chat/channel ID
// variants (-1000000000000-id, -id) for a bare positive integer, and nil for
// zero, negatives and non-integers.
func MarkedIDCandidates(identifier any) []int64 {
	id, ok := barePositiveInt(identifier)
	if !ok {
		return nil
	}
	return []int64{-1000000000000 - id, -id}
}

func barePositiveInt(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		if v > 0 {
			return int64(v), true
		}
	case int64:
		if v > 0 {
			return v, true
		}
	}
	return 0, false
}

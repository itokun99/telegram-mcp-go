package boot

import (
	"context"
	"fmt"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
)

// entitySource implements kit.EntitySource over the account's lazily
// connected gogram client, so kit.Resolver (and every tool module that needs
// reference resolution) resolves identifiers exactly like the Python
// runtime's resolve_entity: lookup, warm-on-miss retry, marked-ID fallback.
type entitySource struct {
	sess *sessionManager
}

// Lookup resolves one identifier and adapts the gogram entity to kit.Entity.
func (s *entitySource) Lookup(identifier any) (kit.Entity, error) {
	raw, err := s.raw(identifier)
	if err != nil {
		return nil, err
	}
	return kit.WrapEntity(raw)
}

// WarmEntities populates the client's entity cache from the dialog list, the
// equivalent of the Python resolver's get_dialogs() warm-up.
func (s *entitySource) WarmEntities() error {
	cl, err := s.sess.client(context.Background())
	if err != nil {
		return err
	}
	_, err = cl.GetDialogs(&telegram.DialogOptions{Limit: 200})
	return err
}

// raw resolves identifier to the concrete gogram entity
// (*telegram.UserObj, *telegram.ChatObj or *telegram.Channel). A peer that
// cannot be resolved is reported as kit.ErrEntityNotFound (so the resolver
// retries with a warm-up); transport and RPC failures other than resolution
// pass through untouched.
func (s *entitySource) raw(identifier any) (any, error) {
	cl, err := s.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	peer, err := cl.ResolvePeer(identifier)
	if err != nil {
		return nil, notFound(cl, err)
	}
	switch p := peer.(type) {
	case *telegram.InputPeerSelf:
		me, err := cl.GetMe()
		return me, notFound(cl, err)
	case *telegram.InputPeerUser:
		user, err := cl.GetUser(p.UserID)
		return user, notFound(cl, err)
	case *telegram.InputPeerUserFromMessage:
		user, err := cl.GetUser(p.UserID)
		return user, notFound(cl, err)
	case *telegram.InputPeerChat:
		chat, err := cl.GetChat(p.ChatID)
		return chat, notFound(cl, err)
	case *telegram.InputPeerChannel:
		channel, err := cl.GetChannel(p.ChannelID)
		return channel, notFound(cl, err)
	case *telegram.InputPeerChannelFromMessage:
		channel, err := cl.GetChannel(p.ChannelID)
		return channel, notFound(cl, err)
	default:
		return nil, fmt.Errorf("%w: could not resolve entity for %v", kit.ErrEntityNotFound, identifier)
	}
}

// peer resolves identifier to the InputPeer RPCs take directly.
func (s *entitySource) peer(identifier any) (telegram.InputPeer, error) {
	cl, err := s.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	peer, err := cl.ResolvePeer(identifier)
	if err != nil {
		return nil, notFound(cl, err)
	}
	return peer, nil
}

// notFound classifies a resolution failure: an RPC rejection (unknown
// username, invalid peer id, an entity Telegram refuses to hand out) becomes
// kit.ErrEntityNotFound, which the resolver retries after warming; anything
// else (a transport error) is returned as-is so it fails loudly instead of
// triggering a retry loop.
func notFound(cl *telegram.Client, err error) error {
	if err == nil {
		return nil
	}
	if cl.ToRpcError(err) != nil {
		return fmt.Errorf("%w: %v", kit.ErrEntityNotFound, err)
	}
	return err
}

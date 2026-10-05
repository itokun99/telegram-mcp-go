package boot

import (
	"context"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/tools/folders"
)

// foldersAdapter implements folders.Client over the account's lazily
// connected gogram client. It embeds the shared entity source, which covers
// the interface's kit.EntitySource half (Lookup / WarmEntities).
type foldersAdapter struct {
	*entitySource
}

var _ folders.Client = (*foldersAdapter)(nil)

func newFoldersAdapter(sess *sessionManager, src *entitySource) folders.Client {
	return &foldersAdapter{entitySource: src}
}

// DialogFilters lists the account's folders (messages.getDialogFilters),
// skipping the system "all chats" default filter like the Python body does.
func (a *foldersAdapter) DialogFilters() ([]folders.DialogFilter, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	resp, err := cl.MessagesGetDialogFilters()
	if err != nil {
		return nil, err
	}
	out := make([]folders.DialogFilter, 0, len(resp.Filters))
	for _, filter := range resp.Filters {
		switch f := filter.(type) {
		case *telegram.DialogFilterObj:
			out = append(out, dialogFilter(f))
		case *telegram.DialogFilterChatlist:
			out = append(out, dialogFilterChatlist(f))
		}
	}
	return out, nil
}

// dialogFilter maps the TL filter onto the folders view.
func dialogFilter(f *telegram.DialogFilterObj) folders.DialogFilter {
	return folders.DialogFilter{
		ID:              f.ID,
		Title:           textWithEntities(f.Title),
		Emoticon:        f.Emoticon,
		Contacts:        f.Contacts,
		NonContacts:     f.NonContacts,
		Groups:          f.Groups,
		Broadcasts:      f.Broadcasts,
		Bots:            f.Bots,
		ExcludeMuted:    f.ExcludeMuted,
		ExcludeRead:     f.ExcludeRead,
		ExcludeArchived: f.ExcludeArchived,
		IncludePeers:    toFoldersPeers(f.IncludePeers),
		ExcludePeers:    toFoldersPeers(f.ExcludePeers),
		PinnedPeers:     toFoldersPeers(f.PinnedPeers),
		TitleNoanimate:  f.TitleNoanimate,
		Color:           f.Color,
	}
}

// dialogFilterChatlist maps a shared (chatlist) folder; the type carries no
// category flags and no exclude list.
func dialogFilterChatlist(f *telegram.DialogFilterChatlist) folders.DialogFilter {
	return folders.DialogFilter{
		ID:             f.ID,
		Title:          textWithEntities(f.Title),
		Emoticon:       f.Emoticon,
		Shared:         true,
		IncludePeers:   toFoldersPeers(f.IncludePeers),
		PinnedPeers:    toFoldersPeers(f.PinnedPeers),
		TitleNoanimate: f.TitleNoanimate,
		Color:          f.Color,
	}
}

// UpdateDialogFilter replaces one folder (messages.updateDialogFilter); a nil
// filter deletes it, which is how the tool passes "delete".
func (a *foldersAdapter) UpdateDialogFilter(id int32, filter *folders.DialogFilter) error {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return err
	}
	if filter == nil {
		_, err := cl.MessagesUpdateDialogFilter(id, nil)
		return err
	}
	_, err = cl.MessagesUpdateDialogFilter(id, toTLDialogFilter(filter))
	return err
}

// toTLDialogFilter builds the update request payload; peer lists carry their
// stored access hashes.
func toTLDialogFilter(filter *folders.DialogFilter) *telegram.DialogFilterObj {
	return &telegram.DialogFilterObj{
		ID:              filter.ID,
		Title:           &telegram.TextWithEntities{Text: filter.Title},
		Emoticon:        filter.Emoticon,
		Contacts:        filter.Contacts,
		NonContacts:     filter.NonContacts,
		Groups:          filter.Groups,
		Broadcasts:      filter.Broadcasts,
		Bots:            filter.Bots,
		ExcludeMuted:    filter.ExcludeMuted,
		ExcludeRead:     filter.ExcludeRead,
		ExcludeArchived: filter.ExcludeArchived,
		IncludePeers:    fromFoldersPeers(filter.IncludePeers),
		ExcludePeers:    fromFoldersPeers(filter.ExcludePeers),
		PinnedPeers:     fromFoldersPeers(filter.PinnedPeers),
		TitleNoanimate:  filter.TitleNoanimate,
		Color:           filter.Color,
	}
}

// UpdateDialogFilterOrder applies a new folder order
// (messages.updateDialogFiltersOrder).
func (a *foldersAdapter) UpdateDialogFilterOrder(order []int32) error {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return err
	}
	_, err = cl.MessagesUpdateDialogFiltersOrder(order)
	return err
}

// ResolvePeer resolves a chat_id into the sendable peer a folder stores.
func (a *foldersAdapter) ResolvePeer(identifier any) (folders.InputPeer, error) {
	peer, err := a.peer(identifier)
	if err != nil {
		return folders.InputPeer{}, err
	}
	return toFoldersPeer(peer)
}

// DescribePeer resolves a stored peer into its display name and entity type;
// an unresolvable peer reports "Unknown" like the Python fallback.
func (a *foldersAdapter) DescribePeer(peer folders.InputPeer) (folders.PeerInfo, error) {
	info := folders.PeerInfo{MarkedID: peer.MarkedID}
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return info, err
	}
	var entity any
	switch peer.Kind {
	case "user":
		entity, err = cl.GetUser(peer.MarkedID)
	case "chat":
		entity, err = cl.GetChat(-peer.MarkedID)
	default:
		entity, err = cl.GetChannel(-1000000000000 - peer.MarkedID)
	}
	if err != nil || entity == nil {
		info.Name = "Unknown"
		info.Type = "Unknown"
		return info, nil
	}
	switch e := entity.(type) {
	case *telegram.UserObj:
		info.Name = userRecord(e).DisplayName()
		info.Username = e.Username
	case *telegram.ChatObj:
		info.Name = e.Title
	case *telegram.Channel:
		info.Name = e.Title
		info.Username = e.Username
	}
	if info.Name == "" {
		info.Name = "Unknown"
	}
	info.Type = kit.GetEntityType(entity)
	return info, nil
}

// FolderLimit reports the account's folder limit together with its premium
// tier (help.getAppConfig's dialog_filters_limit plus the user's premium
// flag); ok is false when either lookup is unavailable.
func (a *foldersAdapter) FolderLimit() (int, bool, bool) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return 0, false, false
	}
	me, err := cl.GetMe()
	if err != nil {
		return 0, false, false
	}
	config, err := cl.HelpGetAppConfig(0)
	if err != nil {
		return 0, me.Premium, false
	}
	obj, ok := config.(*telegram.HelpAppConfigObj)
	if !ok {
		return 0, me.Premium, false
	}
	root, ok := obj.Config.(*telegram.JsonObject)
	if !ok {
		return 0, me.Premium, false
	}
	for _, entry := range root.Value {
		if entry == nil || entry.Key != "dialog_filters_limit" {
			continue
		}
		if number, ok := entry.Value.(*telegram.JsonNumber); ok {
			return int(number.Value), me.Premium, true
		}
	}
	return 0, me.Premium, false
}

// toFoldersPeer converts one TL InputPeer into the folder storage form.
func toFoldersPeer(peer telegram.InputPeer) (folders.InputPeer, error) {
	switch p := peer.(type) {
	case *telegram.InputPeerUser:
		return folders.InputPeer{MarkedID: p.UserID, Kind: "user", AccessHash: p.AccessHash}, nil
	case *telegram.InputPeerChat:
		return folders.InputPeer{MarkedID: -p.ChatID, Kind: "chat"}, nil
	case *telegram.InputPeerChannel:
		return folders.InputPeer{MarkedID: -1000000000000 - p.ChannelID, Kind: "channel", AccessHash: p.AccessHash}, nil
	default:
		return folders.InputPeer{}, errUnsupportedPeer(peer)
	}
}

// fromFoldersPeer rebuilds the TL InputPeer of a stored folder peer.
func fromFoldersPeer(peer folders.InputPeer) telegram.InputPeer {
	switch peer.Kind {
	case "user":
		return &telegram.InputPeerUser{UserID: peer.MarkedID, AccessHash: peer.AccessHash}
	case "chat":
		return &telegram.InputPeerChat{ChatID: -peer.MarkedID}
	default:
		return &telegram.InputPeerChannel{ChannelID: -1000000000000 - peer.MarkedID, AccessHash: peer.AccessHash}
	}
}

func toFoldersPeers(peers []telegram.InputPeer) []folders.InputPeer {
	out := make([]folders.InputPeer, 0, len(peers))
	for _, peer := range peers {
		if converted, err := toFoldersPeer(peer); err == nil {
			out = append(out, converted)
		}
	}
	return out
}

func fromFoldersPeers(peers []folders.InputPeer) []telegram.InputPeer {
	out := make([]telegram.InputPeer, 0, len(peers))
	for _, peer := range peers {
		out = append(out, fromFoldersPeer(peer))
	}
	return out
}

// textWithEntities flattens the TL rich title; the folder tools read the
// plain text only.
func textWithEntities(title *telegram.TextWithEntities) string {
	if title == nil {
		return ""
	}
	return title.Text
}

// errUnsupportedPeer reports a peer class folders cannot store.
func errUnsupportedPeer(peer telegram.InputPeer) error {
	return &unsupportedPeerError{peer: peer}
}

type unsupportedPeerError struct{ peer telegram.InputPeer }

func (e *unsupportedPeerError) Error() string {
	return "folders: unsupported peer type " + kit.GetEntityType(e.peer)
}

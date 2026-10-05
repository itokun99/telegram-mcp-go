package boot

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/tools/contacts"
)

// contactsAdapter implements contacts.Client over the account's lazily
// connected gogram client. The data-shaped interface keeps gogram types out
// of the tool bodies; every method here maps TL objects onto the record
// structs the tools consume.
type contactsAdapter struct {
	sess *sessionManager
}

var _ contacts.Client = (*contactsAdapter)(nil)

func newContactsAdapter(sess *sessionManager) contacts.Client {
	return &contactsAdapter{sess: sess}
}

// GetContacts mirrors get_contacts: the account's saved contacts joined with
// their user objects.
func (a *contactsAdapter) GetContacts() ([]contacts.UserRecord, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	resp, err := cl.ContactsGetContacts(0)
	if err != nil {
		return nil, err
	}
	obj, ok := resp.(*telegram.ContactsContactsObj)
	if !ok {
		return nil, nil
	}
	users := userIndex(obj.Users)
	out := make([]contacts.UserRecord, 0, len(obj.Contacts))
	for _, contact := range obj.Contacts {
		if contact == nil {
			continue
		}
		if user, ok := users[contact.UserID]; ok {
			out = append(out, userRecord(user))
		}
	}
	return out, nil
}

// GetContactIDs mirrors get_contact_ids.
func (a *contactsAdapter) GetContactIDs() ([]int64, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	ids, err := cl.ContactsGetContactIDs(0)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		out = append(out, int64(id))
	}
	return out, nil
}

// SearchContacts mirrors contacts.search, returning the matched users.
func (a *contactsAdapter) SearchContacts(query string, limit int) ([]contacts.UserRecord, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	found, err := cl.ContactsSearch(false, false, query, int32(limit))
	if err != nil {
		return nil, err
	}
	users := userIndex(found.Users)
	out := make([]contacts.UserRecord, 0, len(found.Users))
	for _, user := range users {
		out = append(out, userRecord(user))
	}
	return out, nil
}

// ResolveUser mirrors resolve_entity plus the isinstance(user) check: a peer
// that resolves to a chat or channel reports ErrNotUser, an unresolvable
// identifier reports ErrPeerNotFound.
func (a *contactsAdapter) ResolveUser(identifier any) (contacts.UserRecord, error) {
	raw, err := a.rawEntity(identifier)
	if err != nil {
		return contacts.UserRecord{}, err
	}
	user, ok := raw.(*telegram.UserObj)
	if !ok {
		return contacts.UserRecord{}, fmt.Errorf("%w: %T", contacts.ErrNotUser, raw)
	}
	return userRecord(user), nil
}

// ResolvePeer mirrors resolve_entity for any peer kind, projecting the entity
// through kit.GetMarkedID / kit.GetEntityType like the tools expect.
func (a *contactsAdapter) ResolvePeer(identifier any) (contacts.PeerRecord, error) {
	raw, err := a.rawEntity(identifier)
	if err != nil {
		return contacts.PeerRecord{}, err
	}
	record := contacts.PeerRecord{Kind: kit.PeerKind(kit.GetEntityType(raw))}
	switch entity := raw.(type) {
	case *telegram.UserObj:
		record.ID = entity.ID
		record.Username = entity.Username
		record.Phone = entity.Phone
		record.Name = userRecord(entity).DisplayName()
	case *telegram.ChatObj:
		record.ID = entity.ID
		record.Name = entity.Title
	case *telegram.Channel:
		record.ID = entity.ID
		record.Username = entity.Username
		record.Name = entity.Title
	default:
		return contacts.PeerRecord{}, fmt.Errorf("%w: unsupported entity %T", contacts.ErrPeerNotFound, raw)
	}
	return record, nil
}

// rawEntity resolves through the shared entity source and translates the
// resolver's not-found class into the contacts sentinel.
func (a *contactsAdapter) rawEntity(identifier any) (any, error) {
	src := &entitySource{sess: a.sess}
	raw, err := src.raw(identifier)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", contacts.ErrPeerNotFound, err)
	}
	return raw, nil
}

// AddContactByUsername resolves @username and adds it via contacts.addContact
// (no phone number needed).
func (a *contactsAdapter) AddContactByUsername(username, firstName, lastName string) (bool, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return false, err
	}
	entity, err := cl.ResolveUsername(username)
	if err != nil {
		return false, fmt.Errorf("%w: %v", contacts.ErrPeerNotFound, err)
	}
	user, ok := entity.(*telegram.UserObj)
	if !ok {
		return false, fmt.Errorf("%w: %T", contacts.ErrNotUser, entity)
	}
	updates, err := cl.ContactsAddContact(&telegram.ContactsAddContactParams{
		ID:        &telegram.InputUserObj{UserID: user.ID, AccessHash: user.AccessHash},
		FirstName: firstName,
		LastName:  lastName,
	})
	if err != nil {
		return false, err
	}
	return updates != nil, nil
}

// ImportContact adds one phone-based contact, reporting the imported flag.
func (a *contactsAdapter) ImportContact(contact contacts.PhoneContact) (bool, error) {
	imported, err := a.ImportPhones([]contacts.PhoneContact{contact})
	if err != nil {
		return false, err
	}
	return imported > 0, nil
}

// ImportPhones bulk-imports phone contacts, returning the imported count.
func (a *contactsAdapter) ImportPhones(phoneContacts []contacts.PhoneContact) (int, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return 0, err
	}
	input := make([]*telegram.InputPhoneContact, 0, len(phoneContacts))
	for _, contact := range phoneContacts {
		input = append(input, &telegram.InputPhoneContact{
			ClientID:  rand.Int63(),
			Phone:     contact.Phone,
			FirstName: contact.FirstName,
			LastName:  contact.LastName,
		})
	}
	resp, err := cl.ContactsImportContacts(input)
	if err != nil {
		return 0, err
	}
	return len(resp.Imported), nil
}

// DeleteContact mirrors contacts.deleteContacts.
func (a *contactsAdapter) DeleteContact(user contacts.UserRecord) error {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return err
	}
	_, err = cl.ContactsDeleteContacts([]telegram.InputUser{inputUser(user)})
	return err
}

// BlockUser mirrors contacts.block.
func (a *contactsAdapter) BlockUser(user contacts.UserRecord) error {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return err
	}
	_, err = cl.ContactsBlock(false, inputPeerUser(user))
	return err
}

// UnblockUser mirrors contacts.unblock.
func (a *contactsAdapter) UnblockUser(user contacts.UserRecord) error {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return err
	}
	_, err = cl.ContactsUnblock(false, inputPeerUser(user))
	return err
}

// GetBlocked mirrors contacts.getBlocked, keeping the user entries the
// blocked-users tool reports.
func (a *contactsAdapter) GetBlocked(offset, limit int) ([]contacts.UserRecord, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	resp, err := cl.ContactsGetBlocked(false, int32(offset), int32(limit))
	if err != nil {
		return nil, err
	}
	var blocked []*telegram.PeerBlocked
	var users []telegram.User
	switch obj := resp.(type) {
	case *telegram.ContactsBlockedObj:
		blocked, users = obj.Blocked, obj.Users
	case *telegram.ContactsBlockedSlice:
		blocked, users = obj.Blocked, obj.Users
	default:
		return nil, nil
	}
	index := userIndex(users)
	out := make([]contacts.UserRecord, 0, len(blocked))
	for _, entry := range blocked {
		if entry == nil {
			continue
		}
		peer, ok := entry.PeerID.(*telegram.PeerUser)
		if !ok {
			continue
		}
		if user, ok := index[peer.UserID]; ok {
			out = append(out, userRecord(user))
		}
	}
	return out, nil
}

// GetDialogs mirrors the dialog list filtered to private chats.
func (a *contactsAdapter) GetDialogs() ([]contacts.DialogRecord, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	dialogs, err := cl.GetDialogs(&telegram.DialogOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]contacts.DialogRecord, 0, len(dialogs))
	for _, dialog := range dialogs {
		if !dialog.IsUser() {
			continue
		}
		record := contacts.DialogRecord{UserID: dialog.GetID()}
		if obj, ok := dialog.Dialog.(*telegram.DialogObj); ok {
			record.Unread = obj.UnreadCount
		}
		out = append(out, record)
	}
	return out, nil
}

// GetCommonChats mirrors messages.getCommonChats.
func (a *contactsAdapter) GetCommonChats(user contacts.UserRecord) ([]contacts.ChatRecord, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	common, err := cl.MessagesGetCommonChats(inputUser(user), 0, 100)
	if err != nil {
		return nil, err
	}
	chatsList, ok := common.(*telegram.MessagesChatsObj)
	if !ok {
		return nil, nil
	}
	out := make([]contacts.ChatRecord, 0, len(chatsList.Chats))
	for _, chat := range chatsList.Chats {
		record := contacts.ChatRecord{
			MarkedID: kit.GetMarkedID(chat),
			Type:     kit.GetEntityType(chat),
		}
		switch entity := chat.(type) {
		case *telegram.ChatObj:
			record.Title = entity.Title
		case *telegram.Channel:
			record.Title = entity.Title
		}
		out = append(out, record)
	}
	return out, nil
}

// GetMessages mirrors get_messages(peer, limit) for the last-interaction
// tool: the newest messages of the private chat.
func (a *contactsAdapter) GetMessages(user contacts.UserRecord, limit int) ([]contacts.MessageRecord, error) {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return nil, err
	}
	messages, err := cl.GetHistory(inputPeerUser(user), &telegram.HistoryOption{Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]contacts.MessageRecord, 0, len(messages))
	for _, message := range messages {
		out = append(out, contacts.MessageRecord{
			Date: time.Unix(int64(message.Date()), 0),
			Out:  message.IsOutgoing(),
			Text: message.Text(),
		})
	}
	return out, nil
}

// SendContact mirrors send_contact: the contact card goes out as an
// InputMediaContact message.
func (a *contactsAdapter) SendContact(peer any, contact contacts.OutgoingContact) error {
	cl, err := a.sess.client(context.Background())
	if err != nil {
		return err
	}
	inputPeer, err := cl.ResolvePeer(peer)
	if err != nil {
		return err
	}
	_, err = cl.SendMedia(inputPeer, &telegram.InputMediaContact{
		PhoneNumber: contact.PhoneNumber,
		FirstName:   contact.FirstName,
		LastName:    contact.LastName,
		Vcard:       contact.VCard,
	})
	return err
}

// userIndex maps user IDs to their objects.
func userIndex(users []telegram.User) map[int64]*telegram.UserObj {
	index := make(map[int64]*telegram.UserObj, len(users))
	for _, user := range users {
		if obj, ok := user.(*telegram.UserObj); ok {
			index[obj.ID] = obj
		}
	}
	return index
}

// userRecord projects a gogram user onto the contacts record.
func userRecord(user *telegram.UserObj) contacts.UserRecord {
	return contacts.UserRecord{
		ID:         user.ID,
		AccessHash: user.AccessHash,
		FirstName:  user.FirstName,
		LastName:   user.LastName,
		Username:   user.Username,
		Phone:      user.Phone,
	}
}

// inputUser builds the InputUser of a record (contacts.deleteContacts and the
// common-chats request).
func inputUser(user contacts.UserRecord) telegram.InputUser {
	return &telegram.InputUserObj{UserID: user.ID, AccessHash: user.AccessHash}
}

// inputPeerUser builds the InputPeer of a record.
func inputPeerUser(user contacts.UserRecord) telegram.InputPeer {
	return &telegram.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}
}

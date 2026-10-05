package contacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

type testReporter struct {
	errors   []string
	warnings []string
}

func (r *testReporter) Error(message string)   { r.errors = append(r.errors, message) }
func (r *testReporter) Warning(message string) { r.warnings = append(r.warnings, message) }

func TestMain(m *testing.M) {
	kit.SetDefaultReporter(&testReporter{})
	os.Exit(m.Run())
}

type sentContact struct {
	peer    any
	contact OutgoingContact
}

type fakeClient struct {
	contacts      []UserRecord
	contactsErr   error
	contactIDs    []int64
	searchResult  []UserRecord
	searchLimit   int
	user          UserRecord
	userErr       error
	peer          PeerRecord
	peerErr       error
	addUpdates    bool
	addErr        error
	addCalls      []string
	imported      bool
	importedCalls []PhoneContact
	importCount   int
	dialogs       []DialogRecord
	commonChats   []ChatRecord
	messages      []MessageRecord
	blocked       []UserRecord

	deleted        []UserRecord
	blockedUsers   []UserRecord
	unblockedUsers []UserRecord
	sentContacts   []sentContact
}

func (f *fakeClient) GetContacts() ([]UserRecord, error) {
	if f.contactsErr != nil {
		return nil, f.contactsErr
	}
	return f.contacts, nil
}

func (f *fakeClient) GetContactIDs() ([]int64, error) { return f.contactIDs, nil }

func (f *fakeClient) SearchContacts(query string, limit int) ([]UserRecord, error) {
	f.searchLimit = limit
	return f.searchResult, nil
}

func (f *fakeClient) ResolveUser(identifier any) (UserRecord, error) { return f.user, f.userErr }

func (f *fakeClient) ResolvePeer(identifier any) (PeerRecord, error) { return f.peer, f.peerErr }

func (f *fakeClient) AddContactByUsername(username, firstName, lastName string) (bool, error) {
	f.addCalls = append(f.addCalls, username)
	return f.addUpdates, f.addErr
}

func (f *fakeClient) ImportContact(contact PhoneContact) (bool, error) {
	f.importedCalls = append(f.importedCalls, contact)
	return f.imported, nil
}

func (f *fakeClient) ImportPhones(contacts []PhoneContact) (int, error) {
	f.importedCalls = append(f.importedCalls, contacts...)
	return f.importCount, nil
}

func (f *fakeClient) DeleteContact(user UserRecord) error {
	f.deleted = append(f.deleted, user)
	return nil
}

func (f *fakeClient) BlockUser(user UserRecord) error {
	f.blockedUsers = append(f.blockedUsers, user)
	return nil
}

func (f *fakeClient) UnblockUser(user UserRecord) error {
	f.unblockedUsers = append(f.unblockedUsers, user)
	return nil
}

func (f *fakeClient) GetBlocked(offset, limit int) ([]UserRecord, error) { return f.blocked, nil }

func (f *fakeClient) GetDialogs() ([]DialogRecord, error) { return f.dialogs, nil }

func (f *fakeClient) GetCommonChats(user UserRecord) ([]ChatRecord, error) { return f.commonChats, nil }

func (f *fakeClient) GetMessages(user UserRecord, limit int) ([]MessageRecord, error) {
	return f.messages, nil
}

func (f *fakeClient) SendContact(peer any, contact OutgoingContact) error {
	f.sentContacts = append(f.sentContacts, sentContact{peer: peer, contact: contact})
	return nil
}

type denyAllAllowlist struct{}

func (denyAllAllowlist) AllowsChatID(int64) bool    { return false }
func (denyAllAllowlist) AllowsUsername(string) bool { return false }
func (denyAllAllowlist) Enabled() bool              { return true }

func testContext(t *testing.T, client Client, allowlist kit.ChatAllowlist) context.Context {
	t.Helper()
	store := NewAliasStore(filepath.Join(t.TempDir(), "aliases.json"), "", true)
	deps := &Deps{
		Router:    kit.NewRouter([]string{"default"}),
		Clients:   func(ctx context.Context, account string) (Client, error) { return client, nil },
		Aliases:   store,
		Allowlist: allowlist,
	}
	return WithDeps(context.Background(), deps)
}

func seedAliases(t *testing.T, store *AliasStore, records map[string]aliasRecord) {
	t.Helper()
	normalized := make(map[string]aliasRecord, len(records))
	for key, record := range records {
		normalized[kit.AliasKey(key)] = record
	}
	if err := store.save(normalized); err != nil {
		t.Fatalf("seeding aliases: %v", err)
	}
}

func storeFrom(ctx context.Context) *AliasStore {
	return depsFrom(ctx).Aliases
}

func decodeEnvelope(t *testing.T, text string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, text)
	}
	return payload
}

func resultList(t *testing.T, payload map[string]any) []any {
	t.Helper()
	list, ok := payload["results"].([]any)
	if !ok {
		t.Fatalf("results is not a list: %#v", payload["results"])
	}
	return list
}

func resultMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	m, ok := payload["results"].(map[string]any)
	if !ok {
		t.Fatalf("results is not an object: %#v", payload["results"])
	}
	return m
}

func firstRecord(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	list := resultList(t, payload)
	if len(list) == 0 {
		t.Fatal("results is empty")
	}
	record, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("first record is not an object: %#v", list[0])
	}
	return record
}

type callChecker struct{ t *testing.T }

func check(t *testing.T) callChecker { return callChecker{t} }

func (c callChecker) call(text string, err error) string {
	c.t.Helper()
	if err != nil {
		c.t.Fatalf("handler returned error %v", err)
	}
	return text
}

var inventoryReadOnly = map[string]bool{
	"add_contact":                false,
	"block_user":                 false,
	"delete_contact":             false,
	"delete_contact_alias":       false,
	"export_contacts":            true,
	"get_blocked_users":          true,
	"get_contact_chats":          true,
	"get_contact_ids":            true,
	"get_direct_chat_by_contact": true,
	"get_last_interaction":       true,
	"import_contacts":            false,
	"list_contact_aliases":       true,
	"list_contacts":              true,
	"search_contacts":            true,
	"send_contact":               false,
	"set_contact_alias":          false,
	"unblock_user":               false,
}

func TestAllContactsToolsRegister(t *testing.T) {
	registered := make(map[string]bool)
	for _, name := range mcpserver.RegisteredToolNames() {
		registered[name] = true
	}
	for name := range inventoryReadOnly {
		if !registered[name] {
			t.Errorf("tool %q is not registered", name)
		}
	}
	if len(inventoryReadOnly) != 17 {
		t.Fatalf("expected 17 inventory entries, got %d", len(inventoryReadOnly))
	}
}

func TestDryRunCarriesInventoryAnnotations(t *testing.T) {
	server, err := mcpserver.Build(mcpserver.DefaultRegistry, mcpserver.Options{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var buf bytes.Buffer
	if err := server.DryRun(&buf); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var listed []struct {
		Name         string `json:"name"`
		ReadOnlyHint bool   `json:"readOnlyHint"`
	}
	if err := json.Unmarshal(buf.Bytes(), &listed); err != nil {
		t.Fatalf("dry-run output: %v", err)
	}
	if len(listed) != len(inventoryReadOnly) {
		t.Fatalf("served %d tools, want %d", len(listed), len(inventoryReadOnly))
	}
	for _, tool := range listed {
		want, known := inventoryReadOnly[tool.Name]
		if !known {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		if tool.ReadOnlyHint != want {
			t.Errorf("tool %q readOnlyHint = %v, want %v", tool.Name, tool.ReadOnlyHint, want)
		}
	}
}

func TestListContactsSanitizesNames(t *testing.T) {
	fake := &fakeClient{contacts: []UserRecord{
		{ID: 1, FirstName: "Evil\nName", LastName: "", Username: "evil", Phone: "123"},
		{ID: 2, FirstName: "Good", LastName: "Sort"},
	}}
	ctx := testContext(t, fake, nil)
	text := check(t).call(listContacts(ctx, listContactsInput{}))
	payload := decodeEnvelope(t, text)
	list := resultList(t, payload)
	if len(list) != 2 {
		t.Fatalf("want 2 contacts, got %d", len(list))
	}
	first := list[0].(map[string]any)
	if first["name"] != "Evil Name" {
		t.Errorf("name = %v, want sanitized single line", first["name"])
	}
	if first["username"] != "evil" || first["phone"] != "123" {
		t.Errorf("optional fields missing: %#v", first)
	}
	if _, hasPhone := list[1].(map[string]any)["phone"]; hasPhone {
		t.Error("empty phone should be omitted")
	}
}

func TestListContactsEmpty(t *testing.T) {
	ctx := testContext(t, &fakeClient{}, nil)
	text := check(t).call(listContacts(ctx, listContactsInput{}))
	if text != "No contacts found." {
		t.Errorf("got %q", text)
	}
}

func TestSearchContactsAliasesFirst(t *testing.T) {
	fake := &fakeClient{searchResult: []UserRecord{{ID: 7, FirstName: "Telegram", LastName: "User"}}}
	ctx := testContext(t, fake, nil)
	seedAliases(t, storeFrom(ctx), map[string]aliasRecord{
		"андрей бекендер": {ID: 42, Name: "Андрей"},
	})

	text := check(t).call(searchContacts(ctx, searchContactsInput{Query: "андрей бекендер"}))
	payload := decodeEnvelope(t, text)
	list := resultList(t, payload)
	if len(list) != 2 {
		t.Fatalf("want alias + user, got %d", len(list))
	}
	aliasRecord, _ := list[0].(map[string]any)
	if aliasRecord["alias"] != "андрей бекендер" || aliasRecord["favorite"] != true || aliasRecord["match"] != "exact" {
		t.Errorf("alias record = %#v", aliasRecord)
	}
	if aliasRecord["id"] != float64(42) {
		t.Errorf("alias id = %v, want 42", aliasRecord["id"])
	}
	if fake.searchLimit != searchContactsLimit {
		t.Errorf("search limit = %d, want %d", fake.searchLimit, searchContactsLimit)
	}

	fuzzy := check(t).call(searchContacts(ctx, searchContactsInput{Query: "андрею бекендеру"}))
	fuzzyRecord, _ := resultList(t, decodeEnvelope(t, fuzzy))[0].(map[string]any)
	if fuzzyRecord["match"] != "similar" {
		t.Errorf("fuzzy match kind = %v, want similar", fuzzyRecord["match"])
	}
}

func TestSearchContactsNoResults(t *testing.T) {
	ctx := testContext(t, &fakeClient{}, nil)
	text := check(t).call(searchContacts(ctx, searchContactsInput{Query: "ghost"}))
	if text != "No contacts found matching 'ghost'." {
		t.Errorf("got %q", text)
	}
}

func TestGetContactIDs(t *testing.T) {
	ctx := testContext(t, &fakeClient{contactIDs: []int64{4, 5}}, nil)
	text := check(t).call(getContactIDs(ctx, getContactIDsInput{}))
	if text != "Contact IDs: 4, 5" {
		t.Errorf("got %q", text)
	}
	empty := check(t).call(getContactIDs(testContext(t, &fakeClient{}, nil), getContactIDsInput{}))
	if empty != "No contact IDs found." {
		t.Errorf("got %q", empty)
	}
}

func TestGetDirectChatByContact(t *testing.T) {
	fake := &fakeClient{
		contacts: []UserRecord{{ID: 2, FirstName: "Alice", LastName: "Smith", Username: "alice"}},
		dialogs:  []DialogRecord{{UserID: 2, Unread: 3}},
	}
	ctx := testContext(t, fake, nil)
	text := check(t).call(getDirectChatByContact(ctx, getDirectChatByContactInput{ContactQuery: "ali"}))
	record := firstRecord(t, decodeEnvelope(t, text))
	if record["chat_id"] != float64(2) || record["unread"] != float64(3) {
		t.Errorf("record = %#v", record)
	}
	if record["contact"] != "Alice Smith" || record["username"] != "alice" {
		t.Errorf("record = %#v", record)
	}
}

func TestGetDirectChatByContactNoChat(t *testing.T) {
	fake := &fakeClient{contacts: []UserRecord{{ID: 2, FirstName: "Alice"}}}
	text := check(t).call(getDirectChatByContact(testContext(t, fake, nil), getDirectChatByContactInput{ContactQuery: "alice"}))
	if !strings.Contains(text, "Found contacts: Alice, but no direct chats were found with them.") {
		t.Errorf("got %q", text)
	}
}

func TestGetContactChats(t *testing.T) {
	fake := &fakeClient{
		user:        UserRecord{ID: 2, FirstName: "Alice", LastName: "Smith"},
		dialogs:     []DialogRecord{{UserID: 2, Unread: 4}},
		commonChats: []ChatRecord{{MarkedID: -100123, Title: "Team Chat", Type: "Supergroup"}},
	}
	ctx := testContext(t, fake, nil)
	text := check(t).call(getContactChats(ctx, getContactChatsInput{ContactID: 2}))
	payload := decodeEnvelope(t, text)
	list := resultList(t, payload)
	if len(list) != 2 {
		t.Fatalf("want private + common chat, got %d", len(list))
	}
	private := list[0].(map[string]any)
	if private["type"] != "Private" || private["chat_id"] != float64(2) || private["unread"] != float64(4) {
		t.Errorf("private = %#v", private)
	}
	common := list[1].(map[string]any)
	if common["chat_id"] != float64(-100123) || common["title"] != "Team Chat" || common["type"] != "Supergroup" {
		t.Errorf("common = %#v", common)
	}
	if payload["contact_name"] != "Alice Smith" || payload["contact_id"] != float64(2) {
		t.Errorf("metadata = %#v", payload)
	}
}

func TestGetContactChatsNotAUser(t *testing.T) {
	fake := &fakeClient{userErr: ErrNotUser}
	text := check(t).call(getContactChats(testContext(t, fake, nil), getContactChatsInput{ContactID: 9}))
	if text != "ID 9 is not a user/contact." {
		t.Errorf("got %q", text)
	}
}

func TestGetLastInteraction(t *testing.T) {
	fake := &fakeClient{
		user: UserRecord{ID: 2, FirstName: "Alice"},
		messages: []MessageRecord{
			{Date: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Out: true, Text: "hi"},
			{Date: time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC), Out: false, Text: "bad\u202etext"},
		},
	}
	ctx := testContext(t, fake, nil)
	text := check(t).call(getLastInteraction(ctx, getLastInteractionInput{ContactID: 2}))
	payload := decodeEnvelope(t, text)
	list := resultList(t, payload)
	first := list[0].(map[string]any)
	if first["from"] != "You" {
		t.Errorf("outgoing from = %v", first["from"])
	}
	second := list[1].(map[string]any)
	if second["from"] != "Alice hindi" && second["from"] != "Alice" {
		t.Errorf("incoming from = %v", second["from"])
	}
	if second["text"] != "badtext" {
		t.Errorf("sanitized text = %v", second["text"])
	}
	if payload["contact_name"] != "Alice" {
		t.Errorf("metadata = %#v", payload)
	}
}

func TestGetLastInteractionEmpty(t *testing.T) {
	fake := &fakeClient{user: UserRecord{ID: 2, FirstName: "Alice"}}
	text := check(t).call(getLastInteraction(testContext(t, fake, nil), getLastInteractionInput{ContactID: 2}))
	if !strings.Contains(text, "No messages found with Alice (ID: 2).") {
		t.Errorf("got %q", text)
	}
}

func TestAddContactUsernamePath(t *testing.T) {
	fake := &fakeClient{addUpdates: true}
	ctx := testContext(t, fake, nil)
	text := check(t).call(addContact(ctx, addContactInput{Username: "@bobby", FirstName: "Bob", LastName: "B"}))
	if text != "Contact Bob B (@bobby) added successfully." {
		t.Errorf("got %q", text)
	}
	if len(fake.addCalls) != 1 || fake.addCalls[0] != "bobby" {
		t.Errorf("add calls = %v", fake.addCalls)
	}
}

func TestAddContactErrors(t *testing.T) {
	ctx := testContext(t, &fakeClient{}, nil)
	if text := check(t).call(addContact(ctx, addContactInput{})); text != "Error: Either phone or username must be provided." {
		t.Errorf("got %q", text)
	}
	if text := check(t).call(addContact(ctx, addContactInput{Username: "@"})); text != "Error: Username cannot be empty." {
		t.Errorf("got %q", text)
	}
	fake := &fakeClient{addErr: ErrPeerNotFound}
	text := check(t).call(addContact(testContext(t, fake, nil), addContactInput{Username: "@ghost", FirstName: "G"}))
	if text != "Error: User with username @ghost not found." {
		t.Errorf("got %q", text)
	}
	fake = &fakeClient{addErr: ErrNotUser}
	text = check(t).call(addContact(testContext(t, fake, nil), addContactInput{Username: "@channel"}))
	if text != "Error: Resolved entity is not a user." {
		t.Errorf("got %q", text)
	}
}

func TestAddContactPhonePath(t *testing.T) {
	fake := &fakeClient{imported: true}
	ctx := testContext(t, fake, nil)
	text := check(t).call(addContact(ctx, addContactInput{Phone: "+1555", FirstName: "Bob"}))
	if text != "Contact Bob  added successfully." {
		t.Errorf("got %q", text)
	}
	if len(fake.importedCalls) != 1 || fake.importedCalls[0].Phone != "+1555" {
		t.Errorf("imported = %#v", fake.importedCalls)
	}
}

func TestDeleteContact(t *testing.T) {
	fake := &fakeClient{user: UserRecord{ID: 3, FirstName: "Carl"}}
	text := check(t).call(deleteContact(testContext(t, fake, nil), deleteContactInput{UserID: 3}))
	if text != "Contact with user ID 3 deleted." {
		t.Errorf("got %q", text)
	}
	if len(fake.deleted) != 1 || fake.deleted[0].ID != 3 {
		t.Errorf("deleted = %#v", fake.deleted)
	}
}

func TestBlockAndUnblockUser(t *testing.T) {
	fake := &fakeClient{user: UserRecord{ID: 3}}
	ctx := testContext(t, fake, nil)
	if text := check(t).call(blockUser(ctx, blockUserInput{UserID: 3})); text != "User 3 blocked." {
		t.Errorf("got %q", text)
	}
	if text := check(t).call(unblockUser(ctx, unblockUserInput{UserID: 3})); text != "User 3 unblocked." {
		t.Errorf("got %q", text)
	}
	if len(fake.blockedUsers) != 1 || len(fake.unblockedUsers) != 1 {
		t.Errorf("calls = %v / %v", fake.blockedUsers, fake.unblockedUsers)
	}
}

func TestImportContacts(t *testing.T) {
	fake := &fakeClient{importCount: 2}
	ctx := testContext(t, fake, nil)
	text := check(t).call(importContacts(ctx, importContactsInput{Contacts: []importContactEntry{
		{Phone: "+1", FirstName: "A"},
		{Phone: "+2", FirstName: "B", LastName: "C"},
	}}))
	if text != "Imported 2 contacts." {
		t.Errorf("got %q", text)
	}
	if len(fake.importedCalls) != 2 || fake.importedCalls[1].LastName != "C" {
		t.Errorf("imported = %#v", fake.importedCalls)
	}
}

func TestImportContactsRequiresPhone(t *testing.T) {
	fake := &fakeClient{importCount: 0}
	text := check(t).call(importContacts(testContext(t, fake, nil), importContactsInput{
		Contacts: []importContactEntry{{FirstName: "NoPhone"}},
	}))
	if text != "Error: Each contact must include a phone field." {
		t.Errorf("got %q", text)
	}
}

func TestExportContacts(t *testing.T) {
	fake := &fakeClient{contacts: []UserRecord{{ID: 1, FirstName: "Alice", Username: "alice"}}}
	text := check(t).call(exportContacts(testContext(t, fake, nil), exportContactsInput{}))
	if !strings.Contains(text, `"name": "Alice"`) || !strings.Contains(text, `"username": "alice"`) {
		t.Errorf("got %q", text)
	}
}

func TestGetBlockedUsers(t *testing.T) {
	fake := &fakeClient{blocked: []UserRecord{{ID: 4, FirstName: "Spam"}}}
	text := check(t).call(getBlockedUsers(testContext(t, fake, nil), getBlockedUsersInput{}))
	if !strings.Contains(text, `"name": "Spam"`) || !strings.Contains(text, `"id": 4`) {
		t.Errorf("got %q", text)
	}
}

func TestSendContact(t *testing.T) {
	fake := &fakeClient{}
	ctx := testContext(t, fake, nil)
	text := check(t).call(sendContact(ctx, sendContactInput{
		ChatID: int64(123), PhoneNumber: "+1555", FirstName: "Bob",
	}))
	if text != "Contact sent to chat 123." {
		t.Errorf("got %q", text)
	}
	if len(fake.sentContacts) != 1 || fake.sentContacts[0].contact.PhoneNumber != "+1555" {
		t.Errorf("sent = %#v", fake.sentContacts)
	}
	if fake.sentContacts[0].peer != int64(123) {
		t.Errorf("peer = %#v", fake.sentContacts[0].peer)
	}
}

func TestSendContactAllowlistDenied(t *testing.T) {
	fake := &fakeClient{}
	ctx := testContext(t, fake, denyAllAllowlist{})
	text := check(t).call(sendContact(ctx, sendContactInput{ChatID: int64(123), PhoneNumber: "+1", FirstName: "B"}))
	if !strings.Contains(text, "restricted by privacy policy") {
		t.Errorf("got %q", text)
	}
	if len(fake.sentContacts) != 0 {
		t.Error("send must not happen when the allowlist denies the chat")
	}
}

func TestSetContactAliasSaves(t *testing.T) {
	fake := &fakeClient{peer: PeerRecord{ID: 5, Kind: kit.PeerUser, Name: "Bob", Username: "bob"}}
	ctx := testContext(t, fake, nil)
	text := check(t).call(setContactAlias(ctx, setContactAliasInput{Alias: "бобик", ChatID: "@bobby"}))
	saved := resultMap(t, decodeEnvelope(t, text))
	if saved["saved"] != true || saved["alias"] != "бобик" {
		t.Errorf("payload = %#v", saved)
	}
	store := storeFrom(ctx)
	record, ok := store.exact("бобик")
	if !ok || record.ID != 5 || record.Name != "Bob" || record.Account != "default" {
		t.Errorf("stored record = %#v (ok=%v)", record, ok)
	}
}

func TestSetContactAliasRejectsShadowing(t *testing.T) {
	ctx := testContext(t, &fakeClient{}, nil)
	text := check(t).call(setContactAlias(ctx, setContactAliasInput{Alias: "bobby", ChatID: "@bobby"}))
	payload := resultMap(t, decodeEnvelope(t, text))
	if payload["saved"] != false || payload["reason"] != "alias_shadows_real_identifier" {
		t.Errorf("payload = %#v", payload)
	}
}

func TestSetContactAliasAmbiguousTarget(t *testing.T) {
	ctx := testContext(t, &fakeClient{}, nil)
	seedAliases(t, storeFrom(ctx), map[string]aliasRecord{
		"андрей бекендер": {ID: 42, Name: "Андрей"},
	})
	text := check(t).call(setContactAlias(ctx, setContactAliasInput{Alias: "андрюха", ChatID: "андрей"}))
	payload := resultMap(t, decodeEnvelope(t, text))
	if payload["saved"] != false || payload["reason"] != "ambiguous_target" {
		t.Errorf("payload = %#v", payload)
	}
	candidates, ok := payload["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		t.Errorf("candidates = %#v", payload["candidates"])
	}
}

func TestSetContactAliasReplaceGuard(t *testing.T) {
	fake := &fakeClient{peer: PeerRecord{ID: 5, Kind: kit.PeerUser, Name: "Bob"}}
	ctx := testContext(t, fake, nil)
	seedAliases(t, storeFrom(ctx), map[string]aliasRecord{"бобик": {ID: 5, Name: "Bob"}})

	fake.peer = PeerRecord{ID: 9, Kind: kit.PeerUser, Name: "Other"}
	text := check(t).call(setContactAlias(ctx, setContactAliasInput{Alias: "бобик", ChatID: "@other"}))
	payload := resultMap(t, decodeEnvelope(t, text))
	if payload["saved"] != false || payload["reason"] != "alias_already_used" {
		t.Errorf("payload = %#v", payload)
	}

	text = check(t).call(setContactAlias(ctx, setContactAliasInput{Alias: "бобик", ChatID: "@other", Replace: true}))
	payload = resultMap(t, decodeEnvelope(t, text))
	if payload["saved"] != true {
		t.Errorf("payload = %#v", payload)
	}
	record, _ := storeFrom(ctx).exact("бобик")
	if record.ID != 9 {
		t.Errorf("stored id = %d, want 9", record.ID)
	}
}

func TestSetContactAliasTargetNotFound(t *testing.T) {
	fake := &fakeClient{peerErr: ErrPeerNotFound}
	text := check(t).call(setContactAlias(testContext(t, fake, nil), setContactAliasInput{Alias: "бобик", ChatID: "@bobby"}))
	payload := resultMap(t, decodeEnvelope(t, text))
	if payload["saved"] != false || payload["reason"] != "target_not_found" {
		t.Errorf("payload = %#v", payload)
	}
}

func TestListContactAliases(t *testing.T) {
	ctx := testContext(t, &fakeClient{}, nil)
	store := storeFrom(ctx)
	if text := check(t).call(listContactAliases(ctx, listContactAliasesInput{})); text != "No aliases saved." {
		t.Errorf("got %q", text)
	}
	seedAliases(t, store, map[string]aliasRecord{
		"бобик": {ID: 5, Name: "Bob"},
		"боб":   {ID: 5, Name: "Bob"},
		"алиса": {ID: 9},
	})
	text := check(t).call(listContactAliases(ctx, listContactAliasesInput{}))
	list := resultList(t, decodeEnvelope(t, text))
	if len(list) != 2 {
		t.Fatalf("want 2 rows, got %d: %v", len(list), text)
	}
	first := list[0].(map[string]any)
	if first["id"] != float64(9) || first["name"] != nil {
		t.Errorf("first row = %#v", first)
	}
	second := list[1].(map[string]any)
	aliases, _ := second["aliases"].([]any)
	if second["id"] != float64(5) || len(aliases) != 2 {
		t.Errorf("second row = %#v", second)
	}
}

func TestDeleteContactAlias(t *testing.T) {
	ctx := testContext(t, &fakeClient{}, nil)
	seedAliases(t, storeFrom(ctx), map[string]aliasRecord{"бобик": {ID: 5}})
	text := check(t).call(deleteContactAlias(ctx, deleteContactAliasInput{Alias: "бобик"}))
	if text != "Alias 'бобик' deleted." {
		t.Errorf("got %q", text)
	}
	if _, ok := storeFrom(ctx).exact("бобик"); ok {
		t.Error("alias still present after delete")
	}
	text = check(t).call(deleteContactAlias(ctx, deleteContactAliasInput{Alias: "бобик"}))
	if text != "Alias 'бобик' not found." {
		t.Errorf("got %q", text)
	}
}

func TestGenericErrorReturnsFunnelCode(t *testing.T) {
	fake := &fakeClient{contactsErr: errors.New("boom")}
	text := check(t).call(listContacts(testContext(t, fake, nil), listContactsInput{}))
	if !strings.Contains(text, "(code: CONTACT-ERR-") {
		t.Errorf("got %q", text)
	}
}

func TestFloodWaitReturnsRetryProse(t *testing.T) {
	fake := &fakeClient{contactsErr: &kit.FloodWaitError{Seconds: 30}}
	text := check(t).call(listContacts(testContext(t, fake, nil), listContactsInput{}))
	if !strings.Contains(text, "30 seconds") || !strings.Contains(text, "Do NOT retry immediately") {
		t.Errorf("got %q", text)
	}
}

func TestUnknownFreeTextAsksUser(t *testing.T) {
	ctx := testContext(t, &fakeClient{}, nil)
	text := check(t).call(getContactChats(ctx, getContactChatsInput{ContactID: "некто неизвестный"}))
	payload := resultMap(t, decodeEnvelope(t, text))
	if payload["error"] != "unknown_contact" || payload["nothing_sent"] != true {
		t.Errorf("payload = %#v", payload)
	}
	if _, ok := payload["instruction"].(string); !ok {
		t.Errorf("instruction missing: %#v", payload)
	}
}

func TestStaleAliasAsksUser(t *testing.T) {
	fake := &fakeClient{userErr: ErrPeerNotFound}
	ctx := testContext(t, fake, nil)
	seedAliases(t, storeFrom(ctx), map[string]aliasRecord{"старый друг": {ID: 77, Name: "Old Friend"}})
	text := check(t).call(getLastInteraction(ctx, getLastInteractionInput{ContactID: "старый друг"}))
	payload := resultMap(t, decodeEnvelope(t, text))
	if payload["error"] != "stale_contact" {
		t.Errorf("payload = %#v", payload)
	}
}

func TestAliasResolvesContactReference(t *testing.T) {
	fake := &fakeClient{
		user:     UserRecord{ID: 42, FirstName: "Андрей"},
		messages: []MessageRecord{{Out: false, Text: "привет"}},
	}
	ctx := testContext(t, fake, nil)
	seedAliases(t, storeFrom(ctx), map[string]aliasRecord{"андрей": {ID: 42, Name: "Андрей"}})
	text := check(t).call(getLastInteraction(ctx, getLastInteractionInput{ContactID: "андрей"}))
	payload := decodeEnvelope(t, text)
	if payload["contact_id"] != float64(42) {
		t.Errorf("contact_id = %v, want 42", payload["contact_id"])
	}
}

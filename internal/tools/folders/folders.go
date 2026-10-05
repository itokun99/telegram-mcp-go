// Package folders implements dialog folder MCP tools.
//
// This file ports telegram_mcp/tools/folders.py (7 tools) onto the Go kit.
//
// Tool names, annotations and input fields mirror the Python definitions and
// .omo/go-port/parity/inventory.json; the ctx/account parameters of the
// Python signatures are provided by the process wiring (Deps) instead of the
// input schema. Bodies return formatted error strings (kit.LogAndFormatError,
// the port of runtime.log_and_format_error) and never panic.
package folders

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

// ---------------------------------------------------------------------------
// Process plumbing: the port of with_account / get_client.

// Deps is the process wiring of the folder tools. The boot/session layer
// installs it once before serving; tests replace it through SetDeps.
type Deps struct {
	// Router selects the account of each call and drives the read-only
	// fan-out (the port of runtime.with_account). A nil router behaves as a
	// single unlabeled account.
	Router *kit.Router
	// ClientFor returns the connected client of one account label; the
	// label "" means "the sole account" in single-account mode.
	ClientFor func(label string) (Client, error)
	// Allowlist is the TELEGRAM_ALLOWED_CHAT_IDS privacy gate; nil disables
	// it, like a missing allowlist in Python.
	Allowlist kit.ChatAllowlist
}

// deps is the installed wiring. It is read on every call and must not be
// mutated while tools are serving.
var deps = &Deps{}

// SetDeps installs d as the process wiring of the folder tools and returns
// the previous value, so tests can restore it.
func SetDeps(d *Deps) *Deps {
	previous := deps
	if d == nil {
		d = &Deps{}
	}
	deps = d
	return previous
}

// errNoClient reports a module that was never wired to a Telegram client. It
// is reported through the error funnel, never as a panic.
var errNoClient = errors.New("no Telegram client is connected for the folders tools")

// InputPeer is one chat referenced by a folder, in the form a folder stores
// it: a sendable peer plus the marked ID used for membership comparison
// (Python's utils.get_peer_id).
type InputPeer struct {
	// MarkedID is the marked Telegram ID: the bare ID for a user, the
	// negative ID for a basic group and -1000000000000-id for a channel.
	MarkedID int64
	// Kind is the sendable peer class: "user", "chat" or "channel".
	Kind string
	// AccessHash is the peer's access hash; 0 when the peer needs none.
	AccessHash int64
}

// DialogFilter is one dialog folder, flattening gogram's DialogFilterObj and
// DialogFilterChatlist into the single shape the Python DialogFilter /
// DialogFilterChatlist pair exposed to the tools.
type DialogFilter struct {
	// ID is the folder ID; 0 and 1 are reserved by Telegram for the system
	// folders and 2 is the lowest creatable ID.
	ID int32
	// Title is the folder name.
	Title string
	// Emoticon is the folder emoji; "" when unset.
	Emoticon string
	// Shared marks a folder imported through a chat-folder deep link
	// (Python's DialogFilterChatlist, "type": "shared").
	Shared bool
	// Contacts, NonContacts, Groups, Broadcasts and Bots are the
	// include-everything category flags.
	Contacts    bool
	NonContacts bool
	Groups      bool
	Broadcasts  bool
	Bots        bool
	// ExcludeMuted, ExcludeRead and ExcludeArchived drop matching chats.
	ExcludeMuted    bool
	ExcludeRead     bool
	ExcludeArchived bool
	// IncludePeers, ExcludePeers and PinnedPeers are the folder's explicit
	// peer lists.
	IncludePeers []InputPeer
	ExcludePeers []InputPeer
	PinnedPeers  []InputPeer
	// TitleNoanimate freezes animated emoji in the title; Color is the
	// folder tag color ID. Both are preserved across an update, like the
	// Python bodies do.
	TitleNoanimate bool
	Color          int32
}

// PeerInfo is a folder peer resolved far enough for get_folder to name it.
type PeerInfo struct {
	// MarkedID is the peer's marked ID, reported as the chat "id".
	MarkedID int64
	// Name is the peer's title or first name; "Unknown" when unresolvable.
	Name string
	// Type is the entity type (User, Supergroup, Channel, ...); "Unknown"
	// when unresolvable.
	Type string
	// Username is the @handle without the leading "@"; "" when the peer has
	// none, in which case the key is omitted from the result.
	Username string
}

// Client is the folder-facing slice of a connected Telegram client. The
// session layer implements it over a live gogram client; tests inject a fake,
// so every tool body runs offline.
type Client interface {
	kit.EntitySource

	// DialogFilters returns the account's folders
	// (messages.getDialogFilters). The system "all chats" default folder is
	// not a folder and is left out, matching the Python body's skip of
	// DialogFilterDefault.
	DialogFilters() ([]DialogFilter, error)

	// UpdateDialogFilter replaces one folder (messages.updateDialogFilter);
	// a nil filter deletes it, which is how Python passes filter=None.
	UpdateDialogFilter(id int32, filter *DialogFilter) error

	// UpdateDialogFilterOrder applies a new folder order
	// (messages.updateDialogFiltersOrder).
	UpdateDialogFilterOrder(order []int32) error

	// ResolvePeer resolves a chat_id (marked or bare ID, @username, phone
	// number or "me") into the sendable peer a folder stores.
	ResolvePeer(identifier any) (InputPeer, error)

	// DescribePeer resolves a peer already stored in a folder into its
	// display name and entity type.
	DescribePeer(peer InputPeer) (PeerInfo, error)

	// FolderLimit reports the account's folder limit together with its
	// premium tier (the port of _configured_folder_limit). ok is false when
	// the account tier or Telegram's app config is unavailable, in which
	// case the limit check is skipped exactly as the Python helper's
	// best-effort lookup skips it.
	FolderLimit() (limit int, premium bool, ok bool)
}

// accountRouter returns the configured account router; a nil router behaves
// as a single unlabeled account (kit.Router's single-account mode).
func accountRouter() *kit.Router {
	if deps.Router == nil {
		return kit.NewRouter(nil)
	}
	return deps.Router
}

func clientFor(label string) (Client, error) {
	if deps.ClientFor == nil {
		return nil, errNoClient
	}
	client, err := deps.ClientFor(label)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errNoClient
	}
	return client, nil
}

// runText runs one tool body through the account router and flattens the
// result to the tool's text reply. A failing body becomes the formatted
// funnel string inside its account's result, exactly like the per-account
// try/except of the Python tools, so the multi-account fan-out still tags
// which account failed.
func runText(ctx context.Context, name string, readonly bool, body func(context.Context, Client) (any, error)) string {
	result, err := accountRouter().Route(ctx, "", readonly, func(ctx context.Context, label string) (any, error) {
		client, cerr := clientFor(label)
		if cerr != nil {
			return kit.LogAndFormatError(name, cerr, kit.WithPrefix(kit.CategoryFolder)), nil
		}
		res, berr := body(ctx, client)
		if berr != nil {
			return kit.LogAndFormatError(name, berr, kit.WithPrefix(kit.CategoryFolder)), nil
		}
		return res, nil
	})
	if err != nil {
		return kit.LogAndFormatError(name, err, kit.WithPrefix(kit.CategoryFolder))
	}
	switch value := result.(type) {
	case nil:
		return ""
	case string:
		return value
	default:
		return kit.LogAndFormatError(name,
			fmt.Errorf("folders: unexpected non-text tool result %T", result),
			kit.WithPrefix(kit.CategoryFolder))
	}
}

// resolveChat mirrors @validate_id("chat_id"): the identifier is validated,
// the privacy allowlist is enforced (resolving string identifiers on a miss),
// and the peer is resolved to the sendable peer a folder stores.
func resolveChat(client Client, raw any) (InputPeer, error) {
	peer, err := validatedChatID(client, raw)
	if err != nil {
		return InputPeer{}, err
	}
	return client.ResolvePeer(peer)
}

// validatedChatID runs the validation half of @validate_id("chat_id"):
// range/type validation plus the TELEGRAM_ALLOWED_CHAT_IDS gate.
func validatedChatID(client Client, raw any) (any, error) {
	resolver := kit.NewResolver(client)
	gate := kit.ChatGate{Allowlist: deps.Allowlist, ResolveEntity: resolver.Resolve}
	return gate.Validate("chat_id", normalizeIdentifier(raw))
}

// normalizeIdentifier turns a JSON number into an int64 before validation.
// An MCP input arrives as `any`, so a numeric chat_id decodes to float64,
// which kit.ValidateID does not accept as an ID.
func normalizeIdentifier(value any) any {
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) {
		return value
	}
	return int64(number)
}

// ---------------------------------------------------------------------------
// Shared folder helpers

// firstCreatableFolderID ports the ID allocation of create_folder: IDs 0 and
// 1 belong to the system folders, so the lowest free ID at or above 2 wins.
func firstCreatableFolderID(existing map[int32]bool) int32 {
	id := int32(2)
	for existing[id] {
		id++
	}
	return id
}

// findFolder returns the folder with the requested ID.
func findFolder(filters []DialogFilter, id int32) (DialogFilter, bool) {
	for _, filter := range filters {
		if filter.ID == id {
			return filter, true
		}
	}
	return DialogFilter{}, false
}

// folderNotFound is the message get_folder/add_chat_to_folder/
// remove_chat_from_folder return for an unknown folder ID.
func folderNotFound(id int32) string {
	return fmt.Sprintf("Folder with ID %d not found. Use list_folders to see available folders.", id)
}

// resolveFailed is the message the mutation tools return when a requested
// chat cannot be resolved to a peer.
const resolveFailed = "Failed to resolve a requested chat."

// containsPeer reports whether the peer list already holds markedID.
func containsPeer(peers []InputPeer, markedID int64) bool {
	for _, peer := range peers {
		if peer.MarkedID == markedID {
			return true
		}
	}
	return false
}

// withoutPeer returns the peer list without markedID, preserving order.
func withoutPeer(peers []InputPeer, markedID int64) []InputPeer {
	out := make([]InputPeer, 0, len(peers))
	for _, peer := range peers {
		if peer.MarkedID == markedID {
			continue
		}
		out = append(out, peer)
	}
	return out
}

// appendPeer returns the peer list with peer appended, unless it is already
// present.
func appendPeer(peers []InputPeer, peer InputPeer) []InputPeer {
	if containsPeer(peers, peer.MarkedID) {
		return peers
	}
	return append(peers, peer)
}

// idList normalizes the Union[int, str] / List[Union[int, str]] parameters of
// the Python signatures. Go has no union schema, so these arrive as `any`.
func idList(value any) ([]any, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case []any:
		return typed, true
	case string, int, int32, int64, float64:
		return []any{typed}, true
	default:
		return nil, false
	}
}

// displayID renders a chat identifier the way the Python f-strings did: a
// number as digits, a username as written.
func displayID(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// jsonBlock renders a payload the way Python's json.dumps(indent=2) does:
// two-space indentation and no HTML escaping.
func jsonBlock(value any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// pythonSet renders a set of folder IDs like Python's f-string of a set, so
// reorder_folders reports its missing IDs the way the Python body did.
func pythonSet(ids []int32) string {
	if len(ids) == 0 {
		return "set()"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(int64(id), 10))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// pythonList renders an ID list like Python's f-string of a list.
func pythonList(ids []int32) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(int64(id), 10))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// tooManyFolders is the limit message create_folder returns, mirroring both
// the pre-flight limit check and the server's DIALOG_FILTERS_TOO_MUCH
// rejection.
func tooManyFolders(limitLine string) string {
	return "Cannot create folder: you've reached Telegram's folder limit " + limitLine +
		"Delete a folder first."
}

// ---------------------------------------------------------------------------
// Registration

func init() {
	mcpserver.RegisterTool("list_folders", listFoldersDescription, mcpserver.ToolOptions{
		Title: "List Folders", ReadOnly: true, OpenWorld: true,
	}, handleListFolders)
	mcpserver.RegisterTool("get_folder", getFolderDescription, mcpserver.ToolOptions{
		Title: "Get Folder", ReadOnly: true, OpenWorld: true,
	}, handleGetFolder)
	mcpserver.RegisterTool("create_folder", createFolderDescription, mcpserver.ToolOptions{
		Title: "Create Folder", Destructive: true, OpenWorld: true,
	}, handleCreateFolder)
	mcpserver.RegisterTool("add_chat_to_folder", addChatToFolderDescription, mcpserver.ToolOptions{
		Title: "Add Chat to Folder", Destructive: true, Idempotent: true, OpenWorld: true,
	}, handleAddChatToFolder)
	mcpserver.RegisterTool("remove_chat_from_folder", removeChatFromFolderDescription, mcpserver.ToolOptions{
		Title: "Remove Chat from Folder", Destructive: true, Idempotent: true, OpenWorld: true,
	}, handleRemoveChatFromFolder)
	mcpserver.RegisterTool("delete_folder", deleteFolderDescription, mcpserver.ToolOptions{
		Title: "Delete Folder", Destructive: true, Idempotent: true, OpenWorld: true,
	}, handleDeleteFolder)
	mcpserver.RegisterTool("reorder_folders", reorderFoldersDescription, mcpserver.ToolOptions{
		Title: "Reorder Folders", Destructive: true, Idempotent: true, OpenWorld: true,
	}, handleReorderFolders)
}

// Tool descriptions, ported from the Python docstrings (the Args section
// becomes the JSON schema property descriptions below).

const (
	listFoldersDescription = "Get all dialog folders (filters) with their IDs, names, and emoji.\n" +
		"Returns a list of folders that can be used with other folder tools."
	getFolderDescription            = "Get detailed information about a specific folder including all included chats."
	createFolderDescription         = "Create a new dialog folder."
	addChatToFolderDescription      = "Add a chat to an existing folder."
	removeChatFromFolderDescription = "Remove a chat from a folder."
	deleteFolderDescription         = "Delete a folder. Chats in the folder are preserved, only the folder is removed."
	reorderFoldersDescription       = "Change the order of folders in the folder list."
)

// ---------------------------------------------------------------------------
// list_folders

type listFoldersInput struct{}

func handleListFolders(ctx context.Context, _ listFoldersInput) (string, error) {
	return runText(ctx, "list_folders", true, func(_ context.Context, client Client) (any, error) {
		filters, err := client.DialogFilters()
		if err != nil {
			return nil, err
		}

		folders := make([]map[string]any, 0, len(filters))
		for _, filter := range filters {
			if filter.Shared {
				folders = append(folders, map[string]any{
					"id":                   filter.ID,
					"title":                kit.SanitizeName(filter.Title),
					"emoticon":             optionalString(filter.Emoticon),
					"type":                 "shared",
					"included_peers_count": len(filter.IncludePeers),
					"pinned_peers_count":   len(filter.PinnedPeers),
				})
				continue
			}
			folders = append(folders, map[string]any{
				"id":                   filter.ID,
				"title":                kit.SanitizeName(filter.Title),
				"emoticon":             optionalString(filter.Emoticon),
				"contacts":             filter.Contacts,
				"non_contacts":         filter.NonContacts,
				"groups":               filter.Groups,
				"broadcasts":           filter.Broadcasts,
				"bots":                 filter.Bots,
				"exclude_muted":        filter.ExcludeMuted,
				"exclude_read":         filter.ExcludeRead,
				"exclude_archived":     filter.ExcludeArchived,
				"included_peers_count": len(filter.IncludePeers),
				"excluded_peers_count": len(filter.ExcludePeers),
				"pinned_peers_count":   len(filter.PinnedPeers),
			})
		}

		if len(folders) == 0 {
			return "No folders found. Create one with create_folder tool.", nil
		}
		return jsonBlock(map[string]any{"folders": folders, "count": len(folders)})
	}), nil
}

// optionalString mirrors Python's getattr(f, "emoticon", None): an unset
// emoticon is reported as null, not as an empty string.
func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ---------------------------------------------------------------------------
// get_folder

type getFolderInput struct {
	FolderID int32 `json:"folder_id" jsonschema:"The folder ID (get from list_folders)"`
}

func handleGetFolder(ctx context.Context, in getFolderInput) (string, error) {
	return runText(ctx, "get_folder", true, func(_ context.Context, client Client) (any, error) {
		filters, err := client.DialogFilters()
		if err != nil {
			return nil, err
		}
		folder, ok := findFolder(filters, in.FolderID)
		if !ok {
			return folderNotFound(in.FolderID), nil
		}

		included, err := describePeers(client, folder.IncludePeers)
		if err != nil {
			return nil, err
		}
		excluded, err := describePeers(client, folder.ExcludePeers)
		if err != nil {
			return nil, err
		}
		pinned, err := describePeers(client, folder.PinnedPeers)
		if err != nil {
			return nil, err
		}

		data := map[string]any{
			"id":             folder.ID,
			"title":          kit.SanitizeName(folder.Title),
			"emoticon":       optionalString(folder.Emoticon),
			"included_chats": included,
			"excluded_chats": excluded,
			"pinned_chats":   pinned,
		}
		if folder.Shared {
			data["type"] = "shared"
		} else {
			data["filters"] = map[string]any{
				"contacts":         folder.Contacts,
				"non_contacts":     folder.NonContacts,
				"groups":           folder.Groups,
				"broadcasts":       folder.Broadcasts,
				"bots":             folder.Bots,
				"exclude_muted":    folder.ExcludeMuted,
				"exclude_read":     folder.ExcludeRead,
				"exclude_archived": folder.ExcludeArchived,
			}
		}
		return jsonBlock(data)
	}), nil
}

// describePeers resolves a folder's peer list into chat entries. A peer that
// cannot be resolved degrades to a placeholder instead of failing the whole
// tool, like the Python body's per-peer except.
func describePeers(client Client, peers []InputPeer) ([]map[string]any, error) {
	chats := make([]map[string]any, 0, len(peers))
	for _, peer := range peers {
		info, err := client.DescribePeer(peer)
		if err != nil {
			chats = append(chats, map[string]any{
				"id":   displayID(peer.MarkedID),
				"name": unknownChatName,
				"type": unknownChatType,
			})
			continue
		}
		chat := map[string]any{
			"id":   info.MarkedID,
			"name": kit.SanitizeName(info.Name),
			"type": info.Type,
		}
		if info.Username != "" {
			chat["username"] = info.Username
		}
		chats = append(chats, chat)
	}
	return chats, nil
}

// The Python placeholders for a folder peer that cannot be resolved.
const (
	unknownChatName = "Unknown"
	unknownChatType = "Unknown"
)

// ---------------------------------------------------------------------------
// create_folder

type createFolderInput struct {
	Title        string `json:"title" jsonschema:"Folder name (required)"`
	Emoticon     string `json:"emoticon,omitempty" jsonschema:"Folder emoji (optional, e.g. \"📁\", \"🏠\", \"💼\")"`
	ChatIDs      any    `json:"chat_ids,omitempty" jsonschema:"List of chat IDs or usernames to include (optional)"`
	Contacts     bool   `json:"contacts,omitempty" jsonschema:"Include all contacts"`
	NonContacts  bool   `json:"non_contacts,omitempty" jsonschema:"Include all non-contacts"`
	Groups       bool   `json:"groups,omitempty" jsonschema:"Include all groups"`
	Broadcasts   bool   `json:"broadcasts,omitempty" jsonschema:"Include all channels"`
	Bots         bool   `json:"bots,omitempty" jsonschema:"Include all bots"`
	ExcludeMuted bool   `json:"exclude_muted,omitempty" jsonschema:"Exclude muted chats"`
	ExcludeRead  bool   `json:"exclude_read,omitempty" jsonschema:"Exclude read chats"`
	// ExcludeArchived is a pointer because the Python signature defaults it
	// to true; a plain bool cannot tell "omitted" from an explicit false.
	ExcludeArchived *bool `json:"exclude_archived,omitempty" jsonschema:"Exclude archived chats (default true)"`
}

func handleCreateFolder(ctx context.Context, in createFolderInput) (string, error) {
	return runText(ctx, "create_folder", false, func(_ context.Context, client Client) (any, error) {
		filters, err := client.DialogFilters()
		if err != nil {
			return nil, err
		}
		existing := make(map[int32]bool, len(filters))
		for _, filter := range filters {
			existing[filter.ID] = true
		}

		// The tier lookup is best-effort; a limit the client could not read
		// only means the server rejection below still guards the mutation.
		if limit, premium, ok := client.FolderLimit(); ok && limit > 0 && len(filters) >= limit {
			tier := "regular"
			if premium {
				tier = "Premium"
			}
			return tooManyFolders(fmt.Sprintf("of %d for your %s account (%d folders). ",
				limit, tier, len(filters))), nil
		}

		includePeers := []InputPeer{}
		if requested, ok := idList(in.ChatIDs); ok {
			for _, raw := range requested {
				peer, perr := client.ResolvePeer(normalizeIdentifier(raw))
				if perr != nil {
					return resolveFailed, nil
				}
				includePeers = appendPeer(includePeers, peer)
			}
		}

		newID := firstCreatableFolderID(existing)
		created := &DialogFilter{
			ID:              newID,
			Title:           in.Title,
			Emoticon:        in.Emoticon,
			IncludePeers:    includePeers,
			ExcludePeers:    []InputPeer{},
			PinnedPeers:     []InputPeer{},
			Contacts:        in.Contacts,
			NonContacts:     in.NonContacts,
			Groups:          in.Groups,
			Broadcasts:      in.Broadcasts,
			Bots:            in.Bots,
			ExcludeMuted:    in.ExcludeMuted,
			ExcludeRead:     in.ExcludeRead,
			ExcludeArchived: excludeArchivedDefault(in.ExcludeArchived),
		}
		if err := client.UpdateDialogFilter(newID, created); err != nil {
			// A race, or an app config the account tier lookup missed.
			if strings.Contains(err.Error(), "DIALOG_FILTERS_TOO_MUCH") {
				return tooManyFolders("for your account. "), nil
			}
			return nil, err
		}

		return jsonBlock(map[string]any{
			"success":              true,
			"folder_id":            newID,
			"title":                in.Title,
			"emoticon":             optionalString(in.Emoticon),
			"included_chats_count": len(includePeers),
		})
	}), nil
}

// excludeArchivedDefault ports create_folder's exclude_archived=True default:
// an omitted flag excludes archived chats, an explicit false keeps them.
func excludeArchivedDefault(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

// ---------------------------------------------------------------------------
// add_chat_to_folder

type addChatToFolderInput struct {
	FolderID int32 `json:"folder_id" jsonschema:"The folder ID (get from list_folders)"`
	ChatID   any   `json:"chat_id" jsonschema:"Chat ID or username to add"`
	Pinned   bool  `json:"pinned,omitempty" jsonschema:"Pin the chat in this folder (default false)"`
}

func handleAddChatToFolder(ctx context.Context, in addChatToFolderInput) (string, error) {
	return runText(ctx, "add_chat_to_folder", false, func(_ context.Context, client Client) (any, error) {
		peer, err := resolveChat(client, in.ChatID)
		if err != nil {
			return kit.LogAndFormatError("add_chat_to_folder", err, kit.WithPrefix(kit.CategoryFolder)), nil
		}

		filters, err := client.DialogFilters()
		if err != nil {
			return nil, err
		}
		folder, ok := findFolder(filters, in.FolderID)
		if !ok {
			return folderNotFound(in.FolderID), nil
		}

		alreadyIncluded := containsPeer(folder.IncludePeers, peer.MarkedID)
		alreadyPinned := containsPeer(folder.PinnedPeers, peer.MarkedID)
		if alreadyIncluded && (!in.Pinned || alreadyPinned) {
			return fmt.Sprintf("Chat %s is already in folder %d.", displayID(in.ChatID), in.FolderID), nil
		}

		folder.IncludePeers = appendPeer(folder.IncludePeers, peer)
		if in.Pinned {
			folder.PinnedPeers = appendPeer(folder.PinnedPeers, peer)
		}
		if err := client.UpdateDialogFilter(folder.ID, &folder); err != nil {
			return nil, err
		}

		suffix := ""
		if in.Pinned {
			suffix = " (pinned)"
		}
		return fmt.Sprintf("Chat %s added to folder %d%s.", displayID(in.ChatID), in.FolderID, suffix), nil
	}), nil
}

// ---------------------------------------------------------------------------
// remove_chat_from_folder

type removeChatFromFolderInput struct {
	FolderID int32 `json:"folder_id" jsonschema:"The folder ID (get from list_folders)"`
	ChatID   any   `json:"chat_id" jsonschema:"Chat ID or username to remove"`
}

func handleRemoveChatFromFolder(ctx context.Context, in removeChatFromFolderInput) (string, error) {
	return runText(ctx, "remove_chat_from_folder", false, func(_ context.Context, client Client) (any, error) {
		peer, err := resolveChat(client, in.ChatID)
		if err != nil {
			return kit.LogAndFormatError("remove_chat_from_folder", err, kit.WithPrefix(kit.CategoryFolder)), nil
		}

		filters, err := client.DialogFilters()
		if err != nil {
			return nil, err
		}
		folder, ok := findFolder(filters, in.FolderID)
		if !ok {
			return folderNotFound(in.FolderID), nil
		}

		includeCount := len(folder.IncludePeers)
		pinnedCount := len(folder.PinnedPeers)
		folder.IncludePeers = withoutPeer(folder.IncludePeers, peer.MarkedID)
		folder.PinnedPeers = withoutPeer(folder.PinnedPeers, peer.MarkedID)
		if len(folder.IncludePeers) == includeCount && len(folder.PinnedPeers) == pinnedCount {
			return fmt.Sprintf("Chat %s was not in folder %d.", displayID(in.ChatID), in.FolderID), nil
		}

		if err := client.UpdateDialogFilter(folder.ID, &folder); err != nil {
			return nil, err
		}
		return fmt.Sprintf("Chat %s removed from folder %d.", displayID(in.ChatID), in.FolderID), nil
	}), nil
}

// ---------------------------------------------------------------------------
// delete_folder

type deleteFolderInput struct {
	FolderID int32 `json:"folder_id" jsonschema:"The folder ID to delete (get from list_folders)"`
}

func handleDeleteFolder(ctx context.Context, in deleteFolderInput) (string, error) {
	return runText(ctx, "delete_folder", false, func(_ context.Context, client Client) (any, error) {
		if in.FolderID < 2 {
			return fmt.Sprintf("Cannot delete system folder (ID %d). Only custom folders can be deleted.", in.FolderID), nil
		}

		filters, err := client.DialogFilters()
		if err != nil {
			return nil, err
		}
		folder, ok := findFolder(filters, in.FolderID)
		if !ok {
			return fmt.Sprintf("Folder with ID %d not found (may already be deleted).", in.FolderID), nil
		}

		if err := client.UpdateDialogFilter(in.FolderID, nil); err != nil {
			return nil, err
		}
		return fmt.Sprintf("Folder '%s' (ID %d) deleted. Chats are preserved.",
			kit.SanitizeName(folder.Title), in.FolderID), nil
	}), nil
}

// ---------------------------------------------------------------------------
// reorder_folders

type reorderFoldersInput struct {
	FolderIDs []int32 `json:"folder_ids" jsonschema:"List of folder IDs in the desired order"`
}

func handleReorderFolders(ctx context.Context, in reorderFoldersInput) (string, error) {
	return runText(ctx, "reorder_folders", false, func(_ context.Context, client Client) (any, error) {
		filters, err := client.DialogFilters()
		if err != nil {
			return nil, err
		}
		existing := make(map[int32]bool, len(filters))
		for _, filter := range filters {
			existing[filter.ID] = true
		}

		provided := make(map[int32]bool, len(in.FolderIDs))
		for _, id := range in.FolderIDs {
			if !existing[id] {
				return fmt.Sprintf("Folder ID %d not found. Use list_folders to see available folders.", id), nil
			}
			provided[id] = true
		}

		missing := make([]int32, 0, len(existing))
		for _, filter := range filters {
			if !provided[filter.ID] {
				missing = append(missing, filter.ID)
			}
		}
		if len(missing) > 0 {
			return fmt.Sprintf("All folder IDs must be included. Missing: %s", pythonSet(missing)), nil
		}

		if err := client.UpdateDialogFilterOrder(in.FolderIDs); err != nil {
			return nil, err
		}
		return fmt.Sprintf("Folders reordered: %s", pythonList(in.FolderIDs)), nil
	}), nil
}

package kit

// This file holds the client-facing contracts of the kit. Tools program
// against these interfaces, never against gogram types directly: entity.go
// adapts gogram TL objects to Entity, and the session layer implements
// EntitySource over a live gogram client.

// Entity is a resolved Telegram peer as the tools see it. It hides the
// concrete client object while preserving everything the shared helpers
// need: the bare ID, the coarse peer kind and the optional username.
type Entity interface {
	// BareID is the unmarked Telegram ID (Telethon's entity.id).
	BareID() int64
	// PeerKind classifies the peer for GetEntityType/GetMarkedID.
	PeerKind() PeerKind
	// Username is the @handle without the leading "@"; "" when none.
	Username() string
}

// EntitySource is the small client-facing surface the Resolver needs. The
// gogram-backed implementation lives in the session layer; tests inject
// fakes. An implementation must report a peer it cannot resolve as an error
// satisfying errors.Is(err, ErrEntityNotFound); the resolver retries only
// that class of failure.
type EntitySource interface {
	// Lookup resolves one identifier: a marked/bare integer ID, an
	// @username or handle, a phone number, or "me"/"self".
	Lookup(identifier any) (Entity, error)
	// WarmEntities populates the client's entity cache (a dialog fetch), so
	// identifiers reachable only through the dialog list resolve on retry.
	WarmEntities() error
}

// ChatAllowlist is the parsed TELEGRAM_ALLOWED_CHAT_IDS view the privacy
// gate consults; *config.ChatAllowlist implements it. Tests inject fakes.
type ChatAllowlist interface {
	// AllowsChatID reports whether the (already variant-indexed) ID is
	// allowlisted.
	AllowsChatID(id int64) bool
	// AllowsUsername reports whether the handle (with or without @, any
	// case) is allowlisted.
	AllowsUsername(handle string) bool
	// Enabled reports whether chat access control is active at all.
	Enabled() bool
}

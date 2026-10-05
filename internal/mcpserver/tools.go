package mcpserver

import "context"

// This file holds additive registration helpers for tool packages.

// NewTool builds one tool descriptor bound to a typed handler. It is the same
// constructor RegisterTool uses; RegisterTool simply binds the result into the
// process-wide DefaultRegistry.
//
// Tool packages call NewTool when they need to register into a private
// Registry instead — a test that builds a server from just this module's tools,
// or an embedded server that must not mutate the process-wide surface. The
// returned *Tool is registered with reg.Register.
func NewTool[In any](name, description string, opts ToolOptions, h Handler[In]) *Tool {
	return newTool(name, description, opts, h)
}

// SelfProfile is one account's own profile, as the account tools read it. It
// carries only the fields list_accounts surfaces, so no tool package needs to
// import gogram TL types.
type SelfProfile struct {
	// FirstName is the profile's first name; "" when unset.
	FirstName string
	// LastName is the profile's last name; "" when unset.
	LastName string
	// Phone is the account phone without a leading "+"; "" when hidden.
	Phone string
	// StatusType is the concrete Go type name of the user's online status
	// (for example "UserStatusOnline"), taken from the gogram status object;
	// "" when the account carries no status, mirroring Telethon's None.
	StatusType string
}

// AccountLister enumerates the configured accounts and fetches one account's
// own profile. Label order is the configured discovery order, which is the
// order the account tools list accounts in.
//
// Implementations return an error, never panic, when the label is unknown or
// the profile fetch fails; the account tools degrade a failure to a
// per-account message, mirroring the Python implementation's try/except.
type AccountLister interface {
	// Accounts returns the configured account labels in discovery order.
	Accounts() []string
	// SelfProfile fetches label's own profile.
	SelfProfile(ctx context.Context, label string) (*SelfProfile, error)
}

type accountListerKey struct{}

// WithAccountLister returns a context carrying lister. The boot path installs
// the connected accounts; tests install fakes.
func WithAccountLister(ctx context.Context, lister AccountLister) context.Context {
	return context.WithValue(ctx, accountListerKey{}, lister)
}

// AccountListerFrom returns the lister carried by ctx. ok is false when no
// lister was installed, which tool bodies report as a formatted error rather
// than panicking.
func AccountListerFrom(ctx context.Context) (AccountLister, bool) {
	lister, ok := ctx.Value(accountListerKey{}).(AccountLister)
	return lister, ok
}

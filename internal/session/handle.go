package session

import (
	"fmt"
)

// Auth is the minimum user identity the connect path verifies against
// EXPECTED_USERNAME, without importing the gogram types.
type Auth struct {
	// JustUsername is the primary username (Telegram "username" field).
	JustUsername string
	// ExtraUsernames are additional/linked usernames, when the user's
	// profile carries them.
	ExtraUsernames []string
}

// ClientHandle is a connected gogram client. Implemented by gogramClient;
// tests inject fakes.
type ClientHandle interface {
	// Connect performs the network connect. Adapters report a duplicated auth
	// key by returning *AuthKeyDuplicatedError or by MatchAuthKeyDuplicated
	// returning true for the connect error.
	Connect() error
	// Disconnect tears the connection down.
	Disconnect() error
	// GetAuth fetches the session's user identity; nil means unauthenticated.
	GetAuth() (*Auth, error)
	// MatchAuthKeyDuplicated reports whether err is Telegram's
	// AUTH_KEY_DUPLICATED RPC error. The connect path consults it in addition
	// to the *AuthKeyDuplicatedError type check.
	MatchAuthKeyDuplicated(err error) bool
}

// AuthKeyDuplicatedError means the session's auth key was seen from two
// different IP addresses simultaneously, so Telegram invalidated it and the
// in-flight connection was refused. Transient in practice (a VPN reconnect),
// so the connect path retries it with bounded backoff.
type AuthKeyDuplicatedError struct {
	// Cause is the underlying gogram RPC error, when the adapter wrapped one.
	Cause error
}

// Error implements error.
func (*AuthKeyDuplicatedError) Error() string {
	return "Telegram reports AUTH_KEY_DUPLICATED: this session's auth key was used from two different IP addresses at the same time and was invalidated"
}

// Is implements errors.Is: any *AuthKeyDuplicatedError matches any other.
func (*AuthKeyDuplicatedError) Is(target error) bool {
	_, ok := target.(*AuthKeyDuplicatedError)
	return ok
}

// Unwrap returns the underlying RPC error, if any.
func (e *AuthKeyDuplicatedError) Unwrap() error { return e.Cause }

// UnauthenticatedError means Connect finished without an authorized user.
// Interactive phone login is disabled for a server over stdio.
type UnauthenticatedError struct {
	// Label is the account label that could not be authenticated.
	Label string
}

// Error implements error.
func (e *UnauthenticatedError) Error() string {
	return fmt.Sprintf(
		"Telegram client '%s' is not authorized. Interactive phone login is disabled for the MCP server because it runs over stdio. Generate a session string with session_string_generator, then set TELEGRAM_SESSION_STRING or TELEGRAM_SESSION_STRING_%s; for existing file sessions, run the login outside the MCP server first.",
		e.Label, e.Label,
	)
}

// UserMismatchError means the session is logged into a different account than
// the caller expected (EXPECTED_USERNAME guard).
type UserMismatchError struct {
	// Label is the account label.
	Label string
	// Expected is the normalized username the caller expected.
	Expected string
	// Usernames are the normalized usernames the session actually has.
	Usernames []string
}

// Error implements error.
func (e *UserMismatchError) Error() string {
	return fmt.Sprintf(
		"Telegram client '%s' is logged in to a different account than TELEGRAM_EXPECTED_USERNAME_%s / TELEGRAM_EXPECTED_USERNAME expects: expected %q, session has %v",
		e.Label, e.Label, e.Expected, e.Usernames,
	)
}

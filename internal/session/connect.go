package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// ConnectOptions configures Connect.
type ConnectOptions struct {
	// Label is the account label used in diagnostics and errors.
	Label string
	// Identity is the session's identity string (FileSessionIdentity,
	// StringSessionIdentity or AuthKeySessionIdentity); it names the lock
	// file. Ignored when Held is set.
	Identity string
	// Held is an already-constructed lock to acquire (e.g. a claimed pool
	// slot's lock). When nil, the lock is built from Identity + LockDir.
	Held *SessionLock
	// LockDir overrides the lock directory; "" uses DefaultLockDir.
	LockDir string
	// NewClient constructs the client handle; nil skips the network path and
	// returns a lock-only Connection.
	NewClient func() (ClientHandle, error)
	// ExpectedUsername is the normalized TELEGRAM_EXPECTED_USERNAME[_<LABEL>];
	// empty skips the username check.
	ExpectedUsername string
	// Shared selects shared lock mode (TELEGRAM_SESSION_LOCK=shared).
	Shared bool
	// GraceSeconds bounds lock acquisition; 0 uses DefaultGraceSeconds.
	GraceSeconds float64
	// PollSeconds is the interval between lock attempts; 0 uses DefaultPollInterval.
	PollSeconds float64
	// MaxAuthKeyRetries is the total connect attempts when Telegram reports
	// AUTH_KEY_DUPLICATED; 0 uses 4 (mirroring runner.py's max_attempts).
	MaxAuthKeyRetries int
	// RetryBackoff replaces the default min(2^attempt, 15s) sleep; tests
	// inject a no-sleep clock.
	RetryBackoff func(attempt int) time.Duration
	// Logf receives progress lines; nil logs to stderr.
	Logf func(format string, args ...any)
}

const defaultMaxAuthKeyRetries = 4

// Connection owns a connected client handle plus its session lock. Close()
// disconnects the client and releases the lock. The lock is held for the
// lifetime of the connection; the OS releases it on process exit or crash.
type Connection struct {
	// Label is the account label.
	Label string
	// Lock is the held session lock (non-nil unless constructed directly).
	Lock *SessionLock
	// Client is the connected gogram handle; nil in lock-only mode.
	Client ClientHandle
}

// Connect takes the per-session lock, connects through NewClient with bounded
// AUTH_KEY_DUPLICATED retries, and verifies authorization + expected
// username — the Go form of runner._connect_authorized_client. Any failure
// disconnects the client and releases the lock.
func Connect(ctx context.Context, opts ConnectOptions) (*Connection, error) {
	locked, err := acquireForConnect(ctx, opts)
	if err != nil {
		return nil, err
	}
	if opts.NewClient == nil {
		// Lock-only: the pool path drives its own connect through the claimed
		// slot's lock.
		return &Connection{Label: opts.Label, Lock: locked}, nil
	}
	client, err := opts.NewClient()
	if err != nil {
		locked.Release()
		return nil, fmt.Errorf("constructing telegram client for '%s': %w", opts.Label, err)
	}
	if err := connectWithRetries(ctx, client, opts); err != nil {
		_ = client.Disconnect()
		locked.Release()
		return nil, err
	}
	return &Connection{Label: opts.Label, Lock: locked, Client: client}, nil
}

func acquireForConnect(ctx context.Context, opts ConnectOptions) (*SessionLock, error) {
	lock := opts.Held
	if lock == nil {
		dir := opts.LockDir
		if dir == "" {
			dir = DefaultLockDir()
		}
		var err error
		lock, err = NewSessionLockIn(opts.Label, opts.Identity, dir)
		if err != nil {
			return nil, err
		}
	}
	lock.GraceSeconds = opts.GraceSeconds
	lock.PollSeconds = opts.PollSeconds
	if opts.Shared {
		lock.Mode(LockShared)
	}
	if err := lock.Acquire(ctx); err != nil {
		return nil, err
	}
	return lock, nil
}

// connectWithRetries mirrors runner._connect_authorized_client's retry loop:
// up to max attempts, retrying only *AuthKeyDuplicatedError with min(2^attempt,
// 15s) backoff; any other error propagates immediately.
func connectWithRetries(ctx context.Context, client ClientHandle, opts ConnectOptions) error {
	max := defaultMaxAuthKeyRetries
	if opts.MaxAuthKeyRetries > 0 {
		max = opts.MaxAuthKeyRetries
	}
	for attempt := 1; ; attempt++ {
		err := client.Connect()
		if err == nil {
			break
		}
		duplicated := errors.Is(err, &AuthKeyDuplicatedError{}) || client.MatchAuthKeyDuplicated(err)
		if !duplicated || attempt >= max {
			if duplicated {
				return fmt.Errorf("connecting '%s': all %d attempts hit AuthKeyDuplicatedError (session in use from another IP): %w", opts.Label, attempt, err)
			}
			return fmt.Errorf("connecting '%s': %w", opts.Label, err)
		}
		delay := backoffFor(attempt, opts.RetryBackoff)
		logConnect(opts, "AuthKeyDuplicatedError connecting '%s' (attempt %d/%d): session in use from another IP. Retrying in %s. If this persists, give each concurrent client its own session via TELEGRAM_SESSION_STRINGS or TELEGRAM_SESSION_STRING_%s.", opts.Label, attempt, max, delay, strings.ToUpper(opts.Label))
		if err := sleepCtx(ctx, delay); err != nil {
			return err
		}
	}

	auth, err := client.GetAuth()
	if err != nil {
		return fmt.Errorf("checking authorization for '%s': %w", opts.Label, err)
	}
	if auth == nil {
		return &UnauthenticatedError{Label: opts.Label}
	}
	if err := verifyExpectedUsername(opts, auth); err != nil {
		return err
	}
	return nil
}

// backoffFor is min(2^attempt, 15) seconds — runner.py's delay =
// min(2**attempt, 15).
func backoffFor(attempt int, override func(attempt int) time.Duration) time.Duration {
	if override != nil {
		return override(attempt)
	}
	d := 1 << uint(attempt) // seconds
	if d > 15 {
		d = 15
	}
	return time.Duration(d) * time.Second
}

// verifyExpectedUsername mirrors runner._verify_expected_username: expected
// must be in {primary username} ∪ {extra usernames}, all normalized; a
// mismatch is fatal and Connect disconnects the client on its way out.
func verifyExpectedUsername(opts ConnectOptions, auth *Auth) error {
	if opts.ExpectedUsername == "" {
		return nil
	}
	usernames := map[string]bool{}
	for _, u := range append([]string{auth.JustUsername}, auth.ExtraUsernames...) {
		if u = NormalizeUsername(u); u != "" {
			usernames[u] = true
		}
	}
	if !usernames[opts.ExpectedUsername] {
		sorted := make([]string, 0, len(usernames))
		for u := range usernames {
			sorted = append(sorted, u)
		}
		sort.Strings(sorted)
		return &UserMismatchError{Label: opts.Label, Expected: opts.ExpectedUsername, Usernames: sorted}
	}
	return nil
}

// NormalizeUsername mirrors runner._normalize_username: trim, strip a
// leading @, casefold (lowercase, for the ASCII usernames this uses).
func NormalizeUsername(value string) string {
	return strings.ToLower(strings.TrimLeft(strings.TrimSpace(value), "@"))
}

func logConnect(opts ConnectOptions, format string, args ...any) {
	if opts.Logf != nil {
		opts.Logf(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// Close disconnects the client and releases the session lock.
func (c *Connection) Close() {
	if c == nil {
		return
	}
	if c.Client != nil {
		_ = c.Client.Disconnect()
	}
	if c.Lock != nil {
		c.Lock.Release()
	}
}

// sleepCtx sleeps for d, or returns ctx.Err() if ctx fires first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

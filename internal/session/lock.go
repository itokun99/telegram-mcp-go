// Package session manages Telegram MTProto user sessions and their lifecycle:
// per-session OS locks, session-identity derivation, pool claiming, and the
// lock-protected connect path for the MCP server.
package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Lock directories under os.TempDir(). Kept separate so a pool claim can
// never block a named account's lock (parity with runtime.py).
const (
	// DefaultLockDirName is where per-session locks live.
	DefaultLockDirName = "telegram-mcp-locks"
	// PoolLockDirName is where pool-claim locks live.
	PoolLockDirName = "telegram-mcp-session-locks"
)

// Default lock-acquisition timing (parity with singleton.py).
const (
	// DefaultGraceSeconds is the grace window before a lock acquisition fails.
	DefaultGraceSeconds = 20.0
	// DefaultPollInterval is the poll interval between non-blocking attempts.
	DefaultPollInterval = 0.5
)

// SessionLockError is returned when a session lock is not acquired before the
// grace period elapses.
type SessionLockError struct {
	// LockPath is the lock file held by another process.
	LockPath string
	// HolderPID is the PID recorded by the current exclusive holder, if any.
	HolderPID *int
}

// Error implements error.
func (e *SessionLockError) Error() string {
	where := fmt.Sprintf("lock held: %s", e.LockPath)
	if e.HolderPID != nil {
		where = fmt.Sprintf("lock %s held by PID %d", e.LockPath, *e.HolderPID)
	}
	return fmt.Sprintf(
		"Another telegram-mcp process is already connected with this session (%s). Refusing to connect a second time to avoid Telegram's AuthKeyDuplicatedError. If that other process already exited, this lock will clear on its own -- retry.",
		where,
	)
}

// PoolExhaustedError is returned when every session-pool slot is already
// claimed by another process.
type PoolExhaustedError struct {
	// LockDir is the pool-lock directory whose slots were all taken.
	LockDir string
	// Slots is how many slots were tried.
	Slots int
}

// Error implements error.
func (e *PoolExhaustedError) Error() string {
	return fmt.Sprintf(
		"all %d session-pool slots in %s are already claimed by other processes; refusing to hand out a claimed session (a burned auth key is not recoverable)",
		e.Slots, e.LockDir,
	)
}

// LockMode selects the kind of advisory lock to take.
type LockMode int

// Lock modes.
const (
	// LockExclusive is the default: one process per session.
	LockExclusive LockMode = iota
	// LockShared lets several holders coexist (single-egress-IP host); a
	// shared holder and an exclusive holder never overlap.
	LockShared
)

// SessionLock is an OS-released advisory lock for one Telegram session, keyed
// by the session's identity, not an account label: two labels that map to
// the same session share one lock file. The lock file is held open for the
// lifetime of the process; the OS releases it on exit or crash, so there is
// no stale-lock file to clean up.
type SessionLock struct {
	// Label is the account label, used only in diagnostics.
	Label string
	// GraceSeconds bounds acquisition; 0 (zero value) uses DefaultGraceSeconds.
	GraceSeconds float64
	// PollSeconds is the interval between non-blocking attempts; 0 uses
	// DefaultPollInterval.
	PollSeconds float64

	mode     LockMode
	path     string
	fh       *os.File
	sharedFH *os.File
}

// NewSessionLock builds a lock for identity in the default lock dir
// (os.TempDir()/telegram-mcp-locks, 0700). A mkdir failure is fatal.
func NewSessionLock(label, identity string) (*SessionLock, error) {
	return NewSessionLockIn(label, identity, DefaultLockDir())
}

// NewSessionLockIn builds a lock for identity under dir. Pool claims use
// this so their lock files stay in their own tree.
func NewSessionLockIn(label, identity, dir string) (*SessionLock, error) {
	if dir == "" {
		dir = DefaultLockDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating session lock dir %s: %w", dir, err)
	}
	// Keyed by the session alone: with the label in the name, renaming an
	// account (e.g. "default" -> "work") let an old and a new process both
	// connect the same session, each holding its own lock file.
	return &SessionLock{
		Label: label,
		path:  LockPathFor(dir, identity),
	}, nil
}

// DefaultLockDir returns os.TempDir()/telegram-mcp-locks.
func DefaultLockDir() string {
	return filepath.Join(os.TempDir(), DefaultLockDirName)
}

// PoolLockDir returns os.TempDir()/telegram-mcp-session-locks.
func PoolLockDir() string {
	return filepath.Join(os.TempDir(), PoolLockDirName)
}

// LockPathFor returns the lock file path for identity under dir:
// "session-<first 16 hex of sha256>.lock", matching singleton.py.
func LockPathFor(dir, identity string) string {
	return filepath.Join(dir, "session-"+IdentityHashHex16(identity)+".lock")
}

// Mode selects exclusive (default) or shared.
func (l *SessionLock) Mode(mode LockMode) *SessionLock {
	l.mode = mode
	return l
}

// Path returns the lock file path.
func (l *SessionLock) Path() string { return l.path }

// Holding reports whether this lock object currently holds the lock.
func (l *SessionLock) Holding() bool { return l.fh != nil || l.sharedFH != nil }

// Acquire blocks up to the grace window (polling every poll interval) until
// the lock is free, then takes it. The file is opened O_RDWR|O_CREATE, never
// O_TRUNC: the lock covers byte 0, and truncating a file a live holder owns
// is refused. On failure it returns a *SessionLockError naming the holder
// PID when one is recorded.
func (l *SessionLock) Acquire(ctx context.Context) error {
	if l.Holding() {
		return nil
	}
	shared := l.mode == LockShared
	grace := l.GraceSeconds
	if grace <= 0 {
		grace = DefaultGraceSeconds
	}
	poll := l.PollSeconds
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	deadline := time.Now().Add(time.Duration(grace * float64(time.Second)))

	fh, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening session lock %s: %w", l.path, err)
	}
	for {
		ok, werr := tryLock(fh, shared)
		if ok {
			l.recordHolder(fh, shared)
			if shared {
				l.sharedFH = fh
			} else {
				l.fh = fh
			}
			return nil
		}
		if werr != nil {
			// A platform hard error (no such lock primitive at all):
			// fail immediately rather than poll until the deadline.
			fh.Close()
			return werr
		}
		if time.Now().After(deadline) {
			fh.Close()
			holder := HolderPID(l.path)
			return &SessionLockError{LockPath: l.path, HolderPID: holder}
		}
		select {
		case <-ctx.Done():
			fh.Close()
			return ctx.Err()
		case <-time.After(time.Duration(poll * float64(time.Second))):
		}
	}
}

// TryOnce takes the lock in one non-blocking attempt, with no grace
// polling: it either succeeds and holds the lock, or the lock is contended
// and the method returns (false, nil). A platform hard error is returned
// with (false, err). On any return, if the lock is not held, the file
// handle is closed: the pool claims many slots in a loop, and a lock the
// OS releases only at process exit is held exactly as long as the live
// handle stays open, so a contended slot must not keep its file open.
func (l *SessionLock) TryOnce() (bool, error) {
	if l.Holding() {
		return true, nil
	}
	fh, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("opening session lock %s: %w", l.path, err)
	}
	ok, werr := tryLock(fh, l.mode == LockShared)
	if !ok || werr != nil {
		_ = fh.Close()
		return false, werr
	}
	l.recordHolder(fh, l.mode == LockShared)
	if l.mode == LockShared {
		l.sharedFH = fh
	} else {
		l.fh = fh
	}
	return true, nil
}

// Release clears the holder bytes and unlocks. Safe to call more than once;
// the OS also releases the lock if the process exits first.
func (l *SessionLock) Release() {
	fh := l.fh
	l.fh = nil
	if fh == nil {
		fh = l.sharedFH
		l.sharedFH = nil
	}
	if fh == nil {
		return
	}
	clearHolder(fh)
	unlock(fh)
	fh.Close()
}

// HolderPID reads the PID left in the lock file by the current exclusive
// holder, if any (best effort; shared holders record nothing).
func HolderPID(path string) *int {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return nil
	}
	pid, err := strconv.Atoi(text)
	if err != nil || pid <= 0 {
		return nil
	}
	return &pid
}

// recordHolder writes the holder's PID into the lock file. Exclusive
// holders leave their PID for a refused contender to report; shared holders
// truncate whatever a previous exclusive holder left behind.
func (l *SessionLock) recordHolder(fh *os.File, shared bool) {
	clearHolder(fh)
	if shared {
		return
	}
	pid := strconv.Itoa(os.Getpid())
	if _, err := fh.WriteAt([]byte(pid), 0); err == nil {
		fh.Sync()
	}
}

func clearHolder(fh *os.File) {
	if err := fh.Truncate(0); err != nil {
		return
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		return
	}
}

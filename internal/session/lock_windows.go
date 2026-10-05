//go:build windows

package session

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExExclusive is LockFileEx's dwFlags for a non-blocking exclusive
// lock: LOCKFILE_EXCLUSIVE_LOCK (without it LockFileEx takes a *shared* lock)
// plus LOCKFILE_FAIL_IMMEDIATELY (Python's LK_NBLCK: fail, don't block).
const lockFileExExclusive = windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY

// tryLock mirrors singleton.py's _try_lock on Windows: shared requests are
// satisfied unconditionally (msvcrt byte-range locks have no shared mode),
// exclusive requests take the byte-0 range non-blocking. A lock held by
// another process (ERROR_LOCK_VIOLATION) is a normal poll outcome, not an
// error.
func tryLock(fh *os.File, shared bool) (bool, error) {
	if shared {
		return true, nil
	}
	err := windows.LockFileEx(windows.Handle(fh.Fd()), lockFileExExclusive, 0, 0, 1, nil)
	switch err {
	case nil:
		return true, nil
	case windows.ERROR_LOCK_VIOLATION:
		return false, nil
	default:
		return false, err
	}
}

// unlock mirrors singleton.py's _unlock on Windows: release the whole held
// byte-0 range. Release failures are swallowed (the OS releases the lock
// when the handle closes).
func unlock(fh *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(fh.Fd()), 0, 0, 1, nil)
}

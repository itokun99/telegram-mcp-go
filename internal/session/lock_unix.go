//go:build !windows

package session

import (
	"fmt"
	"os"
	"syscall"
)

// tryLock takes a non-blocking advisory lock on fh's byte 0 with flock:
// LOCK_SH when shared (a shared holder coexists with other shared holders,
// never with an exclusive one), LOCK_EX otherwise.
//
// Returns (true, nil) on success. Contention (EWOULDBLOCK/EAGAIN) is a
// normal polling outcome, not an error: (false, nil). Any other OS failure
// (e.g. the fd is not a regular file) is a hard error the caller must stop
// polling on, so it is returned.
func tryLock(fh *os.File, shared bool) (bool, error) {
	how := syscall.LOCK_EX
	if shared {
		how = syscall.LOCK_SH
	}
	err := syscall.Flock(int(fh.Fd()), how|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	switch err {
	case syscall.EWOULDBLOCK:
		return false, nil
	default:
		return false, fmt.Errorf("flock %s: %w", fh.Name(), err)
	}
}

// unlock releases the advisory lock taken by tryLock. The Python source
// swallows release errors (the OS releases the lock at close/exit anyway),
// so this one does too.
func unlock(fh *os.File) {
	syscall.Flock(int(fh.Fd()), syscall.LOCK_UN)
}

//go:build !windows

package contacts

import (
	"os"
	"syscall"
)

// lockAliasFile takes an exclusive advisory flock; Close releases it.
func lockAliasFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

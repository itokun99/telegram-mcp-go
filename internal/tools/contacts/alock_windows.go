//go:build windows

package contacts

import "os"

// lockAliasFile opens path 0600 without locking: Windows has no flock, and the
// read-modify-write of the alias file is deliberately unlocked there.
func lockAliasFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

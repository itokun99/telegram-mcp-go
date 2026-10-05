//go:build !windows

package paths

import "golang.org/x/sys/unix"

// isReadable reports os.access(path, R_OK) for the readable gate.
func isReadable(path string) bool {
	return unix.Access(path, unix.R_OK) == nil
}

// isWritableDir reports os.access(path, W_OK) for the writable gate.
func isWritableDir(path string) bool {
	return unix.Access(path, unix.W_OK) == nil
}

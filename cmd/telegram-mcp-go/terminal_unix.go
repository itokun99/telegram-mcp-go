//go:build darwin || linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// stdinIsTTY reports whether stdin is a terminal, using the same
// tcgetattr-based probe as Python's os.isatty(): a pipe, a regular file, or
// /dev/null all fail it, even though /dev/null is a character device.
func stdinIsTTY(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), termiosReadRequest)
	return err == nil
}

// readLineNoEcho disables terminal echo around a line read and restores the
// previous termios state afterwards. Non-terminal input falls back to a
// plain read so piped use cannot fail on a failed ioctl.
func readLineNoEcho(f *os.File) (string, error) {
	fd := int(f.Fd())
	termios, err := unix.IoctlGetTermios(fd, termiosReadRequest)
	if err != nil {
		return readLine(f)
	}
	noEcho := *termios
	noEcho.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, termiosWriteRequest, &noEcho); err != nil {
		return readLine(f)
	}
	defer func() { _ = unix.IoctlSetTermios(fd, termiosWriteRequest, termios) }()
	return readLine(f)
}

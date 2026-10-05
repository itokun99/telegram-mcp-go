//go:build darwin

package main

import "golang.org/x/sys/unix"

const (
	termiosReadRequest  = unix.TIOCGETA
	termiosWriteRequest = unix.TIOCSETA
)

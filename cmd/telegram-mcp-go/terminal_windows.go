//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// stdinIsTTY reports whether stdin is a console, the Windows equivalent of
// the unix tcgetattr probe: pipes and files fail GetConsoleMode.
func stdinIsTTY(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// readLineNoEcho disables console echo around a line read and restores the
// previous mode afterwards. Non-console input falls back to a plain read.
func readLineNoEcho(f *os.File) (string, error) {
	handle := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return readLine(f)
	}
	if err := windows.SetConsoleMode(handle, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return readLine(f)
	}
	defer func() { _ = windows.SetConsoleMode(handle, mode) }()
	return readLine(f)
}

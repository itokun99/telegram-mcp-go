//go:build !darwin && !linux && !windows

package main

import "os"

// stdinIsTTY is a best-effort character-device probe on platforms without a
// termios/console API here.
func stdinIsTTY(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func readLineNoEcho(f *os.File) (string, error) {
	return readLine(f)
}

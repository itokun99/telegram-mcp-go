package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// readLine reads one line (without the trailing newline, tolerating CRLF)
// byte by byte, so there is no bufio buffer to lose between prompts.
func readLine(r io.Reader) (string, error) {
	var line strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return strings.TrimRight(line.String(), "\r"), nil
			}
			line.WriteByte(buf[0])
		}
		if err != nil {
			if err == io.EOF && line.Len() > 0 {
				return strings.TrimRight(line.String(), "\r"), nil
			}
			return "", err
		}
	}
}

func promptLine(out io.Writer, in *os.File, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	return readLine(in)
}

// promptSecret prints the prompt, reads a line with terminal echo disabled
// when possible (the getpass() equivalent the Python generator used), and
// restores the cursor to a fresh line.
func promptSecret(out io.Writer, in *os.File, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := readLineNoEcho(in)
	fmt.Fprintln(out)
	if err != nil {
		return "", err
	}
	return line, nil
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itokun99/telegram-mcp-go/internal/config"
)

func pipeStdin(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})
	return r
}

func TestGenSessionRefusesNonTTYStdin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runGenSession(nil, pipeStdin(t), &stdout, &stderr, config.NewInMemory(nil))
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "interactive terminal") {
		t.Errorf("stderr = %q, want the interactive-terminal refusal", stderr.String())
	}
	if strings.Contains(stderr.String(), "TELEGRAM_API_ID") {
		t.Error("the TTY guard must run before credentials are required")
	}
}

func TestGenSessionTTYRequiresCredentials(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runGenSessionWith(nil, pipeStdin(t), &stdout, &stderr, config.NewInMemory(nil), func(*os.File) bool { return true })
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "TELEGRAM_API_ID") {
		t.Errorf("stderr = %q, want the missing TELEGRAM_API_ID error", stderr.String())
	}
}

func TestGenSessionRejectsInvalidLabel(t *testing.T) {
	t.Chdir(t.TempDir())
	src := config.NewInMemory(map[string]string{
		"TELEGRAM_API_ID":   "6",
		"TELEGRAM_API_HASH": "0123456789abcdef0123456789abcdef",
	})
	var stdout, stderr bytes.Buffer
	code := runGenSessionWith([]string{"--label", "bad label!"}, pipeStdin(t), &stdout, &stderr, src, func(*os.File) bool { return true })
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "account label") {
		t.Errorf("stderr = %q, want the label validation error", stderr.String())
	}
}

func TestGenSessionRefusesExistingSessionFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "work.session"), []byte("occupied"), 0o600); err != nil {
		t.Fatalf("seeding existing session file: %v", err)
	}
	src := config.NewInMemory(map[string]string{
		"TELEGRAM_API_ID":   "6",
		"TELEGRAM_API_HASH": "0123456789abcdef0123456789abcdef",
	})
	var stdout, stderr bytes.Buffer
	code := runGenSessionWith([]string{"--label", "work"}, pipeStdin(t), &stdout, &stderr, src, func(*os.File) bool { return true })
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("stderr = %q, want the existing-file refusal", stderr.String())
	}
}

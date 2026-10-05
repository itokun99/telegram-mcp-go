package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRunDryRunServesTheParitySurface is the boot-wiring regression test: the
// default invocation (no subcommand) installs the boot wiring and hands
// --dry-run to mcpserver.Main, which must print the full 132-tool surface
// without touching the network or a real session.
func TestRunDryRunServesTheParitySurface(t *testing.T) {
	t.Setenv("TELEGRAM_API_ID", "12345")
	t.Setenv("TELEGRAM_API_HASH", "dummy-api-hash")
	t.Setenv("TELEGRAM_SESSION_STRING", "1BVtsOK-dummy-session-string")
	t.Setenv("TELEGRAM_EXPOSED_TOOLS", "all")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--dry-run"}, pipeStdin(t), &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr.String())
	}

	var served []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &served); err != nil {
		t.Fatalf("dry-run output is not a JSON array: %v\n%s", err, stdout.String())
	}
	if len(served) != 132 {
		t.Fatalf("served tools = %d, want 132", len(served))
	}

	raw, err := os.ReadFile(filepath.Clean("../../.omo/go-port/parity/inventory.json"))
	if err != nil {
		t.Fatalf("read parity inventory: %v", err)
	}
	var inventory struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatalf("parse parity inventory: %v", err)
	}

	got := make([]string, 0, len(served))
	for _, tool := range served {
		got = append(got, tool.Name)
	}
	want := make([]string, 0, len(inventory.Tools))
	for _, tool := range inventory.Tools {
		want = append(want, tool.Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("served tool names differ from the parity inventory\ngot:  %v\nwant: %v", got, want)
	}
}

func TestRunNoArgsWithoutCredentialsFailsLoudly(t *testing.T) {
	t.Setenv("TELEGRAM_API_ID", "")
	var stdout, stderr bytes.Buffer
	if code := run(nil, pipeStdin(t), &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "TELEGRAM_API_ID") {
		t.Errorf("stderr = %q, want the missing-credentials message", stderr.String())
	}
}

func TestRunHelpListsSubcommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, pipeStdin(t), &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"gen-session", "migrate-session"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("usage output is missing %q: %q", want, stdout.String())
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"frobnicate"}, pipeStdin(t), &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr = %q, want the unknown-command error", stderr.String())
	}
}

package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarnathcjd/gogram/telegram"
	"github.com/itokun99/telegram-mcp-go/internal/config"
)

// telethonSessionString builds the Telethon v1 string for dc 2,
// 149.154.167.51, port 443, auth_key bytes 0..255.
func telethonSessionString() string {
	payload := []byte{2, 149, 154, 167, 51, 0x01, 0xBB}
	for i := 0; i < 256; i++ {
		payload = append(payload, byte(i))
	}
	return "1" + base64.RawURLEncoding.EncodeToString(payload)
}

func migrateEnv(extra map[string]string) config.EnvSource {
	env := map[string]string{
		"TELEGRAM_API_ID":   "6",
		"TELEGRAM_API_HASH": "0123456789abcdef0123456789abcdef",
	}
	for k, v := range extra {
		env[k] = v
	}
	return config.NewInMemory(env)
}

func TestMigrateSessionWritesSessionFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "acct")
	src := migrateEnv(map[string]string{"TELEGRAM_SESSION_STRING": telethonSessionString()})

	var stdout, stderr bytes.Buffer
	if code := runMigrateSession([]string{"--target", target}, &stdout, &stderr, src); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "TELEGRAM_SESSION_NAME="+target) {
		t.Errorf("stdout = %q, want the TELEGRAM_SESSION_NAME instruction", stdout.String())
	}

	path := target + ".session"
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session file was not written: %v", err)
	}
	client, err := telegram.NewClient(telegram.ClientConfig{
		AppID:        6,
		AppHash:      "0123456789abcdef0123456789abcdef",
		Session:      path,
		NoPreconnect: true,
		NoUpdates:    true,
		DisableCache: true,
		LogLevel:     telegram.LogError,
	})
	if err != nil {
		t.Fatalf("reloading written session: %v", err)
	}
	defer func() { _ = client.Disconnect() }()
	raw := client.ExportRawSession()
	if len(raw.Key) != 256 || raw.Key[0] != 0 || raw.Key[255] != 255 {
		t.Errorf("reloaded key does not match the fixture bytes (len=%d)", len(raw.Key))
	}
	if raw.Hostname != "149.154.167.51" {
		t.Errorf("reloaded Hostname = %q, want 149.154.167.51", raw.Hostname)
	}
	if raw.AppID != 6 {
		t.Errorf("reloaded AppID = %d, want 6", raw.AppID)
	}
}

func TestMigrateSessionLabelledAccount(t *testing.T) {
	target := filepath.Join(t.TempDir(), "acct")
	src := migrateEnv(map[string]string{
		"TELEGRAM_SESSION_STRING_WORK_2": telethonSessionString(),
		"TELEGRAM_SESSION_NAME_WORK_2":   target,
	})

	var stdout, stderr bytes.Buffer
	if code := runMigrateSession([]string{"--account", "work-2"}, &stdout, &stderr, src); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if _, err := os.Stat(target + ".session"); err != nil {
		t.Fatalf("session file was not written: %v", err)
	}
	if !strings.Contains(stdout.String(), "TELEGRAM_SESSION_NAME_WORK_2="+target) {
		t.Errorf("stdout = %q, want the TELEGRAM_SESSION_NAME_WORK_2 instruction", stdout.String())
	}
}

func TestMigrateSessionRefusesExistingTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "acct")
	if err := os.WriteFile(target+".session", []byte("occupied"), 0o600); err != nil {
		t.Fatalf("seeding existing target: %v", err)
	}
	src := migrateEnv(map[string]string{"TELEGRAM_SESSION_STRING": telethonSessionString()})

	var stdout, stderr bytes.Buffer
	if code := runMigrateSession([]string{"--target", target}, &stdout, &stderr, src); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("stderr = %q, want the existing-target refusal", stderr.String())
	}
}

func TestMigrateSessionRequiresSessionString(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMigrateSession(nil, &stdout, &stderr, migrateEnv(nil)); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "TELEGRAM_SESSION_STRING must be set") {
		t.Errorf("stderr = %q, want the missing-session-string error", stderr.String())
	}
}

func TestMigrateSessionRejectsNonTelethonString(t *testing.T) {
	gogramString := (&telegram.Session{Key: make([]byte, 256), Hostname: "149.154.167.51", AppID: 6}).Encode()
	src := migrateEnv(map[string]string{"TELEGRAM_SESSION_STRING": gogramString})

	var stdout, stderr bytes.Buffer
	if code := runMigrateSession(nil, &stdout, &stderr, src); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not a Telethon StringSession") {
		t.Errorf("stderr = %q, want the format rejection", stderr.String())
	}
}

func TestMigrateSessionRejectsInvalidAccountLabel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMigrateSession([]string{"--account", "work!"}, &stdout, &stderr, migrateEnv(nil)); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "account label") {
		t.Errorf("stderr = %q, want the label validation error", stderr.String())
	}
}

func TestMigrateEnvNames(t *testing.T) {
	cases := []struct {
		account       string
		stringEnv     string
		nameEnv       string
		defaultTarget string
		wantErr       bool
	}{
		{"", "TELEGRAM_SESSION_STRING", "TELEGRAM_SESSION_NAME", "telegram_mcp_session", false},
		{"work", "TELEGRAM_SESSION_STRING_WORK", "TELEGRAM_SESSION_NAME_WORK", "telegram_mcp_session_work", false},
		{"work-2", "TELEGRAM_SESSION_STRING_WORK_2", "TELEGRAM_SESSION_NAME_WORK_2", "telegram_mcp_session_work_2", false},
		{"work!", "", "", "", true},
		{"bad label", "", "", "", true},
	}
	for _, tc := range cases {
		stringEnv, nameEnv, defaultTarget, err := migrateEnvNames(tc.account)
		if tc.wantErr {
			if err == nil {
				t.Errorf("migrateEnvNames(%q) succeeded, want error", tc.account)
			}
			continue
		}
		if err != nil {
			t.Errorf("migrateEnvNames(%q): %v", tc.account, err)
			continue
		}
		if stringEnv != tc.stringEnv || nameEnv != tc.nameEnv || defaultTarget != tc.defaultTarget {
			t.Errorf("migrateEnvNames(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.account, stringEnv, nameEnv, defaultTarget, tc.stringEnv, tc.nameEnv, tc.defaultTarget)
		}
	}
}

func TestSessionTargetPath(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "acct")
	for _, target := range []string{base, base + ".session"} {
		got, err := sessionTargetPath(target)
		if err != nil {
			t.Fatalf("sessionTargetPath(%q): %v", target, err)
		}
		if got != base+".session" {
			t.Errorf("sessionTargetPath(%q) = %q, want %q", target, got, base+".session")
		}
	}
}

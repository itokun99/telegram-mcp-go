package mcpserver

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/itokun99/telegram-mcp-go/internal/config"
)

func runMain(t *testing.T, env map[string]string, reg *Registry, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := mainWith(config.NewInMemory(env), reg, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestDryRunContainsRegisteredFakeTool(t *testing.T) {
	code, stdout, stderr := runMain(t, validEnv(), registryWithFakes(), "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	var tools []dryRunTool
	if err := json.Unmarshal([]byte(stdout), &tools); err != nil {
		t.Fatalf("dry-run stdout is not JSON: %v\n%s", err, stdout)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if want := []string{"read_tool", "write_tool"}; !slices.Equal(names, want) {
		t.Fatalf("dry-run tools = %v, want %v (sorted, full surface)", names, want)
	}
	for _, tool := range tools {
		switch tool.Name {
		case "read_tool":
			if !tool.ReadOnlyHint {
				t.Fatalf("read_tool entry = %+v, want readOnlyHint", tool)
			}
		case "write_tool":
			if !tool.DestructiveHint || tool.ReadOnlyHint {
				t.Fatalf("write_tool entry = %+v, want destructive non-read-only", tool)
			}
		}
	}
}

func TestDryRunAppliesReadOnlyPruning(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "read-only"
	code, stdout, stderr := runMain(t, env, registryWithFakes(), "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if strings.Contains(stdout, "write_tool") {
		t.Fatalf("read-only dry-run leaked write_tool: %s", stdout)
	}
	if !strings.Contains(stdout, "read_tool") {
		t.Fatalf("read-only dry-run missing read_tool: %s", stdout)
	}
}

func TestDryRunUnknownAllowlistAbortsStartup(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_EXPOSED_TOOLS"] = "read-only+send_mesage"
	code, stdout, stderr := runMain(t, env, registryWithFakes(), "--dry-run")
	if code == 0 {
		t.Fatalf("unknown allowlist must abort before printing tools; stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "send_mesage") {
		t.Fatalf("stderr must name the unknown tool: %s", stderr)
	}
	if stdout != "" {
		t.Fatalf("aborted dry-run must print no tool list: %s", stdout)
	}
}

func TestMainFailsLoudWithoutCredentials(t *testing.T) {
	code, stdout, stderr := runMain(t, map[string]string{}, registryWithFakes(), "--dry-run")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stderr, "TELEGRAM_API_ID") {
		t.Fatalf("stderr must name the missing credential: %s", stderr)
	}
	if stdout != "" {
		t.Fatalf("failed startup must print no tool list: %s", stdout)
	}
}

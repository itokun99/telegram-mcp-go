package mcpserver_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"

	_ "github.com/itokun99/telegram-mcp-go/internal/tools/accounts"
	_ "github.com/itokun99/telegram-mcp-go/internal/tools/chats"
	_ "github.com/itokun99/telegram-mcp-go/internal/tools/contacts"
	_ "github.com/itokun99/telegram-mcp-go/internal/tools/events"
	_ "github.com/itokun99/telegram-mcp-go/internal/tools/folders"
	_ "github.com/itokun99/telegram-mcp-go/internal/tools/groups"
	_ "github.com/itokun99/telegram-mcp-go/internal/tools/media"
	_ "github.com/itokun99/telegram-mcp-go/internal/tools/messages"
	_ "github.com/itokun99/telegram-mcp-go/internal/tools/profile"
)

const inventoryPath = "../../.omo/go-port/parity/inventory.json"

type inventoryEntry struct {
	Name string `json:"name"`
}

type inventory struct {
	Total int              `json:"total"`
	Tools []inventoryEntry `json:"tools"`
}

// TestToolSurface diffs every registered tool name against the parity
// inventory. It lives in the external test package because the tool packages
// import mcpserver, so only an out-of-package test may blank-import them.
func TestToolSurface(t *testing.T) {
	registered := mcpserver.RegisteredToolNames()
	if len(registered) == 0 {
		t.Fatal("registered tool surface is empty: the tool packages were not blank-imported")
	}

	seen := map[string]bool{}
	for _, n := range registered {
		if seen[n] {
			t.Errorf("duplicate registration: %s", n)
		}
		seen[n] = true
	}

	raw, err := os.ReadFile(filepath.Clean(inventoryPath))
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var inv inventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatalf("parse inventory: %v", err)
	}

	want := make([]string, 0, len(inv.Tools))
	for _, e := range inv.Tools {
		want = append(want, e.Name)
	}
	sort.Strings(want)

	got := append([]string(nil), registered...)
	sort.Strings(got)

	var missing, extra []string
	inInv := map[string]bool{}
	for _, n := range want {
		inInv[n] = true
	}
	inReg := map[string]bool{}
	for _, n := range got {
		inReg[n] = true
	}
	for _, n := range want {
		if !inReg[n] {
			missing = append(missing, n)
		}
	}
	for _, n := range got {
		if !inInv[n] {
			extra = append(extra, n)
		}
	}

	if len(missing) > 0 {
		t.Errorf("missing tools (%d): %s", len(missing), strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		t.Errorf("tools not in inventory (%d): %s", len(extra), strings.Join(extra, ", "))
	}
	if len(missing) == 0 && len(extra) == 0 {
		t.Logf("tool surface parity OK: %d/%d registered names match the inventory", len(got), len(want))
	}
}

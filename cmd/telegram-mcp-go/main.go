// Command telegram-mcp-go hosts the Go port's command-line entry points:
// the gen-session and migrate-session session utilities, alongside the MCP
// server served by internal/mcpserver.Main.
package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/itokun99/telegram-mcp-go/internal/boot"
	"github.com/itokun99/telegram-mcp-go/internal/config"
	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

const usage = `telegram-mcp-go - Telegram MCP server

Usage:
  telegram-mcp-go [flags]      Serve the MCP server (stdio by default; --dry-run prints the tool list)
  telegram-mcp-go [command]

Commands:
  gen-session      Log in to Telegram (interactive terminal only) and write <label>.session
  migrate-session  Convert a Telethon StringSession into a gogram .session file

Run 'telegram-mcp-go <command> -h' for the flags of each command.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// loadCredentials reads TELEGRAM_API_ID / TELEGRAM_API_HASH, mirroring the
// two Python scripts' direct os.getenv reads. The server's config.Load is
// intentionally not used here: it refuses to load when no session is
// configured, and gen-session's entire job is to create the first one.
func loadCredentials(src config.EnvSource) (int32, string, error) {
	rawID, _ := src.Get("TELEGRAM_API_ID")
	rawID = strings.TrimSpace(rawID)
	hash, _ := src.Get("TELEGRAM_API_HASH")
	hash = strings.TrimSpace(hash)
	if rawID == "" || hash == "" {
		return 0, "", errors.New("TELEGRAM_API_ID and TELEGRAM_API_HASH must be set. Create an application at https://my.telegram.org/apps and export both (or put them in .env and source it)")
	}
	apiID, err := strconv.Atoi(rawID)
	if err != nil {
		return 0, "", fmt.Errorf("TELEGRAM_API_ID must be an integer (got %q)", rawID)
	}
	if apiID <= 0 || apiID > math.MaxInt32 {
		return 0, "", fmt.Errorf("TELEGRAM_API_ID must be a positive integer (got %q)", rawID)
	}
	return int32(apiID), hash, nil
}

func run(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "gen-session":
			return runGenSession(args[1:], stdin, stdout, stderr, config.OS{})
		case "migrate-session":
			return runMigrateSession(args[1:], stdout, stderr, config.OS{})
		case "help", "-h", "--help":
			fmt.Fprint(stdout, usage)
			return 0
		}
		if !strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
			return 2
		}
	}
	// The default invocation (no subcommand) is the MCP server: install the
	// boot wiring, then hand the flags (--dry-run) to mcpserver.Main.
	if err := boot.Install(config.OS{}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return mcpserver.Main(args, stdout, stderr)
}

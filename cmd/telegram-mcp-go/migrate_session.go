package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/itokun99/telegram-mcp-go/internal/config"
	"github.com/itokun99/telegram-mcp-go/internal/session"
)

// runMigrateSession is the migrate-session subcommand: the Go replacement
// for migrate_session.py. It reads a Telethon StringSession from the
// environment, converts it to gogram's session encoding, and writes
// <target>.session. The conversion is offline; unlike the Python original
// it does not connect to Telegram, so it also works on a host that cannot
// reach Telegram directly.
func runMigrateSession(args []string, stdout, stderr io.Writer, src config.EnvSource) int {
	fs := flag.NewFlagSet("migrate-session", flag.ContinueOnError)
	fs.SetOutput(stderr)
	account := fs.String("account", "", "migrate TELEGRAM_SESSION_STRING_<LABEL> instead of the default account (for example: --account work)")
	target := fs.String("target", "", "destination session path without the .session suffix; defaults to TELEGRAM_SESSION_NAME[_<LABEL>] or telegram_mcp_session[_<label>]")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	stringEnv, nameEnv, defaultTarget, err := migrateEnvNames(*account)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	apiID, apiHash, err := loadCredentials(src)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	sessionString, _ := src.Get(stringEnv)
	if strings.TrimSpace(sessionString) == "" {
		fmt.Fprintf(stderr, "Error: %s must be set\n", stringEnv)
		return 1
	}

	targetName := strings.TrimSpace(*target)
	if targetName == "" {
		if fromEnv, ok := src.Get(nameEnv); ok && strings.TrimSpace(fromEnv) != "" {
			targetName = strings.TrimSpace(fromEnv)
		} else {
			targetName = defaultTarget
		}
	}
	targetPath, err := sessionTargetPath(targetName)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	if _, err := os.Stat(targetPath); err == nil {
		fmt.Fprintf(stderr, "Migration failed: destination already exists: %s. Choose another --target.\n", targetPath)
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "Migration failed: %v\n", err)
		return 1
	}

	parsed, err := session.ParseTelethonString(sessionString)
	if err != nil {
		fmt.Fprintf(stderr, "Migration failed: %v\n", err)
		return 1
	}
	if err := session.WriteGogramSessionFile(targetPath, parsed, apiID, apiHash); err != nil {
		fmt.Fprintf(stderr, "Migration failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Migration successful: %s -> %s\n", stringEnv, targetPath)
	fmt.Fprintln(stdout, "\nUpdate your environment:")
	fmt.Fprintf(stdout, "  %s=%s\n", nameEnv, strings.TrimSuffix(targetPath, ".session"))
	fmt.Fprintf(stdout, "  remove or comment out %s\n", stringEnv)
	fmt.Fprintln(stdout, "\nKeep the .session file private and do not commit it.")
	return 0
}

// migrateEnvNames mirrors migrate_session.py's _env_names(): plain names for
// the default account, _<SUFFIX> for a labelled one. '-' becomes '_' in the
// uppercase env suffix and in the default file label, again as in Python.
func migrateEnvNames(account string) (stringEnv, nameEnv, defaultTarget string, err error) {
	label := strings.TrimSpace(account)
	if label == "" {
		return "TELEGRAM_SESSION_STRING", "TELEGRAM_SESSION_NAME", "telegram_mcp_session", nil
	}
	if !validSessionLabel(label) {
		return "", "", "", errors.New("account label may contain only letters, numbers, '-' and '_'")
	}
	suffix := strings.ToUpper(strings.ReplaceAll(label, "-", "_"))
	fileLabel := strings.ToLower(strings.ReplaceAll(label, "-", "_"))
	return "TELEGRAM_SESSION_STRING_" + suffix, "TELEGRAM_SESSION_NAME_" + suffix, "telegram_mcp_session_" + fileLabel, nil
}

// validSessionLabel mirrors the Python guard
// label.replace("-", "").replace("_", "").isalnum(): ASCII letters, digits,
// '-' and '_' only. The label becomes part of a file name, so the set stays
// deliberately narrow.
func validSessionLabel(label string) bool {
	if label == "" {
		return false
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// sessionTargetPath mirrors migrate_session.py's _target_paths(): expand '~',
// strip one .session suffix, resolve against the working directory, and
// append .session ourselves (gogram takes the path verbatim and would
// otherwise default to session.dat).
func sessionTargetPath(target string) (string, error) {
	expanded := strings.TrimSuffix(expandUserPath(target), ".session")
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolving target %q: %w", target, err)
	}
	return abs + ".session", nil
}

func expandUserPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

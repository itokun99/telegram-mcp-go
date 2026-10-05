package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/amarnathcjd/gogram/telegram"
	"github.com/itokun99/telegram-mcp-go/internal/config"
	"github.com/itokun99/telegram-mcp-go/internal/session"
)

// runGenSession is the gen-session subcommand: the Go replacement for
// session_string_generator.py. It logs in over the phone-code flow and
// writes a gogram file session named <label>.session.
//
// Interactive auth stays a local-CLI-only path, exactly as in Python: the
// MCP server must never read phone/code prompts off its protocol stream, so
// this command refuses to run unless stdin is a terminal.
func runGenSession(args []string, stdin *os.File, stdout, stderr io.Writer, src config.EnvSource) int {
	return runGenSessionWith(args, stdin, stdout, stderr, src, stdinIsTTY)
}

func runGenSessionWith(args []string, stdin *os.File, stdout, stderr io.Writer, src config.EnvSource, isTTY func(*os.File) bool) int {
	fs := flag.NewFlagSet("gen-session", flag.ContinueOnError)
	fs.SetOutput(stderr)
	labelFlag := fs.String("label", "", "account label; names the output file <label>.session (prompted when empty)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if !isTTY(stdin) {
		fmt.Fprintln(stderr, "Error: gen-session needs an interactive terminal: stdin is not a TTY.")
		fmt.Fprintln(stderr, "Phone/code login is disabled when stdin is piped or redirected. Run gen-session from a real terminal, then copy the .session file to the machine that runs the server.")
		return 1
	}

	apiID, apiHash, err := loadCredentials(src)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	fmt.Fprint(stdout, "\n----- Telegram Session Generator -----\n\n")
	fmt.Fprintln(stdout, "This command logs in to your Telegram account and writes a session file.")
	fmt.Fprintln(stdout, "Reference it from your environment as TELEGRAM_SESSION_NAME[_<LABEL>] (without the .session suffix).")
	fmt.Fprint(stdout, "\nYour credentials are only used for local authentication and never leave this machine.\n\n")

	label := strings.ToLower(strings.TrimSpace(*labelFlag))
	if label == "" {
		prompted, err := promptLine(stdout, stdin, "Account label (optional, e.g. 'work', 'personal'; leave empty for default): ")
		if err != nil {
			fmt.Fprintf(stderr, "Error: reading account label: %v\n", err)
			return 1
		}
		label = strings.ToLower(strings.TrimSpace(prompted))
	}
	if label == "" {
		label = "default"
	}
	if !validSessionLabel(label) {
		fmt.Fprintf(stderr, "Error: account label %q may contain only letters, numbers, '-' and '_'\n", label)
		return 1
	}

	name := label + ".session"
	if _, err := os.Stat(name); err == nil {
		fmt.Fprintf(stderr, "Error: %s already exists. Choose another --label or move the existing file away.\n", name)
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "Error: checking %s: %v\n", name, err)
		return 1
	}

	client, err := telegram.NewClient(telegram.ClientConfig{
		AppID:        apiID,
		AppHash:      apiHash,
		Session:      name,
		DeviceConfig: session.DeviceConfig(),
		NoPreconnect: true,
		NoUpdates:    true,
		DisableCache: true,
		LogLevel:     telegram.LogWarn,
	})
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	defer func() { _ = client.Disconnect() }()

	fmt.Fprintln(stdout, "Connecting to Telegram...")
	if err := client.Connect(); err != nil {
		fmt.Fprintf(stderr, "Error: %v\nFailed to generate a session file. Please try again.\n", err)
		return 1
	}

	authorized, err := client.IsAuthorized()
	if err != nil {
		fmt.Fprintf(stderr, "Error: checking authorization: %v\n", err)
		return 1
	}
	if !authorized {
		if code := interactiveLogin(client, stdin, stdout); code != 0 {
			return code
		}
	}

	// gogram's Login persists to memory only; this is the write that lands
	// the auth key in the file session.
	if err := client.SaveSession(false); err != nil {
		fmt.Fprintf(stderr, "Error: the login succeeded but the session file could not be written: %v\n", err)
		return 1
	}

	path, err := filepath.Abs(name)
	if err != nil {
		path = name
	}
	envName := "TELEGRAM_SESSION_NAME"
	if label != "default" {
		envName += "_" + strings.ToUpper(strings.ReplaceAll(label, "-", "_"))
	}
	fmt.Fprintln(stdout, "\nAuthentication successful!")
	fmt.Fprintf(stdout, "\nCreated %s\n", path)
	fmt.Fprintln(stdout, "\nAdd this to your .env file as:")
	fmt.Fprintf(stdout, "%s=%s\n", envName, strings.TrimSuffix(path, ".session"))
	fmt.Fprintln(stdout, "\nIMPORTANT: Keep this file private and never share it with anyone!")
	return 0
}

// interactiveLogin runs the phone-or-bot-token login, mirroring
// session_string_generator.py's _phone_login(). The callbacks keep the
// prompts on this command's own streams instead of letting the library read
// os.Stdin directly.
func interactiveLogin(client *telegram.Client, stdin *os.File, stdout io.Writer) int {
	phone, err := promptLine(stdout, stdin, "Please enter your phone (or bot token): ")
	if err != nil {
		fmt.Fprintf(stdout, "\nError: reading phone number: %v\n", err)
		return 1
	}
	phone = strings.TrimSpace(phone)
	if phone == "" {
		fmt.Fprintln(stdout, "\nError: no phone number entered. Please try again.")
		return 1
	}

	fail := func(err error) int {
		fmt.Fprintf(stdout, "\nError: %v\nFailed to generate a session file. Please try again.\n", err)
		return 1
	}

	if strings.Contains(phone, ":") {
		if err := client.LoginBot(phone); err != nil {
			return fail(err)
		}
		return 0
	}

	ok, err := client.Login(phone, &telegram.LoginOptions{
		CodeCallback: func() (string, error) {
			return promptLine(stdout, stdin, "\nPlease enter the code you received: ")
		},
		PasswordCallback: func() (string, error) {
			return promptSecret(stdout, stdin, "\nTwo-factor authentication enabled. Please enter your password: ")
		},
		OnWrongPassword: func(attempt, maxRetries int) bool {
			fmt.Fprintln(stdout, "Invalid password, please try again.")
			return attempt < maxRetries
		},
		MaxRetries: 3,
	})
	if err != nil {
		return fail(err)
	}
	if !ok {
		return fail(errors.New("login did not complete"))
	}
	return 0
}

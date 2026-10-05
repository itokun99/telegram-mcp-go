package boot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/itokun99/telegram-mcp-go/internal/config"
	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/kit/paths"
	"github.com/itokun99/telegram-mcp-go/internal/tools/chats"
	"github.com/itokun99/telegram-mcp-go/internal/tools/contacts"
	"github.com/itokun99/telegram-mcp-go/internal/tools/events"
	"github.com/itokun99/telegram-mcp-go/internal/tools/folders"
	"github.com/itokun99/telegram-mcp-go/internal/tools/groups"
	"github.com/itokun99/telegram-mcp-go/internal/tools/media"
	"github.com/itokun99/telegram-mcp-go/internal/tools/messages"

	_ "github.com/itokun99/telegram-mcp-go/internal/tools/accounts"
	_ "github.com/itokun99/telegram-mcp-go/internal/tools/profile"
)

// Install loads the environment once and installs every tool module's runtime
// seam. It performs no network I/O: the Telegram connection is established
// lazily by the first tool call. A multi-account configuration is refused
// (see the package doc) instead of guessing which account to serve.
func Install(src config.EnvSource) error {
	cfg, err := config.Load(src)
	if err != nil {
		return err
	}
	return install(cfg)
}

func install(cfg *config.Config) error {
	if len(cfg.Accounts) != 1 {
		return multiAccountError(cfg)
	}
	label, account := onlyAccount(cfg)

	sess, err := newSessionManager(cfg, account)
	if err != nil {
		return err
	}
	gate, err := newPathGate(cfg)
	if err != nil {
		return err
	}
	src := &entitySource{sess: sess}
	router := kit.NewRouter([]string{label})
	allowlist := chatAllowlist(cfg)
	aliases := loadAliases(cfg)

	messages.Configure(messages.Runtime{
		Clients: func(account string) (messages.Client, error) {
			cl, err := sess.clientFor(context.Background(), account)
			if err != nil {
				return nil, err
			}
			return messages.NewGogramClient(cl), nil
		},
		Allowlist: allowlist,
		Mode:      cfg.TranscribeMode,
		Engine:    cfg.TranscribeEngine,
		Settings:  cfg.Transcribe,
		Env:       cfg.Source,
		Cache:     kit.NewTranscribeCache(cfg.Transcribe.CacheDir),
	})

	groups.SetRuntime(groups.Runtime{
		Router: router,
		Client: func(ctx context.Context, account string) (groups.Client, error) {
			return sess.clientFor(ctx, account)
		},
		FileResolver: func(ctx context.Context, toolName, rawPath string) (string, error) {
			return gate.ResolveReadable(ctx, nil, toolName, rawPath)
		},
	})

	chats.SetClientProvider(func(ctx context.Context, account string) (chats.Client, error) {
		if _, err := sess.clientFor(ctx, account); err != nil {
			return nil, err
		}
		return newChatsAdapter(sess), nil
	})
	chats.SetChatAllowlist(allowlist)

	contacts.Configure(&contacts.Deps{
		Router: router,
		Clients: func(ctx context.Context, account string) (contacts.Client, error) {
			if _, err := sess.clientFor(ctx, account); err != nil {
				return nil, err
			}
			return newContactsAdapter(sess), nil
		},
		Aliases:   contacts.NewAliasStore(cfg.AliasesFile, cfg.XDGStateHome, cfg.ContactFuzzy),
		Allowlist: allowlist,
	})

	folders.SetDeps(&folders.Deps{
		Router: router,
		ClientFor: func(label string) (folders.Client, error) {
			if _, err := sess.clientFor(context.Background(), label); err != nil {
				return nil, err
			}
			return newFoldersAdapter(sess, src), nil
		},
		Allowlist: allowlist,
	})

	media.SetDeps(&media.Deps{
		Router: router,
		ClientFor: func(label string) (media.Client, error) {
			if _, err := sess.clientFor(context.Background(), label); err != nil {
				return nil, err
			}
			return newMediaAdapter(sess, src), nil
		},
		Paths:     gate,
		Allowlist: allowlist,
	})

	events.Configure(events.Deps{
		Resolver:     kit.NewResolver(src),
		Aliases:      aliases,
		Allowlist:    allowlist,
		FeedFile:     cfg.EventFeedFile,
		XDGStateHome: cfg.XDGStateHome,
		EventFeed:    cfg.EventFeed,
	})
	return nil
}

// onlyAccount returns the sole configured account (install checked the count).
func onlyAccount(cfg *config.Config) (string, config.Account) {
	for label, account := range cfg.Accounts {
		return label, account
	}
	return "", config.Account{}
}

// multiAccountError refuses a multi-account configuration: the per-account
// fan-out is not ported, and silently serving one account would misroute
// every write.
func multiAccountError(cfg *config.Config) error {
	labels := make([]string, 0, len(cfg.Accounts))
	for label := range cfg.Accounts {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return fmt.Errorf(
		"multi-account mode is not wired yet: %d accounts configured (%s). Run the server with exactly one session: TELEGRAM_SESSION_STRING, TELEGRAM_SESSION_NAME, or a single TELEGRAM_SESSION_STRING_<LABEL>/TELEGRAM_SESSION_NAME_<LABEL>.",
		len(labels), strings.Join(labels, ", "),
	)
}

// chatAllowlist narrows the parsed allowlist to the interface the tool
// modules accept; an unset TELEGRAM_ALLOWED_CHAT_IDS becomes a nil interface
// (no gate) instead of a typed nil.
func chatAllowlist(cfg *config.Config) kit.ChatAllowlist {
	if cfg.AllowedChats == nil {
		return nil
	}
	return cfg.AllowedChats
}

// loadAliases builds the events module's alias snapshot. An unreadable store
// is a documented degraded read: kit.LoadAliases logs its constant warning,
// returns an empty usable store, and the alias features degrade without
// taking the server down (the Python loader behaves the same way).
func loadAliases(cfg *config.Config) *kit.Aliases {
	aliases, _ := kit.LoadAliases(cfg.AliasesFile, cfg.XDGStateHome, cfg.ContactFuzzy)
	return aliases
}

// newPathGate resolves the server CLI roots (the Python
// _configure_allowed_roots_from_cli contract: expand, create, symlink-resolve,
// de-duplicate; a failure aborts startup) and builds the fail-closed gate.
func newPathGate(cfg *config.Config) (*paths.Gate, error) {
	roots, err := paths.ResolveServerRoots(cfg.AllowedRoots)
	if err != nil {
		return nil, err
	}
	return paths.New(paths.Settings{
		ServerRoots:              roots,
		AllowServerRootsFallback: cfg.AllowServerRootsFallback,
		ServerRootsOnly:          cfg.ServerRootsOnly,
		RootsTimeout:             rootsTimeout(cfg.RootsRequestTimeout),
		ExtensionAllowlists:      cfg.ExtensionAllowlists,
	}), nil
}

// rootsTimeout converts TELEGRAM_ROOTS_TIMEOUT_SECONDS; nil (and a value of
// zero or less, which Python reads as "wait forever") means no timeout.
func rootsTimeout(seconds *float64) *time.Duration {
	if seconds == nil || *seconds <= 0 {
		return nil
	}
	d := time.Duration(*seconds * float64(time.Second))
	return &d
}

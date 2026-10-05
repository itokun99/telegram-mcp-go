package boot

import (
	"context"
	"fmt"
	"strings"
	"sync"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/config"
	"github.com/itokun99/telegram-mcp-go/internal/kit"
	"github.com/itokun99/telegram-mcp-go/internal/session"
)

// sessionManager owns one account's lazily established Telegram connection.
// The first tool call that needs a client connects (taking the per-session
// lock and verifying the expected username); later calls reuse it. A failed
// attempt is not cached: the next call retries, mirroring the Python
// runtime's ensure_connected.
type sessionManager struct {
	cfg     *config.Config
	account config.Account
	label   string
	router  *kit.Router
	// proxy is resolved once at install time so a malformed per-label proxy
	// aborts startup instead of the first tool call.
	proxy telegram.Proxy

	mu     sync.Mutex
	conn   *session.Connection
	gogram *telegram.Client
}

// newSessionManager resolves the account's proxy and prepares the lazy
// connection. It performs no network I/O.
func newSessionManager(cfg *config.Config, account config.Account) (*sessionManager, error) {
	proxy, err := gogramProxy(cfg, account.Label)
	if err != nil {
		return nil, err
	}
	return &sessionManager{
		cfg:     cfg,
		account: account,
		label:   account.Label,
		router:  kit.NewRouter([]string{account.Label}),
		proxy:   proxy,
	}, nil
}

// client returns the connected gogram client, connecting on first use.
func (m *sessionManager) client(ctx context.Context) (*telegram.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gogram != nil {
		return m.gogram, nil
	}

	var handle *gogramHandle
	conn, err := session.Connect(ctx, session.ConnectOptions{
		Label:    m.label,
		Identity: m.identity(),
		NewClient: func() (session.ClientHandle, error) {
			h, err := m.newHandle()
			if err != nil {
				return nil, err
			}
			handle = h
			return h, nil
		},
		ExpectedUsername: m.cfg.ExpectedUsernames[m.label],
		Shared:           m.cfg.SessionLockMode == "shared",
		GraceSeconds:     m.cfg.LockGraceSeconds,
	})
	if err != nil {
		return nil, err
	}
	m.conn = conn
	m.gogram = handle.client
	return m.gogram, nil
}

// clientFor resolves an account label and returns the connected client. An
// empty label selects the sole account; any other label must match it, with
// the same wording kit.Router uses for a multi-account setup.
func (m *sessionManager) clientFor(ctx context.Context, account string) (*telegram.Client, error) {
	if _, err := m.router.Label(account); err != nil {
		return nil, err
	}
	return m.client(ctx)
}

// identity is the session identity the advisory lock is keyed on, mirroring
// runtime.session_identity: the string session's value, or the file session's
// absolute path.
func (m *sessionManager) identity() string {
	if m.account.SessionString != "" {
		return session.StringSessionIdentity(m.account.SessionString)
	}
	return session.FileSessionIdentity(sessionFilePath(m.account.SessionName))
}

// newHandle constructs the gogram client handle for this account. It performs
// no network I/O (NoPreconnect); session.Connect drives Connect itself.
func (m *sessionManager) newHandle() (*gogramHandle, error) {
	clientCfg := telegram.ClientConfig{
		AppID:        int32(m.cfg.APIID),
		AppHash:      m.cfg.APIHash,
		DeviceConfig: session.DeviceConfig(),
		SessionName:  m.label,
		NoPreconnect: true,
		NoUpdates:    true,
		LogLevel:     telegram.LogWarn,
	}
	if m.proxy != nil {
		clientCfg.Proxy = m.proxy
	}
	if m.account.SessionString != "" {
		clientCfg.StringSession = m.account.SessionString
	} else {
		clientCfg.Session = sessionFilePath(m.account.SessionName)
	}
	client, err := telegram.NewClient(clientCfg)
	if err != nil {
		return nil, err
	}
	return &gogramHandle{client: client}, nil
}

// sessionFilePath mirrors Telethon's session naming: the configured name is a
// file name, and the .session suffix is appended only when it is not already
// there, so both "work" and "work.session" name the same file.
func sessionFilePath(name string) string {
	if strings.HasSuffix(name, ".session") {
		return name
	}
	return name + ".session"
}

// gogramHandle adapts *telegram.Client to session.ClientHandle: the connect
// path needs the connect/disconnect pair, the authorized identity and the
// AUTH_KEY_DUPLICATED matcher.
type gogramHandle struct {
	client *telegram.Client
}

// Connect performs the network connect.
func (h *gogramHandle) Connect() error { return h.client.Connect() }

// Disconnect tears the connection down.
func (h *gogramHandle) Disconnect() error { return h.client.Disconnect() }

// GetAuth returns the session's user identity; nil means unauthenticated, so
// the connect path reports the actionable UnauthenticatedError.
func (h *gogramHandle) GetAuth() (*session.Auth, error) {
	authorized, err := h.client.IsAuthorized()
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, nil
	}
	me, err := h.client.GetMe()
	if err != nil {
		return nil, err
	}
	auth := &session.Auth{JustUsername: me.Username}
	for _, extra := range me.Usernames {
		if extra != nil && extra.Username != "" {
			auth.ExtraUsernames = append(auth.ExtraUsernames, extra.Username)
		}
	}
	return auth, nil
}

// MatchAuthKeyDuplicated reports whether err is Telegram's
// AUTH_KEY_DUPLICATED RPC error.
func (h *gogramHandle) MatchAuthKeyDuplicated(err error) bool {
	return h.client.MatchRPCError(err, "AUTH_KEY_DUPLICATED")
}

// gogramProxy maps the parsed proxy settings onto gogram's proxy type. The
// per-label resolution happens here, at install time, so a malformed
// TELEGRAM_PROXY_* value aborts startup (config.ResolveProxy's contract).
func gogramProxy(cfg *config.Config, label string) (telegram.Proxy, error) {
	pc, err := cfg.ResolveProxy(label)
	if err != nil {
		return nil, err
	}
	base := telegram.BaseProxy{Host: pc.Host, Port: pc.Port}
	switch pc.Type {
	case "":
		return nil, nil
	case "socks5":
		return &telegram.Socks5Proxy{BaseProxy: base, Username: pc.Username, Password: pc.Password}, nil
	case "socks4":
		return &telegram.Socks4Proxy{BaseProxy: base, UserID: pc.Username}, nil
	case "http":
		return &telegram.HttpProxy{BaseProxy: base, Username: pc.Username, Password: pc.Password}, nil
	case "mtproxy":
		return &telegram.MTProxy{BaseProxy: base, Secret: pc.Secret}, nil
	default:
		// ResolveProxy validates the type; this guards a future type that is
		// accepted there but has no gogram mapping.
		return nil, fmt.Errorf("proxy type '%s' has no gogram mapping", pc.Type)
	}
}

package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ProxyConfig is the resolved proxy settings for one account label.
// It is produced by ResolveProxy(label) and consumed by the gogram client
// constructor in the session layer.
type ProxyConfig struct {
	// Type is "" when no proxy is configured for this label.
	// One of: socks5, socks4, http, mtproxy.
	Type string
	Host string
	Port int

	// Username/Password are set only when present.
	Username string
	Password string
	// RDNS is true unless explicitly disabled; it mirrors Python's
	// unconditional proxy["rdns"] key (default True).
	RDNS bool
	// Secret is the MTProxy hex secret; required when Type is "mtproxy".
	Secret string
	// Connection is the gogram connection type for mtproxy. Kept as a
	// string so the session layer can map it; empty for non-mtproxy.
	Connection string
}

var proxyTypesAll = map[string]bool{"socks5": true, "socks4": true, "http": true, "mtproxy": true}

// proxyGet mirrors runtime._get_proxy_env: the per-label value overrides the
// global; an empty global value yields "" (unset). An empty label skips the
// suffixed lookup (a label only exists once, so "" means global-only).
func (c *Config) proxyGet(name, label string) string {
	if label != "" {
		suffixed, _ := c.Source.Get("TELEGRAM_PROXY_" + name + "_" + strings.ToUpper(label))
		if suffixed != "" {
			return suffixed
		}
	}
	g, _ := c.Source.Get("TELEGRAM_PROXY_" + name)
	return g
}

// loadProxy validates the global (unsuffixed) proxy configuration at load
// time so a bad global setting aborts startup before any per-label
// resolution. Per-label overrides are checked in ResolveProxy.
func (c *Config) loadProxy() error {
	pc, err := c.ResolveProxy("")
	if err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	c.Proxy = pc
	return nil
}

// ResolveProxy returns the ProxyConfig for a label, mirroring
// runtime._build_proxy_for_label. It fails loud (error) on a malformed
// proxy configuration so the server aborts startup instead of silently
// bypassing the proxy.
func (c *Config) ResolveProxy(label string) (ProxyConfig, error) {
	pc := ProxyConfig{}
	pc.Type = strings.ToLower(strings.TrimSpace(c.proxyGet("TYPE", label)))
	if pc.Type == "" {
		return pc, nil
	}
	if !proxyTypesAll[pc.Type] {
		return pc, fmt.Errorf("invalid TELEGRAM_PROXY_TYPE '%s'. Expected one of: http, mtproxy, socks4, socks5.", pc.Type)
	}
	pc.Host = strings.TrimSpace(c.proxyGet("HOST", label))
	portRaw := c.proxyGet("PORT", label)
	if pc.Host == "" || portRaw == "" {
		return pc, fmt.Errorf("TELEGRAM_PROXY_HOST and TELEGRAM_PROXY_PORT are required when TELEGRAM_PROXY_TYPE is set.")
	}
	port, err := strconv.Atoi(strings.TrimSpace(portRaw))
	if err != nil {
		return pc, fmt.Errorf("TELEGRAM_PROXY_PORT must be an integer, got '%s'.", portRaw)
	}
	pc.Port = port

	if pc.Type == "mtproxy" {
		pc.Secret = c.proxyGet("SECRET", label)
		if pc.Secret == "" {
			return pc, fmt.Errorf("TELEGRAM_PROXY_SECRET is required for mtproxy.")
		}
		pc.Connection = "mtproxy"
		return pc, nil
	}

	// SOCKS4/SOCKS5/HTTP. Python always sets rdns in the proxy dict
	// (default true); an explicit TELEGRAM_PROXY_RDNS=off/0 disables it.
	pc.RDNS = parseBoolEnv(c.proxyGet("RDNS", label), true)

	pc.Username = c.proxyGet("USERNAME", label)
	pc.Password = c.proxyGet("PASSWORD", label)
	return pc, nil
}

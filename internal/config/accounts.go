package config

import (
	"regexp"
	"sort"
	"strings"
)

// sessionPoolRe splits TELEGRAM_SESSION_STRINGS on whitespace, comma or
// semicolon. Mirrors Python re.split(r"[\s,;]+", raw).
var sessionPoolRe = regexp.MustCompile(`[\s,;]+`)

// discoverAccounts scans src for TELEGRAM_SESSION_STRING_<LABEL> /
// TELEGRAM_SESSION_NAME_<LABEL> (multi-account mode) and the unsuffixed
// pair, then parses the session pool.
//
// Rules (mirroring runtime._discover_accounts):
//   - suffixed vars yield lower-cased labels
//   - the unsuffixed vars yield "default"; the session pool takes
//     precedence for the default account, then TELEGRAM_SESSION_STRING,
//     then TELEGRAM_SESSION_NAME
//   - no session at all -> ErrNoSession (actionable)
func (c *Config) discoverAccounts() error {
	c.Accounts = map[string]Account{}

	for _, key := range c.Source.Keys() {
		switch {
		case strings.HasPrefix(key, "TELEGRAM_SESSION_STRING_"):
			val, _ := c.Source.Get(key)
			if val == "" {
				continue
			}
			label := strings.ToLower(strings.TrimPrefix(key, "TELEGRAM_SESSION_STRING_"))
			c.Accounts[label] = Account{Label: label, SessionString: val}
		case strings.HasPrefix(key, "TELEGRAM_SESSION_NAME_"):
			val, _ := c.Source.Get(key)
			if val == "" {
				continue
			}
			label := strings.ToLower(strings.TrimPrefix(key, "TELEGRAM_SESSION_NAME_"))
			c.Accounts[label] = Account{Label: label, SessionName: val}
		}
	}

	// Backward-compatible unsuffixed variables.
	c.SessionPool = parseSessionPool(mustGet(c.Source, "TELEGRAM_SESSION_STRINGS"))
	sessionString := mustGet(c.Source, "TELEGRAM_SESSION_STRING")
	sessionName := mustGet(c.Source, "TELEGRAM_SESSION_NAME")

	if _, hasDefault := c.Accounts["default"]; !hasDefault {
		switch {
		case len(c.SessionPool) > 0:
			c.Accounts["default"] = Account{Label: "default", SessionString: c.SessionPool[0]}
		case sessionString != "":
			c.Accounts["default"] = Account{Label: "default", SessionString: sessionString}
		case sessionName != "":
			c.Accounts["default"] = Account{Label: "default", SessionName: sessionName}
		}
	}

	if len(c.Accounts) == 0 {
		return ErrNoSession
	}

	// Per-label expected usernames (used to refuse a session logged into a
	// different account at connect time).
	c.ExpectedUsernames = map[string]string{}
	unsuffixed := normalizeUsername(mustGet(c.Source, "TELEGRAM_EXPECTED_USERNAME"))
	if unsuffixed != "" {
		c.ExpectedUsernames["default"] = unsuffixed
	}
	for _, key := range c.Source.Keys() {
		if !strings.HasPrefix(key, "TELEGRAM_EXPECTED_USERNAME_") {
			continue
		}
		val, _ := c.Source.Get(key)
		norm := normalizeUsername(val)
		if norm == "" {
			continue
		}
		c.ExpectedUsernames[strings.ToLower(strings.TrimPrefix(key, "TELEGRAM_EXPECTED_USERNAME_"))] = norm
	}

	return nil
}

// parseSessionPool mirrors runtime._parse_session_pool: whitespace/comma/
// semicolon separated, de-duplicated, order-preserving.
func parseSessionPool(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	seen := map[string]bool{}
	pool := []string{}
	for _, tok := range sessionPoolRe.Split(raw, -1) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] {
			continue
		}
		seen[tok] = true
		pool = append(pool, tok)
	}
	return pool
}

// normalizeUsername mirrors runner._normalize_username: trim, strip a
// leading @, casefold (lowercase for the ASCII usernames this uses).
func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimLeft(strings.TrimSpace(value), "@"))
}

// sortedAccountLabels returns account labels in a stable order for error
// messages.
func sortedAccountLabels(c *Config) []string {
	out := make([]string, 0, len(c.Accounts))
	for k := range c.Accounts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

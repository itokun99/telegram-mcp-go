package config

import (
	"fmt"
	"strings"
)

var (
	exposedToolsModes    = map[string]bool{"all": true, "read-only": true}
	exposedToolsAllowSep = "+"
)

// loadExposedTools parses TELEGRAM_EXPOSED_TOOLS.
//
// Accepted shapes (mirrors runtime._get_exposed_tools_mode):
//   - "all" (default): every tool is exposed
//   - "read-only": only tools annotated readOnlyHint
//   - "read-only+name,name": read-only plus the named write tools
//
// Invalid base modes, "+names" under a non-read-only base, an empty
// allowlist, or unknown tool names all fail loud — a typo must not
// silently degrade into a narrower allowlist. Unknown-name validation
// needs the registered tool names, so Load only records the parsed
// shape; ValidateExposedTools(names...) performs the abort check.
func (c *Config) loadExposedTools() error {
	raw, set := c.Source.Get("TELEGRAM_EXPOSED_TOOLS")
	// os.getenv("TELEGRAM_EXPOSED_TOOLS", "all"): only UNSET gives the
	// "all" default; a variable set to an empty string aborts (mode "" is
	// not an accepted base mode), matching the Python source exactly.
	if !set {
		raw = "all"
	}
	mode := strings.ToLower(strings.TrimSpace(raw))

	base, allowlist := splitExposedToolsMode(mode)
	if !exposedToolsModes[base] {
		return fmt.Errorf("invalid TELEGRAM_EXPOSED_TOOLS '%s'. Expected one of: all, read-only.", raw)
	}
	if !strings.Contains(mode, exposedToolsAllowSep) {
		c.ExposedMode = base
		return nil
	}
	if base != "read-only" {
		return fmt.Errorf("invalid TELEGRAM_EXPOSED_TOOLS '%s'. The '%s tool,tool' allowlist is only valid with read-only.", raw, exposedToolsAllowSep)
	}
	if len(allowlist) == 0 {
		return fmt.Errorf("invalid TELEGRAM_EXPOSED_TOOLS '%s'. The '%s' allowlist must name at least one tool.", raw, exposedToolsAllowSep)
	}
	c.ExposedMode = "read-only"
	c.ExposedAllowlist = allowlist
	return nil
}

// splitExposedToolsMode splits a normalised mode into its base and the
// write-tool allowlist after the "+".
func splitExposedToolsMode(mode string) (string, []string) {
	base, rawAllowlist, sep := strings.Cut(mode, exposedToolsAllowSep)
	if !sep {
		return base, nil
	}
	names := []string{}
	for _, n := range strings.Split(rawAllowlist, ",") {
		n = strings.TrimSpace(n)
		if n != "" {
			names = append(names, n)
		}
	}
	return base, names
}

// ValidateExposedTools aborts startup when the configured allowlist names
// tools that are not registered (mirrors runtime._apply_exposed_tools_mode
// failing loud on unknown names).
func (c *Config) ValidateExposedTools(registered []string) error {
	if c.ExposedMode != "read-only" || len(c.ExposedAllowlist) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, n := range registered {
		known[n] = true
	}
	unknown := []string{}
	for _, n := range c.ExposedAllowlist {
		if !known[n] {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("invalid TELEGRAM_EXPOSED_TOOLS allowlist: unknown tool(s) %s.", strings.Join(unknown, ", "))
}

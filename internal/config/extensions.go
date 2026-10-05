package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// loadFileExtensions parses TELEGRAM_FILE_EXTENSIONS into per-tool
// extension overrides (mirrors runtime._get_file_extension_overrides).
//
// Shape: 'tool:.ext,.ext;tool2:.pdf'. Unset/blank -> no overrides.
// A malformed entry aborts startup here, at parse time: a typo must not
// silently produce a narrower (or wider) allowlist that looks like it
// worked. Unknown tool names are checked later, against the registered
// tools, in ValidateFileExtensions.
func (c *Config) loadFileExtensions() error {
	c.ExtensionOverrides = map[string][]string{}
	// Keep a stable order (the Python source uses sets; Go consumers get
	// sorted lists).
	raw := strings.TrimSpace(mustGet(c.Source, "TELEGRAM_FILE_EXTENSIONS"))
	if raw == "" {
		c.ExtensionAllowlists = mergedExtensionAllowlists(c.ExtensionOverrides)
		return nil
	}

	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		tool, exts, found := strings.Cut(entry, ":")
		tool = strings.ToLower(strings.TrimSpace(tool))
		if !found || tool == "" {
			return fmt.Errorf("invalid TELEGRAM_FILE_EXTENSIONS '%s'. Each entry must look like 'tool:.ext,.ext', entries separated by ';'.", raw)
		}
		extensions := map[string]bool{}
		for _, rawExt := range strings.Split(exts, ",") {
			token := strings.ToLower(strings.TrimSpace(rawExt))
			if token == "" {
				return fmt.Errorf("invalid TELEGRAM_FILE_EXTENSIONS '%s'. Tool '%s' has an empty extension entry.", raw, tool)
			}
			if !strings.HasPrefix(token, ".") {
				token = "." + token
			}
			if !fileExtensionTokenRe.MatchString(token) {
				return fmt.Errorf("invalid TELEGRAM_FILE_EXTENSIONS '%s'. Malformed extension '%s' for tool '%s'.", raw, strings.TrimSpace(rawExt), tool)
			}
			extensions[token] = true
		}
		if _, dup := c.ExtensionOverrides[tool]; dup {
			return fmt.Errorf("invalid TELEGRAM_FILE_EXTENSIONS '%s'. Tool '%s' is named more than once.", raw, tool)
		}
		c.ExtensionOverrides[tool] = sortedKeys(extensions)
	}
	c.ExtensionAllowlists = mergedExtensionAllowlists(c.ExtensionOverrides)
	return nil
}

// fileExtensionTokenRe mirrors runtime._FILE_EXTENSION_TOKEN_PATTERN:
// '.<alnum|-|_>' with at least one char after the dot.
var fileExtensionTokenRe = regexp.MustCompile(`^\.[A-Za-z0-9_-]+$`)

// mergedExtensionAllowlists mirrors runtime._apply_file_extension_overrides:
// TELEGRAM_FILE_EXTENSIONS entries replace a tool's whole default set;
// tools not named keep their default (send_file/upload_file stay
// unrestricted when unset).
func mergedExtensionAllowlists(overrides map[string][]string) map[string][]string {
	merged := map[string][]string{}
	for k, v := range defaultExtensionLists {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

// ValidateFileExtensions aborts startup when an override names a tool that
// is not registered (mirrors runtime._apply_file_extension_overrides).
func (c *Config) ValidateFileExtensions(registered []string) error {
	if len(c.ExtensionOverrides) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, n := range registered {
		known[n] = true
	}
	unknown := []string{}
	for n := range c.ExtensionOverrides {
		if !known[n] {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("invalid TELEGRAM_FILE_EXTENSIONS: unknown tool(s) %s.", strings.Join(unknown, ", "))
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

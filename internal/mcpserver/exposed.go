package mcpserver

import (
	"fmt"
	"sort"
	"strings"
)

// applyExposure implements TELEGRAM_EXPOSED_TOOLS over the registered tools.
//
//	mode "all" (or ""):        every tool is served
//	mode "read-only":          only ReadOnly tools, plus allowlist names
//	allowlist:                 must only name registered tools; a typo aborts
//	                           startup instead of silently narrowing the surface
//
// It mirrors runtime._apply_exposed_tools_mode.
func applyExposure(tools []*Tool, mode string, allowlist []string) ([]*Tool, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "all":
		return tools, nil
	case "read-only":
	default:
		return nil, fmt.Errorf("invalid TELEGRAM_EXPOSED_TOOLS '%s'. Expected one of: all, read-only.", mode)
	}

	registered := make(map[string]bool, len(tools))
	for _, t := range tools {
		registered[t.Name] = true
	}
	allowed := make(map[string]bool, len(allowlist))
	unknownSet := make(map[string]bool)
	var unknown []string
	for _, name := range allowlist {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !registered[name] {
			if !unknownSet[name] {
				unknownSet[name] = true
				unknown = append(unknown, name)
			}
			continue
		}
		allowed[name] = true
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("invalid TELEGRAM_EXPOSED_TOOLS allowlist: unknown tool(s) %s.", strings.Join(unknown, ", "))
	}

	served := make([]*Tool, 0, len(tools))
	for _, t := range tools {
		if t.Options.ReadOnly || allowed[t.Name] {
			served = append(served, t)
		}
	}
	return served, nil
}

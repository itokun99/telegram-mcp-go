package mcpserver

import (
	"time"

	"github.com/itokun99/telegram-mcp-go/internal/config"
)

// OptionsFromConfig maps the parsed environment (internal/config) onto the
// boot options.
func OptionsFromConfig(cfg *config.Config) Options {
	opts := Options{
		Transport:        cfg.Transport,
		Host:             cfg.Host,
		Port:             cfg.Port,
		ExposedMode:      cfg.ExposedMode,
		ExposedAllowlist: cfg.ExposedAllowlist,
	}
	if !cfg.ToolTimeoutUnlimited && cfg.ToolTimeoutSeconds > 0 {
		opts.ToolTimeout = time.Duration(cfg.ToolTimeoutSeconds * float64(time.Second))
	}
	return opts
}

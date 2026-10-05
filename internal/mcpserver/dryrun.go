package mcpserver

import (
	"encoding/json"
	"io"
)

// dryRunTool is one entry of the --dry-run tool listing.
type dryRunTool struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

// DryRun writes the sorted served tool list as a JSON array to w, one line.
// It never connects to Telegram and never starts a transport: the parity
// gate diffs this output (after TELEGRAM_EXPOSED_TOOLS pruning) against the
// Python tool surface.
func (s *Server) DryRun(w io.Writer) error {
	out := make([]dryRunTool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, dryRunTool{
			Name:            t.Name,
			Description:     t.Description,
			ReadOnlyHint:    t.Options.ReadOnly,
			DestructiveHint: t.Options.Destructive,
			IdempotentHint:  t.Options.Idempotent,
			OpenWorldHint:   t.Options.OpenWorld,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

// Package boot installs the tool modules' runtime seams and hands the
// process over to internal/mcpserver.
//
// The tool packages register their tools from init(); importing this package
// (the command's default path imports it) therefore makes the process-wide
// mcpserver.DefaultRegistry carry the full 132-tool surface. Install then
// loads the environment once and wires every module's seam with lazy,
// session-backed clients:
//
//   - messages.Configure, groups.SetRuntime, chats.SetClientProvider +
//     SetChatAllowlist, contacts.Configure, folders.SetDeps, media.SetDeps,
//     events.Configure.
//
// The Telegram connection is established on the first tool call, never at
// install time: --dry-run and a server that only lists tools touch no
// network and no session. Connections use internal/session.Connect, so the
// per-session advisory lock, the expected-username guard and the
// AUTH_KEY_DUPLICATED retry loop are all in force; the lock is released by
// the OS when the process exits.
//
// # Deferrals
//
//   - Single-account mode only. Install refuses a configuration with more
//     than one account instead of guessing which one to serve; the refusal
//     names the configured labels and the variables that select one. The
//     per-account fan-out of kit.Router, the TELEGRAM_SESSION_STRINGS pool
//     and per-label locking remain unported.
//   - profile tools are registered but cannot be wired: profile.Deps travel
//     on the tool call context (profile.WithDeps) and the MCP boot path has
//     no context hook to install them process-wide, so every profile call
//     answers with the module's "not connected" funnel text. The
//     gogram-backed profile.Client adapter is written and ready
//     (newProfileAdapter); installation needs a per-call context hook in
//     internal/mcpserver or a process-wide default in internal/tools/profile.
//   - list_accounts is registered but cannot be wired for the same reason:
//     mcpserver.WithAccountLister is context-only and nothing installs it.
//   - events.Configure installs the module's configuration (resolver,
//     aliases, allowlist, feed path); no incoming-message feeder is wired,
//     so the wait tools time out until a runner path calls
//     events.OnIncoming from a live update handler.
//   - TELEGRAM_PROXY_RDNS has no gogram counterpart and is accepted but not
//     applied.
package boot

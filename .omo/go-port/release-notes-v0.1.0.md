# telegram-mcp-go v0.1.0

First release of the Go rewrite of telegram-mcp: a single static binary (no Python, no pip/uv)
serving **132 MCP tools** over stdio (default) or streamable HTTP.

## Highlights
- 132 tools across nine domains (messages, groups, chats, contacts, media, profile, folders,
  events, accounts) under the same names as the Python server; 55 read-only.
- Built on gogram (MTProto user sessions) + modelcontextprotocol/go-sdk; environment variables
  match the Python 2.x server (see README env table).
- Fail-closed file-path gates, prompt-injection sanitizer, FloodWait-safe error funnel,
  per-session advisory locks with a session pool.
- Transports: stdio, streamable HTTP (MCP_TRANSPORT=http, MCP_HOST/MCP_PORT), legacy SSE.

## Migrating from the Python server
- `telegram-mcp-go gen-session` — interactive login in a terminal, writes a portable .session file.
- `telegram-mcp-go migrate-session` — converts a Telethon StringSession into a gogram .session file
  (offline, round-trip tested).

## Docker
- Distroless static image (same name, telegram-mcp-go); `--build-arg VERSION=$(git describe --tags)` stamps the binary.

## Known limitations
- Voice transcription: groq / telegram-native / openai engines are ported; local faster-whisper is
  not — the whisper engine maps to an OpenAI-compatible endpoint (TELEGRAM_WHISPER_BASE_URL).
- Single-account configurations only; a multi-account environment is refused loudly at boot.
- This repository is a GitHub fork of the Python project. GitHub provides no un-fork API, so the
  fork flag remains even though the Python implementation has been removed; project identity is in
  this README/CHANGELOG.
- Tool modules carry per-domain gogram-typed seams; SDK adapters live outside internal/tools
  (recorded in .omo/go-port/waiver-launch-run3.md).

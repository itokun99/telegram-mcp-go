# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The Go module is versioned independently of the retired Python implementation,
which shipped as `telegram-mcp` 2.x. The Go line starts at `0.0.1` and this
release is its first tagged version.

## [0.1.0] - 2026-10-05

The Python server is gone. `telegram-mcp-go` is a single static Go binary that
speaks the Model Context Protocol, built on
[gogram](https://github.com/Amarnathcjd/gogram) for MTProto and the
[MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk). No interpreter, no
virtualenv, no dependency resolution at start-up — one binary, one `.env`, one
Telegram session.

### Added

- **132 tools across nine domains**, served under the same names as the Python
  server: messages (34), groups (25), chats (19), contacts (17), media (13),
  profile (11), folders (7), events (5), accounts (1). 55 of them are read-only.
- **Three transports**: stdio (default for desktop clients), streamable HTTP,
  and SSE, selected with `MCP_TRANSPORT`.
- **Single-binary CLI**: `telegram-mcp-go` serves the tool surface, and the
  `gen-session` and `migrate-session` subcommands create a login or convert a
  legacy Telethon `StringSession` into a gogram session file.
- **`--dry-run`**: prints the served tool list as sorted JSON and exits 0 with
  no Telegram connection, transport, or credentials.
- **Session layer** (`internal/session`): connect with retry on duplicate auth
  key, per-session OS advisory locks, a session pool, device identity, and an
  expected-username check.
- **Configuration contract** (`internal/config`): 62 environment variables with
  fail-loud validation. Malformed `TELEGRAM_EXPOSED_TOOLS`,
  `TELEGRAM_FILE_EXTENSIONS`, `TELEGRAM_SESSION_LOCK`, `MCP_TRANSPORT`,
  `MCP_PORT`, `TELEGRAM_API_ID`, proxy settings, and `FLOOD_SLEEP_THRESHOLD`
  abort start-up with a single-line message instead of degrading.
- **Exposure pruning**: `TELEGRAM_EXPOSED_TOOLS=read-only` removes the 77
  mutating tools from the served surface.
- **Shared helpers** (`internal/kit`): one error funnel that logs a code and a
  category (never message content, ids, chat names, or paths) and returns the
  user-facing string; a sanitizer that strips control, zero-width, and bidi
  characters from all Telegram-sourced text; a fail-closed file-path gate; a
  transcription helper with four engines; a contact-sheet builder; and an
  account router that fans read-only calls out across every configured account.
- **FloodWait handling**: the returned prose tells the agent not to retry
  immediately and states the wait duration; the log record carries no payload.
- **Two-stage `Dockerfile`** producing a `CGO_ENABLED=0` static binary on
  `distroless/static-debian12:nonroot`, plus a matching `docker-compose.yml`.
- **Offline test suite**: 15 packages, no network and no live Telegram session —
  configuration uses an injected `EnvSource`, HTTP uses `httptest`, and
  `gogram` is reached through per-domain interface seams.

### Changed

- Every tool body is a formatted-string return, never a crash. Failures come
  back as readable text plus a stable error code, and every call is bounded by
  `TELEGRAM_TOOL_TIMEOUT_SECONDS`.
- Tool schemas are derived from typed Go input structs; there are no
  hand-written JSON schemas.
- Read-only hints are a security boundary, not documentation: they decide what
  a read-only client can see, so a wrong hint is a security hole.
- Multi-account routing: a read-only call with no `account` argument runs
  against every configured account concurrently and joins the results; a write
  requires an explicit label as soon as more than one account exists.
- Unconfigured means deny: missing path roots, missing allowlists, and missing
  credentials produce a refusal string, never a permissive default.

### Removed

- The entire Python implementation: `telegram_mcp/`, `main.py`, `sanitize.py`,
  `migrate_session.py`, `session_string_generator.py`, and the pytest suite in
  `tests/`.
- Python packaging and environment pins: `pyproject.toml`, `poetry.lock`,
  `requirements.txt`, `uv.lock`, `.python-version`, and the root `__init__.py`.
- `manifest.json` and `claude_desktop_config.json`: both launched
  `main.py` through `uv`, so they described the retired server rather than the Go
  binary. Desktop clients configure the Go binary directly — see the README
  section "MCP Client Configuration".

### Known gaps

These are deliberate or unfinished, and are documented in full under "Known
Gaps" in the README:

1. The boot path does not open an MTProto session yet and the CLI does not yet
   dispatch the server action, so tools that need a client answer
   not-connected.
2. `engine=whisper` is an OpenAI-compatible HTTP endpoint
   (`TELEGRAM_WHISPER_BASE_URL`) instead of an in-process model; audio leaves the
   machine unless that endpoint is local.
3. The transcript cache is `data/transcripts/transcripts.json` rather than
   SQLite, and a pre-Go cache is not readable — the first run re-transcribes.
4. `TELEGRAM_ALLOWED_ROOTS` and `TELEGRAM_ALLOWED_CHAT_IDS` parse and validate
   but are not yet installed into the tool dependencies; both fail closed.
5. `MCP_ALLOWED_HOSTS` and `MCP_ALLOWED_ORIGINS` are parsed, not enforced by the
   HTTP listener.
6. `manifest.json` and `claude_desktop_config.json` are gone (see Removed), so
   the MCPB bundle is not published from this repository.

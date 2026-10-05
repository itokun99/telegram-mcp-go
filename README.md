<div align="center">
  <img src="https://capsule-render.vercel.app/api?type=waving&color=gradient&height=200&section=header&text=Telegram%20MCP%20Server&fontSize=50&fontAlignY=35&animation=fadeIn&fontColor=FFFFFF&descAlignY=55&descAlign=62" alt="Telegram MCP Server" width="100%" />
</div>

![MCP Badge](https://badge.mcpx.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-green?style=flat-square)](https://opensource.org/licenses/Apache-2.0)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?style=flat-square)
![Tools](https://img.shields.io/badge/MCP%20tools-132-00ADD8?style=flat-square)

A Telegram integration for Claude, Cursor, Codex, and every other MCP-compatible client. It exposes Telegram account, chat, message, contact, media, folder, and admin operations over the [Model Context Protocol](https://modelcontextprotocol.io/) as a single static Go binary — **132 tools**, built on [gogram](https://github.com/Amarnathcjd/gogram) (MTProto) and the [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk).

No runtime interpreter, no virtualenv, no dependency resolution at start-up: one binary, one `.env`, one Telegram session.

## Contents

- [What It Can Do](#what-it-can-do)
- [Requirements](#requirements)
- [Install](#install)
- [Quick Start](#quick-start)
- [CLI](#cli)
- [MCP Client Configuration](#mcp-client-configuration)
- [Transports](#transports)
- [Multi-Account Setup](#multi-account-setup)
- [Device Identity](#device-identity)
- [Expected Account Check](#expected-account-check)
- [Proxy Support](#proxy-support)
- [File Path Security](#file-path-security)
- [Chat Access Privacy (Allowlist)](#chat-access-privacy-allowlist)
- [Voice Transcription](#voice-transcription)
- [Event Feed](#event-feed)
- [Docker](#docker)
- [Environment Variables](#environment-variables)
- [Known Gaps](#known-gaps)
- [Development](#development)
- [Security Notes](#security-notes)
- [Troubleshooting](#troubleshooting)
- [License](#license)

## What It Can Do

132 tools across nine domains. Every tool body is a formatted-string return, never a crash: failures come back as readable text plus an error code, and every call is bounded by `TELEGRAM_TOOL_TIMEOUT_SECONDS`.

| Domain | Tools | What it covers |
|---|---|---|
| Messages | 34 | send, reply, edit, delete, forward, pin, schedule, polls, reactions, search, context inspection, inline-button inspection and callback presses, rich formatting |
| Groups | 25 | create groups/channels, join/leave, invite/remove members, admins, bans, default permissions, slow mode, forum topics, invite links, member admin status |
| Chats | 19 | list chats, metadata, archive, pin, read receipts, common chats, direct-chat lookup, `t.me` links |
| Contacts | 17 | list, search, add, delete, block/unblock, contact aliases, fuzzy name matching, contact sheet |
| Media | 13 | download/send photos, documents, video, voice notes; contact sheets; path-gated file access |
| Profile | 11 | own profile, name/about/bio updates, profile photo, privacy settings |
| Folders | 7 | list, create, rename, delete, add/remove chats |
| Events | 5 | incoming-message event feed for callback-style clients |
| Accounts | 1 | `list_accounts`, plus per-call routing by account label |

55 of the 132 tools are read-only; `TELEGRAM_EXPOSED_TOOLS=read-only` prunes the other 77 from the surface (see [Known Gaps](#known-gaps) for the current cutover status).

### Rich formatting

`send_message`, `reply_to_message`, and `edit_message` accept `parse_mode=md` / `html` (classic Telegram markup) and `parse_mode=rich` / `rich_markdown` / `rich_html`, which allow full Markdown and HTML — tables, headings, formulas, collapsible sections. Rich modes require Telegram Premium on the sending account; Premium is re-checked on every call and, without it, nothing is sent and the tool returns a structured `telegram_premium_required` result so the agent can reformat with a classic mode and retry.

### Prompt-injection sanitization

Every piece of Telegram-sourced text that reaches a tool result passes through the sanitizer in `internal/kit` first (`SanitizeUserContent`, `SanitizeName`, `SanitizeDict`): control characters, zero-width joiners and bidi overrides are stripped, and lengths are clamped. Message bodies, contact names, button labels and chat names are all covered.

## Requirements

- Go 1.27+ (to build from source), or Docker.
- Telegram `api_id` and `api_hash` from <https://my.telegram.org/apps>.
- An authenticated session: either a portable session string or a session file (see [CLI](#cli)).

## Install

### With Go

```bash
go install github.com/itokun99/telegram-mcp-go/cmd/telegram-mcp-go@latest
```

The binary lands in `$(go env GOPATH)/bin/telegram-mcp-go`.

### With Homebrew

```bash
brew install itokun99/telegram-mcp-go/tap/telegram-mcp-go
```

The tap is published alongside the tagged release; until it exists, use `go install` above.

### From source

```bash
git clone https://github.com/itokun99/telegram-mcp-go.git
cd telegram-mcp-go
go build -trimpath -o telegram-mcp-go ./cmd/telegram-mcp-go
```

## Quick Start

### 1. Get API credentials

Create an application at <https://my.telegram.org/apps> and copy `api_id` (a number) and `api_hash` (a string).

### 2. Write `.env`

```bash
TELEGRAM_API_ID=123456
TELEGRAM_API_HASH=0123456789abcdef0123456789abcdef
```

Full variable reference: [Environment Variables](#environment-variables). `.env` is read once at start-up; every value can also be supplied as a real environment variable, which wins over the file in most launchers.

### 3. Create a session

Interactive login runs as a local CLI, never inside the server process:

```bash
telegram-mcp-go gen-session
```

It prints a portable session string. Put it in `.env`:

```bash
TELEGRAM_SESSION_STRING=<printed session string>
```

Already have a session string from the pre-Go implementation? Convert it instead of logging in again:

```bash
telegram-mcp-go migrate-session --in "<old session string>" --out ./migrate
```

### 4. Run the server

```bash
telegram-mcp-go              # stdio transport, the default
MCP_TRANSPORT=http telegram-mcp-go   # streamable HTTP on 127.0.0.1:8765
```

Check the served surface without touching Telegram:

```bash
telegram-mcp-go --dry-run    # sorted JSON tool list, exit 0
```

### 5. Point an MCP client at it

See [MCP Client Configuration](#mcp-client-configuration).

## CLI

One binary, three modes.

### `telegram-mcp-go` — the server

```
telegram-mcp-go [-dry-run]
```

| Flag | Effect |
|---|---|
| `-dry-run` | Print the served tool list as sorted JSON and exit 0. No Telegram connection, no transport, no credentials needed beyond a parseable config. |

Everything else is configured through the environment: `MCP_TRANSPORT`, `MCP_HOST`, `MCP_PORT`, `TELEGRAM_EXPOSED_TOOLS`, `TELEGRAM_TOOL_TIMEOUT_SECONDS`. The server exits non-zero with a single-line message on stderr when the configuration is invalid (see [fail-loud](#fail-loud-vs-default) below) and `0` on a clean SIGINT/SIGTERM shutdown.

### `telegram-mcp-go gen-session` — create a session string

```
telegram-mcp-go gen-session
```

Drives gogram's phone/code login flow and prints the resulting portable session string. It reads `TELEGRAM_API_ID` and `TELEGRAM_API_HASH` from the environment, requires a terminal (it refuses to run without a TTY, because the interactive flow must never share a process with an MCP transport), and writes nothing to disk — pipe the output into your `.env`.

### `telegram-mcp-go migrate-session` — convert a legacy session string

```
telegram-mcp-go migrate-session --in "<session string>" --out ./migrate
```

Decodes a pre-Go session string, rebuilds the equivalent gogram session (DC id, address, port, auth key, user id, app id), and writes `<out>_<label>.session` plus the printable session string. Use it to keep an existing login instead of re-authenticating. No network access is required.

## MCP Client Configuration

### stdio (recommended for desktop clients)

The client launches the binary; nothing listens on a port.

```json
{
  "mcpServers": {
    "telegram": {
      "command": "telegram-mcp-go",
      "env": {
        "TELEGRAM_API_ID": "123456",
        "TELEGRAM_API_HASH": "0123456789abcdef0123456789abcdef",
        "TELEGRAM_SESSION_STRING": "<session string>"
      }
    }
  }
}
```

If the binary is not on the client's `PATH`, give an absolute path (the Docker form is `"command": "docker", "args": ["run", "-i", "--rm", "--env-file", ".env", "telegram-mcp-go"]`).

**Claude Desktop** — `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS), `%APPDATA%\Claude\claude_desktop_config.json` (Windows), `~/.config/Claude/claude_desktop_config.json` (Linux).

**Cursor** — `.cursor/mcp.json` in the project, or the global `~/.cursor/mcp.json`.

### http (recommended for a shared, long-lived server)

```bash
MCP_TRANSPORT=http MCP_HOST=127.0.0.1 MCP_PORT=8765 telegram-mcp-go
```

```json
{
  "mcpServers": {
    "telegram": {
      "type": "http",
      "url": "http://127.0.0.1:8765"
    }
  }
}
```

The HTTP transport is stateless: long-lived clients survive a server restart instead of losing their session ID.

### Claude Code

```bash
claude mcp add telegram -- telegram-mcp-go
claude mcp add --transport http telegram http://127.0.0.1:8765
```

### Codex

```toml
[mcp_servers.telegram]
command = "telegram-mcp-go"
env = { TELEGRAM_API_ID = "123456", TELEGRAM_API_HASH = "0123456789abcdef0123456789abcdef", TELEGRAM_SESSION_STRING = "<session string>" }
```

## Transports

| `MCP_TRANSPORT` | Behavior |
|---|---|
| `stdio` (default) | Newline-delimited JSON-RPC on stdin/stdout. One process per client. |
| `http` | Streamable HTTP bound to `MCP_HOST:MCP_PORT` (default `127.0.0.1:8765`), stateless. |
| `sse` | Legacy HTTP+SSE transport on the same address. |

Any other value aborts start-up. `MCP_ALLOWED_HOSTS` and `MCP_ALLOWED_ORIGINS` are parsed for the HTTP transports; they are not yet enforced by the listener — see [Known Gaps](#known-gaps). **Bind to loopback** unless you put an authenticating reverse proxy in front of the endpoint: the MCP endpoint itself is unauthenticated.

## Multi-Account Setup

Give each account a label by suffixing its variables with `_<LABEL>` (uppercase). Labels are discovered from the credentials themselves, so declaring a session for a label creates the account.

```bash
TELEGRAM_API_ID=123456
TELEGRAM_API_HASH=0123456789abcdef0123456789abcdef

TELEGRAM_SESSION_STRING_PERSONAL=<string>
TELEGRAM_EXPECTED_USERNAME_PERSONAL=@personal_account

TELEGRAM_SESSION_STRING_WORK=<string>
TELEGRAM_PROXY_TYPE_WORK=socks5
TELEGRAM_PROXY_HOST_WORK=127.0.0.1
TELEGRAM_PROXY_PORT_WORK=1080
```

Tool calls take an `account` argument naming the label; omitting it routes to the default account. `list_accounts` returns the labels in discovery order together with each account's own profile.

The shared API ID/hash apply to every account; only session, expected-username and proxy variables are per-label. Declaring a session for a label with no matching API credentials fails loudly at start-up.

### Session pool

One account, several concurrent clients:

```bash
# Whitespace, comma or semicolon separated; the first free entry is claimed.
TELEGRAM_SESSION_STRINGS=<string 1> <string 2> <string 3>
```

Each pooled string is claimed through an advisory lock, so two server processes sharing one account take different logins instead of colliding on the same session.

### Session lock modes

| `TELEGRAM_SESSION_LOCK` | Effect |
|---|---|
| `exclusive` (default) | One server process per session; others wait up to `TELEGRAM_LOCK_GRACE_SECONDS` (default 20) and then exit. |
| `shared` | Multiple processes may hold the same session. |

Any other value aborts start-up. A session file (`TELEGRAM_SESSION_NAME`, default `telegram_session`) is used when no session string is set; a session string always takes precedence.

## Device Identity

Telegram shows these three strings in the active-sessions list. They default to the host platform values; override them to make a server's sessions recognizable:

```bash
TELEGRAM_DEVICE_MODEL=My Server
TELEGRAM_SYSTEM_VERSION=Linux
TELEGRAM_APP_VERSION=1.0
```

## Expected Account Check

A session string is a bearer credential: whoever holds it owns the account. Pin the session to the account you expect, and the server refuses to start otherwise:

```bash
TELEGRAM_EXPECTED_USERNAME=@my_account          # global
TELEGRAM_EXPECTED_USERNAME_WORK=@work_account  # per label
```

The check is disabled while unset.

## Proxy Support

```bash
TELEGRAM_PROXY_TYPE=socks5      # socks5 | socks4 | http | mtproxy
TELEGRAM_PROXY_HOST=127.0.0.1
TELEGRAM_PROXY_PORT=1080
TELEGRAM_PROXY_USERNAME=user    # optional
TELEGRAM_PROXY_PASSWORD=pass    # optional
TELEGRAM_PROXY_RDNS=true        # default true
TELEGRAM_PROXY_SECRET=...       # required for mtproxy (hex secret)
```

Prefix any of these with the account label for a per-account proxy (`TELEGRAM_PROXY_TYPE_WORK`, `TELEGRAM_PROXY_HOST_WORK`, ...). A malformed proxy setting aborts start-up rather than silently connecting directly.

## File Path Security

File tools never touch a path the operator has not allowed. Roots come from two places:

- **Client roots** — the MCP `roots/list` result from the connected client.
- **Server roots** — `TELEGRAM_ALLOWED_ROOTS`, a delimiter-separated path list.

Client roots replace server roots. Every unusable state (no roots, a client that does not implement roots, a roots call that times out) ends in deny-all unless you opt in explicitly:

```bash
TELEGRAM_ALLOWED_ROOTS=/data/media:/srv/downloads   # explicit server roots
TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK=true          # server roots may replace an empty client list
TELEGRAM_SERVER_ROOTS_ONLY=true                    # use server roots, skip roots/list entirely
TELEGRAM_ROOTS_TIMEOUT_SECONDS=10                  # bound the roots/list wait
```

Path traversal (`../`) and symlink escapes are rejected; the failure messages are stable strings, so a client can react to them. Accepted extensions per tool are narrowed with `TELEGRAM_FILE_EXTENSIONS`:

```bash
# tool:.ext,.ext entries, semicolon separated, case-insensitive, leading dot optional
TELEGRAM_FILE_EXTENSIONS="download_media:.jpg,.mp4;send_document:.pdf"
```

## Chat Access Privacy (Allowlist)

Restrict every chat-addressed tool to an explicit set of chats. `-100…` supergroup ids and `@usernames` are accepted; a malformed id aborts start-up.

```bash
TELEGRAM_ALLOWED_CHAT_IDS=123456789,-1001234567890,@my_channel
```

Leave it unset to disable the gate.

## Voice Transcription

`TELEGRAM_TRANSCRIBE` selects when transcription happens: `off`, `on-demand` (the default — the agent asks for a transcript) or `auto` (prefetch within a per-call budget). `TELEGRAM_TRANSCRIBE_ENGINE` selects the engine:

| Engine | Requirements |
|---|---|
| `telegram` | none — Telegram's server-side transcription of voice notes and video |
| `groq` (default) | `GROQ_API_KEY` |
| `openai` | `TELEGRAM_TRANSCRIBE_OPENAI_URL` (+ optional `TELEGRAM_TRANSCRIBE_OPENAI_API_KEY` for keyless local servers) |
| `whisper` | `TELEGRAM_WHISPER_BASE_URL` — see [Known Gaps](#known-gaps) |

```bash
TELEGRAM_TRANSCRIBE=auto
TELEGRAM_TRANSCRIBE_ENGINE=groq
GROQ_API_KEY=gsk_...
TELEGRAM_TRANSCRIBE_LANGUAGE=en        # ISO-639-1 hint; unset = auto-detect
TELEGRAM_TRANSCRIBE_TIMEOUT=120        # seconds, HTTP engines
```

Transcripts are cached under `TELEGRAM_TRANSCRIPT_CACHE_DIR` (default `data/transcripts`) in a JSON file with `0700`/`0600` permissions. The cache is keyed by source, so a row written by one engine is never served to another; entries never expire. An existing cache from the pre-Go implementation is **not** readable — see [Known Gaps](#known-gaps).

Transcripts are machine readings, not quotes: results always carry `source` next to `text`.

## Event Feed

`TELEGRAM_EVENT_FEED=true` lets a callback-capable client receive incoming messages as events instead of polling. The feed is appended to `TELEGRAM_EVENT_FEED_FILE` (default under `$XDG_STATE_HOME`, else `~/.local/state`), and `TELEGRAM_ALIASES_FILE` points at an aliases JSON file that resolves chat nicknames to ids.

## Docker

```bash
cp .env.example .env      # fill in api_id / api_hash / session string
docker compose up --build -d
curl http://127.0.0.1:8765   # MCP endpoint, if MCP_TRANSPORT=http
```

The image is a two-stage build: `golang:1.27` compiles a static, `CGO_ENABLED=0`, `-trimpath` binary; the runtime stage is `gcr.io/distroless/static-debian12:nonroot` — distroless rather than `scratch` because gogram needs a CA bundle to verify TLS against Telegram. The image runs as uid/gid `65532` and ships no shell.

`docker-compose.yml` serves streamable HTTP on `127.0.0.1:8765` with `MCP_HOST=0.0.0.0` inside the container, and persists two paths:

| Host path | Container path | Why |
|---|---|---|
| `./transcript_cache` | `/app/data/transcripts` | transcript cache; otherwise lost on every rebuild. Prepare it with `mkdir -p ./transcript_cache && chown 65532:65532 ./transcript_cache` |
| `./telegram_sessions` | `/app/telegram_sessions` | session files — uncomment the volume when you do not supply `TELEGRAM_SESSION_STRING` |

The transcript cache holds plaintext transcripts of your personal chats. Give it its own backup and retention policy rather than letting it ride along with a general backup.

## Environment Variables

### Fail-loud vs default

Two failure modes, and the difference matters when you debug a start-up that refuses to boot:

- **Fail-loud** — a malformed or missing value aborts start-up with a one-line error on stderr and a non-zero exit. Used for anything whose bad value would silently weaken security or route traffic somewhere wrong.
- **Default** — an invalid or empty value falls back to the documented default.

### Credentials and sessions

| Variable | Default | Behavior |
|---|---|---|
| `TELEGRAM_API_ID` | — | **Required.** The numeric `api_id`; a missing or non-integer value fails loud. |
| `TELEGRAM_API_HASH` | — | **Required.** `api_hash` string. |
| `TELEGRAM_SESSION_STRING` | — | Portable session string; takes precedence over the session file. |
| `TELEGRAM_SESSION_NAME` | `telegram_session` | Session file name, used when no session string is set. |
| `TELEGRAM_SESSION_STRINGS` | — | Pool of session strings (whitespace, comma or semicolon separated). Takes precedence over `TELEGRAM_SESSION_STRING`; the first free entry is claimed under an advisory lock. |
| `TELEGRAM_SESSION_LOCK` | `exclusive` | `exclusive` or `shared`; anything else fails loud. |
| `TELEGRAM_LOCK_GRACE_SECONDS` | `20` | Seconds to wait for a locked session before exiting; non-integer fails loud. |
| `TELEGRAM_EXPECTED_USERNAME` | — | Refuse to start unless the session belongs to this account. |
| `TELEGRAM_DEVICE_MODEL` | platform default | Device string shown in active sessions. |
| `TELEGRAM_SYSTEM_VERSION` | platform default | OS string shown in active sessions. |
| `TELEGRAM_APP_VERSION` | platform default | App string shown in active sessions. |
| `TELEGRAM_SESSION_STRING_<LABEL>` | — | Per-account session string; the label is discovered from these variables. |
| `TELEGRAM_SESSION_NAME_<LABEL>` | `telegram_session_<LABEL>` | Per-account session file. |
| `TELEGRAM_EXPECTED_USERNAME_<LABEL>` | — | Per-account expected-username check. |

### Tool surface

| Variable | Default | Behavior |
|---|---|---|
| `TELEGRAM_EXPOSED_TOOLS` | `all` | `all`, `read-only`, or `read-only+tool,tool` to re-add named writes. An unknown tool name or a malformed value fails loud. |
| `TELEGRAM_FILE_EXTENSIONS` | per-tool defaults | `tool:.ext,.ext` entries separated by `;`. Case-insensitive, leading dot optional; a duplicate tool or malformed extension fails loud. |
| `TELEGRAM_TOOL_TIMEOUT_SECONDS` | `55` | Per-call ceiling. `0` or negative disables it. Non-numeric falls back to the default. |
| `TELEGRAM_FLOOD_SLEEP_THRESHOLD` | `60` | Seconds a `FloodWait` may sleep before the tool fails instead; non-integer fails loud. |

### File access

| Variable | Default | Behavior |
|---|---|---|
| `TELEGRAM_ALLOWED_ROOTS` | — | Delimiter-separated path list; empty means deny-all. |
| `TELEGRAM_ALLOW_SERVER_ROOTS_FALLBACK` | `false` | Allow server roots to replace an empty or unusable client roots list. |
| `TELEGRAM_SERVER_ROOTS_ONLY` | `false` | Use server roots and skip the `roots/list` request. |
| `TELEGRAM_ROOTS_TIMEOUT_SECONDS` | — | Bound the `roots/list` wait; non-integer fails loud. |

### Privacy

| Variable | Default | Behavior |
|---|---|---|
| `TELEGRAM_ALLOWED_CHAT_IDS` | — | Comma-separated chat ids, `-100…` supergroup ids and `@usernames`. A malformed id fails loud. |

### Proxy

| Variable | Default | Behavior |
|---|---|---|
| `TELEGRAM_PROXY_TYPE` | — | `socks5`, `socks4`, `http` or `mtproxy`; anything else fails loud. |
| `TELEGRAM_PROXY_HOST` | — | Proxy host. |
| `TELEGRAM_PROXY_PORT` | — | Proxy port; non-integer fails loud. |
| `TELEGRAM_PROXY_USERNAME` | — | Proxy username. |
| `TELEGRAM_PROXY_PASSWORD` | — | Proxy password. |
| `TELEGRAM_PROXY_RDNS` | `true` | Remote DNS resolution through the proxy. |
| `TELEGRAM_PROXY_SECRET` | — | Hex secret; required when the type is `mtproxy`. |
| `TELEGRAM_PROXY_<NAME>_<LABEL>` | — | Per-account override of any of the above. |

### Transcription

| Variable | Default | Behavior |
|---|---|---|
| `TELEGRAM_TRANSCRIBE` | `on-demand` | `off`, `on-demand` or `auto`; anything else fails loud. |
| `TELEGRAM_TRANSCRIBE_ENGINE` | `groq` | `telegram`, `groq`, `openai` or `whisper`; anything else fails loud. |
| `GROQ_API_KEY` | — | Required whenever the `groq` engine resolves. |
| `TELEGRAM_TRANSCRIBE_LANGUAGE` | auto | ISO-639-1 lowercase language hint. |
| `TELEGRAM_TRANSCRIBE_TIMEOUT` | `120` | Seconds for HTTP engines; non-integer fails loud. |
| `TELEGRAM_TRANSCRIBE_OPENAI_URL` | — | Base URL or full `/audio/transcriptions` URL; required for `engine=openai`. |
| `TELEGRAM_TRANSCRIBE_OPENAI_API_KEY` | — | Optional key, for keyless local servers. |
| `TELEGRAM_TRANSCRIBE_OPENAI_MODEL` | engine default | Model name. |
| `TELEGRAM_TRANSCRIBE_OPENAI_MAX_MB` | `25` | Upload ceiling in MB. |
| `TELEGRAM_TRANSCRIBE_GROQ_MAX_MB` | `25` | Upload ceiling in MB for the Groq engine. |
| `TELEGRAM_TRANSCRIBE_WHISPER_MODEL` | `small` | Model name for the `whisper` engine. |
| `TELEGRAM_TRANSCRIBE_WHISPER_DEVICE` | `auto` | `auto`, `cpu` or `cuda`; anything else fails loud. |
| `TELEGRAM_TRANSCRIBE_WHISPER_COMPUTE_TYPE` | engine default | e.g. `int8`, `float16`. |
| `TELEGRAM_TRANSCRIBE_WHISPER_MODEL_DIR` | — | Model cache location. |
| `TELEGRAM_TRANSCRIBE_MAX_VOICES` | `5` | Per-call prefetch budget in `auto` mode. |
| `TELEGRAM_TRANSCRIBE_MAX_SECONDS` | `300` | Per-call prefetch budget in `auto` mode. |
| `TELEGRAM_TRANSCRIPT_CACHE_DIR` | `data/transcripts` | Cache directory (mode `0700`, file `0600`). |

### Events, aliases and misc

| Variable | Default | Behavior |
|---|---|---|
| `TELEGRAM_EVENT_FEED` | `false` | Enable the incoming-event feed. |
| `TELEGRAM_EVENT_FEED_FILE` | under `$XDG_STATE_HOME` | Event feed file path. |
| `XDG_STATE_HOME` | `~/.local/state` | Base directory for state files. |
| `TELEGRAM_ALIASES_FILE` | — | Aliases JSON under the state directory; a missing file means no aliases. |
| `TELEGRAM_CONTACT_FUZZY` | `true` | Fuzzy contact-name matching. |
| `TELEGRAM_LINK_DOMAIN` | `t.me` | Domain used to build message links. |
| `TELEGRAM_MCP_ERROR_LOG` | `<project root>/mcp_errors.log` | Override the error-log path. |
| `TELEGRAM_MCP_PROJECT_ROOT` | nearest ancestor with `go.mod` | Override the project root used to place `mcp_errors.log`. |

### Transport

| Variable | Default | Behavior |
|---|---|---|
| `MCP_TRANSPORT` | `stdio` | `stdio`, `http` or `sse`; anything else fails loud. |
| `MCP_HOST` | `127.0.0.1` | Bind address for the HTTP transports. |
| `MCP_PORT` | `8765` | Bind port; non-integer fails loud. |
| `MCP_ALLOWED_HOSTS` | — | Comma-separated hosts, `:*` port suffix allowed. |
| `MCP_ALLOWED_ORIGINS` | — | Comma-separated origins. |

## Known Gaps

Everything in this section is a real deviation from the behavior the pre-Go implementation had. Each one is deliberate or unfinished, not a bug to file.

### 1. Cutover status: session boot and CLI subcommands

The MTProto layer (`internal/session`: connect, advisory locks, session pool, device identity, expected-username check) is complete and unit-tested, but the boot path does not open a session yet — `mcpserver.Main` loads the configuration, builds the server and serves the tool surface, and every tool that needs a client answers with a not-connected error. The single-binary entry point (`telegram-mcp-go` as the server action, plus the `gen-session` and `migrate-session` subcommands) lands with the same cutover; the flag surface of those subcommands may still move before the tag.

### 2. Local whisper is now an OpenAI-compatible endpoint

The pre-Go implementation loaded a faster-whisper model in-process: the audio never left the machine, and the model/device/compute-type variables selected a local runtime. Go has no in-process equivalent that could be added without new dependencies, so `engine=whisper` now posts to an OpenAI-compatible `/audio/transcriptions` endpoint:

```bash
TELEGRAM_TRANSCRIBE_ENGINE=whisper
TELEGRAM_WHISPER_BASE_URL=http://localhost:8000/v1   # required
TELEGRAM_WHISPER_MODEL=small                          # defaults to TELEGRAM_TRANSCRIBE_WHISPER_MODEL
TELEGRAM_WHISPER_DEVICE=auto                          # defaults to TELEGRAM_TRANSCRIBE_WHISPER_DEVICE
```

Audio leaves the machine unless that endpoint is itself local. `TELEGRAM_TRANSCRIBE_WHISPER_COMPUTE_TYPE` and `..._MODEL_DIR` are parsed but have no local runtime to configure, and the "faster-whisper is not installed" message is replaced by base-URL validation. Use `engine=telegram` when you want transcription that never leaves Telegram's servers.

### 3. The transcript cache is a JSON file

The cache is `data/transcripts/transcripts.json` (mode `0600`) instead of a SQLite database, because no SQLite driver could be added to the module. Semantics match: the key is pinned by source, an unpinned read prefers the default engine's row and then the newest, and entries never expire. An existing cache from the pre-Go implementation is **not** readable — the first run after upgrading re-transcribes everything once.

### 4. Policy parsed at start-up is not yet installed into the tools

`internal/config` parses and validates `TELEGRAM_ALLOWED_ROOTS`, `TELEGRAM_ALLOWED_CHAT_IDS`, the file-extension overrides and the transcription/event-feed settings, but the boot path does not install them into the tool-layer dependency structs yet. Until it does:

- file-path tools deny every path (the gate falls back to its unconfigured state, which is deny-all);
- `TELEGRAM_ALLOWED_CHAT_IDS` does not restrict chat-addressed tools.

Both fail closed, and both are on the wiring list for the cutover. Do not rely on them as a security boundary until the wiring lands.

### 5. `MCP_ALLOWED_HOSTS` / `MCP_ALLOWED_ORIGINS` are parsed but not enforced

The two variables are read at start-up, but the HTTP listener does not apply them to incoming requests, so there is no DNS-rebinding guard yet. Bind to `127.0.0.1`, or put an authenticating reverse proxy in front of the endpoint.

### 6. No MCPB desktop bundle

The `.mcpb` desktop extension manifest is not part of the Go build. Configure the client with a config file (see [MCP Client Configuration](#mcp-client-configuration)); there is no double-click installer.

### 7. The SDK is not confined to `internal/kit` and `internal/session`

`internal/tools/messages` and `internal/tools/groups` import gogram directly for their domain adapters (message objects, RPC params), and their tests build gogram fixtures. Tool *bodies* still run against interface seams with injected fakes, so the offline test suite exercises the real logic; the layering rule "the SDK lives behind a seam" is upheld in spirit but not literally. This deviation is on the record in `.omo/go-port/verify-tools-verdict.md` and waived in `.omo/go-port/waiver-launch-run3.md`.

## Development

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

The whole suite is offline: `t.TempDir()`, `httptest` for HTTP engines, and injected fakes for gogram. No test opens a network connection or a real Telegram session.

```
cmd/telegram-mcp-go/     process entry point
internal/config/         environment parsing and validation (fail-loud rules)
internal/session/        MTProto sessions: connect, advisory locks, pool, device identity
internal/kit/            shared helpers: sanitizer, error funnel, aliases, rich text,
                         photo sources, contact sheets, transcription, kit/paths (path gate)
internal/mcpserver/      MCP boot: registry, exposure pruning, transports, --dry-run
internal/tools/          the 132 tools, one package per domain
```

Adding a tool: create or extend a package under `internal/tools/`, register it with `mcpserver.RegisterTool[In]` (typed input struct — the SDK derives the JSON schema from it), set the safety annotations honestly (`ReadOnly` is what `TELEGRAM_EXPOSED_TOOLS=read-only` keys on), and return the formatted error string instead of panicking. `internal/mcpserver/tool_surface_test.go` asserts the registered set against `.omo/go-port/parity/inventory.json`, so a rename or a missing registration fails the build's tests.

## Security Notes

- **The session string is a bearer credential.** Anyone holding it owns the account. Pin it with `TELEGRAM_EXPECTED_USERNAME`, keep it out of version control, and rotate it from the Telegram app's active-session list if it leaks.
- **Tool annotations are a security boundary.** `TELEGRAM_EXPOSED_TOOLS=read-only` prunes every tool without `readOnlyHint`; a wrong annotation is a real leak, not a cosmetic hint.
- **File access is deny-all by default.** Roots come from the client or from `TELEGRAM_ALLOWED_ROOTS`, and every other state is a refusal.
- **Error strings are written for a model, not a human.** `FloodWait` and privacy denials return prose that says what happened and what not to do; they never echo message content, ids or paths into the logs.
- **The HTTP endpoint is unauthenticated.** Loopback or a reverse proxy only.
- **Prompt injection is sanitized, not solved.** Control characters, zero-width and bidi overrides are stripped from Telegram-sourced text before it reaches the model; the model still reads untrusted content and should be told so.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `invalid TELEGRAM_API_ID '...'` at start-up | The value is missing or not an integer. Use the numeric `api_id`, not the hash. |
| `invalid MCP_TRANSPORT '...'` | One of `stdio`, `http`, `sse` only. |
| `invalid TELEGRAM_EXPOSED_TOOLS ...` | Use `all`, `read-only`, or `read-only+name,name` with real tool names. |
| Start-up exits immediately with a lock timeout | Another process holds the session. Wait, or set `TELEGRAM_SESSION_LOCK=shared`. |
| `TELEGRAM_WHISPER_BASE_URL is not configured...` | `engine=whisper` needs an OpenAI-compatible endpoint; see [Known Gaps](#known-gaps). |
| `GROQ_API_KEY is not configured...` | Set the key or switch `TELEGRAM_TRANSCRIBE_ENGINE`. |
| Every tool answers "not connected" | The session boot is not wired yet; see [Known Gaps](#known-gaps). |
| File tools deny every path | Roots are not configured (or not installed yet); see [Known Gaps](#known-gaps). |
| Clients time out against the HTTP endpoint | The server binds `127.0.0.1` by default; set `MCP_HOST=0.0.0.0` inside a container. |

## License

Apache 2.0 — see [LICENSE](LICENSE).

Portions of the behavior implemented here originate from the Telegram MTProto ecosystem; Telegram itself is not affiliated with this project.

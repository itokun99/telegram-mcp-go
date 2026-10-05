import json, os
R = "/Users/aleph/Projects/telegram-mcp-go"
dags = os.path.join(R, ".omo/dags")
os.makedirs(dags, exist_ok=True)

PRE = """REPO: /Users/aleph/Projects/telegram-mcp-go. Python "telegram-mcp" (Telethon + FastMCP, 132 MCP tools) is being ported to Go. go1.27.1 at /opt/homebrew/bin/go.
go.mod pins github.com/amarnathcjd/gogram v1.7.71 (IMPORT PATH IS LOWERCASE amarnathcjd - the capitalized form is rejected by the Go module proxy; never use it) and github.com/modelcontextprotocol/go-sdk v1.8.0.
gogram v1.7.71 source: /tmp/gogram-src/github.com/Amarnathcjd/gogram@v1.7.71 (re-download if missing: curl -sL https://proxy.golang.org/github.com/!amarnathcjd/gogram/@v/v1.7.71.zip then unzip into /tmp/gogram-src/github.com/Amarnathcjd/). Grep it for exact signatures; do not guess.
Foundations exist (READ them, build on top, do NOT rewrite): internal/config (env config), internal/session (locks/pool/connect), internal/kit (helpers; path gate at internal/kit/paths), internal/mcpserver (MCP stdio/http boot + tool registration API).
HARD RULES: NEVER git commit/push. NO new go.mod dependencies. No panics in library paths; tool bodies return formatted error strings (mirror how the Python logs errors - read telegram_mcp/logging.py shape). WRITE the code files NOW - investigate just enough to write, then write.
GATES before reporting done (all must exit 0): go build ./... ; go vet <your packages> ; go test <your packages> -count=1 (offline tests only: t.TempDir, fake HTTP via httptest, fake gogram client via interface injection - no network, no real Telegram connection).
"""

PRE_VERIFY = """REPO: /Users/aleph/Projects/telegram-mcp-go. You are the VERIFIER node: you do NOT fix code - you run gates and write a verdict file. One verdict line per gate: `PASS` or `FAIL <what>: <why>`. Then a final line `SUMMARY: N/M PASS`. Write your verdict to the given path. STOP WHEN the verdict file is written.
"""

def N(id, category, prompt, deps=None, label=None):
    d = {"id": id, "category": category, "prompt": prompt}
    if deps: d["dependsOn"] = deps
    if label: d["label"] = label
    return d

r2 = {
 "key": "go-port-runtime",
 "name": "Go port: runtime layer",
 "nodes": [
  N("rich","unspecified-high", PRE + "\n"
    "TASK: Port the Python rich-markup builder to internal/kit/rich.go (+ rich_test.go).\n"
    "Source of truth: make_rich_input and entity-building code in telegram_mcp/runtime.py (grep 'make_rich_input', 'entities') plus the render assertions in tests/.\n"
    "gogram facts: RichBuilder API in telegram/formatting.go (around L687): RichBuilderNewText, .Bolds/.Italics/.Codes/.Underlines/.Strikethroughs/.Spoilers/.InlineTexts; leaf builders RichPlain/RichBold/RichItalic/RichFixed/RichMarked/RichUnderline/RichStrike/RichEmpty; consumed by (*Client).SendRich(peer, *RichBuilder, opts...) / EditRich. SendOptions (telegram/messages.go L17-70) carries Entities []MessageEntity + ParseMode (markdown|html).\n"
    "DELIVERABLE: internal/kit/rich.go exposing BuildRich(text, parseMode string) returning {Message string; Entities []telegram.MessageEntity; Plain string} usable straight in SendOptions. Support: markdown, html, plain fallback, custom-emoji, blockquote/spoiler/mention/hashtag/inline-bot-link. Output must match the Python semantics byte-identical where Python tests assert rendered strings.\n"
    "SCOPE: internal/kit/rich*.go only.\n"
    "VERIFY: go test ./internal/kit/ -run Rich -count=1 green AND go build ./... exit 0; every rich_test.go case maps to a Python test case.", None, "rich markup"),
  N("aliases","quick", PRE + "\n"
    "TASK: Port alias + contact-fuzzy matching to internal/kit/alias.go (+ alias_test.go).\n"
    "Source of truth: TELEGRAM_ALIASES_FILE + TELEGRAM_CONTACT_FUZZY rows in .omo/go-port/env-spec.md and the alias/fuzzy code in telegram_mcp/runtime.py (grep 'alias', 'fuzzy'). internal/config already parses these envs - REUSE those structs, do not duplicate parsing.\n"
    "DELIVERABLE: an Aliases resolver (exact-file hit first, fuzzy fallback per Python semantics, case-folding per Python) with an API like Resolve(name string) (string, bool) that entity resolution (a later run) can consume, plus offline tests.\n"
    "SCOPE: internal/kit/alias*.go only.\n"
    "VERIFY: tests with t.TempDir alias files: exact hit / miss / fuzzy hit / case-fold; go test ./internal/kit/ -run Alias -count=1 green.", None, "aliases+fuzzy"),
  N("media-photo","quick", PRE + "\n"
    "TASK: Port contact-sheet + photo-source to internal/kit/contactsheet.go + internal/kit/photo.go (+ tests).\n"
    "Source of truth: telegram_mcp/contact_sheet.py (Pillow grid -> Go stdlib image/png + image/draw ONLY, no new deps; keep the 1:1 grid/label/size semantics of the Python) and telegram_mcp/photo_source.py (avatar/media source selection logic: GetPeerPhoto / PhotoID / file-id precedence - port the decision table verbatim; use gogram APIs for actual fetch: grep gogram source for (*Client).DownloadMedia / ResolveMedia / photo structs).\n"
    "SCOPE: internal/kit/contactsheet*.go, internal/kit/photo*.go only.\n"
    "VERIFY: offline tests: grid layout math over N sample in-memory images (no network), photo-source decision table branches; go test ./internal/kit/ -run 'ContactSheet|Photo' -count=1 green.", None, "contact sheet + photo source"),
  N("transcription","unspecified-high", PRE + "\n"
    "TASK: Port voice transcription to internal/kit/transcribe.go (+ transcribe_test.go).\n"
    "Source of truth: telegram_mcp/transcription.py + .omo/go-port/env-spec.md rows (TELEGRAM_TRANSCRIPTION_ENGINE; GROQ_API_KEY; MAX_VOICES / MAX_SECONDS batch budgets; TELEGRAM_TRANSCRIPT_CACHE_DIR; openai/whisper engine envs).\n"
    "Engines: groq (default; OpenAI-compatible voice endpoint), openai (whisper-1), telegram-native pipeline where Python does it server-side, and whisper as the DOCUMENTED EXCEPTION: Python's local faster-whisper has no Go equivalent - map engine 'whisper' to an OpenAI-compatible endpoint using TELEGRAM_WHISPER_BASE_URL / TELEGRAM_WHISPER_MODEL / TELEGRAM_WHISPER_DEVICE envs and mark it with a // KNOWN-GAP comment naming the exception.\n"
    "Cache: go.mod has NO sqlite driver and deps are forbidden - use a JSON-file cache in TELEGRAM_TRANSCRIPT_CACHE_DIR with the same hit/miss/expiry semantics as Python's SQLite cache; document the deviation in a comment.\n"
    "DELIVERABLE: Transcribe(ctx, opts) returning {Text string; Source string} with error strings IDENTICAL to Python's budget/validation messages (grep the exact strings in transcription.py).\n"
    "SCOPE: internal/kit/transcribe*.go only.\n"
    "VERIFY: offline tests with httptest fake backend + t.TempDir cache dir: cache hit, cache miss, budget-exceeded error string; go test ./internal/kit/ -run Transcribe -count=1 green.", None, "transcription engines"),
  N("device","quick", PRE + "\n"
    "TASK: Port device/client identity to internal/session/device.go (+ device_test.go).\n"
    "Source of truth: telegram_mcp/client_identity.py + .omo/go-port/env-spec.md (TELEGRAM_DEVICE_MODEL / TELEGRAM_DEVICE_SYSTEM_VERSION / TELEGRAM_DEVICE_APP_VERSION - default values MUST equal the Python defaults byte-for-byte; copy them from the Python source, not from memory).\n"
    "DELIVERABLE: DeviceConfig() returning gogram-compatible device fields (grep gogram source for the device/params struct used at client construction) populated with the Python defaults or env overrides.\n"
    "SCOPE: internal/session/device*.go only.\n"
    "VERIFY: test asserts every default string; go test ./internal/session/ -count=1 green.", None, "device identity"),
  N("verify-runtime","quick", PRE_VERIFY + "\n"
    "RUN these gates, capturing each result:\n"
    "1 go build ./...\n"
    "2 go vet ./internal/...\n"
    "3 go test ./internal/... -count=1\n"
    "4 grep internal/kit for the symbols BuildRich, the alias resolver, contactsheet grid builder, photo source resolver, Transcribe; grep internal/session for DeviceConfig.\n"
    "5 git diff -- go.mod go.sum must be empty (no dependency drift).\n"
    "Write your verdict to .omo/go-port/verify-runtime-verdict.md (one line per gate, then SUMMARY: N/M PASS).",
    ["rich","aliases","media-photo","transcription","device"], "verify runtime layer"),
 ],
}

TOOL_RULES = PRE + """
TOOL-WAVE RULES: (a) tool NAMES must match the module's entries in .omo/go-port/parity/inventory.json EXACTLY - read that file first, it is the authority; (b) register each tool through the internal/mcpserver registration API (grep internal/mcpserver for it; if a piece is missing add a minimal ADDITIVE helper to internal/mcpserver/tools.go - nothing else in that package); (c) input schema fields mirror the Python tool signatures (drop ctx/account params - Go takes them from kit ctx); readonly flags per the inventory entry; port the Python docstring Args section into the JSON schema descriptions; (d) body: kit helpers + gogram client via the internal/session/kit APIs - NEVER raise/panic: return formatted error strings mirroring the Python log_and_format_error output; (e) offline tests: fake gogram client behind an interface, assert every tool name in your module registers AND at least one behavior assertion per tool where cheap; no network.
"""
waves = [
  ("messages","unspecified-high",34),("groups","unspecified-high",25),("chats","unspecified-high",19),
  ("contacts","unspecified-high",17),("media","unspecified-high",13),("profile","quick",11),
  ("folders","quick",7),("events","quick",5),("accounts","quick",1),
]
nodes = []
for mod, cat, n in waves:
    prompt = (TOOL_RULES + "\n"
      + "TASK: Port the " + mod + " module (" + str(n) + " tools) into internal/tools/" + mod + "/" + mod + ".go (+ " + mod + "_test.go).\n"
      + "Find each tool's Python definition by grepping the tool names from .omo/go-port/parity/inventory.json (module " + mod + ") across main.py and telegram_mcp/ (definitions may live in telegram_mcp/tools/" + mod + ".py or inline in main.py/runtime.py - follow the name, not the filename).\n"
      + "SCOPE: internal/tools/" + mod + "/** plus additive helpers in internal/mcpserver/tools.go only.\n"
      + "VERIFY: your test asserts all " + str(n) + " tool names register; go build ./... exit 0; go test ./internal/tools/" + mod + "/ -count=1 green.")
    nodes.append(N(mod, cat, prompt, None, "tools: " + mod + " (" + str(n) + ")"))
nodes.append(N("verify-tools","quick", PRE_VERIFY + "\n"
    "RUN these gates, capturing each result:\n"
    "1 go build ./...\n"
    "2 go vet ./...\n"
    "3 go test ./internal/tools/... ./internal/mcpserver/ -count=1\n"
    "4 PARITY DIFF: enumerate every registered tool name via the internal/mcpserver registry (or by running a small in-process list - add a temporary TestToolSurface test in internal/mcpserver if needed and keep it), diff against .omo/go-port/parity/inventory.json (132 names). One FAIL line per missing/renamed tool: `FAIL <tool>: missing|renamed`.\n"
    "5 Grep-verify no gogram import inside internal/tools/** (tools must go through kit/session APIs only).\n"
    "Write your verdict to .omo/go-port/verify-tools-verdict.md (tool lines, then SUMMARY: N/132 PASS).",
    [m for m,*_ in waves], "verify tool surface (132)"))
r3 = {"key":"go-port-tools","name":"Go port: 132 tool waves","nodes":nodes}

r4 = {"key":"go-port-cutover","name":"Go port: cutover","nodes":[
 N("cli-sessions","unspecified-high", PRE + "\n"
    "TASK: Port session_string_generator.py + migrate_session.py into Go CLI subcommands under cmd/telegram-mcp-go/ (grep cmd/ for the existing main/subcommand pattern; ADD subcommands, do not rewrite existing wiring): `gen-session` and `migrate-session`.\n"
    "gen-session: Python forbids interactive code/phone auth over MCP - KEEP that restriction: only run when stdin is a TTY (detect), drive gogram phone auth (grep gogram source for the phone-auth + session export APIs), write <label>.session. TELEGRAM_API_ID/API_HASH from env (internal/config already parses them).\n"
    "migrate-session: parse a Telethon StringSession (base64 payload: 8-byte header [0x01 version + padding, 4-byte session id] then the packed fields dc/port/ip/authkey - read the exact unpack sequence from session_string_generator.py/migrate_session.py, and consult .omo/go-port/session-spec.md rejected-layouts table), map to a gogram session struct (grep gogram source for the Session struct + import/export APIs), write <out>_<label>.session.\n"
    "VERIFY: offline round-trip test: a fixed base64 Telethon-format fixture constant -> unpacked field values asserted -> gogram session built -> string export stable; no network; go test ./... green.\n"
    "SCOPE: cmd/telegram-mcp-go/** + internal/session/migrate*.go only.", None, "gen-session + migrate-session CLIs"),
 N("dockerfile","quick", PRE + "\n"
    "TASK: Replace the Python Dockerfile with a Go build: two-stage - golang:1.27 builder (CGO_ENABLED=0, -ldflags version stamp) -> scratch/distroless static final image, image name telegram-mcp-go; rewrite .dockerignore for Go (drop python artifacts, add go cache/paths); keep docker-compose.yml env wiring intact.\n"
    "Do NOT build the image (no network gate) - prove the binary compiles instead: go build -o /dev/null ./cmd/... exit 0 AND gofmt -l cmd internal clean.\n"
    "SCOPE: Dockerfile, .dockerignore, docker-compose.yml only.\n"
    "VERIFY: grep -ci python Dockerfile .dockerignore is 0; go build ./cmd/... exit 0.", None, "Go Dockerfile"),
 N("docs","quick", PRE + "\n"
    "TASK: Rewrite README.md + AGENTS.md for the Go product: install (brew/go), the gen-session + migrate-session CLI usage, the full env table sourced from .omo/go-port/env-spec.md, Docker usage, MCP client config snippets (stdio + http), and a Known Gaps section (local-whisper exception -> OpenAI-compatible endpoint; transcript cache is JSON-file not SQLite; any other deviation found in .omo/go-port/verify-*-verdict.md files).\n"
    "REMOVE every Python/pip/uv/Telethon reference.\n"
    "SCOPE: README.md + AGENTS.md only.\n"
    "VERIFY: grep -rEi 'telethon|fastmcp|(^|[^a-z])pip |(^|[^a-z])uv |python3?' README.md AGENTS.md prints nothing.", None, "README/AGENTS rewrite"),
 N("delete-python","quick", PRE + "\n"
    "TASK: Cutover delete - remove ALL Python source from the tree: git rm -r -f then rm -rf: telegram_mcp/  main.py  sanitize.py  migrate_session.py  session_string_generator.py  pyproject.toml  poetry.lock  requirements.txt  uv.lock  .python-version  __init__.py  and the Python test suite tests/ (check: if tests/ contains any Go tests keep those). KEEP: .omo/go-port/ (parity inventory + specs), cmd/, internal/, go.mod, go.sum, the NEW Go Dockerfile, rewritten README/AGENTS.md, CHANGELOG.md (ADD a v0.1.0 entry describing the Go port), manifest.json/claude_desktop_config.json ONLY if they reference the Go binary.\n"
    "VERIFY: find . -name '*.py' -not -path './.git/*' -not -path './.omo/*' prints NOTHING; go build ./... still exit 0; git status shows the staged deletions (do NOT commit - the orchestrator commits).\n"
    "SCOPE: deletions only + CHANGELOG.md addition.", ["cli-sessions","dockerfile","docs"], "delete Python source"),
 N("verify-cutover","quick", PRE_VERIFY + "\n"
    "RUN these gates, capturing each result:\n"
    "1 go build ./...\n"
    "2 go vet ./...\n"
    "3 go test ./... -count=1\n"
    "4 find . -name '*.py' -not -path './.git/*' -not -path './.omo/*' must print nothing\n"
    "5 grep -ci python Dockerfile .dockerignore is 0\n"
    "6 grep -rEi 'telethon|fastmcp' README.md AGENTS.md prints nothing\n"
    "7 .omo/go-port/parity/inventory.json still present with 132 tool entries\n"
    "8 git status shows only: staged Python deletions + new Go artifacts (no commit yet - orchestrator commits)\n"
    "Write your verdict to .omo/go-port/verify-cutover-verdict.md (one line per gate, then SUMMARY: N/8 PASS).", ["delete-python"], "verify cutover"),
]}

for name, obj in (("go-port-runtime", r2), ("go-port-tools", r3), ("go-port-cutover", r4)):
    p = os.path.join(dags, name + ".json")
    json.dump(obj, open(p, "w"), indent=1)
    print(name + ".json ->", os.path.getsize(p), "bytes, nodes:", len(obj["nodes"]))
total = sum(n for _,_,n in waves)
print("wave tool count:", total, "(expect 132)")

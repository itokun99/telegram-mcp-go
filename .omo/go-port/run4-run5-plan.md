# Run 4 — cutover (stage; start after Run 3 verify PASSes)

Nodes (disjoint scopes, all depend on a "run3-verify" gate node first):
1. cli-sessions (unspecified-high): port session_string_generator.py + migrate_session.py -> cmd/telegram-mcp-go subcommands `gen-session` and `migrate-session`.
   - gen-session: interactive code/phone flow is NOT supported (Python forbids client.start() over MCP; keep the same limit) — accept TELEGRAM_API_ID/API_HASH, drive gogram phone auth ONLY when run as a local CLI with TTY (detect isatty), else error.
   - migrate-session: read Telethon StringSession (v1 layout: b64 of 8-byte header [0x01 version + pad, 4-byte session_id] + packed >iI s256s q i I for dc/port/ip/authkey/... per telethon/sessions/string.py — the session-spec.md rejected-layouts table records gogram's incompatible JSON layout) -> parse -> build gogram Session struct (DCID, IP, Port, AuthKey bytes, UserID, AppID) -> telegram.NewClient + ImportRawSession -> ExportStringSession -> write <out>_<label>.session.
   - VERIFY: offline round-trip test: Telethon-format fixture bytes -> gogram string session -> re-import ok; no network.
2. dockerfile (quick): replace Dockerfile (python:3.12-slim + pip install) with distroless/go-builder two-stage: golang:1.27 build -ldflags version -> static binary -> scratch/distroless. Update .dockerignore; keep image name telegram-mcp-go.
3. docs (quick): rewrite README.md + AGENTS.md: Go install (brew/go), gen-session CLI usage, env table (from .omo/go-port/env-spec.md), Docker usage, MCP client config (stdio/http). Remove all Python/pip/uv references.
4. delete-python (quick, runs LAST in run4): git rm -r telegram_mcp/ main.py pyproject.toml uv.lock Dockerfile.old tests/ (Python suites) + *.py tracked files. KEEP: .omo/go-port/ parity inventory, CHANGELOG.md (add v0.1.0 entry). VERIFY: git ls-files '*.py' prints nothing; go build ./... still green; grep -r 'Telethon\|fastmcp\|uv ' README.md AGENTS.md empty.
5. verify-cutover (quick): 8 gates: go build/vet/test green; ls-files no *.py; README no python refs; Dockerfile is go-based; .omo/go-port/parity/inventory.json still 132; git status clean-ish (only intended deletions).

# Run 5 — release (main session, no dag needed)
- git add -A; commit "Port to Go: gogram + MCP go-sdk, 132-tool parity" (footer per git_master config: no co-authored-by).
- push to origin main (itokun99/telegram-mcp-go).
- Detach fork relationship: gh api -X DELETE repos/itokun99/telegram-mcp-go/forks OR `gh api repos/itokun99/telegram-mcp-go -X PATCH -f parent=''` — note: GitHub does not support unforking via API; document the constraint and instead: rename default branch? No — use `gh repo` settings: set `gh api -X PATCH repos/... -f description=...` and leave fork flag if API-inaccessible; record limitation in release notes. (Check: as of 2026 GitHub still has no unfork API; the fork flag remains but description/readme detach the identity.)
- git tag -a v0.1.0 -m "telegram-mcp-go v0.1.0 — Go port, 132 tools"; push tag; gh release create v0.1.0 --title "telegram-mcp-go v0.1.0" --notes <release-notes.md: what changed, gogram session migration steps, transcription engine gaps (local whisper exception), env parity notes>.
- Final gate: remote verify `git ls-remote origin refs/tags/v0.1.0`.

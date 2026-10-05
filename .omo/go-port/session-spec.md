# Session semantics: Telethon -> gogram

Decision record for the Go port. Every gogram claim below was read out of v1.7.71
source, never docs — from the module cache at
`$(go env GOMODCACHE)/github.com/!amarnath!c!j!d/gogram@v1.7.71` and, independently,
from the unpacked tree at
`/tmp/gogram-src/github.com/AmarnathCjd/gogram@v1.7.71` (the two `internal/session`
copies are byte-identical; `diff -q` clean). Every Telethon claim comes from
`telethon/sessions/string.py` @ branch `v1`.

Import path is lowercase: `github.com/amarnathcjd/gogram` (module line in its
`go.mod`), so `telegram` = `github.com/amarnathcjd/gogram/telegram`.

## 0. Corrected premise: what gogram v1.7.71 actually offers

The task brief said gogram "has a `session.Manager` with SQLite/JSON storages and
`ImportSession`/`ExportSession`". That is **false for v1.7.71**. Verified:

| Claim | Reality in v1.7.71 |
|---|---|
| `session.Manager` | does not exist (`grep -rn 'SessionManager' -> 0 hits`) |
| SQLite session storage | does not exist (no sqlite driver, no sqlite code) |
| `ImportSession` / `ExportSession` | exist only as **methods on `*telegram.Client`** (`telegram/client.go:871-910`) |

**Importable (public) API** — this is the whole surface we may use:

- `telegram.NewClient(telegram.ClientConfig) (*telegram.Client, error)`
- `telegram.NewClientConfigBuilder(appID int32, appHash string) *ClientConfigBuilder`
  with `.WithSession(path)`, `.WithSessionAESKey(string)`, `.WithStringSession(string)`,
  `.WithMemorySession()`
- `telegram.Session{Key, Hash []byte; Salt int64; Hostname string; AppID int32}`
  (`telegram/helpers.go:1840`) with `(*telegram.Session).Encode() string` and
  `(*telegram.Session).Decode(string) error` — the only public encoder/decoder
- `(*telegram.Client).ImportSession(string) (bool, error)` / `.ImportStringSession(string)`
- `(*telegram.Client).ImportRawSession(*telegram.Session) error`
- `(*telegram.Client).ExportSession() string` / `.ExportStringSession() string` /
  `.ExportRawSession() *telegram.Session`
- via the embedded `*mtproto.MTProto`: `.SaveSession(mem bool) error`,
  `.DeleteSession() error`, `.LoadSession(...)`, `.ImportAuth(string)`,
  `.GetDC() int`, `.SetAddr(string)`, `.GetAddr() string`
- `telegram.ClientConfig`: `AppID`, `AppHash`, `Session` (path), `StringSession`,
  `SessionAESKey`, `MemorySession`, `DataCenter` (default `telegram.DefaultDataCenter` = 4),
  `IpAddr`, `Proxy`, `NoPreconnect`, `DeviceConfig`, `Transport`, …

**Internal — must never be referenced by our code** (Go forbids the import):

- `internal/session.Session`, `internal/session.StringSession`,
  `NewStringSession`, `NewEmptyStringSession`, `NewFromFile`, `NewInMemory`,
  `SessionLoader`
- `internal/utils.Sha1Byte`, `internal/aes_ige` (AES-IGE), `internal/encoding/tl`

Consequence: exactly **two** storages exist — gogram's file session
(`ClientConfig.Session`) and gogram's in-memory session (`MemorySession: true`).
There is no third backend to design for, and no SQLite layer to port.

## Decision 1 — storage type and file layout

**Use gogram's file session for named accounts; use `MemorySession: true` for every
string/pool session.**

| Config source | Storage | ClientConfig |
|---|---|---|
| `TELEGRAM_SESSION_NAME[_<LABEL>]` | file | `Session: <resolved path>`, `MemorySession: false` |
| `TELEGRAM_SESSION_STRING[_<LABEL>]` | memory | `StringSession: <converted>`, `MemorySession: true` |
| claimed `TELEGRAM_SESSION_STRINGS` slot | memory | `StringSession: <converted>`, `MemorySession: true` |

Rationale: the pool exists so N concurrent processes each hold a *different* auth
key; a file per slot would add a second shared resource (and a second writer) for
no gain, and mirrors Telethon's `StringSession`, which never touches disk.

**File layout** (mirrors `_resolve_session_path()` in `runtime.py:526`):

- Resolve a relative name against the project root, then the directory containing
  the discovered `.env`, then CWD; first hit wins.
- Append `.session` ourselves: `name` -> `<root>/name.session` (Telethon's
  `SQLiteSession` did this implicitly; gogram uses `Session` verbatim and only
  defaults to `session.dat`, so the Go layer owns the extension).
- gogram's own `joinAbsWorkingDir` then makes it absolute, and `Store()` creates
  the parent dir `0700` and writes the file `0600`.

**Mandatory format guard.** A Telethon `.session` is SQLite; a gogram session file
is AES-IGE-obfuscated JSON. Pointing `Session:` at a Telethon file does **not**
fail safe: `NewMTProto` only *warns* on a load error, then `SaveSession` overwrites
the file, destroying the Python session. So before constructing the client, open
the target, read the first 16 bytes, and refuse if they equal
`"SQLite format 3\x00"`: exit with "this is a Telethon SQLite session — run the
migrator first". Reject unconditionally; never auto-convert.

**Never hand-write a gogram session file.** `tokenStorageFormat` JSON is encrypted
through `internal/aes_ige`, which we cannot import. Only gogram may write it.

## Decision 2 — importing a Python Telethon StringSession

### Verdict: the two formats are incompatible, so a converter is mandatory

`(*telegram.Client).ExportStringSession()` is **JSON**, not protobuf and not a TL
binary blob. Its chain is
`ExportStringSession -> ExportSession -> internal/session.StringSession.Encode()`
(`telegram/client.go:870-885`), and that encoder is
(`internal/session/string.go:14`, `:25-29`, `:46-52`):

```go
const sessionPrefix = "1BvE"                                   // legacy: "1BvX"
type StringSession struct {
    AuthKey     []byte `json:"key,omitempty"`      // AUTH_KEY
    AuthKeyHash []byte `json:"hash,omitempty"`     // AUTH_KEY_HASH
    DcID        int    `json:"dc_id,omitempty"`    // DC ID
    IpAddr      string `json:"ip_addr,omitempty"`  // IP address of DC
    AppID       int32  `json:"app_id,omitempty"`   // APP_ID
}
func (s *StringSession) Encode() string {
    jsonSession, err := json.Marshal(s)
    return sessionPrefix + base64.RawURLEncoding.EncodeToString(jsonSession)
}
```

Telethon's `StringSession` is a `struct.pack('>B{}sH256s')` binary record. Two
different encodings of two different schemas, neither a prefix of the other, and
gogram's decoder rejects anything not starting `1BvE`/`1BvX`
(`ErrInvalidSession`). **There is no direct import path**; a converter that
unpacks Telethon's bytes and re-emits gogram's JSON form is required, and it is
the only place the two formats meet.

### The real Telethon layout (brief was wrong)

`telethon/sessions/string.py` (v1) is:

```python
CURRENT_VERSION = '1'
_STRUCT_PREFORMAT = '>B{}sH256s'
ip_len = 4 if len(string) == 352 else 16
self._dc_id, ip, self._port, key = struct.unpack(
    _STRUCT_PREFORMAT.format(ip_len), StringSession.decode(string))
```

So the wire form is:

```
"1"                                     1 ASCII byte, NOT base64
base64.urlsafe_b64encode(...)           standard urlsafe alphabet, padded
  uint8   dc_id          (big-endian)   NOT int32
  bytes   ip             packed IPv4 (4B) or IPv6 (16B)
  uint16  port           (big-endian)
  bytes   auth_key       256 raw bytes
```

There is **no** 8-byte version+session_id header, **no** `int64 user_id`, no
`is_owner`, no server salt. Everything is big-endian (`>`); the version lives
outside the base64 payload.

**Layouts explicitly rejected by the parser**, so nobody re-adds them:

| Asserted layout | Why rejected |
|---|---|
| 8-byte header (version 1 + 4-byte `session_id`), then `int32 dc_id`, `int32 port`, `string ip`, `bytes auth_key`, `int64 user_id` | not Telethon's `StringSession`; it is the shape other MTProto libraries use. Telethon stores no `session_id` and no `user_id` in the string at all — `dc_id` is `uint8`, not `int32`, and the version is an ASCII `'1'` *outside* the base64. |
| gogram `1BvX` legacy (`_=_:`-separated, 5 fields) | a gogram-internal legacy form, never produced by Telethon; nothing to migrate. |

`ConvertTelethonString` therefore opens with a format probe, not a blind unpack:
require `len(s) > 0 && s[0] == '1'`, base64-decode the remainder, require
`len(rest)-259 == 4 || len(rest)-259 == 16`, and require the trailing key to be
exactly 256 bytes. Anything else returns `ErrNotTelethonStringSession` — the
migrator must never emit a half-parsed key.

### gogram's string format

`internal/session.StringSession` marshals
`{key, hash, dc_id, ip_addr, app_id}` as JSON, then `Encode()` returns
`"1BvE" + base64.RawURLEncoding(json)`. `MTProto.ImportAuth` reads `key`, `hash`,
`ip_addr`, and `app_id` — it **ignores `dc_id`** (the address comes from
`ip_addr`, and `GetDC()` reverse-looks it up in gogram's `DcList`).
`AuthKeyHash` is `sha1(auth_key)[12:20]`.

Note the asymmetry between the two encoders: `ExportSession` passes the live
`dcId` into `NewStringSession`, but `(*telegram.Session).Encode()`
(`telegram/helpers.go:1848-1853`) hardcodes `0` for it. A string we build through
the public `telegram.Session` API therefore carries `"dc_id": 0`, which is
harmless on import only because `ip_addr` drives the address. Set
`ClientConfig.DataCenter` as well when the DC id matters.

### Chosen API: `(*telegram.Session).Encode()`

`telegram.Session` is the only public session struct, and `Encode()` the only
public encoder — we never touch `internal/session`.

```go
// ConvertTelethonString turns one Telethon StringSession into a gogram session string.
func ConvertTelethonString(s string, appID int32) (string, error) {
    // 0. probe: len(s) > 0 && s[0] == '1' else ErrNotTelethonStringSession
    // 1. rest, err := base64.URLEncoding.DecodeString(s[1:])
    // 2. ipLen := len(rest) - 1 - 2 - 256   // derive, don't trust the 352 heuristic
    // 3. dcID := rest[0]; ip := net.IP(rest[1:1+ipLen]).String()
    //    port := binary.BigEndian.Uint16(rest[1+ipLen:]); key := rest[3+ipLen:]
    // 4. len(key) == 256 or error;  port is dropped (gogram owns the DC port)
    // 5. sum := sha1.Sum(key);  hash := sum[12:20]
    // 6. return (&telegram.Session{Key: key, Hash: hash,
    //        Hostname: ip, AppID: appID}).Encode(), nil
}
```

`Encode()` fills a missing `Hash` with `sha1(key)[12:20]`; setting `Hash`
explicitly keeps it deterministic instead of relying on that fallback.

**Feed it in one of two ways:**

- **A (default, no live client):** `ClientConfig.StringSession = converted` with
  `AppID` also set in the config. `loadAuth` -> `ImportAuth` applies the key, sets
  the address, sets `appID` only when it is still 0 (so config wins), then calls
  `SaveSession(m.memorySession)` — which is exactly how a migrated key lands on
  disk for a file session.
- **B (migrator / live client):** `client.ImportRawSession(&telegram.Session{Key,
  Hash, Salt: 0, Hostname: ip, AppID: appID})`. **Trap:** `MTProto.LoadSession`
  does `m.appID = sess.AppID` unconditionally, so passing `AppID: 0` *erases* the
  configured api_id. `Salt: 0` is correct — Telethon's string carries no salt and
  gogram re-negotiates it.

Two more gogram behaviours the migrator must respect: with `AppID == 0` and no
session, `telegram.NewClient` prompts on stdin (`telegram/client.go:257`), which
would hang a server — always set `AppID`, and set `NoPreconnect: true` in the
migrator. Migrations run offline: read `TELEGRAM_SESSION_STRING*` + api id/hash,
convert, print or write the gogram string, never log the key or its hash.

## Decision 3 — lock files

Directory `filepath.Join(os.TempDir(), "telegram-mcp-locks")`, created with
`os.MkdirAll(dir, 0o700)`; a mkdir failure is fatal at startup (parity with
`singleton.py`, which does not fall back). Pool claim locks keep their own
directory, `telegram-mcp-session-locks`, so a pool claim can never block a named
account's lock.

Name: `session-<hex>.lock`, where `hex` is the first 16 hex chars of
`sha256.Sum256([]byte(identity))`.

Identity, matching `session_identity()`:

| Session kind | Identity string |
|---|---|
| file | `"file:" + abs(path)` |
| string / memory | `"string:" + hex(sha256(authKey))` |
| test double | `"anon:" + <pointer>` |

Keying on the auth-key hash — not on the serialized string — keeps one identity
across encodings (a Telethon string, a gogram `1BvE` string, and a file session
holding the same key all agree), which is the property Python was reaching for
when it hashed the serialized session.

**flock semantics.** Open `os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)` —
never `O_TRUNC`: the lock covers byte 0, and truncating a file a live holder owns
is refused. Take it with stdlib `syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)`
(exclusive) or `syscall.LOCK_SH|syscall.LOCK_NB` (shared); release with
`syscall.LOCK_UN`. This keeps the dependency set to what is already pinned —
no `golang.org/x/sys`. Windows has no shared `flock`: put the syscall behind
`//go:build !windows` and have the Windows build take no lock in shared mode and
return a hard error in exclusive mode, rather than silently pretending to lock.

**Grace loop.** `TELEGRAM_SESSION_LOCK` = `exclusive` (default) | `shared`; any
other value is a startup `SystemExit`, matching `runner.py:49`. Poll every
`500ms` until `time.Since(start) >= TELEGRAM_LOCK_GRACE_SECONDS` (default 20s),
then fail naming the holder. Never block a goroutine's event loop on the poll —
`flock` here is non-blocking, so a plain ticker loop is fine.

**PID recording.** Exclusive holder: `f.Truncate(0)` then
`f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)` + `f.Sync()`. Shared holder:
truncate to empty (several holders, no single PID). `holderPID()` re-opens the
path read-only and returns the value only if it parses as a positive integer.
Release clears the holder bytes before unlocking. The OS drops the lock on exit or
crash, so there is no stale-lock reaper.

**Migration hazard (must be recorded).** Python's per-session lock uses
`sha256(...)[:16]` but the pool path in `runtime.py:623` uses
`sha1(...)[:16]`. Go uses sha256 for both. A Python process and a Go process
holding the *same* session would therefore take *different* lock files and both
connect — reproducing exactly the `AuthKeyDuplicatedError` the lock exists to
prevent. Until Python's pool digest is switched to sha256, run at most one
runtime per session, or set `TELEGRAM_SESSION_LOCK=shared` on a single-egress-IP
host.

## Decision 4 — pool claiming order

`TELEGRAM_SESSION_STRINGS` wins the `"default"` label (suffixed
`TELEGRAM_SESSION_STRING_<LABEL>` is collected first; unsuffixed
`TELEGRAM_SESSION_STRING` / `TELEGRAM_SESSION_NAME` only fill `"default"` if it
is still unset).

1. Split on `[\s,;]+` after trimming; drop empties; de-duplicate keeping
   first-occurrence order.
2. For each slot in order: derive identity from the slot's auth key, take
   `LOCK_EX|LOCK_NB` on `telegram-mcp-session-locks/session-<16 hex>.lock`.
3. First success wins: record the PID, append the `*os.File` to a
   process-lifetime slice (never closed, so the OS releases it), log
   `Using Telegram session slot i/N.` on stderr, return the converted string.
4. All slots taken -> fatal startup error, never hand out a claimed slot:
   refusing to start is recoverable, a burned auth key is not.

The claim is taken *before* the client connects, exactly as in Python, so two
processes racing the same slot can never both reach `connect()`.

## Verification hooks

- Converter: round-trip a known Telethon string through `ConvertTelethonString`
  and back through `(*telegram.Session).Decode`; assert `Key`, `Hostname`,
  `AppID`, and `len(Hash) == 8`. Also assert the IPv6 (368-char) path.
- Converter rejection: feed it a gogram `1BvE` string, a `1BvX` legacy string, and
  a Telethon string with a truncated key; all must return
  `ErrNotTelethonStringSession` and log nothing.
- Encoder assertion: the produced string must start with `1BvE` and its base64url
  payload must unmarshal into a JSON object with `key`/`hash`/`ip_addr` — that is
  the contract `MTProto.ImportAuth` actually parses.
- Locks: re-exec helper binary (`os.Exec`) to prove `LOCK_EX` exclusion, `LOCK_SH`
  coexistence, and PID reporting.
- Pool: launch M > N claimers against N slots; exactly N succeed, the rest exit
  with the fatal message.
- Format guard: a file starting with `SQLite format 3\x00` must abort before
  `telegram.NewClient`.

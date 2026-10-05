package kit

// This file ports the saved-contact alias layer of runtime.py: alias_key,
// aliases_file_path, load_aliases, is_handle_like, _same_word, _covers,
// match_aliases and apply_alias — the part of reference resolution that turns
// a free-text wording like "андрей" into a chat ID. Only the read path is
// ported; the write path (save_aliases / update_aliases) is not.
//
// Loading never fails the chat tools: a missing or damaged file degrades to "no
// aliases" plus a constant warning line, exactly like the Python
// logger.warning, so a hand-broken file cannot take message sending down.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ErrAliasStoreUnreadable marks an aliases file that exists but could not be
// read or parsed (runtime.AliasStoreUnreadable). A read still degrades to no
// aliases; only a future write must refuse to overwrite data it could not read,
// because a degraded read plus a write-back deletes every saved alias.
var ErrAliasStoreUnreadable = errors.New("saved contacts could not be read; no changes were written")

// LegacyAliasesFile is the pre-XDG install-directory aliases.json, read as a
// fallback so existing installs keep resolving (runtime._LEGACY_ALIASES_FILE):
// consulted only when TELEGRAM_ALIASES_FILE is unset and the state-dir file is
// missing, never written. Empty disables the fallback; tests point it at a
// temporary file.
var LegacyAliasesFile = "aliases.json"

// aliasFileUnreadable is the constant warning line for a damaged aliases file.
// Like every other logged line it carries no path, content or exception text.
const aliasFileUnreadable = "Ignoring unreadable aliases file; saved aliases were not changed."

// A username is >=5 chars of [A-Za-z0-9_]; phone/id/self references must never
// be matched against a stored alias, or an alias could hijack a real account.
var handleLikeRE = regexp.MustCompile(`^@?[a-zA-Z0-9_]{5,}$`)

// selfRefs mirrors runtime._SELF_REFS.
var selfRefs = map[string]bool{"me": true, "self": true}

// AliasRecord is one saved contact, the record shape of runtime.load_aliases:
// the normalized Key, the chat/user ID and the optional display name and
// account label ("" when absent).
type AliasRecord struct {
	Key     string
	ID      int64
	Name    string
	Account string
}

// Aliases is an immutable snapshot of the aliases file: load once, resolve many
// times, concurrently. The zero value and a nil *Aliases resolve nothing.
type Aliases struct {
	path    string
	fuzzy   bool
	records map[string]AliasRecord
	// order is the file's key order, so suggestions are deterministic instead
	// of following Go's randomized map iteration.
	order []string
}

// AliasesFilePath ports runtime.aliases_file_path: TELEGRAM_ALIASES_FILE when
// set, otherwise telegram-mcp/aliases.json under XDG_STATE_HOME, defaulting to
// ~/.local/state. It is a runtime data location, never the install directory.
func AliasesFilePath(aliasesFile, xdgStateHome string) string {
	if aliasesFile != "" {
		return aliasesFile
	}
	base := xdgStateHome
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "telegram-mcp", "aliases.json")
}

// LoadAliases ports runtime.load_aliases. The three arguments are the values
// internal/config has already parsed (Config.AliasesFile,
// Config.XDGStateHome, Config.ContactFuzzy); the environment is not re-read.
//
// The returned *Aliases is always non-nil and usable. A missing file is not an
// error (no aliases configured yet); an unreadable or malformed one returns
// ErrAliasStoreUnreadable alongside a warning, and the store stays empty.
func LoadAliases(aliasesFile, xdgStateHome string, fuzzy bool) (*Aliases, error) {
	path := AliasesFilePath(aliasesFile, xdgStateHome)
	if aliasesFile == "" && !fileExists(path) && fileExists(LegacyAliasesFile) {
		path = LegacyAliasesFile
	}
	store := &Aliases{path: path, fuzzy: fuzzy, records: map[string]AliasRecord{}}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return store, nil
		}
		DefaultReporter().Warning(aliasFileUnreadable)
		return store, ErrAliasStoreUnreadable
	}
	records, order, parseErr := parseAliases(data)
	if parseErr != nil {
		DefaultReporter().Warning(aliasFileUnreadable)
		return store, ErrAliasStoreUnreadable
	}
	store.records, store.order = records, order
	return store, nil
}

// Path is the file this snapshot was read from.
func (a *Aliases) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

// Fuzzy reports whether TELEGRAM_CONTACT_FUZZY was on for this snapshot.
func (a *Aliases) Fuzzy() bool { return a != nil && a.fuzzy }

// Resolve turns a free-text reference into a stored chat ID, as the decimal
// string the entity resolver consumes, reporting whether the wording was a
// saved alias at all. A miss is never an error: the caller passes the original
// text through to the entity resolver unchanged.
//
// Two stages, in this order:
//
//  1. The exact AliasKey hit wins outright.
//  2. Otherwise the fuzzy match, which resolves only when it is unambiguous —
//     exactly one stored alias covers the query. Two candidates are a question
//     for the user, not a recipient.
//
// Handle-like input (username, phone number, numeric ID, "me"/"self") never
// resolves, matching runtime.apply_alias: a real @artemis must not be answered
// with whatever the user once saved under a similar wording.
func (a *Aliases) Resolve(name string) (string, bool) {
	if a == nil || IsHandleLike(name) {
		return "", false
	}
	if record, ok := a.records[AliasKey(name)]; ok {
		return strconv.FormatInt(record.ID, 10), true
	}
	matches := a.suggest(name)
	if len(matches) == 1 {
		return strconv.FormatInt(matches[0].ID, 10), true
	}
	return "", false
}

// Suggest ports runtime.match_aliases: the exact key wins outright, otherwise
// every alias whose tokens cover every query token, in file order. An empty
// key, handle-like input and TELEGRAM_CONTACT_FUZZY=false all yield nothing.
//
// Candidates are per alias, not per person: two aliases pointing at one ID stay
// two entries (runtime's ask payload collapses them into a confirmation), so
// this is what a "which one?" list is built from.
func (a *Aliases) Suggest(query string) []AliasRecord {
	if a == nil {
		return nil
	}
	if record, ok := a.records[AliasKey(query)]; ok {
		return []AliasRecord{record}
	}
	return a.suggest(query)
}

func (a *Aliases) suggest(query string) []AliasRecord {
	if !a.fuzzy || IsHandleLike(query) {
		return nil
	}
	key := AliasKey(query)
	if key == "" {
		return nil
	}
	queryTokens := strings.Fields(key)
	var matches []AliasRecord
	for _, alias := range a.order {
		if covers(queryTokens, strings.Fields(alias)) {
			matches = append(matches, a.records[alias])
		}
	}
	return matches
}

// AliasKey ports runtime.alias_key: trim, strip leading "@", lower-case, fold
// "ё" to "е" and collapse internal whitespace, so visually identical spellings
// collide on purpose.
//
// Divergence: Python runs unicodedata.normalize("NFC", …) first. Go's standard
// library has no normalizer and golang.org/x/text is not a dependency here, so
// a decomposed combining-mark spelling is not composed — every alias this
// server stores is already NFC because it came from the same JSON round trip.
func AliasKey(text string) string {
	key := strings.ToLower(strings.TrimSpace(text))
	key = strings.TrimLeft(key, "@")
	key = strings.ReplaceAll(key, "ё", "е")
	return strings.Join(strings.Fields(key), " ")
}

// IsHandleLike ports runtime.is_handle_like: true for anything that could be a
// real username, phone number, ID or self reference.
func IsHandleLike(value string) bool {
	candidate := strings.TrimSpace(value)
	bare := strings.TrimLeft(candidate, "@")
	if strings.HasPrefix(candidate, "+") {
		return true
	}
	if isDigits(strings.TrimLeft(bare, "-")) {
		return true
	}
	if selfRefs[strings.ToLower(bare)] {
		return true
	}
	return handleLikeRE.MatchString(candidate)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// parseAliases ports the load_aliases body: a legacy `{alias: id}` object
// upgrades on read, a row whose ID is not an integer is skipped while every
// good row survives, names are sanitized and keys are normalized.
func parseAliases(data []byte) (map[string]AliasRecord, []string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber() // keep IDs exact; float64 would round large chat IDs

	// Decode through the token stream so the file's key order survives: a
	// map[string]any would hand rows back in Go's randomized iteration order,
	// while Python's json.load keeps insertion (file) order.
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		// `null`, a top-level array or a scalar: json.loads of a non-object
		// rejects the file, so it counts as damaged.
		return nil, nil, fmt.Errorf("aliases file must be a JSON object")
	}

	records := make(map[string]AliasRecord)
	var order []string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		alias, ok := keyToken.(string)
		if !ok {
			return nil, nil, fmt.Errorf("aliases file must be a JSON object")
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		record, ok := aliasRow(alias, value)
		if !ok {
			continue // skip the bad row, keep every good one
		}
		if _, seen := records[record.Key]; !seen {
			order = append(order, record.Key)
		}
		records[record.Key] = record
	}
	if _, err := decoder.Token(); err != nil { // consume the closing '}'
		return nil, nil, err
	}
	if decoder.More() {
		// trailing garbage after the object: json.loads rejects it, so the
		// file counts as damaged.
		return nil, nil, fmt.Errorf("aliases file must be a JSON object")
	}
	return records, order, nil
}

// aliasRow converts one `{alias: value}` row: an object value (id/name/account)
// or a bare legacy ID. ok is false for rows whose ID does not coerce; those are
// skipped while every good row survives.
func aliasRow(alias string, value any) (AliasRecord, bool) {
	record := AliasRecord{}
	if fields, ok := value.(map[string]any); ok {
		id, ok := coerceID(fields["id"])
		if !ok {
			return AliasRecord{}, false
		}
		record.ID = id
		record.Name = aliasName(fields["name"])
		if account, ok := fields["account"]; ok && account != nil {
			record.Account = fmt.Sprint(account)
		}
	} else {
		id, ok := coerceID(value)
		if !ok {
			return AliasRecord{}, false
		}
		record.ID = id
	}
	record.Key = AliasKey(alias)
	return record, true
}

// coerceID ports `int(record["id"])`: JSON integers, integer strings and JSON
// floats (truncated toward zero, like Python's int()) become an int64, and
// anything else fails the conversion so the row is skipped.
func coerceID(value any) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		if id, err := v.Int64(); err == nil {
			return id, true
		}
		if f, err := v.Float64(); err == nil && f >= -(1<<63) && f < 1<<63 {
			return int64(f), true
		}
	case float64:
		if v >= -(1<<63) && v < 1<<63 {
			return int64(v), true
		}
	case int:
		return int64(v), true
	case int64:
		return v, true
	case string:
		if id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return id, true
		}
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// aliasName ports `sanitize_name(str(record["name"])) if record.get("name")
// else None`: a missing or falsy name yields "", anything else is stringified
// first (a numeric name is a string in Python too).
func aliasName(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		if f, err := v.Float64(); err == nil && f == 0 {
			return ""
		}
		return v.String()
	case bool:
		if !v {
			return ""
		}
		return "true"
	}
	return ""
}

// covers ports runtime._covers: true when every query token claims a DISTINCT
// alias token. Without the distinctness two query words could land on the same
// alias word, so "андрей андреев" would match a stored "андрей" and the surname
// the user added to name someone else was free. Kuhn's algorithm, as in Python.
func covers(queryTokens, aliasTokens []string) bool {
	if len(queryTokens) > len(aliasTokens) {
		return false
	}
	taken := make([]string, len(aliasTokens)) // query token holding each alias slot
	seen := make([]bool, len(aliasTokens))

	var assign func(token string) bool
	assign = func(token string) bool {
		for index, aliasToken := range aliasTokens {
			if seen[index] || !sameWord(token, aliasToken) {
				continue
			}
			seen[index] = true
			if taken[index] == "" || assign(taken[index]) {
				taken[index] = token
				return true
			}
		}
		return false
	}

	for _, token := range queryTokens {
		for i := range seen {
			seen[i] = false
		}
		if !assign(token) {
			return false
		}
	}
	return true
}

// sameWord ports runtime._same_word: true when two tokens are the same word,
// tolerating an inflected ending. Russian inflects at the end
// ("Андрею"/"андрей", "главному"/"главный"), so a real inflection keeps a long
// shared stem and swaps a few trailing characters. Three guards, each pinned by
// a table of name pairs in alias_test.go: a stem of >=4 characters (or a
// one-character swap on equal-length words, so "лена"/"лене" works without
// letting "олег"/"олеся" through), endings of at most three characters, and a
// >=0.65 similarity backstop.
func sameWord(a, b string) bool {
	if a == b {
		return true
	}
	x, y := []rune(a), []rune(b)
	shared := commonPrefixLen(x, y)
	if len(x)-shared > 3 || len(y)-shared > 3 {
		return false
	}
	if shared < 4 && !(len(x) == len(y) && shared == len(x)-1) {
		return false
	}
	return similarity(x, y) >= 0.65
}

func commonPrefixLen(a, b []rune) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}

// similarity is the >=0.65 backstop of sameWord, standing in for Python's
// difflib.SequenceMatcher.ratio(), which Go's standard library does not provide
// and which cannot be added as a dependency here. The longest common
// subsequence is used instead: both measures grow monotonically with the shared
// prefix the guards above already require, so they agree on every pair that
// reaches this check (the shortest, "лена"/"лене", scores 0.75 either way).
func similarity(a, b []rune) float64 {
	rows, cols := len(a), len(b)
	if rows+cols == 0 {
		return 1
	}
	previous := make([]int, cols+1)
	current := make([]int, cols+1)
	for i := 1; i <= rows; i++ {
		current[0] = 0
		for j := 1; j <= cols; j++ {
			switch {
			case a[i-1] == b[j-1]:
				current[j] = previous[j-1] + 1
			case previous[j] >= current[j-1]:
				current[j] = previous[j]
			default:
				current[j] = current[j-1]
			}
		}
		previous, current = current, previous
	}
	return 2 * float64(previous[cols]) / float64(rows+cols)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

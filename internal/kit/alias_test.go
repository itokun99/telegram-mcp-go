package kit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAliases writes payload as the aliases file in a fresh temp dir and
// returns its path plus a loader bound to it.
func writeAliases(t *testing.T, payload any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aliases.json")
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal aliases: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write aliases: %v", err)
	}
	return path
}

func loadAliases(t *testing.T, path string, fuzzy bool) *Aliases {
	t.Helper()
	store, err := LoadAliases(path, "", fuzzy)
	if err != nil {
		t.Fatalf("LoadAliases(%q): %v", path, err)
	}
	return store
}

func TestAliasResolveExactHit(t *testing.T) {
	path := writeAliases(t, map[string]any{
		"андрей":        12345,
		"работа":        map[string]any{"id": -1001234567890, "name": "Работа", "account": "main"},
		"Чикичев Игорь": 719969066,
	})
	store := loadAliases(t, path, true)

	for _, query := range []string{"андрей", "Андрей", "@андрей", " андрей ", "АНДРЕЙ"} {
		got, ok := store.Resolve(query)
		if !ok || got != "12345" {
			t.Errorf("Resolve(%q) = %q, %v; want 12345, true", query, got, ok)
		}
	}

	// Legacy flat rows upgrade on read and keys are normalized on both sides.
	if got, ok := store.Resolve("чикичев игорь"); !ok || got != "719969066" {
		t.Errorf("normalized key = %q, %v; want 719969066", got, ok)
	}
	if got, ok := store.Resolve("работа"); !ok || got != "-1001234567890" {
		t.Errorf("legacy object row = %q, %v; want -1001234567890", got, ok)
	}
	if record := store.records[AliasKey("работа")]; record.Name != "Работа" || record.Account != "main" {
		t.Errorf("record fields = %+v; want name Работа, account main", record)
	}
	if store.Path() != path {
		t.Errorf("Path() = %q, want %q", store.Path(), path)
	}
}

func TestAliasResolveMissReturnsNothing(t *testing.T) {
	path := writeAliases(t, map[string]any{
		"андрей":        map[string]any{"id": 111},
		"бекендер":      map[string]any{"id": 222},
		"чикичев игорь": 719969066,
	})
	store := loadAliases(t, path, true)

	for _, query := range []string{
		"никого нет",
		"игорь смирнов",  // a query word that lands nowhere disqualifies the alias
		"андрей андреев", // two query words may not reuse one stored token
		"   ",
		"бекендеру фронтендеру",
	} {
		if got, ok := store.Resolve(query); ok {
			t.Errorf("Resolve(%q) = %q, true; want a miss", query, got)
		}
	}
}

func TestAliasResolveFuzzyHitAndCaseFold(t *testing.T) {
	path := writeAliases(t, map[string]any{
		"андрей бекендер": 111,
		"ёлка":            222,
	})
	store := loadAliases(t, path, true)

	for _, want := range []struct {
		query string
		id    string
	}{
		{"Андрею Бекендеру", "111"}, // inflected endings on both tokens
		{"бекендер андрею", "111"},  // word order is free
		{"андрею", "111"},           // a subset of the stored tokens
		{"елка", "222"},             // ё folded to е on both sides
		{"Ёлка", "222"},             // case folding on top of the ё fold
	} {
		got, ok := store.Resolve(want.query)
		if !ok || got != want.id {
			t.Errorf("Resolve(%q) = %q, %v; want %s, true", want.query, got, ok, want.id)
		}
	}
}

func TestAliasResolveFuzzyDisabled(t *testing.T) {
	path := writeAliases(t, map[string]any{"андрей бекендер": 111})
	store := loadAliases(t, path, false)

	if store.Fuzzy() {
		t.Error("Fuzzy() must report the TELEGRAM_CONTACT_FUZZY=no snapshot as off")
	}
	if got, ok := store.Resolve("Андрею бекендеру"); ok {
		t.Errorf("Resolve with fuzzy off = %q, true; want a miss", got)
	}
	if got, ok := store.Resolve("андрей бекендер"); !ok || got != "111" {
		t.Errorf("exact hit with fuzzy off = %q, %v; want 111, true", got, ok)
	}
}

func TestAliasResolveAmbiguousNeverResolves(t *testing.T) {
	path := writeAliases(t, map[string]any{
		"андрей бекендер": 111,
		"андрей смирнов":  222,
	})
	store := loadAliases(t, path, true)

	if got, ok := store.Resolve("андрей"); ok {
		t.Errorf("ambiguous Resolve = %q, true; want a miss", got)
	}
	matches := store.Suggest("андрей")
	if len(matches) != 2 {
		t.Fatalf("Suggest returned %d candidates, want 2", len(matches))
	}
	if matches[0].ID != 111 || matches[1].ID != 222 {
		t.Errorf("candidates = %d/%d, want 111/222 in file order", matches[0].ID, matches[1].ID)
	}
}

func TestAliasResolveHandleLikeNeverResolves(t *testing.T) {
	path := writeAliases(t, map[string]any{
		"артем js": 809133446,
		"me":       1,
		"andrey":   2,
	})
	store := loadAliases(t, path, true)

	for _, query := range []string{"artemis", "@artemis", "+79990000000", "12345", "-1001234567890", "me", "self"} {
		if got, ok := store.Resolve(query); ok {
			t.Errorf("Resolve(%q) = %q, true; want a miss", query, got)
		}
	}
	if !IsHandleLike("@artemis") || IsHandleLike("андрей") {
		t.Error("IsHandleLike misclassified a username or a free-text alias")
	}
}

func TestAliasSuggestDedupeIsLeftToTheCaller(t *testing.T) {
	path := writeAliases(t, map[string]any{
		"андрей бекендер": 111,
		"бекендер":        111, // same person under a second wording
	})
	store := loadAliases(t, path, true)

	matches := store.Suggest("бекендеру")
	if len(matches) != 2 {
		t.Fatalf("Suggest returned %d candidates, want 2 aliases for one ID", len(matches))
	}
	seen := map[string]bool{}
	for _, match := range matches {
		seen[match.Key] = true
	}
	if !seen["андрей бекендер"] || !seen["бекендер"] {
		t.Errorf("candidates = %v, want both alias keys", seen)
	}
}

func TestAliasLoadMissingFileHasNoAliases(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")
	store, err := LoadAliases(missing, "", true)
	if err != nil {
		t.Fatalf("missing file must not fail the load: %v", err)
	}
	if store == nil {
		t.Fatal("LoadAliases returned a nil store for a missing file")
	}
	if got, ok := store.Resolve("андрей"); ok {
		t.Errorf("Resolve on an empty store = %q, true; want a miss", got)
	}
}

func TestAliasLoadDamagedFileWarnsAndDegrades(t *testing.T) {
	rep := &recordingReporter{}
	SetDefaultReporter(rep)
	defer SetDefaultReporter(nil)

	cases := map[string]string{
		"not json":         "not json",
		"top-level array":  `["a list, not an object"]`,
		"null document":    "null",
		"trailing garbage": `{"ok": 5} {"and": 6}`,
	}
	for name, content := range cases {
		path := filepath.Join(t.TempDir(), "aliases.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("%s: write: %v", name, err)
		}
		store, err := LoadAliases(path, "", true)
		if !errors.Is(err, ErrAliasStoreUnreadable) {
			t.Errorf("%s: err = %v, want ErrAliasStoreUnreadable", name, err)
		}
		if store == nil {
			t.Fatalf("%s: nil store", name)
		}
		if got, ok := store.Resolve("ok"); ok {
			t.Errorf("%s: Resolve = %q, true; want a miss", name, got)
		}
	}
	if len(rep.warnings) != len(cases) {
		t.Errorf("warnings = %d, want %d", len(rep.warnings), len(cases))
	}
	for _, warning := range rep.warnings {
		if warning != aliasFileUnreadable {
			t.Errorf("warning %q is not the constant line", warning)
		}
	}
}

func TestAliasLoadSkipsBadRowKeepsGoodOnes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.json")
	// Latin keys of five or more characters would be handle-like usernames, so
	// the coerced rows are named in Russian.
	content := `{"ok": 5, "bad": {"id": "not-an-int"}, "no-id": {"name": "x"}, "дробь": 12.9, "строка": "77"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store := loadAliases(t, path, true)

	for _, want := range []struct {
		query string
		id    string
	}{
		{"ok", "5"},
		{"дробь", "12"},  // int(12.9) truncates toward zero, like Python
		{"строка", "77"}, // int("77")
	} {
		if got, ok := store.Resolve(want.query); !ok || got != want.id {
			t.Errorf("Resolve(%q) = %q, %v; want %s", want.query, got, ok, want.id)
		}
	}
	for _, query := range []string{"bad", "no-id"} {
		if _, ok := store.Resolve(query); ok {
			t.Errorf("Resolve(%q) resolved; the row must be skipped", query)
		}
	}
}

func TestAliasLoadLegacyFallbackWhenOverrideUnset(t *testing.T) {
	state := t.TempDir()
	legacy := writeAliases(t, map[string]any{"чикичев игорь": 719969066})
	original := LegacyAliasesFile
	LegacyAliasesFile = legacy
	defer func() { LegacyAliasesFile = original }()

	store, err := LoadAliases("", state, true)
	if err != nil {
		t.Fatalf("LoadAliases with the legacy fallback: %v", err)
	}
	if got, ok := store.Resolve("чикичев игорь"); !ok || got != "719969066" {
		t.Errorf("legacy fallback Resolve = %q, %v; want 719969066", got, ok)
	}

	// An explicit override never consults the legacy file.
	override := writeAliases(t, map[string]any{"андрей": 5})
	store, err = LoadAliases(override, state, true)
	if err != nil {
		t.Fatalf("LoadAliases with an override: %v", err)
	}
	if _, ok := store.Resolve("чикичев игорь"); ok {
		t.Error("an override must shadow the legacy aliases file")
	}
}

func TestAliasFilePathResolution(t *testing.T) {
	if got := AliasesFilePath("/var/tmp/aliases.json", "/state"); got != "/var/tmp/aliases.json" {
		t.Errorf("override ignored: %q", got)
	}
	want := filepath.Join("/state", "telegram-mcp", "aliases.json")
	if got := AliasesFilePath("", "/state"); got != want {
		t.Errorf("state-dir path = %q, want %q", got, want)
	}
	if got := AliasesFilePath("", ""); !strings.Contains(got, filepath.Join(".local", "state", "telegram-mcp")) {
		t.Errorf("default state-dir path = %q, want the ~/.local/state default", got)
	}
}

func TestAliasKeyNormalizesSpellings(t *testing.T) {
	cases := map[string]string{
		"андрей":       "андрей",
		"  Андрей  ":   "андрей",
		"@андрей":      "андрей",
		"@@андрей":     "андрей",      // every leading @ goes, like lstrip("@")
		"Ёлка":         "елка",        // ё folds to е
		"Работа  Глав": "работа глав", // runs of whitespace collapse to one space
		"Работа\tГлав": "работа глав",
		"":             "",
		"   ":          "",
		"@":            "",
	}
	for input, want := range cases {
		if got := AliasKey(input); got != want {
			t.Errorf("AliasKey(%q) = %q, want %q", input, got, want)
		}
	}
}

// The whole safety of fuzzy matching rests on these two tables, pinned from
// tests/test_aliases.py: an inflection of the same name must match, a different
// person's name must not, so no threshold tweak can silently start misrouting
// messages.
func TestAliasSameWordAcceptsInflections(t *testing.T) {
	inflections := [][2]string{
		{"андрею", "андрей"},
		{"игорю", "игорь"},
		{"марии", "мария"},
		{"бекендеру", "бекендер"},
		{"контакту", "контакт"},
		{"мертвому", "мертвый"}, // adjectives swap 3 chars, not 1
		{"главному", "главный"},
		{"старшему", "старший"},
		{"лена", "лене"}, // short names swap a single character
		{"саша", "сашу"},
		{"иван", "ивана"},
		{"александру", "александр"},
	}
	for _, pair := range inflections {
		if !sameWord(pair[0], pair[1]) {
			t.Errorf("sameWord(%q, %q) = false; want an accepted inflection", pair[0], pair[1])
		}
	}
}

func TestAliasSameWordRejectsDifferentPeople(t *testing.T) {
	different := [][2]string{
		{"артем", "артур"},
		{"макс", "марк"},
		{"ольга", "олег"},
		{"олег", "олеся"}, // looks exactly like an inflection, is not
		{"анна", "антон"},
		{"иван", "игорь"},
		{"смирнов", "сидоров"},
		{"бекендер", "фронтендер"},
		{"дима", "дина"},
		{"инна", "инга"},
		{"вера", "вероника"},
		{"владимир", "владислав"},
		{"сергей", "сергеевич"},
	}
	for _, pair := range different {
		if sameWord(pair[0], pair[1]) {
			t.Errorf("sameWord(%q, %q) = true; these are different people", pair[0], pair[1])
		}
	}
}

func TestAliasNilStoreResolvesNothing(t *testing.T) {
	var store *Aliases
	if got, ok := store.Resolve("андрей"); ok || got != "" {
		t.Errorf("nil store Resolve = %q, %v; want a miss", got, ok)
	}
	if store.Suggest("андрей") != nil {
		t.Error("nil store Suggest must return nothing")
	}
	if store.Path() != "" || store.Fuzzy() {
		t.Error("nil store accessors must report the zero values")
	}
}

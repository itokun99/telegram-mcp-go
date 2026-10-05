package config

import (
	"strings"
	"testing"
)

// validEnv returns a minimal environment that passes Load: credentials, one
// default session, nothing else set (every other variable at its default).
func validEnv() map[string]string {
	return map[string]string{
		"TELEGRAM_API_ID":         "123456",
		"TELEGRAM_API_HASH":       "0123456789abcdef0123456789abcdef",
		"TELEGRAM_SESSION_STRING": "sessionA",
	}
}

func loadValid(t *testing.T) *Config {
	t.Helper()
	c, err := Load(NewInMemory(validEnv()))
	if err != nil {
		t.Fatalf("Load(validEnv) failed: %v", err)
	}
	return c
}

func mustFail(t *testing.T, env map[string]string, wantSub string) {
	t.Helper()
	_, err := Load(NewInMemory(env))
	if err == nil {
		t.Fatalf("expected Load to fail containing %q, got nil error", wantSub)
	}
	if !strings.Contains(err.Error(), wantSub) {
		t.Fatalf("expected error containing %q, got: %v", wantSub, err)
	}
}

func TestLoadMissingAPIID(t *testing.T) {
	env := validEnv()
	delete(env, "TELEGRAM_API_ID")
	_, err := Load(NewInMemory(env))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_API_ID is not set") {
		t.Fatalf("want actionable missing-API_ID error, got: %v", err)
	}
}

func TestLoadBlankAPIID(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_API_ID"] = "  "
	_, err := Load(NewInMemory(env))
	if err == nil {
		t.Fatal("blank TELEGRAM_API_ID must fail (Python int('') raises)")
	}
}

func TestLoadInvalidAPIID(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_API_ID"] = "abc"
	mustFail(t, env, "invalid TELEGRAM_API_ID")
}

func TestLoadAPIIDWhitespaceTrimmed(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_API_ID"] = "  777  "
	c := loadValidFrom(t, env)
	if c.APIID != 777 {
		t.Fatalf("APIID = %d, want 777", c.APIID)
	}
}

func TestLoadAPIHash(t *testing.T) {
	c := loadValid(t)
	if !c.APIHashSet || c.APIHash != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("APIHash = %q set=%v", c.APIHash, c.APIHashSet)
	}
}

func TestNoSessionFails(t *testing.T) {
	env := validEnv()
	delete(env, "TELEGRAM_SESSION_STRING")
	_, err := Load(NewInMemory(env))
	if err == nil {
		t.Fatal("no session configured must fail")
	}
	if !strings.Contains(err.Error(), "TELEGRAM_SESSION_STRING") {
		t.Fatalf("error must be actionable (name the fix), got: %v", err)
	}
}

func TestDiscoverSuffixedAccounts(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_SESSION_STRING_WORK"] = "worksess"
	env["TELEGRAM_SESSION_NAME_personal"] = "sessions/personal"
	c := loadValidFrom(t, env)

	if c.Accounts["work"].SessionString != "worksess" {
		t.Fatalf("work account = %+v, want session string 'worksess'", c.Accounts["work"])
	}
	if c.Accounts["personal"].SessionName != "sessions/personal" {
		t.Fatalf("personal account = %+v, want session name", c.Accounts["personal"])
	}
	if _, hasDefault := c.Accounts["default"]; !hasDefault {
		t.Fatal("unsuffixed session must also yield the default account")
	}
	if len(c.Accounts) != 3 {
		t.Fatalf("expected 3 accounts, got %d: %v", len(c.Accounts), c.Accounts)
	}
}

func TestDiscoverSkipsEmptySuffixed(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_SESSION_STRING_EMPTY"] = ""
	c := loadValidFrom(t, env)
	if len(c.Accounts) != 1 {
		t.Fatalf("empty suffixed value must be skipped, got accounts: %v", c.Accounts)
	}
}

func TestDefaultAccountPrecedence(t *testing.T) {
	// Pool beats the unsuffixed string.
	env := validEnv()
	env["TELEGRAM_SESSION_STRINGS"] = "A B,C"
	c := loadValidFrom(t, env)
	if c.Accounts["default"].SessionString != "A" {
		t.Fatalf("pool must win for default, got %q", c.Accounts["default"].SessionString)
	}
	if len(c.SessionPool) != 3 {
		t.Fatalf("pool = %v, want 3 entries", c.SessionPool)
	}

	// String beats name.
	env = validEnv()
	delete(env, "TELEGRAM_SESSION_STRINGS")
	env["TELEGRAM_SESSION_NAME"] = "named_session"
	c = loadValidFrom(t, env)
	if c.Accounts["default"].SessionString != "sessionA" {
		t.Fatal("TELEGRAM_SESSION_STRING must beat TELEGRAM_SESSION_NAME")
	}

	// Name only.
	env = validEnv()
	delete(env, "TELEGRAM_SESSION_STRING")
	env["TELEGRAM_SESSION_NAME"] = "named_session"
	c = loadValidFrom(t, env)
	if c.Accounts["default"].SessionName != "named_session" {
		t.Fatalf("want name fallback, got %+v", c.Accounts["default"])
	}
}

func TestParseSessionPoolDedup(t *testing.T) {
	pool := parseSessionPool("s1 s2 s1;s3,s2  s4")
	want := []string{"s1", "s2", "s3", "s4"}
	if len(pool) != len(want) {
		t.Fatalf("pool = %v, want %v", pool, want)
	}
	for i := range want {
		if pool[i] != want[i] {
			t.Fatalf("pool[%d] = %q, want %q (%v)", i, pool[i], want[i], pool)
		}
	}
	if parseSessionPool("   ") != nil {
		t.Fatal("blank pool must parse to nil")
	}
}

func TestSessionLockModes(t *testing.T) {
	c := loadValid(t)
	if c.SessionLockMode != "exclusive" {
		t.Fatalf("default lock mode = %q, want exclusive", c.SessionLockMode)
	}

	env := validEnv()
	env["TELEGRAM_SESSION_LOCK"] = "Shared"
	c = loadValidFrom(t, env)
	if c.SessionLockMode != "shared" {
		t.Fatalf("lock mode = %q, want shared (case-insensitive)", c.SessionLockMode)
	}

	env = validEnv()
	env["TELEGRAM_SESSION_LOCK"] = "both"
	mustFail(t, env, "invalid TELEGRAM_SESSION_LOCK")
}

func TestLockGraceSeconds(t *testing.T) {
	if got := lockGraceSeconds(NewInMemory(validEnv())); got != 20 {
		t.Fatalf("default grace = %f, want 20", got)
	}
	env := validEnv()
	env["TELEGRAM_LOCK_GRACE_SECONDS"] = "5.5"
	if got := lockGraceSeconds(NewInMemory(env)); got != 5.5 {
		t.Fatalf("grace = %f, want 5.5", got)
	}
	env["TELEGRAM_LOCK_GRACE_SECONDS"] = "not-a-number"
	if got := lockGraceSeconds(NewInMemory(env)); got != 20 {
		t.Fatalf("malformed grace must fall back to 20, got %f", got)
	}
}

func TestExpectedUsernameNormalization(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_EXPECTED_USERNAME"] = "  @MyUser "
	env["TELEGRAM_EXPECTED_USERNAME_WORK"] = "@Other"
	c := loadValidFrom(t, env)
	if c.ExpectedUsernames["default"] != "myuser" {
		t.Fatalf("default username = %q, want 'myuser' (trim, strip @, lowercase)", c.ExpectedUsernames["default"])
	}
	if c.ExpectedUsernames["work"] != "other" {
		t.Fatalf("work username = %q, want 'other'", c.ExpectedUsernames["work"])
	}
}

func TestExpectedUsernamePerLabelOverridesGlobal(t *testing.T) {
	// Per-label override resolution happens at connect time; Load only
	// records per-label values — a label with no override keeps none here.
	env := validEnv()
	env["TELEGRAM_EXPECTED_USERNAME"] = "global"
	c := loadValidFrom(t, env)
	if c.ExpectedUsernames["default"] != "global" {
		t.Fatalf("default = %q, want 'global'", c.ExpectedUsernames["default"])
	}
	if got := len(c.ExpectedUsernames); got != 1 {
		t.Fatalf("expected only the default entry, got %d: %v", got, c.ExpectedUsernames)
	}
}

func loadValidFrom(t *testing.T, env map[string]string) *Config {
	t.Helper()
	c, err := Load(NewInMemory(env))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	return c
}

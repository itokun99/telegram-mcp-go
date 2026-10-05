package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// EnvSource models the process environment so config parsing stays pure and
// offline-testable: Python's runtime layer reads these same variables through
// os.getenv / os.environ.items(); OS is the real one, InMemory is pinned in tests.
type EnvSource interface {
	// Get returns the value of key and whether it is set.
	Get(key string) (string, bool)
	// Keys returns every defined environment variable name.
	Keys() []string
}

// OS is the EnvSource backed by the live process environment.
type OS struct{}

// Get implements EnvSource over os.LookupEnv.
func (OS) Get(key string) (string, bool) { return os.LookupEnv(key) }

// Keys implements EnvSource over os.Environ.
func (OS) Keys() []string {
	keys := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		keys = append(keys, kv[:i])
	}
	sort.Strings(keys)
	return keys
}

// InMemory is a hermetic EnvSource for tests.
type InMemory struct {
	M map[string]string
}

// NewInMemory builds an InMemory source from a map (nil-safe).
func NewInMemory(m map[string]string) *InMemory {
	if m == nil {
		m = map[string]string{}
	}
	return &InMemory{M: m}
}

// Get implements EnvSource.
func (s *InMemory) Get(key string) (string, bool) {
	v, ok := s.M[key]
	return v, ok
}

// Keys implements EnvSource, sorted for deterministic discovery.
func (s *InMemory) Keys() []string {
	keys := make([]string, 0, len(s.M))
	for k := range s.M {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ErrNoSession is returned when no Telegram session is configured at all.
// Mirrors runtime._discover_accounts(): it prints an actionable message and
// aborts startup instead of starting a server with no accounts.
var ErrNoSession = fmt.Errorf(
	"no Telegram session configured. " +
		"Set TELEGRAM_SESSION_STRING or TELEGRAM_SESSION_STRING_<LABEL> in the environment (or .env)")

// mustGet returns the value of key ("" when unset). The many config parsers
// read plain optional variables this way; Load-level fail-loud checks that
// need the set/unset distinction call c.Source.Get directly.
func mustGet(src EnvSource, key string) string {
	v, _ := src.Get(key)
	return v
}

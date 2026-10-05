package session

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

// sessionPoolRe splits TELEGRAM_SESSION_STRINGS on whitespace, comma or
// semicolon. Mirrors runtime._parse_session_pool.
var sessionPoolRe = regexp.MustCompile(`[\s,;]+`)

// ParseSessionPool splits raw TELEGRAM_SESSION_STRINGS into a de-duplicated,
// order-preserving list of slots.
func ParseSessionPool(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	seen := map[string]bool{}
	pool := []string{}
	for _, tok := range sessionPoolRe.Split(raw, -1) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] {
			continue
		}
		seen[tok] = true
		pool = append(pool, tok)
	}
	return pool
}

// ClaimedSlot is a pool slot this process holds. Its Lock is a
// process-lifetime handle (mirroring runtime._SESSION_LOCKS): it is never
// released while the process is connected, and the OS releases it at exit
// or crash. Never Release it while connected.
type ClaimedSlot struct {
	// Index is the 0-based slot position in the parsed pool.
	Index int
	// Session is the serialized session string this slot claims.
	Session string
	// Lock is the held exclusive advisory lock on the slot.
	Lock *SessionLock
}

var (
	claimedMu sync.Mutex
	claimed   []*ClaimedSlot
)

// ClaimSlot claims the first free slot of TELEGRAM_SESSION_STRINGS (raw) via
// an exclusive advisory lock under PoolLockDir (decision 4 of the session
// spec). The claim is taken BEFORE a client connects, so two processes
// racing the same slot can never both reach connect(). The returned slot is
// recorded in a process-lifetime slice and its lock file is never closed.
//
// All slots taken -> *PoolExhaustedError: refusing to start is recoverable,
// a burned auth key is not — a claimed session is never handed out.
func ClaimSlot(raw string) (*ClaimedSlot, error) {
	return claimSlotIn(PoolLockDir(), raw)
}

// claimSlotIn is ClaimSlot against an explicit lock directory, so tests can
// claim slots under a temp dir.
func claimSlotIn(dir, raw string) (*ClaimedSlot, error) {
	pool := ParseSessionPool(raw)
	if len(pool) == 0 {
		return nil, fmt.Errorf("no session configured: set TELEGRAM_SESSION_STRINGS (whitespace/comma/semicolon separated, one slot per concurrent client)")
	}
	for idx, s := range pool {
		lock, err := NewSessionLockIn("pool", StringSessionIdentity(s), dir)
		if err != nil {
			return nil, err
		}
		taken, err := lock.TryOnce()
		if err != nil {
			return nil, err
		}
		if !taken {
			continue
		}
		slot := &ClaimedSlot{Index: idx, Session: s, Lock: lock}
		claimedMu.Lock()
		claimed = append(claimed, slot)
		claimedMu.Unlock()
		fmt.Fprintf(os.Stderr, "Using Telegram session slot %d/%d.\n", idx+1, len(pool))
		return slot, nil
	}
	return nil, &PoolExhaustedError{LockDir: dir, Slots: len(pool)}
}

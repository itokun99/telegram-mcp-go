package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClient is a ClientHandle double: tests script its connect errors and
// identity, and inspect the call counts. All network paths stay offline.
type fakeClient struct {
	mu              sync.Mutex
	connectCalls    int
	disconnectCalls int
	connectErr      func(call int) error
	auth            *Auth
	authErr         error
	matchedErrs     []error
}

func (f *fakeClient) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls++
	if f.connectErr != nil {
		return f.connectErr(f.connectCalls)
	}
	return nil
}

func (f *fakeClient) Disconnect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnectCalls++
	return nil
}

func (f *fakeClient) GetAuth() (*Auth, error) { return f.auth, f.authErr }

func (f *fakeClient) MatchAuthKeyDuplicated(err error) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.matchedErrs {
		if errors.Is(err, m) {
			return true
		}
	}
	return false
}

func (f *fakeClient) counts() (connects, disconnects int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectCalls, f.disconnectCalls
}

func connectTestOptions(t *testing.T, name string, fake *fakeClient, mutate func(*ConnectOptions)) ConnectOptions {
	t.Helper()
	opts := ConnectOptions{
		Label:     "acct",
		Identity:  "string:connect-test-" + name,
		LockDir:   t.TempDir(),
		NewClient: func() (ClientHandle, error) { return fake, nil },
		Logf:      func(string, ...any) {},
	}
	if mutate != nil {
		mutate(&opts)
	}
	return opts
}

func requireLockHeld(t *testing.T, dir, identity string) {
	t.Helper()
	lock, err := NewSessionLockIn("probe", identity, dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn(probe): %v", err)
	}
	lock.GraceSeconds, lock.PollSeconds = 0.05, 0.01
	err = lock.Acquire(context.Background())
	var lockErr *SessionLockError
	if !errors.As(err, &lockErr) {
		if err == nil {
			lock.Release()
		}
		t.Fatalf("expected the session lock to be held, Acquire returned %v", err)
	}
}

func requireLockFree(t *testing.T, dir, identity string) {
	t.Helper()
	lock, err := NewSessionLockIn("probe", identity, dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn(probe): %v", err)
	}
	lock.GraceSeconds, lock.PollSeconds = 1, 0.01
	if err := lock.Acquire(context.Background()); err != nil {
		t.Fatalf("expected the session lock to be free: %v", err)
	}
	lock.Release()
}

func TestConnectRetriesAuthKeyDuplicated(t *testing.T) {
	fake := &fakeClient{
		connectErr: func(call int) error {
			if call <= 2 {
				return &AuthKeyDuplicatedError{Cause: errors.New("rpc: AUTH_KEY_DUPLICATED")}
			}
			return nil
		},
		auth: &Auth{JustUsername: "owner"},
	}
	var backoffAttempts []int
	opts := connectTestOptions(t, "retry-success", fake, func(o *ConnectOptions) {
		o.RetryBackoff = func(attempt int) time.Duration {
			backoffAttempts = append(backoffAttempts, attempt)
			return 0
		}
	})

	conn, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn == nil || conn.Client == nil {
		t.Fatal("Connect returned no client handle")
	}
	if conn.Lock == nil || !conn.Lock.Holding() {
		t.Fatal("a connected session must hold its lock")
	}
	if got := fmt.Sprint(backoffAttempts); got != "[1 2]" {
		t.Fatalf("retry backoffs fired for attempts %v, want [1 2]", backoffAttempts)
	}
	connects, _ := fake.counts()
	if connects != 3 {
		t.Fatalf("Connect calls = %d, want 3 (two duplicated failures, then success)", connects)
	}

	requireLockHeld(t, opts.LockDir, opts.Identity)
	conn.Close()
	requireLockFree(t, opts.LockDir, opts.Identity)
}

func TestConnectGivesUpAfterMaxDuplicateAttempts(t *testing.T) {
	fake := &fakeClient{
		connectErr: func(int) error { return &AuthKeyDuplicatedError{} },
		auth:       &Auth{},
	}
	opts := connectTestOptions(t, "retry-exhaust", fake, func(o *ConnectOptions) {
		o.RetryBackoff = func(int) time.Duration { return 0 }
	})

	if _, err := Connect(context.Background(), opts); err == nil {
		t.Fatal("Connect succeeded despite a permanent AuthKeyDuplicatedError")
	} else if !errors.Is(err, &AuthKeyDuplicatedError{}) {
		t.Fatalf("Connect error %v does not wrap *AuthKeyDuplicatedError", err)
	}
	connects, disconnects := fake.counts()
	if connects != 4 {
		t.Fatalf("Connect calls = %d, want 4 (the default maximum)", connects)
	}
	if disconnects != 1 {
		t.Fatalf("Disconnect calls = %d, want exactly 1", disconnects)
	}
	requireLockFree(t, opts.LockDir, opts.Identity)
}

// TestConnectRetriesAdapterMatch covers the ClientHandle.MatchAuthKeyDuplicated
// seam: an adapter that reports duplication without the concrete error type
// still triggers the bounded retry.
func TestConnectRetriesAdapterMatch(t *testing.T) {
	sentinel := errors.New("rpc: AUTH_KEY_DUPLICATED")
	fake := &fakeClient{
		connectErr: func(call int) error {
			if call == 1 {
				return sentinel
			}
			return nil
		},
		matchedErrs: []error{sentinel},
		auth:        &Auth{},
	}
	opts := connectTestOptions(t, "match-seam", fake, func(o *ConnectOptions) {
		o.RetryBackoff = func(int) time.Duration { return 0 }
	})

	conn, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn.Close()
	if connects, _ := fake.counts(); connects != 2 {
		t.Fatalf("Connect calls = %d, want 2 (one matched failure, then success)", connects)
	}
}

func TestConnectDoesNotRetryOtherErrors(t *testing.T) {
	sentinel := errors.New("network down")
	fake := &fakeClient{
		connectErr: func(int) error { return fmt.Errorf("dialing: %w", sentinel) },
	}
	opts := connectTestOptions(t, "no-retry", fake, func(o *ConnectOptions) {
		o.RetryBackoff = func(int) time.Duration { return 0 }
	})

	if _, err := Connect(context.Background(), opts); !errors.Is(err, sentinel) {
		t.Fatalf("Connect error %v lost its cause %v", err, sentinel)
	}
	if connects, _ := fake.counts(); connects != 1 {
		t.Fatalf("Connect calls = %d, want 1 (no retry for a non-duplicated error)", connects)
	}
	requireLockFree(t, opts.LockDir, opts.Identity)
}

func TestConnectRefusesWhenSessionLockIsHeld(t *testing.T) {
	dir := t.TempDir()
	identity := "string:connect-test-contended"

	holder, err := NewSessionLockIn("holder", identity, dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn(holder): %v", err)
	}
	holder.GraceSeconds, holder.PollSeconds = 1, 0.01
	if err := holder.Acquire(context.Background()); err != nil {
		t.Fatalf("holder Acquire: %v", err)
	}
	defer holder.Release()

	constructed := false
	fake := &fakeClient{auth: &Auth{}}
	_, err = Connect(context.Background(), ConnectOptions{
		Label:        "acct",
		Identity:     identity,
		LockDir:      dir,
		NewClient:    func() (ClientHandle, error) { constructed = true; return fake, nil },
		GraceSeconds: 0.15,
		PollSeconds:  0.01,
		Logf:         func(string, ...any) {},
	})
	var lockErr *SessionLockError
	if !errors.As(err, &lockErr) {
		t.Fatalf("want *SessionLockError while the session lock is held, got %v", err)
	}
	if constructed {
		t.Fatal("Connect constructed a client without the session lock")
	}
	if connects, _ := fake.counts(); connects != 0 {
		t.Fatalf("Connect called the client %d times without the lock", connects)
	}
}

func TestConnectRejectsUnauthenticatedSession(t *testing.T) {
	fake := &fakeClient{auth: nil}
	opts := connectTestOptions(t, "unauthenticated", fake, nil)

	_, err := Connect(context.Background(), opts)
	var unauthenticated *UnauthenticatedError
	if !errors.As(err, &unauthenticated) {
		t.Fatalf("want *UnauthenticatedError, got %v", err)
	}
	if _, disconnects := fake.counts(); disconnects != 1 {
		t.Fatalf("Disconnect calls = %d, want exactly 1", disconnects)
	}
	requireLockFree(t, opts.LockDir, opts.Identity)
}

func TestConnectEnforcesExpectedUsername(t *testing.T) {
	fake := &fakeClient{
		auth: &Auth{JustUsername: "someone_else", ExtraUsernames: []string{"@Some_Extra"}},
	}
	opts := connectTestOptions(t, "username-mismatch", fake, func(o *ConnectOptions) {
		o.ExpectedUsername = "wanted"
	})

	_, err := Connect(context.Background(), opts)
	var mismatch *UserMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want *UserMismatchError, got %v", err)
	}
	if mismatch.Expected != "wanted" {
		t.Fatalf("UserMismatchError.Expected = %q, want %q", mismatch.Expected, "wanted")
	}
	if _, disconnects := fake.counts(); disconnects != 1 {
		t.Fatalf("Disconnect calls = %d, want exactly 1 for a username mismatch", disconnects)
	}
	requireLockFree(t, opts.LockDir, opts.Identity)
}

func TestConnectAcceptsExpectedUsernameFromExtraUsernames(t *testing.T) {
	fake := &fakeClient{
		auth: &Auth{JustUsername: "primary", ExtraUsernames: []string{"@Linked"}},
	}
	opts := connectTestOptions(t, "username-match", fake, func(o *ConnectOptions) {
		o.ExpectedUsername = "linked"
	})

	conn, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn.Close()
	requireLockFree(t, opts.LockDir, opts.Identity)
}

func TestConnectLockOnlyMode(t *testing.T) {
	opts := connectTestOptions(t, "lock-only", &fakeClient{}, nil)
	opts.NewClient = nil

	conn, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.Client != nil {
		t.Fatal("lock-only Connect returned a client")
	}
	if conn.Lock == nil || !conn.Lock.Holding() {
		t.Fatal("lock-only Connect did not hold its lock")
	}
	requireLockHeld(t, opts.LockDir, opts.Identity)
	conn.Close()
	requireLockFree(t, opts.LockDir, opts.Identity)
}

// TestConnectReleasesHeldLockOnFailure covers the pool path: Connect operates
// on an already-claimed slot lock and must release the claim when the client
// cannot connect.
func TestConnectReleasesHeldLockOnFailure(t *testing.T) {
	dir := t.TempDir()
	identity := "string:connect-test-held"
	held, err := NewSessionLockIn("pool", identity, dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn: %v", err)
	}
	if taken, err := held.TryOnce(); err != nil || !taken {
		t.Fatalf("TryOnce: taken=%v err=%v, want true", taken, err)
	}

	fake := &fakeClient{connectErr: func(int) error { return errors.New("boom") }}
	_, err = Connect(context.Background(), ConnectOptions{
		Label:        "acct",
		Held:         held,
		NewClient:    func() (ClientHandle, error) { return fake, nil },
		RetryBackoff: func(int) time.Duration { return 0 },
		Logf:         func(string, ...any) {},
	})
	if err == nil {
		t.Fatal("Connect succeeded although the client failed")
	}
	requireLockFree(t, dir, identity)
}

func TestBackoffForIsExponentialCappedAt15s(t *testing.T) {
	want := map[int]time.Duration{
		1: 2 * time.Second,
		2: 4 * time.Second,
		3: 8 * time.Second,
		4: 15 * time.Second,
		5: 15 * time.Second,
	}
	for attempt, d := range want {
		if got := backoffFor(attempt, nil); got != d {
			t.Fatalf("backoffFor(%d, nil) = %s, want %s", attempt, got, d)
		}
	}
	override := func(int) time.Duration { return 7 * time.Millisecond }
	if got := backoffFor(2, override); got != override(0) {
		t.Fatalf("backoffFor ignores the override: %s", got)
	}
}

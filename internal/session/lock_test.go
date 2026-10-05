package session

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The lock tests exercise real OS advisory locks: contention is proved with a
// re-executed copy of this test binary holding the lock in a separate
// process, so the assertions are about flock/LockFileEx semantics, not about
// this package's book-keeping. The helper holds the lock until its stdin
// reaches EOF, so sequencing needs no sleeps.

const (
	helperEnv         = "TELEGRAM_SESSION_LOCK_HELPER"
	helperDirEnv      = "TELEGRAM_SESSION_LOCK_HELPER_DIR"
	helperIdentityEnv = "TELEGRAM_SESSION_LOCK_HELPER_IDENTITY"
	helperModeEnv     = "TELEGRAM_SESSION_LOCK_HELPER_MODE"
)

// TestLockHelperProcess is not a unit test: startLockHelper re-executes the
// test binary with -test.run=TestLockHelperProcess, and this function
// acquires the named lock, prints "LOCKED <pid>", and holds it until stdin
// reaches EOF.
func TestLockHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	lock, err := NewSessionLockIn("helper", os.Getenv(helperIdentityEnv), os.Getenv(helperDirEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: %v\n", err)
		os.Exit(3)
	}
	if os.Getenv(helperModeEnv) == "shared" {
		lock.Mode(LockShared)
	}
	lock.GraceSeconds, lock.PollSeconds = 5, 0.02
	if err := lock.Acquire(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "helper: %v\n", err)
		os.Exit(4)
	}
	fmt.Printf("LOCKED %d\n", os.Getpid())
	_, _ = io.Copy(io.Discard, os.Stdin)
	lock.Release()
}

// startLockHelper starts a helper process holding the lock for dir/identity
// in the given mode and returns its PID plus an idempotent stop function
// (also registered as test cleanup) that makes it release the lock and exit.
func startLockHelper(t *testing.T, dir, identity string, mode LockMode) (int, func()) {
	t.Helper()
	modeName := "exclusive"
	if mode == LockShared {
		modeName = "shared"
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess")
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		helperDirEnv+"="+dir,
		helperIdentityEnv+"="+identity,
		helperModeEnv+"="+modeName,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("helper stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("helper stdout: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting lock helper: %v", err)
	}

	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- line
	}()

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			_ = stdin.Close()
			waited := make(chan struct{})
			go func() {
				_ = cmd.Wait()
				close(waited)
			}()
			select {
			case <-waited:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				<-waited
			}
		})
	}
	t.Cleanup(stop)

	select {
	case line := <-lines:
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "LOCKED ")
		if !ok {
			t.Fatalf("lock helper did not acquire the lock (line %q, stderr: %s)", line, stderr.String())
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || pid <= 0 {
			t.Fatalf("lock helper reported a bad pid %q", rest)
		}
		return pid, stop
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for the lock helper to acquire the lock")
		return 0, stop
	}
}

// TestLockExclusiveContention proves against a real second process that the
// contention error names the holder's PID and that the lock frees itself when
// the holder exits (the OS drops it).
func TestLockExclusiveContention(t *testing.T) {
	dir := t.TempDir()
	identity := "string:lock-test-exclusive"

	holderPID, stop := startLockHelper(t, dir, identity, LockExclusive)

	contender, err := NewSessionLockIn("contender", identity, dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn: %v", err)
	}
	contender.GraceSeconds, contender.PollSeconds = 0.2, 0.01

	start := time.Now()
	err = contender.Acquire(context.Background())
	elapsed := time.Since(start)

	var lockErr *SessionLockError
	if !errors.As(err, &lockErr) {
		t.Fatalf("Acquire while held by another process: want *SessionLockError, got %v", err)
	}
	if lockErr.HolderPID == nil || *lockErr.HolderPID != holderPID {
		t.Fatalf("SessionLockError.HolderPID = %v, want the helper's pid %d", lockErr.HolderPID, holderPID)
	}
	if contender.Holding() {
		t.Fatal("a refused contender reports Holding() == true")
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("Acquire gave up after %s; it must poll until the 200ms grace window elapses", elapsed)
	}

	stop()
	contender.GraceSeconds, contender.PollSeconds = 2, 0.01
	if err := contender.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire after the holder exited: %v", err)
	}
	if !contender.Holding() {
		t.Fatal("successful Acquire did not record the held lock")
	}

	rival, err := NewSessionLockIn("rival", identity, dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn(rival): %v", err)
	}
	rival.GraceSeconds, rival.PollSeconds = 0.1, 0.01
	if err := rival.Acquire(context.Background()); !errors.As(err, &lockErr) {
		t.Fatalf("rival Acquire while held in-process: want *SessionLockError, got %v", err)
	}
	if lockErr.HolderPID == nil || *lockErr.HolderPID != os.Getpid() {
		t.Fatalf("SessionLockError.HolderPID = %v, want this process's pid %d", lockErr.HolderPID, os.Getpid())
	}

	contender.Release()
	if contender.Holding() {
		t.Fatal("Release left the lock held")
	}
	rival.GraceSeconds = 2
	if err := rival.Acquire(context.Background()); err != nil {
		t.Fatalf("rival Acquire after Release: %v", err)
	}
	rival.Release()
}

// TestLockSharedNeverOverlapsExclusive proves the shared/exclusive matrix
// across processes: shared+shared coexist, exclusive is refused while any
// shared holder lives, and exclusive succeeds only after every shared holder
// (in both processes) released.
func TestLockSharedNeverOverlapsExclusive(t *testing.T) {
	dir := t.TempDir()
	identity := "string:lock-test-shared"

	_, stop := startLockHelper(t, dir, identity, LockShared)

	ex, err := NewSessionLockIn("ex", identity, dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn(ex): %v", err)
	}
	ex.GraceSeconds, ex.PollSeconds = 0.15, 0.01
	var lockErr *SessionLockError
	if err := ex.Acquire(context.Background()); !errors.As(err, &lockErr) {
		t.Fatalf("exclusive while a shared holder lives: want *SessionLockError, got %v", err)
	}

	sh, err := NewSessionLockIn("sh", identity, dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn(sh): %v", err)
	}
	sh.Mode(LockShared)
	sh.GraceSeconds, sh.PollSeconds = 1, 0.01
	if err := sh.Acquire(context.Background()); err != nil {
		t.Fatalf("shared Acquire alongside another shared holder: %v", err)
	}
	if !sh.Holding() {
		t.Fatal("shared Acquire did not record the held lock")
	}

	stop()
	ex.GraceSeconds = 0.15
	if err := ex.Acquire(context.Background()); !errors.As(err, &lockErr) {
		t.Fatalf("exclusive while this process still holds a shared lock: want *SessionLockError, got %v", err)
	}

	sh.Release()
	ex.GraceSeconds = 2
	if err := ex.Acquire(context.Background()); err != nil {
		t.Fatalf("exclusive after every shared holder released: %v", err)
	}
	ex.Release()
}

// TestTryOnceIsNonBlocking matches the pool path's contract: one attempt,
// no grace polling, and a contended slot is reported as (false, nil) while
// the loser keeps no claim.
func TestTryOnceIsNonBlocking(t *testing.T) {
	dir := t.TempDir()
	identity := "string:lock-test-tryonce"

	_, stop := startLockHelper(t, dir, identity, LockExclusive)

	lock, err := NewSessionLockIn("pool", identity, dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn: %v", err)
	}
	taken, err := lock.TryOnce()
	if err != nil {
		t.Fatalf("TryOnce on a contended lock: %v", err)
	}
	if taken || lock.Holding() {
		t.Fatal("TryOnce claimed a lock held by another process")
	}

	stop()
	taken, err = lock.TryOnce()
	if err != nil || !taken {
		t.Fatalf("TryOnce after the holder exited: taken=%v err=%v, want true", taken, err)
	}
	if !lock.Holding() {
		t.Fatal("TryOnce success did not record the held lock")
	}
	lock.Release()
}

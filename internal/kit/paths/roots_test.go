package paths

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// errTransportFailure mirrors the Python RuntimeError("transport failure").
var errTransportFailure = errors.New("transport failure")

// fakeLister implements RootsLister for offline tests (the analogue of the
// Python _DummySession / _FailingSession / _HangingRootsSession helpers).
type fakeLister struct {
	roots []Root
	err   error
	hang  bool
	calls int
}

func (f *fakeLister) ListRoots(ctx context.Context) ([]Root, error) {
	f.calls++
	if f.hang {
		// Wait for the gate's own deadline instead of sleeping, so the
		// timeout branch is asserted deterministically.
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.roots, nil
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func TestRootsUnsupportedDetection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"not implemented", errors.New("not implemented"), true},
		{"missing list_roots", errors.New("missing list_roots"), true},
		{"other attribute error", errors.New("other"), false},
		{"wire method not found", errors.New(`method not found: "roots/list"`), true},
		{"boom", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRootsUnsupportedError(tc.err); got != tc.want {
				t.Fatalf("isRootsUnsupportedError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestResolveServerRootsDedupesAndCreatesMissing(t *testing.T) {
	base := realTempDir(t)
	root := mustMkdir(t, filepath.Join(base, "root"))

	roots, err := ResolveServerRoots([]string{root, root})
	if err != nil {
		t.Fatalf("ResolveServerRoots: %v", err)
	}
	if !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("roots = %v, want [%s]", roots, root)
	}

	missing := filepath.Join(base, "missing")
	roots, err = ResolveServerRoots([]string{missing})
	if err != nil {
		t.Fatalf("ResolveServerRoots(missing): %v", err)
	}
	if !reflect.DeepEqual(roots, []string{missing}) {
		t.Fatalf("roots = %v, want [%s]", roots, missing)
	}
	if _, err := os.Stat(missing); err != nil {
		t.Fatalf("missing root was not created: %v", err)
	}
}

func TestEmptyClientRootsDenyByDefault(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	gate := quietGate(Settings{ServerRoots: []string{root}})

	roots, status := gate.EffectiveRoots(context.Background(), &fakeLister{})
	if len(roots) != 0 || status != StatusClientDenyAll {
		t.Fatalf("EffectiveRoots = %v, %q; want empty, client_deny_all", roots, status)
	}
}

func TestEmptyClientRootsFallBackWhenEnabled(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	gate := quietGate(Settings{ServerRoots: []string{root}, AllowServerRootsFallback: true})
	lister := &fakeLister{}

	roots, status := gate.EffectiveRoots(context.Background(), lister)
	if status != StatusServerFallback || !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("EffectiveRoots = %v, %q; want [%s], server_fallback", roots, status, root)
	}

	roots, err := gate.EnsureRoots(context.Background(), lister, "download_media")
	if err != nil || !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("EnsureRoots = %v, %v; want [%s], nil", roots, err, root)
	}
}

func TestEmptyClientRootsFallbackNoopWithoutServerRoots(t *testing.T) {
	gate := quietGate(Settings{AllowServerRootsFallback: true})

	roots, status := gate.EffectiveRoots(context.Background(), &fakeLister{})
	if len(roots) != 0 || status != StatusClientDenyAll {
		t.Fatalf("EffectiveRoots = %v, %q; want empty, client_deny_all", roots, status)
	}
}

func TestBareClientPathsRecovered(t *testing.T) {
	base := realTempDir(t)
	rootA := mustMkdir(t, filepath.Join(base, "a"))
	rootB := mustMkdir(t, filepath.Join(base, "b"))

	gate := quietGate(Settings{})
	lister := RootsFunc(func(context.Context) ([]Root, error) {
		return []Root{{URI: rootA}, {URI: rootB}, {URI: "not-a-path"}}, nil
	})

	roots, status := gate.EffectiveRoots(context.Background(), lister)
	if status != StatusReady {
		t.Fatalf("status = %q, want ready", status)
	}
	if !reflect.DeepEqual(roots, []string{rootA, rootB}) {
		t.Fatalf("roots = %v, want [%s %s]", roots, rootA, rootB)
	}
}

func TestRecoverBareRootPathAcceptsWindowsDriveShape(t *testing.T) {
	// A Windows drive path is recovered even on POSIX, where it is a literal
	// relative path resolved against the working directory — the same shape
	// the Python recovery test asserts.
	const windowsRoot = `C:\Users\dev\workspace`
	got, ok := recoverBareRootPath(windowsRoot)
	if !ok {
		t.Fatal("windows drive path was not recovered")
	}
	if !filepath.IsAbs(got) || !strings.HasSuffix(got, windowsRoot) {
		t.Fatalf("recovered = %q, want an absolute path ending in %q", got, windowsRoot)
	}
}

func TestRootsMethodNotFoundFallsBackToServerAllowlist(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	gate := quietGate(Settings{ServerRoots: []string{root}})
	lister := &fakeLister{err: errors.New(`method not found: "roots/list"`)}

	roots, status := gate.EffectiveRoots(context.Background(), lister)
	if status != StatusUnsupportedFallback || !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("EffectiveRoots = %v, %q; want [%s], unsupported_fallback", roots, status, root)
	}
}

func TestListRootsUnexpectedErrorFallsBackWhenOptIn(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	gate := quietGate(Settings{ServerRoots: []string{root}, AllowServerRootsFallback: true})

	roots, status := gate.EffectiveRoots(context.Background(), &fakeLister{err: errors.New("boom")})
	if status != StatusServerFallback || !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("EffectiveRoots = %v, %q; want [%s], server_fallback", roots, status, root)
	}
}

func TestListRootsUnexpectedErrorDeniesWithoutOptIn(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	gate := quietGate(Settings{ServerRoots: []string{root}})

	roots, status := gate.EffectiveRoots(context.Background(), &fakeLister{err: errors.New("boom")})
	if len(roots) != 0 || status != StatusError {
		t.Fatalf("EffectiveRoots = %v, %q; want empty, error", roots, status)
	}
}

func TestListRootsTimeoutFallsBackWhenOptIn(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	timeout := 20 * time.Millisecond
	gate := quietGate(Settings{ServerRoots: []string{root}, AllowServerRootsFallback: true, RootsTimeout: &timeout})

	roots, status := gate.EffectiveRoots(context.Background(), &fakeLister{hang: true})
	if status != StatusServerFallback || !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("EffectiveRoots = %v, %q; want [%s], server_fallback", roots, status, root)
	}
}

func TestListRootsTimeoutDeniesWithoutOptIn(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	timeout := 20 * time.Millisecond
	gate := quietGate(Settings{ServerRoots: []string{root}, RootsTimeout: &timeout})

	roots, status := gate.EffectiveRoots(context.Background(), &fakeLister{hang: true})
	if len(roots) != 0 || status != StatusTimeout {
		t.Fatalf("EffectiveRoots = %v, %q; want empty, timeout", roots, status)
	}

	_, err := gate.EnsureRoots(context.Background(), &fakeLister{hang: true}, "download_media")
	if err == nil || !strings.Contains(err.Error(), "roots/list") || !strings.Contains(err.Error(), "TELEGRAM_SERVER_ROOTS_ONLY") {
		t.Fatalf("error = %v, want the timeout denial message", err)
	}
}

func TestServerRootsOnlySkipsClientRootsRequest(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	gate := quietGate(Settings{ServerRoots: []string{root}, ServerRootsOnly: true})
	lister := &fakeLister{err: errors.New("roots/list must not be called")}
	ctx := context.Background()

	roots, status := gate.EffectiveRoots(ctx, lister)
	if status != StatusServerOnly || !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("EffectiveRoots = %v, %q; want [%s], server_only", roots, status, root)
	}
	roots, err := gate.EnsureRoots(ctx, lister, "download_media")
	if err != nil || !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("EnsureRoots = %v, %v; want [%s], nil", roots, err, root)
	}
	if lister.calls != 0 {
		t.Fatalf("ListRoots was called %d times, want 0", lister.calls)
	}
}

func TestServerRootsOnlyIgnoresClientRootsWhenServerRootsSet(t *testing.T) {
	base := realTempDir(t)
	serverRoot := mustMkdir(t, filepath.Join(base, "server"))
	clientRoot := mustMkdir(t, filepath.Join(base, "client"))
	gate := quietGate(Settings{ServerRoots: []string{serverRoot}, ServerRootsOnly: true})
	lister := &fakeLister{roots: []Root{{URI: fileURI(clientRoot)}}}

	roots, status := gate.EffectiveRoots(context.Background(), lister)
	if status != StatusServerOnly || !reflect.DeepEqual(roots, []string{serverRoot}) {
		t.Fatalf("EffectiveRoots = %v, %q; want [%s], server_only", roots, status, serverRoot)
	}
	if lister.calls != 0 {
		t.Fatalf("ListRoots was called %d times, want 0", lister.calls)
	}
}

func TestServerRootsOnlyKeepsClientRootsWithoutServerRoots(t *testing.T) {
	clientRoot := mustMkdir(t, filepath.Join(realTempDir(t), "client"))
	gate := quietGate(Settings{ServerRootsOnly: true})
	lister := &fakeLister{roots: []Root{{URI: fileURI(clientRoot)}}}

	roots, status := gate.EffectiveRoots(context.Background(), lister)
	if status != StatusReady || !reflect.DeepEqual(roots, []string{clientRoot}) {
		t.Fatalf("EffectiveRoots = %v, %q; want [%s], ready", roots, status, clientRoot)
	}
}

func TestClientRootsStillReplaceServerRootsByDefault(t *testing.T) {
	base := realTempDir(t)
	serverRoot := mustMkdir(t, filepath.Join(base, "server"))
	clientRoot := mustMkdir(t, filepath.Join(base, "client"))
	gate := quietGate(Settings{ServerRoots: []string{serverRoot}})
	lister := &fakeLister{roots: []Root{{URI: fileURI(clientRoot)}}}

	roots, status := gate.EffectiveRoots(context.Background(), lister)
	if status != StatusReady || !reflect.DeepEqual(roots, []string{clientRoot}) {
		t.Fatalf("EffectiveRoots = %v, %q; want [%s], ready", roots, status, clientRoot)
	}
}

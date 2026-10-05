package kit

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestRouterSingleModePassthrough(t *testing.T) {
	router := NewRouter([]string{"only"})
	calls := 0
	call := func(ctx context.Context, account string) (any, error) {
		calls++
		return "single", nil
	}
	got, err := router.Route(context.Background(), "", false, call)
	if err != nil || got != "single" || calls != 1 {
		t.Fatalf("write passthrough = %v, %v (calls %d)", got, err, calls)
	}
	got, err = router.Route(context.Background(), "", true, call)
	if err != nil || got != "single" || calls != 2 {
		t.Fatalf("readonly passthrough must not tag in single mode: %v, %v", got, err)
	}
}

func TestRouterMultiWriteRequiresAccount(t *testing.T) {
	router := NewRouter([]string{"work", "personal"})
	calls := 0
	got, err := router.Route(context.Background(), "", false, func(ctx context.Context, account string) (any, error) {
		calls++
		return "x", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Error: 'account' is required. Available accounts: work, personal"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if calls != 0 {
		t.Errorf("fan-out ran a write without an account (%d calls)", calls)
	}
}

func TestRouterMultiExplicitAccountPassthrough(t *testing.T) {
	router := NewRouter([]string{"work", "personal"})
	var seen string
	got, err := router.Route(context.Background(), "WORK", false, func(ctx context.Context, account string) (any, error) {
		seen = account
		return "result:" + account, nil
	})
	if err != nil || got != "result:WORK" || seen != "WORK" {
		t.Errorf("explicit-account passthrough = %v, %v (seen %q)", got, err, seen)
	}
}

func TestRouterReadonlyFanOutJoinsStrings(t *testing.T) {
	router := NewRouter([]string{"work", "personal"})
	var mu sync.Mutex
	seen := []string{}
	got, err := router.Route(context.Background(), "", true, func(ctx context.Context, account string) (any, error) {
		mu.Lock()
		seen = append(seen, account)
		mu.Unlock()
		return "value-" + account, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "[work]\nvalue-work\n\n[personal]\nvalue-personal"
	if got != want {
		t.Errorf("fan-out join = %q, want %q", got, want)
	}
	sort.Strings(seen)
	if !reflect.DeepEqual(seen, []string{"personal", "work"}) {
		t.Errorf("accounts called: %v", seen)
	}
}

func TestRouterReadonlyFanOutRunsConcurrently(t *testing.T) {
	labels := []string{"a", "b", "c"}
	router := NewRouter(labels)
	var mu sync.Mutex
	started := 0
	allStarted := make(chan struct{})
	got, err := router.Route(context.Background(), "", true, func(ctx context.Context, account string) (any, error) {
		mu.Lock()
		started++
		if started == len(labels) {
			close(allStarted)
		}
		mu.Unlock()
		select {
		case <-allStarted:
		case <-time.After(2 * time.Second):
			return nil, fmt.Errorf("account %s: fan-out is not concurrent", account)
		}
		return "ok-" + account, nil
	})
	if err != nil {
		t.Fatalf("fan-out blocked: %v", err)
	}
	want := "[a]\nok-a\n\n[b]\nok-b\n\n[c]\nok-c"
	if got != want {
		t.Errorf("join = %q, want %q", got, want)
	}
}

func TestRouterReadonlyFanOutFlattensContent(t *testing.T) {
	router := NewRouter([]string{"a", "b"})
	got, err := router.Route(context.Background(), "", true, func(ctx context.Context, account string) (any, error) {
		if account == "a" {
			return []any{"index for a", "IMG:a"}, nil
		}
		return "text for b", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"[a]", "index for a", "IMG:a", "[b]", "text for b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("flattened = %#v, want %#v", got, want)
	}
}

func TestRouterReadonlyFanOutPropagatesErrors(t *testing.T) {
	router := NewRouter([]string{"a", "b"})
	boom := errors.New("boom")
	var mu sync.Mutex
	called := []string{}
	got, err := router.Route(context.Background(), "", true, func(ctx context.Context, account string) (any, error) {
		mu.Lock()
		called = append(called, account)
		mu.Unlock()
		if account == "b" {
			return nil, boom
		}
		return "ok", nil
	})
	if !errors.Is(err, boom) || got != nil {
		t.Errorf("error propagation = %v, %v", got, err)
	}
	if len(called) != 2 {
		t.Errorf("every account must run: %v", called)
	}
}

func TestRouterLabel(t *testing.T) {
	single := NewRouter([]string{"only"})
	if single.Multi() {
		t.Error("single-account router reported multi mode")
	}
	if label, err := single.Label(""); err != nil || label != "only" {
		t.Errorf("single Label(\"\") = %q, %v", label, err)
	}

	multi := NewRouter([]string{"work", "personal"})
	if !multi.Multi() {
		t.Error("multi-account router not in multi mode")
	}
	if label, err := multi.Label("WORK"); err != nil || label != "work" {
		t.Errorf("case-insensitive label = %q, %v", label, err)
	}
	if _, err := multi.Label(""); err == nil || err.Error() != "Account is required. Available accounts: work, personal" {
		t.Errorf("missing-account error = %v", err)
	}
	if _, err := multi.Label("missing"); err == nil || err.Error() != "Unknown account 'missing'. Available accounts: work, personal" {
		t.Errorf("unknown-account error = %v", err)
	}

	labels := multi.Labels()
	labels[0] = "hacked"
	if multi.Labels()[0] != "work" {
		t.Error("Labels must return a copy")
	}
}

func TestRouterRoutePassesContext(t *testing.T) {
	type key struct{}
	router := NewRouter([]string{"only"})
	ctx := context.WithValue(context.Background(), key{}, "value")
	_, err := router.Route(ctx, "", false, func(ctx context.Context, account string) (any, error) {
		if ctx.Value(key{}) != "value" {
			return nil, errors.New("context not propagated")
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

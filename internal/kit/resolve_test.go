package kit

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type funcSource struct {
	lookup func(identifier any) (Entity, error)
	warm   func() error
}

func (f *funcSource) Lookup(identifier any) (Entity, error) { return f.lookup(identifier) }

func (f *funcSource) WarmEntities() error {
	if f.warm == nil {
		return nil
	}
	return f.warm()
}

func TestResolveWarmsCacheAndRetries(t *testing.T) {
	entity := fakeEntity{id: 123, kind: PeerUser}
	var calls []any
	warmCount := 0
	source := &funcSource{
		lookup: func(identifier any) (Entity, error) {
			calls = append(calls, identifier)
			if len(calls) >= 2 {
				return entity, nil
			}
			return nil, fmt.Errorf("cold cache: %w", ErrEntityNotFound)
		},
		warm: func() error { warmCount++; return nil },
	}
	got, err := NewResolver(source).Resolve(int64(123))
	if err != nil {
		t.Fatal(err)
	}
	if got.BareID() != 123 {
		t.Errorf("resolved %#v", got)
	}
	if !reflect.DeepEqual(calls, []any{int64(123), int64(123)}) {
		t.Errorf("lookup calls = %v", calls)
	}
	if warmCount != 1 {
		t.Errorf("warm calls = %d, want 1", warmCount)
	}
}

func TestResolveTriesMarkedIDCandidates(t *testing.T) {
	entity := fakeEntity{id: 123, kind: PeerChannel}
	var calls []any
	warmCount := 0
	source := &funcSource{
		lookup: func(identifier any) (Entity, error) {
			calls = append(calls, identifier)
			if identifier == int64(-1000000000123) {
				return entity, nil
			}
			return nil, fmt.Errorf("cold cache: %w", ErrEntityNotFound)
		},
		warm: func() error { warmCount++; return nil },
	}
	got, err := NewResolver(source).Resolve(int64(123))
	if err != nil {
		t.Fatal(err)
	}
	if got.BareID() != 123 {
		t.Errorf("resolved %#v", got)
	}
	want := []any{int64(123), int64(123), int64(-1000000000123)}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("lookup calls = %v, want %v", calls, want)
	}
	if warmCount != 1 {
		t.Errorf("warm calls = %d, want 1", warmCount)
	}
}

func TestResolveFailsAfterAllCandidates(t *testing.T) {
	var calls []any
	source := &funcSource{
		lookup: func(identifier any) (Entity, error) {
			calls = append(calls, identifier)
			return nil, fmt.Errorf("missing %v: %w", identifier, ErrEntityNotFound)
		},
	}
	_, err := NewResolver(source).Resolve(int64(123))
	if !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("error = %v, want ErrEntityNotFound", err)
	}
	want := []any{int64(123), int64(123), int64(-1000000000123), int64(-123)}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("lookup calls = %v, want %v", calls, want)
	}
}

func TestResolvePropagatesOtherErrorsWithoutWarming(t *testing.T) {
	boom := errors.New("network down")
	warmCount := 0
	source := &funcSource{
		lookup: func(identifier any) (Entity, error) { return nil, boom },
		warm:   func() error { warmCount++; return nil },
	}
	_, err := NewResolver(source).Resolve(int64(123))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
	if warmCount != 0 {
		t.Errorf("warm calls = %d, want 0", warmCount)
	}
}

func TestResolveSkipsMarkedCandidatesForStrings(t *testing.T) {
	var calls []any
	source := &funcSource{
		lookup: func(identifier any) (Entity, error) {
			calls = append(calls, identifier)
			return nil, fmt.Errorf("missing: %w", ErrEntityNotFound)
		},
		warm: func() error { return errors.New("dialogs unavailable, ignored") },
	}
	if _, err := NewResolver(source).Resolve("chat"); !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(calls, []any{"chat", "chat"}) {
		t.Errorf("lookup calls = %v", calls)
	}
}

func TestWarmInBackground(t *testing.T) {
	var mu sync.Mutex
	warmCount := 0
	block := make(chan struct{})
	warmStarted := make(chan struct{})
	warmDone := make(chan struct{})
	source := &funcSource{
		lookup: func(identifier any) (Entity, error) { return nil, ErrEntityNotFound },
		warm: func() error {
			mu.Lock()
			warmCount++
			first := warmCount == 1
			mu.Unlock()
			if first {
				close(warmStarted)
				<-block
			}
			close(warmDone)
			return nil
		},
	}
	resolver := NewResolver(source)
	resolver.WarmInBackground()
	select {
	case <-warmStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("background warm never started")
	}
	resolver.WarmInBackground()
	close(block)
	select {
	case <-warmDone:
	case <-time.After(2 * time.Second):
		t.Fatal("background warm never finished")
	}
	mu.Lock()
	defer mu.Unlock()
	if warmCount != 1 {
		t.Errorf("warm count = %d, want 1", warmCount)
	}
}

package kit

// This file ports resolve_entity's cache-backed resolution: a small
// client-facing interface, warm-on-miss retry, and marked-ID fallback.

import (
	"errors"
	"fmt"
	"sync"
)

// ErrEntityNotFound marks an identifier the source could not resolve (cold
// cache or a dead peer). Sources wrap this error; the resolver retries only
// this class of failure.
var ErrEntityNotFound = errors.New("entity not found")

// Resolver implements resolve_entity over an EntitySource: it looks up the
// identifier, warms the client's entity cache on a not-found failure and
// retries, then tries the marked ID variants of a bare positive integer.
type Resolver struct {
	source EntitySource
	warmMu sync.Mutex
	bgOnce sync.Once
}

// NewResolver builds a Resolver over a source.
func NewResolver(source EntitySource) *Resolver { return &Resolver{source: source} }

// Resolve resolves identifier to an Entity, mirroring
// runtime._resolve_with_retries minus the connection management (that lives
// in the session layer): lookup; on ErrEntityNotFound warm the entity cache
// (dialog fetch) and retry once; on a second ErrEntityNotFound, when
// identifier is a bare positive integer, try -1000000000000-id and then -id.
// Warm failures are non-fatal, like the swallowed get_dialogs().
func (r *Resolver) Resolve(identifier any) (Entity, error) {
	entity, err := r.source.Lookup(identifier)
	if err == nil {
		return entity, nil
	}
	if !errors.Is(err, ErrEntityNotFound) {
		return nil, err
	}

	_ = r.warm()

	entity, err = r.source.Lookup(identifier)
	if err == nil {
		return entity, nil
	}
	if !errors.Is(err, ErrEntityNotFound) {
		return nil, err
	}

	last := err
	candidates := MarkedIDCandidates(identifier)
	for _, candidate := range candidates {
		entity, err = r.source.Lookup(candidate)
		if err == nil {
			return entity, nil
		}
		if !errors.Is(err, ErrEntityNotFound) {
			return nil, err
		}
		last = err
	}
	return nil, fmt.Errorf(
		"could not resolve entity for %v, including marked variants %v: %w",
		identifier, candidates, last,
	)
}

// WarmInBackground warms the entity cache once per Resolver in a goroutine,
// so startup is never blocked by a slow dialog fetch (the equivalent of
// runner.py warming the cache in a background task). The first Resolve on a
// cold cache still warms synchronously.
func (r *Resolver) WarmInBackground() {
	r.bgOnce.Do(func() {
		go func() { _ = r.warm() }()
	})
}

func (r *Resolver) warm() error {
	r.warmMu.Lock()
	defer r.warmMu.Unlock()
	return r.source.WarmEntities()
}

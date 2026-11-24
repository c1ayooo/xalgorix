package llm

import (
	"context"
	"errors"
	"log"
	"sync"
)

// ChainResolver is a Resolver over an ordered list of endpoints — the task's
// model chain. Position 0 is the active entry; Advance moves to the next
// candidate after a provider-level failure (quota exhausted, auth rejected,
// hard overload) that no key rotation can fix. It is the profile-level analog
// of the key pool: same failure classes, one level up.
//
// Concurrency: the CLI/worker loops resolve per request and Advance only from
// the failing request's retry path, so a mutex is enough. Once the chain is
// exhausted, Resolve keeps returning the last entry (the client's own backoff
// owns the wait); Advance reports false so the caller stops retrying.
type ChainResolver struct {
	mu        sync.Mutex
	endpoints []Endpoint
	idx       int
}

// NewChainResolver builds a resolver over eps. Callers pass at least one
// endpoint; an empty slice yields a resolver that always errors.
func NewChainResolver(eps []Endpoint) *ChainResolver {
	return &ChainResolver{endpoints: append([]Endpoint(nil), eps...)}
}

// Resolve returns the current endpoint.
func (r *ChainResolver) Resolve(context.Context) (Endpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.endpoints) == 0 {
		return Endpoint{}, errors.New("llm: model chain has no endpoints")
	}
	return r.endpoints[r.idx], nil
}

// Advance switches to the next endpoint. reason is logged only (never leaves
// the process). It reports whether another endpoint exists.
func (r *ChainResolver) Advance(reason string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.idx+1 >= len(r.endpoints) {
		return false
	}
	r.idx++
	log.Printf("[llm] model chain failover after %s → entry %d/%d", reason, r.idx+1, len(r.endpoints))
	return true
}

// SetEndpoints replaces the chain in place (hot task edit). The current index
// is clamped to the new length so an in-flight retry keeps a valid endpoint;
// callers keep using the same resolver pointer, so every client sharing it
// (main agent + planner clone) switches on its next LLM call.
func (r *ChainResolver) SetEndpoints(eps []Endpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endpoints = append([]Endpoint(nil), eps...)
	if r.idx >= len(r.endpoints) {
		r.idx = len(r.endpoints) - 1
	}
	if r.idx < 0 {
		r.idx = 0
	}
}

// Current reports the zero-based position of the endpoint in use.
func (r *ChainResolver) Current() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.idx
}

// Len reports the chain length.
func (r *ChainResolver) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.endpoints)
}

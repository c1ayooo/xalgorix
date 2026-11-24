// API-key pool rotation for provider rate limits.
//
// KeyRotator spreads outbound API-key-authenticated requests across a
// pool of operator-configured keys (XALGORIX_API_KEYS plus
// XALGORIX_API_KEY). Picks advance round-robin, so a pool of N keys
// raises the effective provider rate ceiling roughly N times.
//
// Cooldown state lives in the process-wide key-health registry
// (keyhealth.go), so a key that is out of credit or rejected is skipped
// by every scan — not just the client that discovered it. Quota and
// authentication failures cool a key for minutes; transient rate limits
// cool it for 90s.
//
// Thread-safe: concurrent chatWithRetry / ChatStream goroutines from
// parallel scans share one rotator per llm.Client, and the registry is
// shared by every client in the process.

package llm

import (
	"sync"
	"time"
)

// KeyRotator hands out keys from a fixed pool in round-robin order,
// skipping keys that are cooling down after a failure.
type KeyRotator struct {
	mu   sync.Mutex
	keys []string
	idx  int
	last string
	// sticky selects overflow-failover instead of round-robin: keep using one key
	// until it enters cooldown, then fail over to the next and stay there. Set once
	// at construction via SetSticky.
	sticky bool
}

// NewKeyRotator builds a rotator from the given candidate keys. Empty
// entries and duplicates are dropped; order is preserved. Returns nil
// when no usable key remains so callers can leave the feature off.
func NewKeyRotator(keys []string) *KeyRotator {
	deduped := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		deduped = append(deduped, k)
	}
	if len(deduped) == 0 {
		return nil
	}
	return &KeyRotator{keys: deduped}
}

// Pick returns the next eligible key, advancing round-robin and
// skipping keys still in cooldown. When every key is cooling down it
// returns the key whose cooldown expires soonest instead of failing:
// the caller still needs a key to attempt, and its own bounded
// backoff prevents hammering.
func (r *KeyRotator) Pick() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.sticky {
		return r.pickStickyLocked(now)
	}
	return r.pickRoundRobinLocked(now)
}

// PickExcluding returns an eligible key other than exclude, used for the
// immediate failover after the current request's key was marked dead. ok=false
// means no other key can serve the request right now.
func (r *KeyRotator) PickExcluding(exclude string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for i := 0; i < len(r.keys); i++ {
		j := (r.idx + i) % len(r.keys)
		k := r.keys[j]
		if k == exclude {
			continue
		}
		if until := keyHealthUntil(KeyFingerprint(k)); until.IsZero() || now.After(until) {
			r.idx = j + 1
			r.last = k
			return k, true
		}
	}
	return "", false
}

// pickRoundRobinLocked advances one key per call, skipping cooling keys — both
// keys carry load simultaneously, raising the effective rate ceiling.
func (r *KeyRotator) pickRoundRobinLocked(now time.Time) string {
	for range r.keys {
		k := r.keys[r.idx%len(r.keys)]
		r.idx++
		if until := keyHealthUntil(KeyFingerprint(k)); until.IsZero() || now.After(until) {
			r.last = k
			return k
		}
	}
	return r.pickSoonestLocked()
}

// pickStickyLocked keeps returning the current key until it enters cooldown, then
// fails over to the next eligible key and stays there (overflow failover): one key
// at a time, switching only on a failure, so an idle key is held in reserve.
func (r *KeyRotator) pickStickyLocked(now time.Time) string {
	for i := 0; i < len(r.keys); i++ {
		j := (r.idx + i) % len(r.keys)
		k := r.keys[j]
		if until := keyHealthUntil(KeyFingerprint(k)); until.IsZero() || now.After(until) {
			r.idx = j // stick to this key until it too cools
			r.last = k
			return k
		}
	}
	return r.pickSoonestLocked()
}

// pickSoonestLocked returns the key whose cooldown expires soonest, used when every
// key is cooling: the caller still needs a key to attempt, and its own bounded
// backoff prevents hammering.
func (r *KeyRotator) pickSoonestLocked() string {
	best := ""
	var bestUntil time.Time
	for _, k := range r.keys {
		until := keyHealthUntil(KeyFingerprint(k))
		if best == "" || until.Before(bestUntil) {
			best, bestUntil = k, until
		}
	}
	r.last = best
	return best
}

// SetSticky switches the rotator between round-robin (false, default) and
// overflow-failover (true). In sticky mode the rotator keeps returning one key
// until that key enters cooldown, then fails over to the next and stays there,
// instead of alternating every request. Intended to be set once at construction.
func (r *KeyRotator) SetSticky(v bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sticky = v
}

// MarkLastRateLimited puts the key most recently returned by Pick into the
// short transient-rate-limit cooldown so subsequent picks concentrate on the
// remaining keys.
func (r *KeyRotator) MarkLastRateLimited() { r.MarkRateLimitedKey("") }

// MarkRateLimitedKey cools the exact key that failed. Concurrent requests
// cannot then cool the wrong key through the shared `last` cursor.
func (r *KeyRotator) MarkRateLimitedKey(key string) {
	r.markKey(key, false, "rate limited (429 / usage window)")
}

// MarkQuotaExhaustedKey is MarkRateLimitedKey with the long quota cooldown.
func (r *KeyRotator) MarkQuotaExhaustedKey(key string) {
	r.markKey(key, true, "quota exhausted (balance / credit)")
}

// MarkLastQuotaExhausted puts the key most recently returned by Pick into the
// longer quota-exhausted cooldown.
func (r *KeyRotator) MarkLastQuotaExhausted() { r.MarkQuotaExhaustedKey("") }

// MarkFailureKey records a request failure against the exact key it used,
// classifying the window: quota/credit and authentication failures are hard
// (long cooldown; auth also drops the key from the pool), everything else is a
// transient rate-limit cooldown.
func (r *KeyRotator) MarkFailureKey(key, errMsg string) {
	if r == nil || key == "" {
		return
	}
	classified := ClassifyErrorString(errMsg)
	hard := classified.Class == ErrorClassQuotaExhausted || isAuthFailure(errMsg)
	if isAuthFailure(errMsg) {
		r.dropKey(key)
	}
	keyHealthTrip(KeyFingerprint(key), errMsg, hard)
}

// NoteSuccessKey clears the breaker for the key that just served a request.
func (r *KeyRotator) NoteSuccessKey(key string) {
	if r == nil || key == "" {
		return
	}
	keyHealthPass(KeyFingerprint(key))
}

// MarkInvalidKey removes a key from the pool for the lifetime of the process.
// A 401/403 is an authentication failure, not a rate limit: cooling it down
// only to retry it later cannot succeed, and in round-robin mode a single dead
// key fails every other request while the healthy keys sit idle. Dropping it
// makes the remaining keys carry the scan with no 401 storm. The pool may
// become empty; Pick then returns "" and the caller surfaces a clear error
// instead of looping.
func (r *KeyRotator) MarkInvalidKey(key string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if key == "" {
		key = r.last
	}
	r.mu.Unlock()
	if key == "" {
		return
	}
	r.dropKey(key)
	keyHealthTrip(KeyFingerprint(key), "invalid API key (401/403)", true)
}

// dropKey removes one key from the rotation pool.
func (r *KeyRotator) dropKey(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.keys[:0]
	for _, k := range r.keys {
		if k != key {
			kept = append(kept, k)
		}
	}
	r.keys = kept
	if r.idx >= len(r.keys) {
		r.idx = 0
	}
	r.last = ""
}

// markKey cools the supplied key, or the most recent Pick when key is empty
// (compatibility for callers without an endpoint in scope).
func (r *KeyRotator) markKey(key string, hard bool, msg string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if key == "" {
		key = r.last
	}
	r.mu.Unlock()
	if key == "" {
		return
	}
	keyHealthTrip(KeyFingerprint(key), msg, hard)
}

// Len reports how many distinct keys the pool holds.
func (r *KeyRotator) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.keys)
}

// Contains reports whether key is one of the pooled keys. Used so the client can
// rotate an endpoint that already carries a pooled key (e.g. a provider profile
// that resolved to the first key while XALGORIX_API_KEY is empty), without ever
// touching a credential that is not in the operator's own pool.
func (r *KeyRotator) Contains(key string) bool {
	if r == nil || key == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k == key {
			return true
		}
	}
	return false
}

// Keys returns the current pool keys (read-only copy). Used by the status
// endpoint; callers must never expose the key material.
func (r *KeyRotator) Keys() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.keys))
	copy(out, r.keys)
	return out
}

// Strategy renders the active rotation strategy for the status API.
func (r *KeyRotator) Strategy() string {
	if r == nil {
		return "off"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sticky {
		return "sticky"
	}
	return "round_robin"
}

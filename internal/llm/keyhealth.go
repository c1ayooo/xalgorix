package llm

// API-key health (ported from 4zreco's llmpool.Registry, 2026-11-24).
//
// The key pool rotates for load, but rotation alone is not failover: an
// exhausted (billing-locked) key must be skipped by EVERY scan immediately,
// and the operator must be able to see why traffic moved. This registry is the
// process-wide circuit breaker behind both behaviours:
//
//   - hard failures (quota exhausted, invalid key) cool the key for a long
//     window (15m, escalating to 30m on repeats) — the account will not
//     recover within a scan;
//   - transient failures (rate limit / overload) cool it briefly (90s), the
//     same window the rotator has always used, so the next attempt lands on a
//     different key;
//   - a successful call clears the state.
//
// It is process-wide on purpose (the 4zreco lesson): rebuilding a client must
// not forget that a key is dead. State is in memory; a restart retries each key
// once and re-learns — acceptable, and no credentials or health ever leave the
// process except through the masked status endpoint.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Key health windows.
const (
	keyRateLimitCooldown   = 90 * time.Second
	keyQuotaCooldown       = 15 * time.Minute
	keyQuotaRepeatCooldown = 30 * time.Minute
)

// KeyHealthState is one key's breaker state, as exposed to the status API.
type KeyHealthState struct {
	Fails     int       `json:"fails"`
	Trips     int       `json:"trips"`
	OpenUntil time.Time `json:"open_until,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	LastAt    time.Time `json:"last_at,omitempty"`
	LastOK    time.Time `json:"last_ok,omitempty"`
}

// Open reports whether the key is cooling down.
func (s KeyHealthState) Open() bool { return time.Now().Before(s.OpenUntil) }

type keyHealthRegistry struct {
	mu sync.Mutex
	m  map[string]*KeyHealthState
}

var keyPoolHealth = &keyHealthRegistry{m: make(map[string]*KeyHealthState)}

// KeyFingerprint is the stable, non-secret identity of one API key. The status
// API and the registry only ever handle fingerprints, never key material.
func KeyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:8])
}

// keyHealthUntil returns the key's cooldown end (zero when not cooling).
func keyHealthUntil(fingerprint string) time.Time {
	keyPoolHealth.mu.Lock()
	defer keyPoolHealth.mu.Unlock()
	if st := keyPoolHealth.m[fingerprint]; st != nil {
		return st.OpenUntil
	}
	return time.Time{}
}

// keyHealthOpen reports whether the key is inside its cooldown window.
func keyHealthOpen(fingerprint string) bool {
	return time.Now().Before(keyHealthUntil(fingerprint))
}

// keyHealthTrip records a failure and opens (or extends) the cooldown. msg is
// the provider's error (trimmed by the caller); hard selects the long window.
func keyHealthTrip(fingerprint, msg string, hard bool) {
	if fingerprint == "" {
		return
	}
	keyPoolHealth.mu.Lock()
	defer keyPoolHealth.mu.Unlock()
	st := keyPoolHealth.m[fingerprint]
	if st == nil {
		st = &KeyHealthState{}
		keyPoolHealth.m[fingerprint] = st
	}
	st.Fails++
	st.LastError = trimKeyError(msg)
	st.LastAt = time.Now()
	st.Trips++
	if hard {
		window := keyQuotaCooldown
		if st.Trips > 1 {
			window = keyQuotaRepeatCooldown
		}
		st.OpenUntil = time.Now().Add(window)
	} else {
		st.OpenUntil = time.Now().Add(keyRateLimitCooldown)
	}
}

// keyHealthPass records a success: the key is fully trusted again.
func keyHealthPass(fingerprint string) {
	if fingerprint == "" {
		return
	}
	keyPoolHealth.mu.Lock()
	defer keyPoolHealth.mu.Unlock()
	if st := keyPoolHealth.m[fingerprint]; st != nil {
		st.Fails = 0
		st.OpenUntil = time.Time{}
		st.LastOK = time.Now()
	}
}

// KeyPoolHealthSnapshot returns a copy of every tracked key's state.
func KeyPoolHealthSnapshot() map[string]KeyHealthState {
	keyPoolHealth.mu.Lock()
	defer keyPoolHealth.mu.Unlock()
	out := make(map[string]KeyHealthState, len(keyPoolHealth.m))
	for fp, st := range keyPoolHealth.m {
		out[fp] = *st
	}
	return out
}

// ResetKeyPoolHealth clears the breaker for one fingerprint ("" = all). This is
// the operator's "try the key again now" action.
func ResetKeyPoolHealth(fingerprint string) {
	keyPoolHealth.mu.Lock()
	defer keyPoolHealth.mu.Unlock()
	if fingerprint == "" {
		keyPoolHealth.m = make(map[string]*KeyHealthState)
		return
	}
	delete(keyPoolHealth.m, fingerprint)
}

// KeyFailoverEvent is one observable automatic switch off a key. The Live page
// renders these so "traffic moved" is visible without reading server logs. It
// carries fingerprints only, never key material.
type KeyFailoverEvent struct {
	At            time.Time `json:"at"`
	Reason        string    `json:"reason"` // rate_limit | quota_exhausted | overloaded | auth
	From          string    `json:"from,omitempty"`
	To            string    `json:"to,omitempty"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
}

const keyFailoverRingSize = 20

type keyFailoverRing struct {
	mu     sync.Mutex
	events []KeyFailoverEvent
}

var keyFailoverLog = &keyFailoverRing{events: make([]KeyFailoverEvent, 0, keyFailoverRingSize)}

// RecordKeyFailover appends one switch to the process-wide ring. Called from the
// client retry loop at the exact point a request moves to another key; health
// state is read after the cooldown is armed, so CooldownUntil is the window the
// failed key is skipped for.
func RecordKeyFailover(reason, fromKey, toKey string) {
	event := KeyFailoverEvent{
		At:     time.Now(),
		Reason: reason,
		From:   KeyFingerprint(fromKey),
		To:     KeyFingerprint(toKey),
	}
	if event.From != "" {
		keyPoolHealth.mu.Lock()
		if st := keyPoolHealth.m[event.From]; st != nil {
			event.CooldownUntil = st.OpenUntil
		}
		keyPoolHealth.mu.Unlock()
	}
	keyFailoverLog.mu.Lock()
	defer keyFailoverLog.mu.Unlock()
	keyFailoverLog.events = append(keyFailoverLog.events, event)
	if len(keyFailoverLog.events) > keyFailoverRingSize {
		keyFailoverLog.events = keyFailoverLog.events[len(keyFailoverLog.events)-keyFailoverRingSize:]
	}
}

// KeyFailoverEvents returns the recent switches, newest first.
func KeyFailoverEvents() []KeyFailoverEvent {
	keyFailoverLog.mu.Lock()
	defer keyFailoverLog.mu.Unlock()
	out := make([]KeyFailoverEvent, 0, len(keyFailoverLog.events))
	for i := len(keyFailoverLog.events) - 1; i >= 0; i-- {
		out = append(out, keyFailoverLog.events[i])
	}
	return out
}

// trimKeyError shortens provider bodies for the status API / logs.
func trimKeyError(msg string) string {
	s := strings.TrimSpace(strings.ReplaceAll(msg, "\n", " "))
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

// maskKeyForLog renders a key for log lines without exposing it: first 8 and
// last 4 characters. Never used for API responses (those mask harder).
func maskKeyForLog(key string) string {
	if len(key) <= 14 {
		return "…"
	}
	return key[:8] + "…" + key[len(key)-4:]
}

// classifyShort renders an error's class for one-line failover logs.
func classifyShort(errStr string) string {
	return string(ClassifyErrorString(errStr).Class)
}

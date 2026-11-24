package llm

import (
	"testing"
	"time"
)

func TestMarkRateLimitedKeyCoolsExactKeyNotLastPick(t *testing.T) {
	freshKeys(t)
	r := NewKeyRotator([]string{"a", "b"})
	if got := r.Pick(); got != "a" {
		t.Fatalf("first pick = %q, want a", got)
	}
	if got := r.Pick(); got != "b" {
		t.Fatalf("second pick = %q, want b", got)
	}
	// `b` is now the shared last cursor; cooling `a` explicitly must not cool b.
	r.MarkRateLimitedKey("a")
	for i := 0; i < 3; i++ {
		if got := r.Pick(); got != "b" {
			t.Fatalf("pick %d = %q, want b while a is cooling", i, got)
		}
	}
}

func TestKeyRotatorRoundRobin(t *testing.T) {
	freshKeys(t)
	r := NewKeyRotator([]string{"a", "b", "c"})
	seen := map[string]int{}
	for range 6 {
		seen[r.Pick()]++
	}
	for _, k := range []string{"a", "b", "c"} {
		if seen[k] != 2 {
			t.Fatalf("expected 2 picks for %q, got %d", k, seen[k])
		}
	}
}

func TestKeyRotatorDedupAndEmpty(t *testing.T) {
	freshKeys(t)
	if got := NewKeyRotator(nil); got != nil {
		t.Fatal("nil input must yield nil rotator")
	}
	r := NewKeyRotator([]string{"a", "", "a", "b"})
	if r.Len() != 2 {
		t.Fatalf("expected dedupe to 2 keys, got %d", r.Len())
	}
}

func TestKeyRotatorCooldownSkip(t *testing.T) {
	freshKeys(t)
	r := NewKeyRotator([]string{"a", "b"})
	first := r.Pick()
	r.MarkLastRateLimited()
	next := r.Pick()
	if next == first {
		t.Fatalf("expected cooldown to skip %q", first)
	}
	// While `first` cools, every pick must land on the other key.
	for range 3 {
		if got := r.Pick(); got != next {
			t.Fatalf("expected %q while %q cools, got %q", next, first, got)
		}
	}
}

func TestKeyRotatorAllCoolingFallsBack(t *testing.T) {
	freshKeys(t)
	r := NewKeyRotator([]string{"a"})
	k := r.Pick()
	r.MarkLastRateLimited()
	if got := r.Pick(); got != k {
		t.Fatalf("single-key pool must still return its key, got %q", got)
	}
}

func TestKeyRotatorAllCoolingPicksPooled(t *testing.T) {
	freshKeys(t)
	r := NewKeyRotator([]string{"a", "b"})
	a := r.Pick()
	r.MarkLastRateLimited()
	b := r.Pick()
	r.MarkLastRateLimited()
	// Both cooling: pick must still return one of the pool keys.
	got := r.Pick()
	if got != a && got != b {
		t.Fatalf("expected a pooled key, got %q", got)
	}
}

func TestKeyRotatorQuotaExhaustedLongerCooldown(t *testing.T) {
	freshKeys(t)
	ResetKeyPoolHealth("")
	t.Cleanup(func() { ResetKeyPoolHealth("") })
	r := NewKeyRotator([]string{"a", "b"})
	// A key rejected with quota_exhausted gets a much longer cooldown than a
	// transient rate limit, so the rotator stops thrashing requests back onto a
	// key whose usage window is drained and instead concentrates on keys with
	// available quota.
	q := r.Pick()
	r.MarkLastQuotaExhausted()
	quotaUntil := keyHealthUntil(KeyFingerprint(q))

	rl := r.Pick()
	r.MarkLastRateLimited()
	rlUntil := keyHealthUntil(KeyFingerprint(rl))

	if q == rl {
		t.Fatalf("precondition: expected two distinct keys, got %q twice", q)
	}
	if quotaUntil.Sub(rlUntil) < 5*time.Minute {
		t.Fatalf("quota-exhausted cooldown must be far longer than the transient one: quota=%v rate_limit=%v", quotaUntil, rlUntil)
	}
	// The quota-exhausted key has the longest cooldown, so every pick must land
	// on the other (sooner-expiring) key while it cools.
	for range 4 {
		if got := r.Pick(); got == q {
			t.Fatalf("quota-exhausted key %q must stay skipped while cooling", q)
		}
	}
}

// TestKeyHealthHardTripAndReset pins the process-wide breaker: a quota failure
// opens the long window immediately, the status snapshot explains why, and a
// manual reset makes the key eligible again.
func TestKeyHealthHardTripAndReset(t *testing.T) {
	freshKeys(t)
	ResetKeyPoolHealth("")
	t.Cleanup(func() { ResetKeyPoolHealth("") })
	fp := KeyFingerprint("sk-hard")
	keyHealthTrip(fp, "402 insufficient balance", true)
	st, ok := KeyPoolHealthSnapshot()[fp]
	if !ok || !st.Open() {
		t.Fatalf("hard failure must open the breaker: %#v ok=%v", st, ok)
	}
	if st.LastError == "" || st.Trips != 1 {
		t.Fatalf("state must carry the error and trip count: %#v", st)
	}
	if st.OpenUntil.Sub(time.Now()) < 10*time.Minute {
		t.Fatalf("quota window too short: %v", st.OpenUntil.Sub(time.Now()))
	}
	ResetKeyPoolHealth(fp)
	if _, ok := KeyPoolHealthSnapshot()[fp]; ok {
		t.Fatal("reset must clear the key state")
	}
	if keyHealthOpen(fp) {
		t.Fatal("reset key must be eligible again")
	}
}

// TestKeyRotatorPickExcludingSkipsDeadKey pins the immediate-failover pick:
// the request that just failed on key A must be retried on B, not A.
func TestKeyRotatorPickExcludingSkipsDeadKey(t *testing.T) {
	freshKeys(t)
	ResetKeyPoolHealth("")
	t.Cleanup(func() { ResetKeyPoolHealth("") })
	r := NewKeyRotator([]string{"a", "b"})
	r.MarkQuotaExhaustedKey("a")
	next, ok := r.PickExcluding("a")
	if !ok || next != "b" {
		t.Fatalf("failover pick = %q ok=%v, want b", next, ok)
	}
	// Both keys dead: no eligible failover target.
	r.MarkQuotaExhaustedKey("b")
	if next, ok := r.PickExcluding("a"); ok {
		t.Fatalf("no key should be eligible, got %q", next)
	}
}

// freshKeys isolates the process-wide key-health registry between tests.
func freshKeys(t *testing.T) {
	t.Helper()
	ResetKeyPoolHealth("")
	t.Cleanup(func() { ResetKeyPoolHealth("") })
}

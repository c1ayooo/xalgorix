package scanctx

import (
	"strings"
	"testing"
	"time"
)

func TestTargetBackoffLifecycle(t *testing.T) {
	sc := New("backoff-test", "")
	if _, _, active := sc.TargetBackoff(); active {
		t.Fatal("a fresh scan must have no backoff")
	}
	sc.SetTargetBackoff(time.Now().Add(2*time.Minute), "HTTP 429")
	remaining, reason, active := sc.TargetBackoff()
	if !active || remaining <= 0 || !strings.Contains(reason, "429") {
		t.Fatalf("backoff = (%s, %q, %v), want active 429", remaining, reason, active)
	}
	msg := sc.TargetBackoffMessage()
	if !strings.Contains(msg, "TARGET BACKOFF") || !strings.Contains(msg, "do not bypass") {
		t.Fatalf("message = %q", msg)
	}
	// A shorter later deadline must not shorten the existing cooldown.
	sc.SetTargetBackoff(time.Now().Add(10*time.Second), "shorter")
	if remaining, _, _ := sc.TargetBackoff(); remaining < 30*time.Second {
		t.Fatalf("a shorter deadline shortened the cooldown: %s", remaining)
	}
	sc.ClearTargetBackoff()
	if _, _, active := sc.TargetBackoff(); active {
		t.Fatal("clear must end the cooldown")
	}
	if sc.TargetBackoffMessage() != "" {
		t.Fatal("no message after clear")
	}
}

func TestTargetBackoffExpires(t *testing.T) {
	sc := New("backoff-expire", "")
	sc.SetTargetBackoff(time.Now().Add(-time.Second), "expired")
	if msg := sc.TargetBackoffMessage(); msg != "" {
		t.Fatalf("expired cooldown still blocks: %q", msg)
	}
}

func TestNilScanContextBackoffSafe(t *testing.T) {
	var sc *ScanContext
	if msg := sc.TargetBackoffMessage(); msg != "" {
		t.Fatalf("nil context message = %q", msg)
	}
	sc.SetTargetBackoff(time.Now().Add(time.Minute), "x")
	sc.ClearTargetBackoff()
}

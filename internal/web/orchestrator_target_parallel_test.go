package web

import "testing"

// TestTargetConcurrencyLimitDefaultsToSerial pins the compatibility contract:
// without the env flag the queue stays serial (the historical behavior), and the
// cap never exceeds 4 or the target count.
func TestTargetConcurrencyLimitDefaultsToSerial(t *testing.T) {
	t.Setenv("XALGORIX_TARGET_CONCURRENCY", "")
	if got := targetConcurrencyLimit(3); got != 1 {
		t.Fatalf("default = %d, want 1 (serial)", got)
	}
	t.Setenv("XALGORIX_TARGET_CONCURRENCY", "2")
	if got := targetConcurrencyLimit(3); got != 2 {
		t.Fatalf("configured = %d, want 2", got)
	}
	if got := targetConcurrencyLimit(1); got != 1 {
		t.Fatalf("single target = %d, want 1", got)
	}
	t.Setenv("XALGORIX_TARGET_CONCURRENCY", "99")
	if got := targetConcurrencyLimit(10); got != 4 {
		t.Fatalf("cap = %d, want 4", got)
	}
	t.Setenv("XALGORIX_TARGET_CONCURRENCY", "not-a-number")
	if got := targetConcurrencyLimit(5); got != 1 {
		t.Fatalf("invalid = %d, want 1", got)
	}
}

package agent

import "testing"

// TestTrimToValidUTF8NonPositiveBudget pins the crash fix: the snapshot's
// remaining-budget arithmetic can go negative, and a negative slice bound
// panicked the whole process mid-scan.
func TestTrimToValidUTF8NonPositiveBudget(t *testing.T) {
	for _, max := range []int{-32, -1, 0} {
		if got := trimToValidUTF8("some text", max); got != "" {
			t.Fatalf("trimToValidUTF8(max=%d) = %q, want empty", max, got)
		}
	}
	if got := trimToValidUTF8("abcdef", 3); got != "abc" {
		t.Fatalf("normal trim = %q", got)
	}
	if got := trimToValidUTF8("汉字", 4); got != "汉" {
		t.Fatalf("utf8 boundary trim = %q", got)
	}
}

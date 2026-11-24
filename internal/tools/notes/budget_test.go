package notes

import (
	"fmt"
	"strings"
	"testing"
)

// IG6a: notes are unbounded in count and the whole block is re-injected on
// compaction. The budgeted form must stay within its byte ceiling, keep a
// deterministic key order, and say how much was omitted.
func TestFormatForContextBudgetIDTruncates(t *testing.T) {
	const ctxID = "scan-notes-budget"
	t.Cleanup(func() { CleanupContext(ctxID) })

	s := getNoteStoreForContext(ctxID)
	s.mu.Lock()
	for i := 0; i < 200; i++ {
		s.store[fmt.Sprintf("key-%03d", i)] = strings.Repeat("v", 100)
	}
	s.mu.Unlock()

	const budget = 2000
	out := FormatForContextBudgetID(ctxID, budget)
	if out == "" {
		t.Fatal("budgeted notes returned empty")
	}
	if len(out) > budget+200 {
		t.Fatalf("notes block %d bytes exceeds budget %d + trailer", len(out), budget)
	}
	if !strings.Contains(out, "more notes omitted") {
		t.Fatalf("notes block missing truncation trailer:\n%s", out)
	}
	if !strings.Contains(out, "key-000:") {
		t.Fatalf("notes block missing first key:\n%s", out)
	}
	if i, j := strings.Index(out, "key-000:"), strings.Index(out, "key-001:"); i < 0 || j < 0 || i > j {
		t.Fatalf("notes not rendered in key order:\n%s", out)
	}
	if again := FormatForContextBudgetID(ctxID, budget); again != out {
		t.Fatal("budgeted notes are not deterministic")
	}
}

func TestFormatForContextBudgetIDEmpty(t *testing.T) {
	const ctxID = "scan-notes-budget-empty"
	t.Cleanup(func() { CleanupContext(ctxID) })
	if got := FormatForContextBudgetID(ctxID, 100); got != "" {
		t.Fatalf("empty store returned %q", got)
	}
}

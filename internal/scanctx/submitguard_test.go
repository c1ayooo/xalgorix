package scanctx

import (
	"strings"
	"testing"
	"time"
)

func TestSubmitGuardProvenance(t *testing.T) {
	g := NewSubmitGuard()
	if g.Observed("flag{abc}") {
		t.Fatal("empty ledger must not claim provenance")
	}
	g.Observe("HTTP/1.1 200 OK\n{\"flag\":\"flag{abc-123}\"}")
	if !g.Observed("flag{abc-123}") {
		t.Fatal("observed output must satisfy provenance")
	}
	if g.Observed("flag{never-printed}") {
		t.Fatal("unobserved value must not satisfy provenance")
	}
}

func TestSubmitGuardCatalogOnlyLearnsFromChallengeOutput(t *testing.T) {
	g := NewSubmitGuard()
	// A plain JSON body with a code field is not a challenge catalog.
	g.Observe(`{"code":"internal-error","message":"boom"}`)
	if g.CatalogSize() != 0 {
		t.Fatalf("catalog learned bogus codes: size=%d", g.CatalogSize())
	}
	g.Observe(`{"challenges":[{"unique_code":"c-08","score":100},{"unique_code":"a-03","score":500}]}`)
	if !g.CatalogContains("c-08") || !g.CatalogContains("a-03") {
		t.Fatalf("catalog missing listed codes: size=%d", g.CatalogSize())
	}
	if g.CatalogContains("c-99") {
		t.Fatal("catalog invented c-99")
	}
}

func TestSubmitGuardBudgetAndDedupe(t *testing.T) {
	g := NewSubmitGuard()
	now := time.Now()
	if v := g.Reserve("c-08", now); !v.Allowed {
		t.Fatalf("first attempt refused: %s", v.Reason)
	}
	// Same code inside the cooldown.
	if v := g.Reserve("c-08", now.Add(time.Second)); v.Allowed {
		t.Fatal("cooldown did not block an immediate repeat")
	}
	if got := g.Attempts("c-08"); got != 1 {
		t.Fatalf("attempts = %d, want 1 (blocked calls must not consume budget)", got)
	}
	// Different codes still allowed until the window fills.
	for _, code := range []string{"c-09", "c-10", "c-11"} {
		if v := g.Reserve(code, now.Add(2*time.Second)); !v.Allowed {
			t.Fatalf("%s refused: %s", code, v.Reason)
		}
	}
	// 1 + 3 = 4 in the window; the fifth is the last allowed one.
	if v := g.Reserve("c-12", now.Add(3*time.Second)); !v.Allowed {
		t.Fatalf("fifth attempt refused too early: %s", v.Reason)
	}
	if v := g.Reserve("c-13", now.Add(4*time.Second)); v.Allowed {
		t.Fatal("rate limit did not fire at the budget cap")
	}
	// After the window passes, budget frees up again.
	if v := g.Reserve("c-12", now.Add(submitGuardBudgetWindow+time.Second)); !v.Allowed {
		t.Fatalf("window did not reset: %s", v.Reason)
	}
}

func TestSubmitGuardCompletedChallenge(t *testing.T) {
	g := NewSubmitGuard()
	now := time.Now()
	if v := g.Reserve("a-01", now); !v.Allowed {
		t.Fatalf("first attempt refused: %s", v.Reason)
	}
	g.MarkCompleted("a-01")
	if !g.Completed("a-01") {
		t.Fatal("completion not recorded")
	}
	v := g.Reserve("a-01", now.Add(time.Hour))
	if v.Allowed {
		t.Fatal("completed challenge must never be re-submitted")
	}
	if !strings.Contains(v.Reason, "already submitted") {
		t.Fatalf("reason = %q", v.Reason)
	}
}

func TestScanContextEnsureSubmitGuard(t *testing.T) {
	sc := New("guard-ensure", t.TempDir())
	if sc.SubmitGuard == nil {
		t.Fatal("New must initialize the submit guard")
	}
	literal := &ScanContext{ID: "literal"}
	if got := literal.EnsureSubmitGuard(); got == nil {
		t.Fatal("EnsureSubmitGuard must materialize on literal contexts")
	}
	if literal.EnsureSubmitGuard() != literal.EnsureSubmitGuard() {
		t.Fatal("EnsureSubmitGuard must be stable")
	}
	var nilCtx *ScanContext
	if nilCtx.EnsureSubmitGuard() != nil {
		t.Fatal("nil context must not allocate")
	}
}

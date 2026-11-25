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

// TestSubmitGuardLearnsBareChallengeListing pins the production bug: the
// platform's /challenges payload is a bare JSON array, so the old keyword gate
// never learned the catalog and real codes were refused as "not in this
// round's challenge catalog".
func TestSubmitGuardLearnsBareChallengeListing(t *testing.T) {
	g := NewSubmitGuard()
	g.Observe(`[{"unique_code":"e1-01","difficulty":"medium","total_score":250,"flag_count":1,"correct_flag_count":0,"is_completed":false,"container_status":"stopped","container_addr":[]},` +
		`{"unique_code":"f1-04","difficulty":"easy","total_score":100,"flag_count":1,"correct_flag_count":0,"is_completed":false,"container_status":"stopped","container_addr":[]},` +
		`{"unique_code":"c-08","difficulty":"medium","total_score":300,"flag_count":1,"correct_flag_count":0,"is_completed":false,"container_status":"stopped","container_addr":[]}]`)
	if got := g.CatalogSize(); got != 3 {
		t.Fatalf("catalog size = %d, want 3", got)
	}
	for _, code := range []string{"e1-01", "f1-04", "c-08"} {
		if !g.CatalogContains(code) {
			t.Fatalf("catalog missing %s", code)
		}
	}
}

// TestSubmitGuardLearnsStartAcknowledgement keeps lifecycle replies
// authoritative even when no full listing was observed: a start/close response
// naming a unique_code proves the code exists in this round.
func TestSubmitGuardLearnsStartAcknowledgement(t *testing.T) {
	g := NewSubmitGuard()
	g.Observe(`{"unique_code":"h-10","container_addr":["10.0.189.10:8443"]}`)
	if !g.CatalogContains("h-10") {
		t.Fatal("start acknowledgement did not teach h-10")
	}
	g.Observe(`{"unique_code":"c-99","error":"boom"}`)
	if g.CatalogContains("c-99") {
		t.Fatal("plain echo taught an unacknowledged code")
	}
}

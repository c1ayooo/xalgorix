package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// hookBeliefPoolBrief projects the VOI-ranked schedulable pool into per-turn
// visibility for forms the delegation nudge never reaches (CORE_LOOP_AUDIT
// §3 gap ①): CTF/benchmark missions and plan-idle single-agent scans.

func TestBeliefPoolBriefDeliversForCTFMission(t *testing.T) {
	ctxID := "belief-" + t.Name()
	sc := scanctx.New(ctxID, "")
	scanctx.Activate(sc)
	first := seedQueued(sc.Ledger, "idor", "/a", 0.5)
	second := seedQueued(sc.Ledger, "sqli", "/b", 0.9)
	state := &ScanState{ScanContextID: ctxID, CTFMission: true}

	res := hookBeliefPoolBrief(state, nil)
	if len(res.Directives) != 1 {
		t.Fatalf("CTF mission with queued pool must brief once, got %d directives", len(res.Directives))
	}
	d := res.Directives[0]
	if d.DedupeKey != "belief-pool-brief" || d.Priority != DirectivePriorityPlanner {
		t.Fatalf("unexpected directive identity: key=%q priority=%d", d.DedupeKey, d.Priority)
	}
	for _, want := range []string{"BELIEF POOL", first, second, "claim_next_hypothesis"} {
		if !strings.Contains(d.Content, want) {
			t.Fatalf("brief content missing %q:\n%s", want, d.Content)
		}
	}
}

func TestBeliefPoolBriefStaysQuietUntilPoolChanges(t *testing.T) {
	ctxID := "belief-" + t.Name()
	sc := scanctx.New(ctxID, "")
	scanctx.Activate(sc)
	seedQueued(sc.Ledger, "idor", "/a", 0.5)
	seedQueued(sc.Ledger, "sqli", "/b", 0.9) // stays queued through the claim below
	state := &ScanState{ScanContextID: ctxID, CTFMission: true}

	res := hookBeliefPoolBrief(state, nil)
	if len(res.Directives) != 1 {
		t.Fatalf("expected first brief, got %d directives", len(res.Directives))
	}
	res.Directives[0].OnDelivered(state)

	if again := hookBeliefPoolBrief(state, nil); len(again.Directives) != 0 {
		t.Fatalf("unchanged pool must not re-deliver, got %d directives", len(again.Directives))
	}

	// A claim mutates the pool (queued -> testing shrinks it): re-brief.
	if _, claimed, err := sc.Ledger.ClaimNextAs("", "tester", scanctx.AuditActorForAgent("tester")); err != nil || !claimed {
		t.Fatalf("claim failed: claimed=%v err=%v", claimed, err)
	}
	if renewed := hookBeliefPoolBrief(state, nil); len(renewed.Directives) != 1 {
		t.Fatalf("changed pool must re-deliver, got %d directives", len(renewed.Directives))
	}
}

func TestBeliefPoolBriefSkipsDelegationPaths(t *testing.T) {
	ctxID := "belief-" + t.Name()
	sc := scanctx.New(ctxID, "")
	scanctx.Activate(sc)
	seedQueued(sc.Ledger, "idor", "/a", 0.5)

	for name, mutate := range map[string]func(*ScanState){
		"specialist":      func(s *ScanState) { s.DelegatedAgent = true },
		"delegation root": func(s *ScanState) { s.DelegationEnabled = true },
		"discovery mode":  func(s *ScanState) { s.DiscoveryMode = true },
		"recon-only mode": func(s *ScanState) { s.ReconOnlyMode = true },
		"plan mid-flight": func(s *ScanState) { s.CTFMission = false },
	} {
		state := &ScanState{ScanContextID: ctxID, CTFMission: true}
		mutate(state)
		if res := hookBeliefPoolBrief(state, nil); len(res.Directives) != 0 {
			t.Fatalf("%s must stay quiet, got %d directives", name, len(res.Directives))
		}
	}
}

func TestBeliefPoolBriefSkipsWithoutLedgerOrPool(t *testing.T) {
	// Unregistered context: ledgerForState resolves nil.
	if res := hookBeliefPoolBrief(&ScanState{ScanContextID: "missing-" + t.Name(), CTFMission: true}, nil); len(res.Directives) != 0 {
		t.Fatalf("missing ledger must stay quiet, got %d directives", len(res.Directives))
	}

	// Empty ledger: nothing schedulable.
	ctxID := "belief-" + t.Name()
	sc := scanctx.New(ctxID, "")
	scanctx.Activate(sc)
	state := &ScanState{ScanContextID: ctxID, CTFMission: true}
	if res := hookBeliefPoolBrief(state, nil); len(res.Directives) != 0 {
		t.Fatalf("empty ledger must stay quiet, got %d directives", len(res.Directives))
	}

	// Settled ledger: every hypothesis terminal.
	id := seedQueued(sc.Ledger, "idor", "/a", 0.5)
	sc.Ledger.SetStatus(id, scanctx.HypothesisProven, "tested")
	if res := hookBeliefPoolBrief(state, nil); len(res.Directives) != 0 {
		t.Fatalf("settled ledger must stay quiet, got %d directives", len(res.Directives))
	}
}

func TestBeliefPoolBriefPlanIdleProfessional(t *testing.T) {
	ctxID := "belief-" + t.Name()
	sc := scanctx.New(ctxID, "")
	scanctx.Activate(sc)
	seedQueued(sc.Ledger, "idor", "/a", 0.5)

	mkState := func(plan *Plan) *ScanState {
		return &ScanState{
			ScanContextID: ctxID, ProfessionalAssessment: true,
			Plan: plan, PlanBuilt: plan != nil,
		}
	}

	// Structural plan still has open work: the planner owns the loop.
	open := NewPlan()
	open.add(&Task{ID: "t1", Title: "probe", Status: TaskPending})
	if res := hookBeliefPoolBrief(mkState(open), nil); len(res.Directives) != 0 {
		t.Fatalf("mid-plan professional scan must stay quiet, got %d directives", len(res.Directives))
	}

	// Exhausted structural plan (idle tail): the belief pool takes over.
	exhausted := NewPlan()
	exhausted.add(&Task{ID: "t1", Title: "probe", Status: TaskCompleted})
	res := hookBeliefPoolBrief(mkState(exhausted), nil)
	if len(res.Directives) != 1 {
		t.Fatalf("plan-idle professional scan must brief, got %d directives", len(res.Directives))
	}
	if !strings.Contains(res.Directives[0].Content, "BELIEF POOL") {
		t.Fatalf("unexpected brief content:\n%s", res.Directives[0].Content)
	}

	// No plan at all: never briefs a professional scan.
	if res := hookBeliefPoolBrief(mkState(nil), nil); len(res.Directives) != 0 {
		t.Fatalf("planless professional scan must stay quiet, got %d directives", len(res.Directives))
	}
}

package orchestrator

// planner_voi_test.go — IG4 acceptance: the pure intent priority function, the
// deterministic ledger VOI aggregation, and the apply() wiring. VOI may only
// reorder intents; it never changes payload, claim CAS or the report gate.

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestIntentPriorityDeterministic(t *testing.T) {
	cases := []struct {
		phase  int
		voi    float64
		weight float64
	}{
		{1, 0, 0}, {6, 0.4, 0.3}, {22, 1, 0.3}, {11, 0.5, 0.17}, {0, 2, -1},
	}
	for _, c := range cases {
		first := intentPriority(c.phase, c.voi, c.weight)
		for i := 0; i < 5; i++ {
			if got := intentPriority(c.phase, c.voi, c.weight); got != first {
				t.Fatalf("intentPriority(%d,%v,%v) not deterministic: %d vs %d",
					c.phase, c.voi, c.weight, first, got)
			}
		}
	}
}

func TestIntentPriorityPhaseOrderingPreservedWithoutVOI(t *testing.T) {
	prev := int(^uint(0) >> 1)
	for phase := 1; phase <= 22; phase++ {
		got := intentPriority(phase, 0, 0)
		if got >= prev {
			t.Fatalf("phase %d priority %d does not rank below the previous phase (%d)", phase, got, prev)
		}
		prev = got
	}
	// Same ordering with a non-zero weight as long as VOI is constant (all
	// zero => the VOI term is a constant offset).
	prev = int(^uint(0) >> 1)
	for phase := 1; phase <= 22; phase++ {
		got := intentPriority(phase, 0, 0.30)
		if got >= prev {
			t.Fatalf("phase %d priority %d does not rank below the previous phase with w=0.30 (%d)", phase, got, prev)
		}
		prev = got
	}
}

func TestIntentPriorityWeightCapAndClamps(t *testing.T) {
	// The weight is capped at 0.30 (scanctx.MaxVOIWeight).
	if got, want := intentPriority(6, 1, 5), intentPriority(6, 1, 0.30); got != want {
		t.Fatalf("weight above cap = %d, want %d (capped)", got, want)
	}
	// NaN / negative weights mean "no VOI term".
	if got, want := intentPriority(6, 1, math.NaN()), intentPriority(6, 0, 0); got != want {
		t.Fatalf("NaN weight = %d, want %d", got, want)
	}
	if got, want := intentPriority(6, 1, -3), intentPriority(6, 0, 0); got != want {
		t.Fatalf("negative weight = %d, want %d", got, want)
	}
	// VOI is clamped into [0,1].
	if got, want := intentPriority(6, 2, 0.30), intentPriority(6, 1, 0.30); got != want {
		t.Fatalf("VOI above 1 = %d, want %d (clamped)", got, want)
	}
	if got, want := intentPriority(6, -1, 0.30), intentPriority(6, 0, 0.30); got != want {
		t.Fatalf("negative VOI = %d, want %d", got, want)
	}
	// Phase is clamped into [1,22].
	if got, want := intentPriority(0, 0, 0), intentPriority(1, 0, 0); got != want {
		t.Fatalf("phase 0 = %d, want phase 1 = %d", got, want)
	}
	if got, want := intentPriority(99, 0, 0), intentPriority(22, 0, 0); got != want {
		t.Fatalf("phase 99 = %d, want phase 22 = %d", got, want)
	}
	// Phase 1 with w=0 scores the top of the scale, phase 22 the bottom.
	if got := intentPriority(1, 0, 0); got != 1000 {
		t.Fatalf("phase 1 priority = %d, want 1000", got)
	}
	if got := intentPriority(22, 0, 0); got != 0 {
		t.Fatalf("phase 22 priority = %d, want 0", got)
	}
}

func TestIntentVOIMaxAggregation(t *testing.T) {
	hyps := []scanctx.Hypothesis{
		{ID: "H-1", VulnClass: "sqli", Endpoint: "/login", VOI: 0.4, VOIEstimated: true},
		{ID: "H-2", VulnClass: "sqli", Endpoint: "/login", VOI: 0.8, VOIEstimated: true},
		{ID: "H-3", VulnClass: "sqli", Endpoint: "/login", VOI: 0.95, VOIEstimated: false}, // not estimated: ignored
		{ID: "H-4", VulnClass: "xss", Endpoint: "/login", VOI: 1, VOIEstimated: true},      // wrong class
		{ID: "H-5", VulnClass: "sqli", Endpoint: "/admin", VOI: 1, VOIEstimated: true},     // wrong endpoint
	}
	if got := intentVOI(hyps, "10.0.1.5:8080", "/login", "sqli"); got != 0.8 {
		t.Fatalf("aggregated VOI = %v, want the max estimated match 0.8", got)
	}
	// Matching by target when no endpoint is supplied.
	if got := intentVOI([]scanctx.Hypothesis{
		{VulnClass: "sqli", Target: "10.0.1.5:8080", VOI: 0.6, VOIEstimated: true},
	}, "10.0.1.5:8080", "", "sqli"); got != 0.6 {
		t.Fatalf("target-matched VOI = %v, want 0.6", got)
	}
	// Missing coordinates, empty pool and unestimated-only pool all yield 0.
	if got := intentVOI(hyps, "", "", "sqli"); got != 0 {
		t.Fatalf("VOI without coordinates = %v, want 0", got)
	}
	if got := intentVOI(nil, "10.0.1.5:8080", "/login", "sqli"); got != 0 {
		t.Fatalf("VOI from empty ledger = %v, want 0", got)
	}
	if got := intentVOI([]scanctx.Hypothesis{
		{ID: "H-1", VulnClass: "sqli", Endpoint: "/login", VOI: 0.9},
	}, "10.0.1.5:8080", "/login", "sqli"); got != 0 {
		t.Fatalf("VOI from an unestimated hypothesis = %v, want 0", got)
	}
}

func applyOne(t *testing.T, store graph.Repository, sctx *scanctx.ScanContext, in plannerIntent) graph.PlanTask {
	t.Helper()
	scanID := "voi-apply"
	p := &Planner{cfg: PlannerConfig{ScanID: scanID, Repo: store, ScanCtx: sctx}, ctx: context.Background()}
	if added, _ := p.apply(plannerOutput{Intents: []plannerIntent{in}}); added != 1 {
		t.Fatalf("apply added = %d, want 1", added)
	}
	tasks, err := store.PlanTasks(context.Background(), scanID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("PlanTasks err=%v tasks=%+v", err, tasks)
	}
	return tasks[0]
}

func ledgerWithVOI(t *testing.T, id, class, endpoint string, voi float64) *scanctx.ScanContext {
	t.Helper()
	sctx := scanctx.New(id, "")
	sctx.Ledger.Upsert(scanctx.Hypothesis{
		ID: "H-1", VulnClass: class, Endpoint: endpoint, Status: scanctx.HypothesisQueued,
		VOI: voi, VOIEstimated: true,
	})
	return sctx
}

func TestApplyVOIChangesOnlyPriority(t *testing.T) {
	in := plannerIntent{
		TaskID: "t-sqli", Title: "Test SQLi", Phase: 6, VulnClass: "sqli",
		Endpoint: "/login", Target: "10.0.1.5:8080", TargetKind: "container",
	}
	low := ledgerWithVOI(t, "voi-low", "sqli", "/login", 0.1)
	low.Ledger.SetVOIWeight(0.30)
	high := ledgerWithVOI(t, "voi-high", "sqli", "/login", 0.9)
	high.Ledger.SetVOIWeight(0.30)

	lowTask := applyOne(t, graph.NewMemoryStore(), low, in)
	highTask := applyOne(t, graph.NewMemoryStore(), high, in)
	if highTask.Priority <= lowTask.Priority {
		t.Fatalf("higher VOI did not raise priority: low=%d high=%d", lowTask.Priority, highTask.Priority)
	}
	lowPayload, _ := json.Marshal(lowTask)
	highPayload, _ := json.Marshal(highTask)
	var lowMap, highMap map[string]any
	_ = json.Unmarshal(lowPayload, &lowMap)
	_ = json.Unmarshal(highPayload, &highMap)
	for _, key := range []string{"priority", "node_id", "created_at", "updated_at"} {
		delete(lowMap, key)
		delete(highMap, key)
	}
	lowNorm, _ := json.Marshal(lowMap)
	highNorm, _ := json.Marshal(highMap)
	if string(lowNorm) != string(highNorm) {
		t.Fatalf("VOI changed more than priority:\nlow  = %s\nhigh = %s", lowNorm, highNorm)
	}

	// With weight 0 (the default) VOI is ignored entirely.
	lowZero := ledgerWithVOI(t, "voi-zero-low", "sqli", "/login", 0.1)
	highZero := ledgerWithVOI(t, "voi-zero-high", "sqli", "/login", 0.9)
	if a, b := applyOne(t, graph.NewMemoryStore(), lowZero, in), applyOne(t, graph.NewMemoryStore(), highZero, in); a.Priority != b.Priority {
		t.Fatalf("VOI affected priority with weight 0: %d vs %d", a.Priority, b.Priority)
	}
}

func TestApplyMissingVOIEqualsZero(t *testing.T) {
	in := plannerIntent{TaskID: "t1", Title: "Test SSRF", Phase: 7, VulnClass: "ssrf", Endpoint: "/fetch", Target: "10.0.1.5:8080"}
	withUnestimated := scanctx.New("voi-missing", "")
	withUnestimated.Ledger.Upsert(scanctx.Hypothesis{
		ID: "H-1", VulnClass: "ssrf", Endpoint: "/fetch", Status: scanctx.HypothesisQueued,
		VOI: 0.9, // sanitize zeroes this because VOIEstimated is false
	})
	withUnestimated.Ledger.SetVOIWeight(0.30)
	// Same weight, empty ledger: an unestimated VOI must aggregate to the same
	// 0 the empty pool yields.
	noHypotheses := scanctx.New("voi-none", "")
	noHypotheses.Ledger.SetVOIWeight(0.30)

	a := applyOne(t, graph.NewMemoryStore(), withUnestimated, in)
	b := applyOne(t, graph.NewMemoryStore(), noHypotheses, in)
	if a.Priority != b.Priority {
		t.Fatalf("missing/unestimated VOI must rank as 0: %d vs %d", a.Priority, b.Priority)
	}
	// And its aggregated value is literally zero.
	if got := intentVOI(withUnestimated.Ledger.All(), "10.0.1.5:8080", "/fetch", "ssrf"); got != 0 {
		t.Fatalf("unestimated VOI aggregated to %v, want 0", got)
	}
}

func TestApplyPhaseOrderingPreserved(t *testing.T) {
	store := graph.NewMemoryStore()
	p := &Planner{cfg: PlannerConfig{ScanID: "voi-order", Repo: store}, ctx: context.Background()}
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{
		{TaskID: "p2", Title: "recon deeper", Phase: 2, VulnClass: "recon", Endpoint: "/"},
		{TaskID: "p6", Title: "injection", Phase: 6, VulnClass: "sqli", Endpoint: "/login"},
		{TaskID: "p18", Title: "cms", Phase: 18, VulnClass: "cms", Endpoint: "/"},
	}})
	if added != 3 {
		t.Fatalf("added = %d, want 3", added)
	}
	tasks, _ := store.PlanTasks(context.Background(), "voi-order")
	byID := map[string]int{}
	for _, task := range tasks {
		byID[task.TaskID] = task.Priority
	}
	if !(byID["p2"] > byID["p6"] && byID["p6"] > byID["p18"]) {
		t.Fatalf("phase ordering not preserved: p2=%d p6=%d p18=%d", byID["p2"], byID["p6"], byID["p18"])
	}
}

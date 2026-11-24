package orchestrator

// gapgen_test.go — IG5 acceptance: the gap candidate generator is
// deterministic, bounded, refuses placeholder/symbolic/control-plane subjects,
// and yields nothing for an empty snapshot.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestGenerateGapCandidatesDeterministicAndBounded(t *testing.T) {
	probed := map[string][]string{}
	queued := make([]scanctx.Hypothesis, 0, 40)
	for i := 0; i < 40; i++ {
		ep := "/ep-" + infoIDFor(i)
		probed[ep] = []string{"sqli"}
		if i%2 == 0 {
			queued = append(queued, scanctx.Hypothesis{
				ID: "H-" + infoIDFor(i), VulnClass: "sqli", Endpoint: ep,
			})
		}
	}
	in := gapCandidateInput{
		Target:         "10.0.1.5:8080",
		Probed:         probed,
		Queued:         queued,
		CoveredIntents: map[string]bool{},
	}
	first := generateGapCandidates(in)
	second := generateGapCandidates(in)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic output:\nfirst  = %+v\nsecond = %+v", first, second)
	}
	if len(first) == 0 {
		t.Fatal("expected candidates")
	}
	if len(first) > maxGapCandidates {
		t.Fatalf("candidates = %d, want <= %d", len(first), maxGapCandidates)
	}
	for _, c := range first {
		if c.Subject != "10.0.1.5:8080" || c.Endpoint == "" {
			t.Fatalf("candidate without a concrete subject/endpoint: %+v", c)
		}
	}
	// The queued-hypothesis rule outranks plain unverified endpoints.
	if first[0].Score != gapScoreUnscheduledHypothesis {
		t.Fatalf("top candidate score = %v, want the unscheduled-hypothesis score", first[0].Score)
	}
}

func TestGenerateGapCandidatesRejectsNonDispatchableSubjects(t *testing.T) {
	base := gapCandidateInput{
		Probed: map[string][]string{"/login": {"sqli"}},
		Queued: []scanctx.Hypothesis{{ID: "H-1", VulnClass: "sqli", Endpoint: "/login"}},
	}
	for _, target := range []string{"", "   ", "CONTAINER_ADDR", "pending-container-address", "task", "task:queue", "tsecbench.zc.tencent.com"} {
		in := base
		in.Target = target
		in.ControlPlane = []string{"tsecbench.zc.tencent.com"}
		if got := generateGapCandidates(in); len(got) != 0 {
			t.Fatalf("target %q produced candidates: %+v", target, got)
		}
	}
}

func TestGenerateGapCandidatesEmptyInput(t *testing.T) {
	if got := generateGapCandidates(gapCandidateInput{}); len(got) != 0 {
		t.Fatalf("empty input produced candidates: %+v", got)
	}
	if got := generateGapCandidates(gapCandidateInput{Target: "10.0.1.5:8080"}); len(got) != 0 {
		t.Fatalf("coordinator-free input produced candidates: %+v", got)
	}
}

func TestGenerateGapCandidatesHonorsOpenIntents(t *testing.T) {
	in := gapCandidateInput{
		Target:   "10.0.1.5:8080",
		Probed:   map[string][]string{"/login": {"sqli"}, "/search": {"xss"}},
		Verified: map[string][]string{"/search": {"xss"}}, // verified: not a coverage gap
		Queued: []scanctx.Hypothesis{
			{ID: "H-1", VulnClass: "sqli", Endpoint: "/login"},
		},
		CoveredIntents: map[string]bool{"/login|sqli": true},
	}
	got := generateGapCandidates(in)
	if len(got) != 0 {
		t.Fatalf("covered/verified coordinates produced candidates: %+v", got)
	}
	// Removing open-intent coverage surfaces the queued belief again.
	in.CoveredIntents = map[string]bool{}
	got = generateGapCandidates(in)
	if len(got) != 1 || got[0].Endpoint != "/login" || got[0].Purpose != "sqli" {
		t.Fatalf("uncovered queued belief not surfaced: %+v", got)
	}
	if len(got[0].Refs) != 2 || got[0].Refs[0] != "H-1" {
		t.Fatalf("candidate refs = %v, want [H-1 /login]", got[0].Refs)
	}
}

func TestGapCandidatesBlockIsReadOnlyAndBounded(t *testing.T) {
	store := graph.NewMemoryStore()
	sctx := scanctx.New("gap-block", "")
	sctx.Coverage.Mark("/login", "sqli")
	sctx.Ledger.Upsert(scanctx.Hypothesis{ID: "H-1", VulnClass: "sqli", Endpoint: "/login", Status: scanctx.HypothesisQueued})
	p := &Planner{cfg: PlannerConfig{
		ScanID: "gap-block", Repo: store, ScanCtx: sctx, Target: "10.0.1.5:8080",
	}, ctx: context.Background()}

	first := p.overview()
	second := p.overview()
	if first != second {
		t.Fatalf("overview with gap candidates is not stable:\n%s\n---\n%s", first, second)
	}
	if !strings.Contains(first, "Gap candidates") || !strings.Contains(first, "endpoint=/login purpose=sqli") {
		t.Fatalf("gap candidates block missing:\n%s", first)
	}
	// Read-only: no intent was written by rendering the overview.
	if tasks, _ := store.PlanTasks(context.Background(), "gap-block"); len(tasks) != 0 {
		t.Fatalf("overview wrote graph state: %+v", tasks)
	}
	lines := 0
	for _, line := range strings.Split(first, "\n") {
		if strings.HasPrefix(line, "- endpoint=") {
			lines++
		}
	}
	if lines == 0 || lines > maxGapCandidates {
		t.Fatalf("gap candidate lines = %d, want within [1,%d]", lines, maxGapCandidates)
	}
}

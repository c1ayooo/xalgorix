package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func typedPool(t *testing.T, id string) (*Pool, *scanctx.ScanContext) {
	t.Helper()
	sctx := scanctx.New(id, t.TempDir())
	t.Cleanup(sctx.Close)
	pool, err := New(context.Background(), Config{
		ScanID: id, Workers: 2, Repo: graph.NewMemoryStore(), Root: &fakeRoot{}, ScanCtx: sctx,
	})
	if err != nil {
		t.Fatal(err)
	}
	return pool, sctx
}

// TestStallReportTypedArtifactClearsStall pins P2 acceptance 1: artifact
// progress keeps an analysis worker out of the stalled set.
func TestStallReportTypedArtifactClearsStall(t *testing.T) {
	pool, sctx := typedPool(t, "typed-artifact")
	sctx.Progress.SetProfile("agent-a", scanctx.ProfileBinary)
	sctx.Progress.MarkSignal("agent-a", scanctx.SignalArtifact)
	pool.trackActive("u-1", "agent-a", "worker", time.Now().Add(-40*time.Minute))
	report := pool.StallReport()
	if len(report) != 0 {
		t.Fatalf("artifact progress still reported stalled: %+v", report)
	}
}

func TestStallReportUsesProfileWindow(t *testing.T) {
	pool, sctx := typedPool(t, "typed-window")
	sctx.Progress.SetProfile("agent-b", scanctx.ProfileBinary)
	pool.trackActive("u-b", "agent-b", "worker", time.Now().Add(-25*time.Minute))
	if report := pool.StallReport(); len(report) != 0 {
		t.Fatalf("binary 30m window should not stall at 25m: %+v", report)
	}
	sctx.Progress.SetProfile("agent-b", scanctx.ProfileWeb)
	if report := pool.StallReport(); len(report) != 1 || report[0].UnitID != "u-b" {
		t.Fatalf("web 20m window should stall at 25m: %+v", report)
	}
}

func TestStallReportCarriesTypedStatus(t *testing.T) {
	pool, _ := typedPool(t, "typed-status")
	pool.trackActive("u-c", "agent-c", "worker", time.Now().Add(-45*time.Minute))
	report := pool.StallReport()
	if len(report) != 1 || report[0].ProgressStatus != scanctx.ProgressStalled || report[0].ProgressReason == "" {
		t.Fatalf("stall report = %+v", report)
	}
}

func TestLastProgressAtUsesTypedSignal(t *testing.T) {
	pool, sctx := typedPool(t, "typed-last")
	started := time.Now().Add(-time.Hour)
	sctx.Progress.MarkSignal("agent-d", scanctx.SignalCoverage)
	if got := pool.lastProgressAt("agent-d", started); !got.After(started) {
		t.Fatalf("lastProgressAt = %s, want a typed-signal timestamp after %s", got, started)
	}
}

func TestProgressProfileForIntent(t *testing.T) {
	pool := &Pool{}
	cases := []struct {
		payload string
		lane    string
		want    scanctx.ProgressProfile
	}{
		{`{"target_kind":"web","target":"https://app.example"}`, "worker", scanctx.ProfileWeb},
		{`{"target_kind":"task","target":"task:analysis"}`, "analysis", scanctx.ProfileMultistage},
		{`{"target_kind":"container","target":"10.0.0.5:22","progress_profile":"binary"}`, "worker", scanctx.ProfileBinary},
		{`{"target_kind":"container","target":"10.0.0.5:22"}`, "worker", scanctx.ProfileDefault},
	}
	for _, c := range cases {
		node := &graph.Node{Payload: c.payload}
		if got := pool.progressProfileForIntent(node, c.lane); got != c.want {
			t.Fatalf("%s/%s = %s, want %s", c.payload, c.lane, got, c.want)
		}
	}
}

func TestPlannerPersistsProgressProfile(t *testing.T) {
	store := graph.NewMemoryStore()
	sctx := scanctx.New("typed-profile", t.TempDir())
	t.Cleanup(sctx.Close)
	p := &Planner{cfg: PlannerConfig{ScanID: "typed-profile", Repo: store, ScanCtx: sctx}, ctx: context.Background()}
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		TaskID: "binary-1", Title: "Analyze firmware image", Phase: 4,
		Target: "10.0.0.5:22", TargetKind: "container", ProgressProfile: "binary",
	}}})
	if added != 1 {
		t.Fatalf("added = %d", added)
	}
	tasks, _ := store.PlanTasks(context.Background(), "typed-profile")
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	node, err := store.GetNode(context.Background(), "typed-profile", tasks[0].NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got := node.PayloadJSON()["progress_profile"]; got != "binary" {
		t.Fatalf("progress_profile = %v", got)
	}
}

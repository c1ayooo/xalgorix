package agent

import (
	"context"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/agentsgraph"
)

func TestPlanSnapshotEmptyPlanIsExplicit(t *testing.T) {
	a := &Agent{state: NewScanState()}
	view, changed := a.publishPlanSnapshot()
	if !changed {
		t.Fatal("first publish must report a change")
	}
	if view.Present || view.Source != "none" {
		t.Fatalf("empty plan projection = %#v", view)
	}
	if view.Tasks == nil || len(view.Tasks) != 0 {
		t.Fatalf("tasks must be an empty array (not null): %#v", view.Tasks)
	}
	if again, changed := a.publishPlanSnapshot(); changed || again.Present {
		t.Fatalf("unchanged republish must not emit: changed=%v view=%#v", changed, again)
	}
}

func TestPlanSnapshotAggregatesMixedProvenance(t *testing.T) {
	st := NewScanState()
	st.PlanBuilt = true
	st.PlanSurfaceRevision = "surf-9"
	st.Plan = NewPlan()
	st.Plan.add(&Task{ID: "recon", Title: "Map surface", Phase: 1, Status: TaskCompleted, Origin: "auto"})
	st.Plan.add(&Task{ID: "test-sqli", Title: "Test SQLi", Phase: 6, Status: TaskActive, VulnClass: "sqli", Endpoint: "/search", Origin: "llm", DependsOn: []string{"recon"}})
	st.Plan.add(&Task{ID: "test-xss", Title: "Test XSS", Phase: 7, Status: TaskSkipped, VulnClass: "xss", Origin: "auto-plan-surface", Disposition: "not_applicable"})

	a := &Agent{state: st}
	view, changed := a.publishPlanSnapshot()
	if !changed {
		t.Fatal("expected a change for a newly built plan")
	}
	if !view.Present || view.Source != "mixed" {
		t.Fatalf("source = %q present=%v, want mixed/true", view.Source, view.Present)
	}
	if view.PlanRevision != "surf-9" || view.SurfaceRevision != "surf-9" {
		t.Fatalf("revision lost: %#v", view)
	}
	if view.CurrentPhase != 6 {
		t.Fatalf("current phase = %d, want the active task's phase 6", view.CurrentPhase)
	}
	if view.Counters.Total != 3 || view.Counters.Completed != 1 || view.Counters.Skipped != 1 || view.Counters.Unfinished != 1 {
		t.Fatalf("counters = %#v", view.Counters)
	}
	// Task detail must survive verbatim (this is what the Planner pane renders).
	var sqli *PlanTaskView
	for i := range view.Tasks {
		if view.Tasks[i].ID == "test-sqli" {
			sqli = &view.Tasks[i]
		}
	}
	if sqli == nil || sqli.VulnClass != "sqli" || sqli.Endpoint != "/search" || len(sqli.DependsOn) != 1 {
		t.Fatalf("task detail lost: %#v", sqli)
	}
	// The published snapshot is what handlers read.
	if got := a.PlanSnapshot(); !got.Present || len(got.Tasks) != 3 {
		t.Fatalf("PlanSnapshot = %#v", got)
	}
}

func TestDelegationSnapshotReportsPoolAndQueue(t *testing.T) {
	release := make(chan struct{})
	graph := agentsgraph.NewWithConfig(context.Background(), 4, 1, func(_ context.Context, _ string, _ string, _ []string, _ string) (string, error) {
		<-release
		return "done", nil
	})
	t.Cleanup(graph.Stop)
	a := &Agent{agentGraph: graph}

	reg := tools.NewRegistry()
	graph.Register(reg)
	spawn := func() {
		if _, err := reg.Execute("spawn_agent", map[string]string{"name": "lane", "task": "work"}); err != nil {
			t.Fatalf("spawn: %v", err)
		}
	}
	spawn()
	spawn()

	deadline := time.Now().Add(3 * time.Second)
	for {
		view := a.DelegationSnapshot()
		if view.Available && view.Running == 1 && view.Queued == 1 {
			if view.Limit != 1 || view.MaxAgents != 4 {
				t.Fatalf("pool caps = %#v", view)
			}
			if len(view.Agents) != 2 {
				t.Fatalf("agents = %d, want 2", len(view.Agents))
			}
			var queued *DelegatedAgent
			for i := range view.Agents {
				if view.Agents[i].Status == "queued" {
					queued = &view.Agents[i]
				}
			}
			if queued == nil || queued.QueuedPosition != 1 || queued.Lane != "lane" {
				t.Fatalf("queued row = %#v", queued)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue state never observed: %#v", view)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
}

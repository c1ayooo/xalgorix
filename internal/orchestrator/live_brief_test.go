package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
)

// overviewRepo satisfies the three repository calls overview() makes; the rest
// of graph.Repository is never reached in this test.
type overviewRepo struct{ graph.Repository }

func (overviewRepo) Counts(context.Context, string) (graph.Counts, error) { return graph.Counts{}, nil }
func (overviewRepo) PlanTasks(context.Context, string) ([]graph.PlanTask, error) {
	return nil, nil
}
func (overviewRepo) ListNodes(context.Context, string, graph.Kind, ...graph.State) ([]graph.Node, error) {
	return nil, nil
}

func TestPlannerUpdateBriefingChangesPromptDirection(t *testing.T) {
	p := NewPlanner(context.Background(), PlannerConfig{
		ScanID:          "s1",
		Target:          "https://example.test",
		Goal:            "old goal",
		UserInstruction: "old instruction",
		Repo:            overviewRepo{},
	})
	defer p.Stop()
	if got := p.overview(); !strings.Contains(got, "old goal") {
		t.Fatalf("initial overview missing the old goal:\n%s", got)
	}
	p.UpdateBriefing(LiveTaskBrief{Goal: "new goal", UserInstruction: "new instruction"})
	got := p.overview()
	if !strings.Contains(got, "new goal") || strings.Contains(got, "old goal") {
		t.Fatalf("overview did not follow the update:\n%s", got)
	}
}

func TestPoolUpdateBriefingIsVisibleSnapshot(t *testing.T) {
	p := &Pool{}
	p.UpdateBriefing(LiveTaskBrief{Goal: "g", Instruction: "i", UserInstruction: "u"})
	if got := p.briefSnapshot(); got.Goal != "g" || got.UserInstruction != "u" {
		t.Fatalf("pool brief = %+v", got)
	}
}

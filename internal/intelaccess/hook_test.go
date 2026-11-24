package intelaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// The conclusion hook is the projection's only derive trigger: concluding an
// intent must leave a proposed access row for every credential/lead the scan
// holds, without the caller doing anything extra.
func TestConcludeHookDerivesAccess(t *testing.T) {
	ctx := context.Background()
	scanID := "scan-hook-intel"
	mem := NewMemoryStore()
	graph.SetConcludeHook(mem.ConcludeHook())
	t.Cleanup(func() {
		graph.SetConcludeHook(nil)
		intelligence.CleanupContext(scanID)
	})

	res, err := intelligence.RecordForContext(scanID, map[string]string{
		"kind": "credential", "title": "app SSH", "content": "root:Passw0rd!",
		"endpoint": "10.0.177.5:22",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("record: err=%v res=%+v", err, res)
	}
	fp, _ := res.Metadata["intel_fingerprint"].(string)

	store := graph.NewMemoryStore()
	intent, err := store.UpsertNode(ctx, graph.NodeInput{
		ScanID: scanID, Kind: graph.KindIntent, UnitKey: "t-1",
		State: graph.StateRunning, Origin: "llm", Owner: "work#1",
		Payload: `{"title":"probe"}`,
	})
	if err != nil {
		t.Fatalf("upsert intent: %v", err)
	}
	if _, err := store.ConcludeIntent(ctx, scanID, intent.ID, "work#1", graph.StateDone, []graph.NodeInput{{
		Kind: graph.KindFact, UnitKey: "fact-1", Payload: `{"claim":"probe done"}`,
	}}); err != nil {
		t.Fatalf("conclude: %v", err)
	}

	access, ok, err := mem.Get(ctx, scanID, fp)
	if err != nil || !ok {
		t.Fatalf("access after conclusion: ok=%v err=%v", ok, err)
	}
	if access.State != StateProposed || access.Kind != KindCredential {
		t.Fatalf("derived access = %+v", access)
	}
	if len(access.Targets) != 1 || access.Targets[0] != "10.0.177.5:22" {
		t.Fatalf("derived targets = %v", access.Targets)
	}
	// Attribution (M4): the concluding intent owns what it derived, which is
	// what the exit gate checks.
	if access.SourceIntentID != intent.ID {
		t.Fatalf("source_intent_id = %d, want %d", access.SourceIntentID, intent.ID)
	}
}

// A failing projection must never fail the conclusion it summarizes: the row
// is idempotent and replayable, the settled intent is not.
func TestConcludeHookFailureDoesNotBlockConclusion(t *testing.T) {
	ctx := context.Background()
	scanID := "scan-hook-fail"
	graph.SetConcludeHook(func(context.Context, graph.Tx, string, int64) error {
		return errors.New("projection unavailable")
	})
	t.Cleanup(func() { graph.SetConcludeHook(nil) })
	_ = scanctx.New(scanID, "")

	store := graph.NewMemoryStore()
	intent, err := store.UpsertNode(ctx, graph.NodeInput{
		ScanID: scanID, Kind: graph.KindIntent, UnitKey: "t-1",
		State: graph.StateRunning, Origin: "llm", Owner: "work#1",
		Payload: `{"title":"probe"}`,
	})
	if err != nil {
		t.Fatalf("upsert intent: %v", err)
	}
	concluded, err := store.ConcludeIntent(ctx, scanID, intent.ID, "work#1", graph.StateDone, []graph.NodeInput{{
		Kind: graph.KindFact, UnitKey: "fact-1", Payload: `{"claim":"probe done"}`,
	}})
	if err != nil {
		t.Fatalf("conclusion must succeed despite hook failure: %v", err)
	}
	if concluded.State != graph.StateDone {
		t.Fatalf("concluded state = %s", concluded.State)
	}
}

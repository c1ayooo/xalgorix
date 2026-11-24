package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// TestConcludeArchivesResultAndHandsOffRef pins P0 test 2: the parent's
// summary becomes an archived artifact reference, and the dependent intent's
// brief carries only that reference (never the prose).
func TestConcludeArchivesResultAndHandsOffRef(t *testing.T) {
	store := graph.NewMemoryStore()
	sctx := scanctx.New("mission-handoff", t.TempDir())
	pool, err := New(context.Background(), Config{
		ScanID: "run-handoff", Workers: 1, Repo: store, Root: &fakeRoot{}, ScanCtx: sctx,
	})
	if err != nil {
		t.Fatal(err)
	}
	addIntent(t, store, "run-handoff", "parent")
	claimed, err := store.ClaimNextIntent(context.Background(), "run-handoff", "work#1", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %+v", err, claimed)
	}
	sctx.Progress.RecordExit("agent-parent", "none", "parent produced a credential")
	pool.conclude(claimed, "work#1", "agent-parent", "parent result: credential for pivot")

	facts, _ := store.ListNodes(context.Background(), "run-handoff", graph.KindFact)
	var ref string
	for _, fact := range facts {
		payload := fact.PayloadJSON()
		if strings.TrimSpace(str(payload["result_of"])) == "parent" {
			ref = strings.TrimSpace(str(payload["result_artifact_id"]))
		}
	}
	if ref == "" {
		t.Fatalf("parent receipt has no result_artifact_id: %+v", facts)
	}
	body, ok := sctx.ToolOutputs.Get(ref)
	if !ok || !strings.Contains(body, "credential for pivot") {
		t.Fatalf("archived result = %q ok=%v", body, ok)
	}

	child, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: "run-handoff", Kind: graph.KindIntent, UnitKey: "child", State: graph.StateOpen,
		Payload: `{"task_id":"child","depends_on":["parent"],"target":"10.0.1.5:8080","target_kind":"container"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	block := pool.intentHandoffBlock(child)
	if !strings.Contains(block, "parent=parent") || !strings.Contains(block, ref) {
		t.Fatalf("handoff block = %q", block)
	}
	if strings.Contains(block, "credential for pivot") {
		t.Fatal("handoff block leaked the parent summary body")
	}
}

// TestPreconditionBlocksDependentIntent pins P0 test 3: a child whose stage
// precondition is not done cannot be claimed.
func TestPreconditionBlocksDependentIntent(t *testing.T) {
	store := graph.NewMemoryStore()
	if _, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: "mission-pre", Kind: graph.KindIntent, UnitKey: "parent", State: graph.StateBlocked,
		Payload: `{"task_id":"parent"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: "mission-pre", Kind: graph.KindIntent, UnitKey: "child", State: graph.StateOpen,
		Payload: `{"task_id":"child","contract":{"version":1,"preconditions":[{"ref_kind":"intent","ref":"parent","need_state":"done"}]}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ClaimNextIntent(context.Background(), "mission-pre", "work#1", time.Minute); err != nil || got != nil {
		t.Fatalf("dependent intent claimed with an unmet stage precondition: %+v err=%v", got, err)
	}
}

// TestDeclaredYieldMustBeProduced pins the activated contract yields check.
func TestDeclaredYieldMustBeProduced(t *testing.T) {
	store := graph.NewMemoryStore()
	intent, _ := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: "mission-yield", Kind: graph.KindIntent, UnitKey: "yield-finding", State: graph.StateOpen,
		Payload: `{"task_id":"yield-finding","contract":{"version":1,"yields":[{"kind":"finding"}]}}`,
	})
	if _, err := store.ConcludePlanned(context.Background(), "mission-yield", intent.ID, graph.StateDone, []graph.NodeInput{{
		ScanID: "mission-yield", Kind: graph.KindFact, State: graph.StateOrigin, UnitKey: "receipt", Payload: `{}`,
	}}); !errors.Is(err, graph.ErrYieldUnmet) {
		t.Fatalf("fact-only conclusion with declared finding yield err = %v, want ErrYieldUnmet", err)
	}
	if _, err := store.ConcludePlanned(context.Background(), "mission-yield", intent.ID, graph.StateDone, []graph.NodeInput{
		{ScanID: "mission-yield", Kind: graph.KindFact, State: graph.StateOrigin, UnitKey: "receipt", Payload: `{}`},
		{ScanID: "mission-yield", Kind: graph.KindFinding, State: graph.StateConfirmed, UnitKey: "finding-1", Payload: `{}`},
	}); err != nil {
		t.Fatalf("matching yield conclusion: %v", err)
	}
}

// TestConcurrentConclusionKeepsSingleWinner pins P0 test 8 at the repository
// boundary: concurrent conclusions of one intent produce one winner and one
// conflict, never two terminal writes.
func TestConcurrentConclusionKeepsSingleWinner(t *testing.T) {
	store := graph.NewMemoryStore()
	addIntent(t, store, "mission-race", "race")
	claimed, err := store.ClaimNextIntent(context.Background(), "mission-race", "work#1", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %+v", err, claimed)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.ConcludeIntent(context.Background(), "mission-race", claimed.ID, "work#1", graph.StateDone, []graph.NodeInput{{
				ScanID: "mission-race", Kind: graph.KindFact, State: graph.StateOrigin,
				UnitKey: "receipt-" + string(rune('a'+i)), Payload: `{}`,
			}})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, graph.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected conclude error: %v", err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("concurrent conclusions: ok=%d conflicts=%d, want 1/1", ok, conflicts)
	}
}

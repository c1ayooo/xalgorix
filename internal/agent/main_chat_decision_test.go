package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// decisionAgent builds a main-agent shell over an in-memory graph authority.
func decisionAgent(t *testing.T, scanID string) (*Agent, *graph.MemoryStore) {
	t.Helper()
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })
	sctx := scanctx.New(scanID, t.TempDir())
	sctx.SetInstanceID(scanID)
	t.Cleanup(sctx.Close)
	return &Agent{state: &ScanState{ScanContextID: sctx.ID}, scanCtx: sctx}, store
}

func insertIntent(t *testing.T, store *graph.MemoryStore, scanID, unitKey string) *graph.Node {
	t.Helper()
	node, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: scanID, Kind: graph.KindIntent, UnitKey: unitKey,
		State: graph.StateOpen, Origin: "test",
		Payload: `{"task_id":"` + unitKey + `","title":"Test ` + unitKey + `","phase":3}`,
	})
	if err != nil {
		t.Fatalf("insert intent %s: %v", unitKey, err)
	}
	return node
}

func TestMainChatRetireIntentDeletesOpenIntent(t *testing.T) {
	ag, store := decisionAgent(t, "scan-retire")
	node := insertIntent(t, store, "scan-retire", "task-1")

	out, err := ag.mainChatRetireIntent(map[string]string{"task_id": "task-1", "reason": "direction exhausted"})
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if !strings.Contains(out, "actor=human") || !strings.Contains(out, "deleted") {
		t.Fatalf("retire output = %q", out)
	}
	got, err := store.GetNode(context.Background(), "scan-retire", node.ID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.State != graph.StateDeleted {
		t.Fatalf("state = %s, want deleted", got.State)
	}
	// The plan view no longer lists a retired intent.
	if tasks, err := store.PlanTasks(context.Background(), "scan-retire"); err != nil || len(tasks) != 0 {
		t.Fatalf("plan tasks after retire = %+v err=%v", tasks, err)
	}
	decisions := ag.scanCtx.Ledger.Decisions()
	if len(decisions) != 1 || decisions[0].Action != "retire_intent" ||
		decisions[0].InfoUse != "none" || decisions[0].NoInfoReason != "operator_decision" ||
		decisions[0].Reason != "direction exhausted" {
		t.Fatalf("retire decision = %+v", decisions)
	}
	// A second retire is a conflict, not a silent success.
	if _, err := ag.mainChatRetireIntent(map[string]string{"unit_id": "task-1", "reason": "again"}); err == nil {
		t.Fatal("retiring a deleted intent must fail")
	}
}

func TestMainChatRetireIntentRejectsNonOpen(t *testing.T) {
	ag, store := decisionAgent(t, "scan-retire-run")
	node := insertIntent(t, store, "scan-retire-run", "task-running")
	if ok, err := store.Transition(context.Background(), "scan-retire-run", node.ID, graph.KindIntent, graph.StateOpen, graph.StateRunning); err != nil || !ok {
		t.Fatalf("transition to running: ok=%v err=%v", ok, err)
	}
	_, err := ag.mainChatRetireIntent(map[string]string{"task_id": "task-running", "reason": "should reject"})
	if err == nil || !strings.Contains(err.Error(), "only open") {
		t.Fatalf("retire of running intent = %v", err)
	}
	got, _ := store.GetNode(context.Background(), "scan-retire-run", node.ID)
	if got.State != graph.StateRunning {
		t.Fatalf("state = %s, want running (unchanged)", got.State)
	}
}

func TestMainChatRetireIntentValidation(t *testing.T) {
	ag, _ := decisionAgent(t, "scan-retire-val")
	if _, err := ag.mainChatRetireIntent(map[string]string{"reason": "no unit"}); err == nil {
		t.Fatal("missing unit id must fail")
	}
	if _, err := ag.mainChatRetireIntent(map[string]string{"task_id": "task-x"}); err == nil {
		t.Fatal("missing reason must fail")
	}
	if _, err := ag.mainChatRetireIntent(map[string]string{"task_id": "missing", "reason": "x"}); err == nil || !strings.Contains(err.Error(), "no intent") {
		t.Fatalf("unknown task = %v", err)
	}
}

func TestMainChatSteerAndKillWork(t *testing.T) {
	graphPool, started := blockingGraph(t)
	sctx := progressTestContext(t, "scan-decide")
	ag := &Agent{state: &ScanState{ScanContextID: sctx.ID}, scanCtx: sctx, agentGraph: graphPool}

	if _, err := ag.mainChatSteerWork(map[string]string{"unit_id": "u1", "message": "  "}); err == nil {
		t.Fatal("empty steer message must fail")
	}

	agentID, err := ag.DispatchWorkerTaskWithSurface("work#1", "do it", nil, "unit-decide", "")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-started
	steered := ""
	graphPool.RegisterSteer(agentID, func(msg string) error {
		steered = msg
		return nil
	})
	out, err := ag.mainChatSteerWork(map[string]string{"unit_id": "unit-decide", "message": "focus on /admin"})
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	if steered != "focus on /admin" || !strings.Contains(out, "actor=human") {
		t.Fatalf("steered=%q out=%q", steered, out)
	}

	// kill_work returns the reason code. A bogus code is rejected before any
	// kill is attempted.
	if out, err := ag.mainChatKillWork(map[string]string{"unit_id": "unit-decide", "reason_code": "bogus"}); err == nil || !strings.Contains(err.Error(), "reason_code") {
		t.Fatalf("bogus reason code = %v out=%q", err, out)
	}
	out, err = ag.mainChatKillWork(map[string]string{"unit_id": "unit-decide", "reason_code": "duplicate", "reason": "same target"})
	if err != nil {
		t.Fatalf("kill duplicate: %v", err)
	}
	if !strings.Contains(out, "reason_code=duplicate") || !strings.Contains(out, "actor=human") {
		t.Fatalf("kill output = %q", out)
	}
}

func TestMainChatKillWorkDefaultsToHuman(t *testing.T) {
	graphPool, started := blockingGraph(t)
	sctx := progressTestContext(t, "scan-decide-human")
	ag := &Agent{state: &ScanState{ScanContextID: sctx.ID}, scanCtx: sctx, agentGraph: graphPool}
	if _, err := ag.DispatchWorkerTaskWithSurface("work#1", "do it", nil, "unit-human", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-started
	out, err := ag.mainChatKillWork(map[string]string{"unit_id": "unit-human"})
	if err != nil {
		t.Fatalf("kill without reason_code: %v", err)
	}
	if !strings.Contains(out, "reason_code=human") {
		t.Fatalf("kill output = %q", out)
	}
}

func TestMainToolsAllowDecisionActions(t *testing.T) {
	for _, tool := range []string{"steer_work", "kill_work", "retire_intent"} {
		if !SurfaceMain.Allows(tool) {
			t.Fatalf("main surface must allow %s", tool)
		}
	}
	// They remain planner capabilities, not worker execution tools.
	for _, tool := range []string{"steer_work", "kill_work", "retire_intent"} {
		if SurfaceWorker.Allows(tool) {
			t.Fatalf("worker surface must not allow %s", tool)
		}
	}
}

func TestReprioritizeIntentRecordsDecision(t *testing.T) {
	ag, store := decisionAgent(t, "scan-priority")
	insertIntent(t, store, "scan-priority", "task-pri")
	if err := ag.ReprioritizeIntent("task-pri", 99000, "urgent operator direction"); err != nil {
		t.Fatalf("reprioritize: %v", err)
	}
	decisions := ag.scanCtx.Ledger.Decisions()
	if len(decisions) != 1 || decisions[0].Action != "reprioritize_intent" ||
		decisions[0].Selected != "task-pri" || decisions[0].Reason != "urgent operator direction" ||
		decisions[0].InfoUse != "none" || decisions[0].NoInfoReason != "operator_decision" {
		t.Fatalf("reprioritize decision = %+v", decisions)
	}
}

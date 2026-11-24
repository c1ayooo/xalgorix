package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

func llmToolCall(name string, args map[string]string) llm.ToolCall {
	return llm.ToolCall{Name: name, Args: args}
}

// proposeAndConfirm exercises the button path (no model turn).
func proposeAndConfirm(t *testing.T, ag *Agent, args map[string]string) string {
	t.Helper()
	if _, err := ag.mainChatProposeIntent(args); err != nil {
		t.Fatalf("propose intent: %v", err)
	}
	ag.mainProposalsMu.Lock()
	id := ""
	for proposalID := range ag.mainProposals {
		id = proposalID
	}
	ag.mainProposalsMu.Unlock()
	if id == "" {
		t.Fatal("propose_intent did not create a draft")
	}
	if _, _, err := ag.ConfirmMainIntentProposal(id); err != nil {
		t.Fatalf("confirm proposal: %v", err)
	}
	return id
}

func TestMainChatProposeConfirmAndHint(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-main", "")
	sctx.SetInstanceID("run-main")
	state := NewScanState()
	state.ScanContextID = sctx.ID
	state.PlannerWorkerMode = true
	ag := &Agent{state: state, scanCtx: sctx, events: make(chan Event, 16)}

	// A proposal alone must not touch the plan.
	out, err := ag.mainChatProposeIntent(map[string]string{
		"title": "Test SQLi on /api/users", "phase": "6",
		"vuln_class": "sqli", "endpoint": "/api/users",
		"target": "https://example.test", "target_kind": "web",
	})
	if err != nil {
		t.Fatalf("propose intent: %v", err)
	}
	if out == "" || !strings.Contains(out, "NOT in the plan") {
		t.Fatalf("propose output = %q", out)
	}
	if tasks, err := store.PlanTasks(context.Background(), "run-main"); err != nil || len(tasks) != 0 {
		t.Fatalf("proposal wrote the plan: tasks=%+v err=%v", tasks, err)
	}
	if got := ag.pendingMainProposalCount(); got != 1 {
		t.Fatalf("pending proposals = %d, want 1", got)
	}

	ag.mainProposalsMu.Lock()
	id := ""
	for proposalID := range ag.mainProposals {
		id = proposalID
	}
	ag.mainProposalsMu.Unlock()
	if _, _, err := ag.ConfirmMainIntentProposal(id); err != nil {
		t.Fatalf("confirm proposal: %v", err)
	}
	tasks, err := store.PlanTasks(context.Background(), "run-main")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %+v err=%v", tasks, err)
	}
	if tasks[0].Origin != "human" || tasks[0].Priority <= 1000 {
		t.Fatalf("intent = %+v", tasks[0])
	}
	if ag.pendingMainProposalCount() != 0 {
		t.Fatal("confirmed proposal still pending")
	}
	if _, err := ag.mainChatAddHint(map[string]string{"message": "focus on authz boundaries"}); err != nil {
		t.Fatalf("add hint: %v", err)
	}
	hints, _ := store.ListNodes(context.Background(), "run-main", graph.KindHint)
	if len(hints) != 1 || hints[0].State != graph.StateActive {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestMainChatProposalRequiresLaterExplicitConfirmation(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-confirm", "")
	sctx.SetInstanceID("run-confirm")
	state := NewScanState()
	state.ScanContextID = sctx.ID
	ag := &Agent{state: state, scanCtx: sctx, events: make(chan Event, 16)}

	if _, err := ag.mainChatProposeIntent(map[string]string{
		"title": "IDOR on /orders", "phase": "5", "target": "https://example.test",
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	ag.mainProposalsMu.Lock()
	id := ""
	for proposalID := range ag.mainProposals {
		id = proposalID
	}
	ag.mainProposalsMu.Unlock()

	// Same turn: rejected even with an explicit "确认".
	if _, err := ag.mainChatConfirmIntent(map[string]string{"proposal_id": id}, "确认"); err == nil {
		t.Fatal("confirm_intent accepted a same-turn confirmation")
	}
	// Later turn without an explicit affirmation: rejected.
	ag.mainTurn = 1
	if _, err := ag.mainChatConfirmIntent(map[string]string{"proposal_id": id}, "顺便看看别的"); err == nil {
		t.Fatal("confirm_intent accepted a non-affirmative message")
	}
	if tasks, _ := store.PlanTasks(context.Background(), "run-confirm"); len(tasks) != 0 {
		t.Fatalf("failed confirm wrote the plan: %+v", tasks)
	}
	// Later turn with an explicit affirmation: written once.
	if _, err := ag.mainChatConfirmIntent(map[string]string{"proposal_id": id}, "确认执行"); err != nil {
		t.Fatalf("explicit confirmation: %v", err)
	}
	if tasks, _ := store.PlanTasks(context.Background(), "run-confirm"); len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	// Idempotent: the draft is gone, a second confirmation fails.
	if _, _, err := ag.ConfirmMainIntentProposal(id); err == nil {
		t.Fatal("second confirmation of the same proposal succeeded")
	}
}

func TestMainChatRejectProposalDoesNotWritePlan(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-reject", "")
	sctx.SetInstanceID("run-reject")
	state := NewScanState()
	state.ScanContextID = sctx.ID
	ag := &Agent{state: state, scanCtx: sctx, events: make(chan Event, 16)}

	if _, err := ag.mainChatProposeIntent(map[string]string{
		"title": "SSRF on /fetch", "phase": "10", "target": "https://example.test",
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	ag.mainProposalsMu.Lock()
	id := ""
	for proposalID := range ag.mainProposals {
		id = proposalID
	}
	ag.mainProposalsMu.Unlock()
	if _, err := ag.RejectMainIntentProposal(id); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if tasks, _ := store.PlanTasks(context.Background(), "run-reject"); len(tasks) != 0 {
		t.Fatalf("rejected proposal wrote the plan: %+v", tasks)
	}
	if ag.pendingMainProposalCount() != 0 {
		t.Fatal("rejected proposal still pending")
	}
}

func TestMainChatAddIntentIsNoLongerAWritablePath(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-direct", "")
	sctx.SetInstanceID("run-direct")
	state := NewScanState()
	state.ScanContextID = sctx.ID
	ag := &Agent{state: state, scanCtx: sctx, events: make(chan Event, 16)}

	out, _ := ag.mainChatToolResult(llmToolCall("add_intent", map[string]string{
		"title": "direct", "target": "https://example.test",
	}), "")
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("add_intent should be rejected: %q", out)
	}
	if tasks, _ := store.PlanTasks(context.Background(), "run-direct"); len(tasks) != 0 {
		t.Fatalf("add_intent wrote the plan: %+v", tasks)
	}
}

func TestMainChatFinishRefusesPendingProposal(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-finish-proposal", "")
	sctx.SetInstanceID("run-finish-proposal")
	state := NewScanState()
	state.ScanContextID = sctx.ID
	ag := &Agent{state: state, scanCtx: sctx, events: make(chan Event, 16)}

	if _, err := ag.mainChatProposeIntent(map[string]string{
		"title": "pending work", "target": "https://example.test",
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	res, finished := ag.mainChatToolResult(llmToolCall("finish", map[string]string{"reason": "done"}), "")
	if finished || !strings.HasPrefix(res, "error:") || !strings.Contains(res, "proposal") {
		t.Fatalf("finish should be refused with a pending proposal: %q finished=%v", res, finished)
	}
}

// TestMainChatReadOnlyTools pins the read-only contract: the four query tools
// surface graph/ledger/finding state and never mutate it.
func TestMainChatReadOnlyTools(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-read", "")
	sctx.SetInstanceID("run-read")
	state := NewScanState()
	state.ScanContextID = sctx.ID
	ag := &Agent{state: state, scanCtx: sctx, events: make(chan Event, 16)}

	proposeAndConfirm(t, ag, map[string]string{
		"title": "SQLi on /login", "phase": "6", "vuln_class": "sqli", "endpoint": "/login",
		"target": "https://example.test", "target_kind": "web",
	})
	ag.ledger().Upsert(scanctx.Hypothesis{
		ID:         "H-1",
		VulnClass:  "sqli",
		Endpoint:   "/login",
		Parameter:  "user",
		Status:     scanctx.HypothesisQueued,
		Confidence: 0.6,
		Evidence:   []scanctx.Evidence{{ID: "E-1", Kind: "probe", Summary: "error-based probe"}},
	})
	reporting.SeedVulnsForContext(sctx.ID, []reporting.Vulnerability{
		{ID: "XALG-1", Title: "SQL injection in login", Severity: "high", Endpoint: "/login", CVSS: 8.1, Verified: true},
	})
	t.Cleanup(func() { reporting.CleanupContext(sctx.ID) })

	before, err := store.PlanTasks(context.Background(), "run-read")
	if err != nil {
		t.Fatalf("plan tasks before: %v", err)
	}

	intents, err := ag.mainChatListIntents(map[string]string{"status": "open"})
	if err != nil || !strings.Contains(intents, "SQLi on /login") {
		t.Fatalf("list_intents = %q err=%v", intents, err)
	}
	if !strings.Contains(intents, "P6") {
		t.Fatalf("list_intents missing phase: %q", intents)
	}
	if !strings.Contains(intents, "example.test") {
		t.Fatalf("list_intents missing dispatch target: %q", intents)
	}

	units, err := ag.mainChatListUnits(map[string]string{"kind": "intent"})
	if err != nil || !strings.Contains(units, "intent") {
		t.Fatalf("list_units = %q err=%v", units, err)
	}

	findings, err := ag.mainChatListFindings(map[string]string{"severity": "high"})
	if err != nil || !strings.Contains(findings, "SQL injection in login") || !strings.Contains(findings, "verified") {
		t.Fatalf("list_findings = %q err=%v", findings, err)
	}

	hyps, err := ag.mainChatListHypotheses(map[string]string{"status": "queued"})
	if err != nil || !strings.Contains(hyps, "H-1") || !strings.Contains(hyps, "evidence=1") {
		t.Fatalf("list_hypotheses = %q err=%v", hyps, err)
	}

	after, err := store.PlanTasks(context.Background(), "run-read")
	if err != nil {
		t.Fatalf("plan tasks after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("read-only tools mutated the plan: before=%d after=%d", len(before), len(after))
	}
	if got := len(reporting.GetVulnerabilitiesForContext(sctx.ID)); got != 1 {
		t.Fatalf("read-only tools mutated findings: got %d", got)
	}
	if got := ag.ledger().Len(); got != 1 {
		t.Fatalf("read-only tools mutated the ledger: got %d hypotheses", got)
	}
}

func TestMainChatFinishRefusesActiveIntent(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-finish", "")
	sctx.SetInstanceID("run-finish")
	state := NewScanState()
	state.ScanContextID = sctx.ID
	ag := &Agent{state: state, scanCtx: sctx, events: make(chan Event, 16)}

	proposeAndConfirm(t, ag, map[string]string{"title": "open work", "phase": "1", "target": "https://example.test"})
	res, finished := ag.mainChatToolResult(llmToolCall("finish", map[string]string{"reason": "done"}), "")
	if finished || !strings.HasPrefix(res, "error:") {
		t.Fatalf("finish should be refused: %q finished=%v", res, finished)
	}
}

func TestMainChatInitialHistoryIncludesResumeTranscript(t *testing.T) {
	a := &Agent{replayTranscript: "PREVIOUS SESSION TRANSCRIPT\nuser: hi\nassistant: hello"}
	history := a.mainChatInitialHistory("system prompt")
	if len(history) != 2 {
		t.Fatalf("history len = %d, want system + replay", len(history))
	}
	if history[0].Role != "system" || history[1].Role != "user" {
		t.Fatalf("roles = %q %q", history[0].Role, history[1].Role)
	}
	if !strings.Contains(history[1].Content, "PREVIOUS SESSION TRANSCRIPT") {
		t.Fatalf("replay missing from history: %q", history[1].Content)
	}
	empty := (&Agent{}).mainChatInitialHistory("system prompt")
	if len(empty) != 1 {
		t.Fatalf("fresh history = %d, want system only", len(empty))
	}
}

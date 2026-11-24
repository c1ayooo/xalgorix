package agent

import (
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func progressTestContext(t *testing.T, id string) *scanctx.ScanContext {
	t.Helper()
	sctx := scanctx.New(id, t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(func() {
		scanctx.Deactivate(id)
		sctx.Cancel()
	})
	return sctx
}

func TestHookProgressLedgerTouchesAndMarks(t *testing.T) {
	sctx := progressTestContext(t, "progress")
	state := &ScanState{ScanContextID: "progress", DelegatedAgentID: "worker-1"}

	// Any tool result touches; a read-only tool never counts as progress.
	hookProgressLedger(state, map[string]string{"tool_name": "terminal_execute", "output": "ok"})
	touch, progress, ok := sctx.Progress.AgentTimes("worker-1")
	if !ok || touch.IsZero() {
		t.Fatalf("tool result did not touch: ok=%v touch=%v", ok, touch)
	}
	if !progress.IsZero() {
		t.Fatalf("terminal_execute counted as progress: %v", progress)
	}

	// Read/list/search tools stay non-progress even when they succeed.
	for _, tool := range []string{"read_notes", "read_ledger", "search_info", "list_findings"} {
		hookProgressLedger(state, map[string]string{"tool_name": tool, "output": "data"})
	}
	if entry := sctx.Progress.Snapshot()["worker-1"]; entry.ProgressCount != 0 {
		t.Fatalf("read-only tools counted as progress: %+v", entry)
	}

	// An information-layer mutation is progress.
	hookProgressLedger(state, map[string]string{"tool_name": "add_note", "output": "saved"})
	_, progress2, _ := sctx.Progress.AgentTimes("worker-1")
	if progress2.IsZero() {
		t.Fatal("add_note did not mark progress")
	}
	if entry := sctx.Progress.Snapshot()["worker-1"]; entry.ProgressCount != 1 {
		t.Fatalf("progress count = %d, want 1", entry.ProgressCount)
	}

	// A failed mutation changed nothing: touch still updates, progress does not.
	hookProgressLedger(state, map[string]string{"tool_name": "record_hypothesis", "error": "boom"})
	if entry := sctx.Progress.Snapshot()["worker-1"]; entry.ProgressCount != 1 {
		t.Fatalf("failed call counted as progress: %+v", entry)
	}

	// Other info-layer mutations count too.
	for _, tool := range []string{
		"record_hypothesis", "add_hypothesis_evidence", "update_hypothesis",
		"report_vulnerability", "submit_verdict", "record_asset",
		"record_intelligence", "ingest_har", "record_deviation", "authz_matrix",
	} {
		hookProgressLedger(state, map[string]string{"tool_name": tool, "output": "ok"})
	}
	if entry := sctx.Progress.Snapshot()["worker-1"]; entry.ProgressCount != 11 {
		t.Fatalf("progress count = %d, want 11", entry.ProgressCount)
	}
}

func TestHookProgressLedgerRootFallbackAndMissingContext(t *testing.T) {
	sctx := progressTestContext(t, "progress-root")
	// A root agent (no delegated id) records under "root".
	rootState := &ScanState{ScanContextID: "progress-root"}
	hookProgressLedger(rootState, map[string]string{"tool_name": "record_hypothesis", "output": "H-1"})
	if _, progress, ok := sctx.Progress.AgentTimes("root"); !ok || progress.IsZero() {
		t.Fatal("root fallback did not record progress")
	}

	// Unknown scan context, nil state and nil args must be inert.
	hookProgressLedger(&ScanState{ScanContextID: "does-not-exist"}, map[string]string{"tool_name": "add_note"})
	hookProgressLedger(nil, map[string]string{"tool_name": "add_note"})
	hookProgressLedger(&ScanState{}, nil)
}

// TestHookProgressIgnoresUnchangedNoteWrite pins the round-5 false-progress
// fix: a successful add_note that stored the same value is not an
// information-layer increment.
func TestHookProgressIgnoresUnchangedNoteWrite(t *testing.T) {
	sctx := progressTestContext(t, "progress-notes")
	state := &ScanState{ScanContextID: "progress-notes", DelegatedAgentID: "worker-1"}
	hookProgressLedger(state, map[string]string{"tool_name": "add_note", "state_changed": "false"})
	if entry := sctx.Progress.Snapshot()["worker-1"]; entry.ProgressCount != 0 {
		t.Fatalf("identical note write counted as progress: %+v", entry)
	}
	hookProgressLedger(state, map[string]string{"tool_name": "add_note", "state_changed": "true"})
	if entry := sctx.Progress.Snapshot()["worker-1"]; entry.ProgressCount != 1 {
		t.Fatalf("changed note write did not count: %+v", entry)
	}
}

// TestHookProgressActiveTool pins the long-tool protection: the tool is marked
// active before execution and cleared on the result.
func TestHookProgressActiveTool(t *testing.T) {
	sctx := progressTestContext(t, "progress-tool")
	state := &ScanState{ScanContextID: "progress-tool", DelegatedAgentID: "worker-1"}
	hookProgressToolStart(state, map[string]string{"tool_name": "terminal_execute"})
	name, started, ok := sctx.Progress.ActiveTool("worker-1")
	if !ok || name != "terminal_execute" || started.IsZero() {
		t.Fatalf("active tool = %q %v %v", name, started, ok)
	}
	hookProgressLedger(state, map[string]string{"tool_name": "terminal_execute", "output": "ok"})
	if _, _, ok := sctx.Progress.ActiveTool("worker-1"); ok {
		t.Fatal("tool result did not clear the active marker")
	}
}

// TestVerifierBridgeMarksWorkerProgress pins XALG004's engine-derived leg: a
// deterministic verifier that actually executed is a progress increment even
// before the model writes a ledger entry.
func TestVerifierBridgeMarksWorkerProgress(t *testing.T) {
	sctx := progressTestContext(t, "progress-verifier")
	state := &ScanState{ScanContextID: "progress-verifier", DelegatedAgentID: "worker-v"}
	hookVerifierEvidenceBridge(state, map[string]string{
		"tool_name": "verify_sqli",
		"endpoint":  "https://example.com/login",
		"output":    "confirmed boolean-based injection",
	})
	entry := sctx.Progress.Snapshot()["worker-v"]
	if entry.ProgressCount != 1 {
		t.Fatalf("verifier execution did not mark progress: %+v", entry)
	}
}

// TestToolActiveBudget pins the per-tool deadline classes (XALG005).
func TestToolActiveBudget(t *testing.T) {
	cases := map[string]time.Duration{
		"terminal_execute":  60 * time.Minute,
		"python_action":     30 * time.Minute,
		"browser_action":    10 * time.Minute,
		"http_request":      5 * time.Minute,
		"some_unknown_tool": 15 * time.Minute,
	}
	for name, want := range cases {
		if got := toolActiveBudget(name); got != want {
			t.Fatalf("toolActiveBudget(%q) = %s, want %s", name, got, want)
		}
	}
}

// TestReconCompletionMarksWorkerProgress pins XALG004's recon leg: completing a
// new recon dimension is a progress increment for the worker that produced it.
func TestReconCompletionMarksWorkerProgress(t *testing.T) {
	sctx := progressTestContext(t, "progress-recon")
	state := &ScanState{ScanContextID: "progress-recon", DelegatedAgentID: "worker-r"}
	hookReconResultTracker(state, map[string]string{
		"tool_name": "discover_client_routes",
		"output":    "Discovered 3 client routes",
	})
	entry := sctx.Progress.Snapshot()["worker-r"]
	if entry.ProgressCount == 0 {
		t.Fatalf("recon dimension completion did not mark progress: %+v", entry)
	}
}

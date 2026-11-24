package agent

import (
	"testing"

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

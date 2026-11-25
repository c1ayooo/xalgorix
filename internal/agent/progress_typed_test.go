package agent

import (
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func typedProgressState(t *testing.T) (*scanctx.ScanContext, *ScanState) {
	t.Helper()
	sctx, state := newTestCtxState(t)
	state.DelegatedAgentID = "worker-p2"
	return sctx, state
}

func TestHookProgressLedgerMarksTypedSignals(t *testing.T) {
	sctx, state := typedProgressState(t)

	cases := []struct {
		tool string
		want scanctx.ProgressSignal
	}{
		{"record_hypothesis", scanctx.SignalBelief},
		{"add_hypothesis_evidence", scanctx.SignalEvidence},
		{"report_vulnerability", scanctx.SignalFinding},
		{"record_asset", scanctx.SignalAsset},
		{"record_intelligence", scanctx.SignalObservation},
		{"complete_work", scanctx.SignalWorkflow},
	}
	for _, c := range cases {
		hookProgressLedger(state, map[string]string{"tool_name": c.tool})
		snap := sctx.Progress.SnapshotFor("worker-p2", sctx.Progress.LastSignalAt("worker-p2"), lastSignalNow())
		if snap.Signals[string(c.want)].Count == 0 {
			t.Fatalf("%s did not produce %s: %+v", c.tool, c.want, snap.Signals)
		}
	}
	// Add_note remains the InfoItem signal.
	hookProgressLedger(state, map[string]string{"tool_name": "add_note"})
	if sctx.Progress.SnapshotFor("worker-p2", lastSignalNow(), lastSignalNow()).Signals[string(scanctx.SignalInfo)].Count == 0 {
		t.Fatal("add_note did not produce the info signal")
	}
}

func TestHookProgressLedgerAnalysisReadIsTyped(t *testing.T) {
	sctx, state := typedProgressState(t)
	hookProgressLedger(state, map[string]string{"tool_name": "read_info_item", "id": "E-1"})
	hookProgressLedger(state, map[string]string{"tool_name": "read_info_item", "id": "E-1"})
	snap := sctx.Progress.SnapshotFor("worker-p2", lastSignalNow(), lastSignalNow())
	if got := snap.Signals[string(scanctx.SignalAnalysis)].Count; got != 1 {
		t.Fatalf("same-reference analysis count = %d, want 1", got)
	}
	hookProgressLedger(state, map[string]string{"tool_name": "read_tool_output", "id": "to_1"})
	snap = sctx.Progress.SnapshotFor("worker-p2", lastSignalNow(), lastSignalNow())
	if snap.Signals[string(scanctx.SignalAnalysis)].Count != 2 || snap.Signals[string(scanctx.SignalArtifact)].Count != 1 {
		t.Fatalf("analysis/artifact signals = %+v", snap.Signals)
	}
}

func TestHookProgressLedgerFailureClassification(t *testing.T) {
	sctx, state := typedProgressState(t)
	for i := 0; i < 3; i++ {
		hookProgressLedger(state, map[string]string{
			"tool_name": "terminal_execute", "error": "dial tcp: connection refused",
		})
	}
	snap := sctx.Progress.SnapshotFor("worker-p2", lastSignalNow(), lastSignalNow())
	if snap.Status != scanctx.ProgressEnvironmentBlocked || snap.LastFailureClass != "unreachable" {
		t.Fatalf("failure snapshot = %+v", snap)
	}
	// A success clears the consecutive count.
	hookProgressLedger(state, map[string]string{"tool_name": "read_info_item", "id": "E-2"})
	snap = sctx.Progress.SnapshotFor("worker-p2", lastSignalNow(), lastSignalNow())
	if snap.ConsecutiveFailures != 0 {
		t.Fatalf("success did not clear failures: %+v", snap)
	}
}

func lastSignalNow() time.Time { return time.Now() }

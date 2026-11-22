package web

// redispatch_resume_test.go — pins the 1:1 task:run re-dispatch primitives
// (REDESIGN 2026-11-22): terminal snapshots reopen as pending under the same
// id with the reservation held, stopping/active snapshots are busy, absent
// snapshots fall through to the standard fresh path, and the hot-resume L2
// transcript rebuilds from events.jsonl with budget discipline.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

func TestReactivateTerminalDispatchSnapshot(t *testing.T) {
	s := newTestServer(t, nil)
	id := "550e8400-e29b-41d4-a716-446655440100"
	req := ScanRequest{Targets: []string{"https://example.test"}, ScanMode: "single",
		Name: "TSec 跑分", Instruction: "continue the benchmark", InstanceID: id}
	if err := s.saveExactDispatchSnapshot(&ScanRecord{
		ID: id, InstanceID: id, Target: "https://example.test", Status: "stopped",
		StopReason: "user_stopped", TotalTokens: 12345,
		Events: []WSEvent{}, Vulns: []VulnSummary{}, SubScans: []SubScanSummary{},
	}); err != nil {
		t.Fatalf("persist terminal snapshot: %v", err)
	}

	ok, err := s.reactivateTerminalDispatchSnapshot(id, req)
	if err != nil || !ok {
		t.Fatalf("reactivate terminal snapshot: ok=%v err=%v", ok, err)
	}
	rec, err := s.loadExactDispatchSnapshot(id)
	if err != nil {
		t.Fatalf("reload snapshot: %v", err)
	}
	if rec.Status != "pending" || rec.StopReason != "" || rec.FinishedAt != "" {
		t.Fatalf("reactivated snapshot is not a clean pending dispatch: %#v", rec)
	}
	if rec.Instruction != "continue the benchmark" || rec.Name != "TSec 跑分" {
		t.Fatalf("reactivation did not apply fresh operator intent: %#v", rec)
	}
	if rec.TotalTokens != 12345 {
		t.Fatalf("reactivation dropped durable counters: %d", rec.TotalTokens)
	}
	// The id must be held in-process so a racing duplicate POST cannot start
	// a second dispatch of the same instance.
	if _, reserved := s.reserveDispatchID(id); reserved {
		t.Fatal("reactivated id was not reserved in-process")
	}
	s.releaseDispatchID(id)
}

func TestReactivateSnapshotRefusesBusyAndMissing(t *testing.T) {
	s := newTestServer(t, nil)
	stopping := "550e8400-e29b-41d4-a716-446655440101"
	if err := s.saveExactDispatchSnapshot(&ScanRecord{
		ID: stopping, InstanceID: stopping, Status: "stopping",
		Events: []WSEvent{}, Vulns: []VulnSummary{}, SubScans: []SubScanSummary{},
	}); err != nil {
		t.Fatalf("persist stopping snapshot: %v", err)
	}
	req := ScanRequest{Targets: []string{"https://example.test"}, ScanMode: "single"}
	if _, err := s.reactivateTerminalDispatchSnapshot(stopping, req); !errors.Is(err, errDispatchBusy) {
		t.Fatalf("stopping snapshot must be busy, got err=%v", err)
	}

	// A "running" snapshot with NO live instance is an orphaned accepted
	// dispatch (finalization cut short, e.g. SIGKILL mid-stop): retryable.
	running := "550e8400-e29b-41d4-a716-446655440102"
	if err := s.saveExactDispatchSnapshot(&ScanRecord{
		ID: running, InstanceID: running, Status: "running",
		Events: []WSEvent{}, Vulns: []VulnSummary{}, SubScans: []SubScanSummary{},
	}); err != nil {
		t.Fatalf("persist running snapshot: %v", err)
	}
	if ok, err := s.reactivateTerminalDispatchSnapshot(running, req); err != nil || !ok {
		t.Fatalf("orphaned running snapshot must be retryable: ok=%v err=%v", ok, err)
	}
	s.releaseDispatchID(running)

	// A genuinely in-flight instance (live, non-terminal) must stay busy.
	inflight := "550e8400-e29b-41d4-a716-446655440104"
	if err := s.saveExactDispatchSnapshot(&ScanRecord{
		ID: inflight, InstanceID: inflight, Status: "running",
		Events: []WSEvent{}, Vulns: []VulnSummary{}, SubScans: []SubScanSummary{},
	}); err != nil {
		t.Fatalf("persist in-flight snapshot: %v", err)
	}
	s.instancesMu.Lock()
	s.instances[inflight] = &ScanInstance{ID: inflight, Status: "running"}
	s.instancesMu.Unlock()
	if _, err := s.reactivateTerminalDispatchSnapshot(inflight, req); !errors.Is(err, errDispatchBusy) {
		t.Fatalf("in-flight instance must be busy, got err=%v", err)
	}

	missing := "550e8400-e29b-41d4-a716-446655440103"
	ok, err := s.reactivateTerminalDispatchSnapshot(missing, req)
	if err != nil || ok {
		t.Fatalf("missing snapshot must fall through to the fresh path: ok=%v err=%v", ok, err)
	}
}

func TestConversationReplayTranscript(t *testing.T) {
	dir := t.TempDir()
	sctx := &scanctx.ScanContext{ScanDir: dir}
	if got := conversationReplayTranscript(sctx); got != "" {
		t.Fatal("replay without a journal must be empty")
	}

	events := []string{
		`{"type":"message","content":"Recon underway on the AI platform","timestamp":"2026-11-22T15:48:01+08:00"}`,
		`{"type":"tool_call","tool_name":"terminal_execute","tool_args":{"command":"curl -s http://10.0.177.16:3000/"},"timestamp":"2026-11-22T15:48:10+08:00"}`,
		`{"type":"tool_result","tool_name":"terminal_execute","output":"flag{c03-demo}","timestamp":"2026-11-22T15:48:12+08:00"}`,
		`{"type":"message","content":"[USER MESSAGE DURING SCAN]: focus c-03 now","timestamp":"2026-11-22T15:49:00+08:00"}`,
		`{"type":"queue_started","timestamp":"2026-11-22T15:30:00+08:00"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, scanEventJournalFile), []byte(strings.Join(events, "\n")), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	out := conversationReplayTranscript(sctx)
	for _, want := range []string{
		"PREVIOUS SESSION TRANSCRIPT",
		"YOU (assistant): Recon underway",
		"YOU called terminal_execute(",
		"RESULT terminal_execute: flag{c03-demo}",
		"OPERATOR: focus c-03 now",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript missing %q:\n%s", want, out)
		}
	}
	// Queue/admission noise stays out of the conversational projection.
	if strings.Contains(out, "queue_started") {
		t.Fatal("non-conversational event leaked into the transcript")
	}
}

func TestConversationReplayTranscriptBudget(t *testing.T) {
	dir := t.TempDir()
	sctx := &scanctx.ScanContext{ScanDir: dir}
	// Enough medium-sized events that the rendered tail exceeds the budget:
	// the newest lines must survive, the oldest head must be dropped.
	var lines []string
	for i := 0; i < 300; i++ {
		content := fmt.Sprintf("evt-%03d %s", i, strings.Repeat("x", 500))
		lines = append(lines, fmt.Sprintf(`{"type":"message","content":%q,"timestamp":"2026-11-22T%02d:%02d:00+08:00"}`, content, i/60, i%60))
	}
	if err := os.WriteFile(filepath.Join(dir, scanEventJournalFile), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	out := conversationReplayTranscript(sctx)
	if !strings.Contains(out, "evt-299") {
		t.Fatal("budget dropped the newest event — the tail must be kept")
	}
	if strings.Contains(out, "evt-000 ") {
		t.Fatal("budget kept the oldest head event — the head must be dropped")
	}
	if len(out) > replayBudgetChars+4096 {
		t.Fatalf("transcript exceeded the budget by more than one line: %d chars", len(out))
	}
}

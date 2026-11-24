package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// newPlatformFinishedCtx creates an activated scan context whose cancellation
// is observable: platform_finished must stop the worker only and leave the
// scan context running.
func newPlatformFinishedCtx(t *testing.T, id string) *scanctx.ScanContext {
	t.Helper()
	sctx := scanctx.New(id, t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(func() {
		scanctx.Deactivate(id)
		sctx.Cancel()
	})
	return sctx
}

func TestHookPlatformFinished_StopsWorkerAndMarks(t *testing.T) {
	sctx := newPlatformFinishedCtx(t, "pf")
	state := &ScanState{ScanContextID: "pf"}
	res := hookPlatformFinished(state, map[string]string{
		"tool_name": "http_request",
		"url":       "https://bench.example.com/openapi/v1/challenges/submit",
		"output":    `{"code":"invalid_state","message":"task task_202611x already finished"}`,
	})
	if res.StopReason != "platform_finished" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.EmitMessage, "扫描继续") {
		t.Fatalf("message must say the scan continues: %q", res.EmitMessage)
	}
	if v, ok := sctx.Notes.Get("platform_finished"); !ok || v == "" {
		t.Fatal("platform_finished marker not written")
	}
	// The task-level end must NOT cancel the scan context (§6: stop submitting,
	// not finish the run).
	if err := sctx.Ctx.Err(); err != nil {
		t.Fatalf("platform_finished cancelled the scan context: %v", err)
	}
	if res := hookPlatformFinished(state, map[string]string{
		"tool_name": "http_request",
		"url":       "https://bench.example.com/openapi/v1/challenges/submit",
		"output":    "needle in a haystack",
	}); res.StopReason != "" {
		t.Fatalf("false positive: %+v", res)
	}
}

func TestHookPlatformFinished_IgnoresSlotThrottle(t *testing.T) {
	sctx := newPlatformFinishedCtx(t, "pf-throttle")
	state := &ScanState{ScanContextID: "pf-throttle"}
	// This is the exact response that killed run 5: an ordinary capacity
	// throttle, not a task-level end.
	res := hookPlatformFinished(state, map[string]string{
		"tool_name": "http_request",
		"url":       "https://bench.example.com/openapi/v1/challenges/start",
		"output":    `{"code":"invalid_state","message":"max active challenge instances reached (3), please close an existing challenge before starting a new one"}`,
	})
	if res.StopReason != "" {
		t.Fatalf("slot throttle must not end the task: %+v", res)
	}
	if _, ok := sctx.Notes.Get("platform_finished"); ok {
		t.Fatal("marker written for slot throttle")
	}
	if err := sctx.Ctx.Err(); err != nil {
		t.Fatalf("slot throttle cancelled the scan context: %v", err)
	}
}

func TestHookPlatformFinished_IgnoresLocalReads(t *testing.T) {
	sctx := newPlatformFinishedCtx(t, "pf-notes")
	state := &ScanState{ScanContextID: "pf-notes"}
	// A note or ledger read can quote a previous task-finished message; it must
	// not be able to stop the run.
	for _, tool := range []string{"read_notes", "read_ledger"} {
		res := hookPlatformFinished(state, map[string]string{
			"tool_name": tool,
			"output":    `old note: {"code":"invalid_state","message":"task task_x already finished"}`,
		})
		if res.StopReason != "" {
			t.Fatalf("%s must not end the task: %+v", tool, res)
		}
	}
	if err := sctx.Ctx.Err(); err != nil {
		t.Fatalf("local read cancelled the scan context: %v", err)
	}
}

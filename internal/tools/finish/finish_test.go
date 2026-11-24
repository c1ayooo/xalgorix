package finish

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func TestFinishToolReturnsSummaryAndMetadata(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg)

	res, err := reg.Execute("finish", map[string]string{"summary": "scan complete"})
	if err != nil {
		t.Fatalf("finish execute: %v", err)
	}
	if res.Output != "scan complete" {
		t.Fatalf("finish output = %q", res.Output)
	}
	if got, ok := res.Metadata["finished"].(bool); !ok || !got {
		t.Fatalf("finish metadata = %#v", res.Metadata)
	}
}

func TestFinishToolRequiresSummary(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg)

	if _, err := reg.Execute("finish", map[string]string{}); err == nil {
		t.Fatal("missing summary was accepted")
	}
}

// The worker completion contract (INTEL_HANDOFF_SPEC D6): every lane declares
// what it handed over, and a malformed or reasonless declaration does NOT end
// the lane (the model must correct it).
func TestCompleteWorkDeclaresExit(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg)

	res, err := reg.Execute("complete_work", map[string]string{
		"summary": "found the way in", "exit": "access:abc123",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("complete_work: err=%v res=%+v", err, res)
	}
	if res.Metadata["work_completed"] != true || res.Metadata["exit"] != "access:abc123" {
		t.Fatalf("metadata = %+v", res.Metadata)
	}

	// Default exit is none; without a reason the lane must keep going.
	res, err = reg.Execute("complete_work", map[string]string{"summary": "done"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Metadata["work_completed"] == true {
		t.Fatalf("reasonless exit=none ended the lane: %+v", res.Metadata)
	}
	if !strings.Contains(res.Output, "blocked_reason") {
		t.Fatalf("refusal must name the requirement: %q", res.Output)
	}

	res, _ = reg.Execute("complete_work", map[string]string{
		"summary": "no route in", "exit": "none", "blocked_reason": "target refused all probes",
	})
	if res.Metadata["exit"] != "none" || res.Metadata["work_completed"] != true {
		t.Fatalf("exit=none with reason = %+v", res.Metadata)
	}
	if !strings.Contains(res.Output, "target refused all probes") {
		t.Fatalf("summary must carry the reason: %q", res.Output)
	}

	for _, bad := range []string{"yolo", "access:", "finding:"} {
		res, _ = reg.Execute("complete_work", map[string]string{"summary": "x", "exit": bad})
		if res.Metadata["work_completed"] == true {
			t.Fatalf("malformed exit %q ended the lane", bad)
		}
	}
}

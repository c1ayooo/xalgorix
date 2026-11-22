package web

import (
	"strings"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

func TestConversationTitleUsesLocalTimeAndFallbackName(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 5, 0, 0, time.UTC)
	got := conversationTitle("任务 A", at)
	if !strings.HasPrefix(got, "任务 A · ") {
		t.Fatalf("title %q lost the task name", got)
	}
	if !strings.Contains(got, at.Local().Format("01-02 15:04")) {
		t.Fatalf("title %q is not in local time", got)
	}
	if empty := conversationTitle("   ", at); !strings.HasPrefix(empty, "未命名任务 · ") {
		t.Fatalf("blank task name fell back to %q", empty)
	}
}

func TestFirstRunesIsRuneSafe(t *testing.T) {
	if got := firstRunes("先确认反射型 XSS 是否成立", 4); got != "先确认反" {
		t.Fatalf("firstRunes cut %q", got)
	}
	if got := firstRunes("short", 24); got != "short" {
		t.Fatalf("firstRunes over-truncated %q", got)
	}
	if got := firstRunes("   padded   ", 24); got != "padded" {
		t.Fatalf("firstRunes did not trim: %q", got)
	}
}

// The engine emits an assistant message mirroring every tool result; rendering
// both doubles each tool card, so the mirror is dropped.
func TestIsToolEcho(t *testing.T) {
	if !isToolEcho("Tool 'terminal_execute' result:\nHTTP/1.1 200 OK") {
		t.Fatal("tool result mirror not detected")
	}
	if isToolEcho("I'll start with a fingerprint of the target.") {
		t.Fatal("normal narration misdetected as echo")
	}
	if isToolEcho("Tool 'x' returned after 3s") {
		t.Fatal("non-result mention misdetected as echo")
	}
}

func TestTruncateTranscriptMarksCut(t *testing.T) {
	short := strings.Repeat("a", 10)
	if out, cut := truncateTranscript(short); out != short || cut {
		t.Fatalf("short output changed: %q cut=%v", out, cut)
	}
	long := strings.Repeat("b", transcriptMaxOutputChars+50)
	out, cut := truncateTranscript(long)
	if !cut || len(out) != transcriptMaxOutputChars {
		t.Fatalf("long output cut=%v len=%d", cut, len(out))
	}
}

func TestApprovalTranscriptLine(t *testing.T) {
	pending := &platform.Approval{Kind: "credential_dump", Status: platform.ApprovalPending}
	if got := approvalTranscriptLine("approval_requested", pending); !strings.Contains(got, "credential_dump") {
		t.Fatalf("requested line lost the kind: %q", got)
	}
	decided := &platform.Approval{
		Kind:      "credential_dump",
		Status:    platform.ApprovalApproved,
		DecidedBy: "operator:c1ay",
		Reason:    "authorized test",
	}
	got := approvalTranscriptLine("approval_decided", decided)
	for _, want := range []string{"credential_dump", platform.ApprovalApproved, "operator:c1ay", "authorized test"} {
		if !strings.Contains(got, want) {
			t.Fatalf("decided line %q missing %q", got, want)
		}
	}
}

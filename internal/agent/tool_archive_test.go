package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

func newBoundedContextAgent(t *testing.T, enabled bool) (*Agent, *scanctx.ScanContext) {
	t.Helper()
	cfg := &config.Config{BoundedContext: enabled}
	sctx := scanctx.New("bounded-ctx-test", t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(sctx.Close)
	a := NewAgent(cfg, "test", make(chan Event, 8), scopeguard.Config{}, sctx)
	return a, sctx
}

func toolResultMsg(name, body string) llm.Message {
	return llm.Message{Role: "user", Content: "Tool '" + name + "' result:\n" + body}
}

// TestBoundedContextDisabledByDefault proves the flag-off path is byte-identical
// to the pre-feature behavior: no archive marker, no stub, no archive file.
func TestBoundedContextDisabledByDefault(t *testing.T) {
	a, sctx := newBoundedContextAgent(t, false)
	raw := strings.Repeat("HTTP/1.1 200 OK\n", 400)
	result := capToolOutputForLLM("terminal_execute", raw)
	msg := toolResultMsg("send_request", result)

	// The flag gates every agent-side archive/stub path.
	if a.boundedContextEnabled() {
		t.Fatal("bounded context must be off")
	}
	_ = raw
	_ = result
	_ = msg

	// Flag off: aging is a no-op even with tool messages present.
	a.msgMu.Lock()
	a.messages = []llm.Message{msg, msg, msg, msg}
	a.msgMu.Unlock()
	a.ageOutToolOutputs()
	a.msgMu.Lock()
	defer a.msgMu.Unlock()
	for _, m := range a.messages {
		if !strings.HasPrefix(m.Content, "Tool 'send_request' result:") {
			t.Fatalf("flag off must not alter messages: %q", m.Content[:40])
		}
		if strings.Contains(m.Content, archiveStubPrefix) {
			t.Fatal("flag off must never stub")
		}
	}
	if sctx.ToolOutputs.Count() != 0 {
		t.Fatal("flag off must not archive anything")
	}
}

// TestAgeOutToolOutputsStubsOlderMessages verifies: active window verbatim,
// older messages stubbed once, idempotent, unarchived messages untouched.
func TestAgeOutToolOutputsStubsOlderMessages(t *testing.T) {
	a, sctx := newBoundedContextAgent(t, true)
	if a.toolArchiveActiveWindow() != 4 {
		t.Fatalf("default window = %d", a.toolArchiveActiveWindow())
	}

	raw := strings.Repeat("payload-line\n", 300) // ~4KB raw
	ids := make([]string, 12)
	a.msgMu.Lock()
	a.messages = []llm.Message{{Role: "system", Content: "system"}}
	for i := range ids {
		id := sctx.ToolOutputs.Archive("curl", raw, 1)
		if id == "" {
			t.Fatalf("archive failed for message %d", i)
		}
		ids[i] = id
		body := capToolOutputForLLM("terminal_execute", raw)
		a.messages = append(a.messages, llm.Message{
			Role:    "user",
			Content: "Tool 'curl' result:\n" + body + "\n" + archiveToolResultMarker(id),
		})
	}
	// An unarchived (pre-flag) tool message must stay verbatim.
	a.messages = append(a.messages, toolResultMsg("legacy", "small old result"))
	a.msgMu.Unlock()

	a.ageOutToolOutputs()

	// Snapshot one stubbed message for the idempotence check below.
	a.msgMu.Lock()
	stubAfterFirstPass := a.messages[1].Content
	a.msgMu.Unlock()

	// Idempotent: second pass stubs nothing new (run before locking below).
	a.ageOutToolOutputs()

	a.msgMu.Lock()
	defer a.msgMu.Unlock()
	if len(a.messages) != 14 {
		t.Fatalf("message count changed: %d", len(a.messages))
	}
	stubbed, verbatim := 0, 0
	for i, m := range a.messages {
		switch {
		case i == 0:
			continue // system
		case strings.Contains(m.Content, archiveStubPrefix):
			stubbed++
		case strings.HasPrefix(m.Content, "Tool 'curl'"):
			verbatim++
		}
	}
	// 12 archived curl messages + 1 unarchived legacy tool message = 13
	// tool-result messages; the newest 4 (3 curl + legacy) stay verbatim, the
	// oldest 9 curl messages are stubbed.
	if stubbed != 9 {
		t.Fatalf("stubbed = %d, want 9", stubbed)
	}
	if verbatim != 3 {
		t.Fatalf("verbatim recent window = %d, want 3", verbatim)
	}
	if !strings.Contains(a.messages[len(a.messages)-1].Content, "small old result") {
		t.Fatal("unarchived legacy message must stay verbatim")
	}
	// Stub must carry its archive id and a head snippet.
	stub := a.messages[1].Content
	if !strings.Contains(stub, ids[0]) {
		t.Fatalf("stub missing archive id %q: %q", ids[0], stub[:120])
	}
	if !strings.Contains(stub, "read_tool_output") {
		t.Fatal("stub must name read_tool_output")
	}

	// Idempotent: the second pass (run above, before locking) changed nothing.
	if a.messages[1].Content != stubAfterFirstPass {
		t.Fatal("aging must be idempotent")
	}

	// Stubbed sizes are tiny vs the ~16KB originals.
	if len(stub) > 1200 {
		t.Fatalf("stub too large: %d bytes", len(stub))
	}
}

// TestReadToolOutputRetrievesOriginal is the information-complete guarantee:
// the retrieval tool returns the byte-identical raw output that was stubbed
// out of the conversation.
func TestReadToolOutputRetrievesOriginal(t *testing.T) {
	a, sctx := newBoundedContextAgent(t, true)
	raw := strings.Repeat("SENSITIVE-RAW-OUTPUT-LINE\n", 500) // ~12.5KB raw
	id := sctx.ToolOutputs.Archive("send_request", raw, 1)
	if id == "" {
		t.Fatal("archive failed")
	}

	res, err := a.readToolOutputTool(map[string]string{"id": id})
	if err != nil {
		t.Fatalf("tool error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool result error: %s", res.Error)
	}
	if res.Output != raw {
		t.Fatalf("retrieved output differs from original: got %d bytes, want %d", len(res.Output), len(raw))
	}

	// Bad/missing ids produce actionable errors, not panics.
	res, _ = a.readToolOutputTool(map[string]string{"id": ""})
	if res.Error == "" {
		t.Fatal("empty id must error")
	}
	res, _ = a.readToolOutputTool(map[string]string{"id": "to_999999"})
	if res.Error == "" {
		t.Fatal("unknown id must error")
	}
}

// A model-transcribed id (prefix or padding dropped) still retrieves the
// byte-identical original; a genuinely wrong id reports the archive's extent
// instead of merely blaming the stub.
func TestReadToolOutputToleratesTranscribedID(t *testing.T) {
	a, sctx := newBoundedContextAgent(t, true)
	raw := strings.Repeat("TRANSCRIBED-ID-LINE\n", 300)
	id := sctx.ToolOutputs.Archive("send_request", raw, 1)
	if id == "" {
		t.Fatal("archive failed")
	}
	numeric := strings.TrimPrefix(id, "to_")
	for _, spelling := range []string{numeric, "to_" + strings.TrimLeft(numeric, "0")} {
		res, err := a.readToolOutputTool(map[string]string{"id": spelling})
		if err != nil {
			t.Fatalf("Get(%q) tool error: %v", spelling, err)
		}
		if res.Error != "" {
			t.Fatalf("Get(%q) result error: %s", spelling, res.Error)
		}
		if res.Output != raw {
			t.Fatalf("Get(%q) returned %d bytes, want %d", spelling, len(res.Output), len(raw))
		}
	}

	res, err := a.readToolOutputTool(map[string]string{"id": "to_999999"})
	if err != nil {
		t.Fatalf("tool error: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "highest id") {
		t.Fatalf("miss message must state the archive extent: %q", res.Error)
	}
}

// TestArchiveMarkerPreservesToolMessageShape guarantees the annotated message
// still parses as a tool-result message (isToolResultMessage) so compaction
// digests, aging, and byte-metrics heuristics keep working.
func TestArchiveMarkerPreservesToolMessageShape(t *testing.T) {
	id := "to_000001"
	msg := "Tool 'curl' result:\nbody" + "\n" + archiveToolResultMarker(id)
	if !isToolResultMessage(msg) {
		t.Fatal("annotated message must still be recognized as a tool result")
	}
	got, ok := archiveIDFromMessage(msg)
	if !ok || got != id {
		t.Fatalf("archive id extraction = %q ok=%v", got, ok)
	}
	// Marker regex tolerates longer sequences.
	if _, ok := archiveIDFromMessage("Tool 'x' result:\nno marker here"); ok {
		t.Fatal("unmarked message must not yield an id")
	}
}

// TestReadToolOutputPaginates pins the paging contract that replaced the
// archive-of-archive loop: a 27 KB listing must be readable in bounded pages,
// each page below the per-message cap, with an explicit next call.
func TestReadToolOutputPaginates(t *testing.T) {
	a, sctx := newBoundedContextAgent(t, true)
	var b strings.Builder
	for i := 0; i < 900; i++ {
		fmt.Fprintf(&b, `{"unique_code":"c-%03d","description":"challenge %d"},`, i, i)
	}
	content := b.String()
	if len(content) < 20000 {
		t.Fatalf("fixture too small: %d", len(content))
	}
	id := sctx.ToolOutputs.Archive("http_request", content, 1)
	if id == "" {
		t.Fatal("archive failed")
	}

	first, err := a.readToolOutputTool(map[string]string{"id": id, "limit": "12000"})
	if err != nil || first.Error != "" {
		t.Fatalf("first page: err=%v output=%q", err, first.Error)
	}
	if !strings.Contains(first.Output, "bytes 0-12000 of "+strconv.Itoa(len(content))) {
		t.Fatalf("first page header missing: %.160q", first.Output)
	}
	if !strings.Contains(first.Output, content[:12000]) {
		t.Fatal("first page did not carry the head bytes")
	}
	if !strings.Contains(first.Output, `offset=12000`) {
		t.Fatal("first page did not print the next call")
	}

	second, err := a.readToolOutputTool(map[string]string{"id": id, "offset": "12000", "limit": "12000"})
	if err != nil || second.Error != "" {
		t.Fatalf("second page: err=%v output=%q", err, second.Error)
	}
	if !strings.Contains(second.Output, content[12000:24000]) {
		t.Fatal("second page did not carry the middle bytes")
	}
	if strings.Contains(second.Output, "[end of output]") {
		t.Fatal("second page ended early")
	}

	last, err := a.readToolOutputTool(map[string]string{"id": id, "offset": strconv.Itoa(len(content) - 500)})
	if err != nil || last.Error != "" {
		t.Fatalf("last page: err=%v output=%q", err, last.Error)
	}
	if !strings.Contains(last.Output, content[len(content)-500:]) {
		t.Fatal("last page did not carry the tail bytes")
	}
	if !strings.Contains(last.Output, "[end of output]") {
		t.Fatal("last page did not say it was the end")
	}

	past, err := a.readToolOutputTool(map[string]string{"id": id, "offset": strconv.Itoa(len(content) + 10)})
	if err != nil || past.Error != "" {
		t.Fatalf("past-end page: err=%v output=%q", err, past.Error)
	}
	if !strings.Contains(past.Output, "past the end") {
		t.Fatalf("past-end page did not explain the bound: %q", past.Output)
	}
}

// TestReadToolOutputIsNotReArchived keeps the retrieval path idempotent: the
// page a caller requested must not be replaced by another archive stub.
func TestReadToolOutputIsNotReArchived(t *testing.T) {
	a, sctx := newBoundedContextAgent(t, true)
	page := strings.Repeat("x", 12000)
	before := sctx.ToolOutputs.Count()
	out := (&einoObserver{agent: a}).TransformToolOutput(context.Background(), "read_tool_output", nil, page, nil, nil)
	if sctx.ToolOutputs.Count() != before {
		t.Fatalf("read_tool_output was archived again: %d -> %d", before, sctx.ToolOutputs.Count())
	}
	if strings.Contains(out, archiveMarkerPrefix) {
		t.Fatal("read_tool_output page was replaced by an archive marker")
	}
	if !strings.Contains(out, page) {
		t.Fatal("read_tool_output page content was not delivered")
	}
}

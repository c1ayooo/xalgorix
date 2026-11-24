package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTranscriptProposalAndMainReplyProjection pins the two transcript
// changes that make the conversation page usable: main-agent replies carry
// role=main, and an intent proposal is one card whose later resolution folds
// into the original status instead of appending a second card.
func TestTranscriptProposalAndMainReplyProjection(t *testing.T) {
	lines := []string{
		`{"type":"message","content":"I can propose that.","timestamp":"2026-11-23T10:00:00Z","result_meta":{"surface":"main"}}`,
		`{"type":"intent_proposed","content":"SQLi on /login","timestamp":"2026-11-23T10:00:01Z","result_meta":{"proposal_id":"p1","task_id":"human-1","title":"SQLi on /login","target":"https://example.test","target_kind":"web","phase":6,"vuln_class":"sqli","endpoint":"/login","status":"pending"}}`,
		`{"type":"intent_proposal_resolved","content":"SQLi on /login","timestamp":"2026-11-23T10:00:02Z","result_meta":{"proposal_id":"p1","task_id":"human-1","status":"confirmed"}}`,
		`{"type":"tool_result","tool_name":"propose_intent","output":"Proposed intent p1","timestamp":"2026-11-23T10:00:03Z"}`,
	}
	seen := make(map[string]struct{})
	var items []transcriptItem
	for _, line := range lines {
		foldTranscriptLine([]byte(line), seen, &items)
	}
	items = collapseProposalStatuses(items)
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 (reply, proposal, tool result): %+v", len(items), items)
	}
	if items[0].Kind != "assistant" || items[0].Role != "main" {
		t.Fatalf("main reply projection = %+v", items[0])
	}
	if items[1].Kind != "proposal" || items[1].Proposal == nil {
		t.Fatalf("proposal item = %+v", items[1])
	}
	if got := items[1].Proposal.Status; got != "confirmed" {
		t.Fatalf("proposal status = %q, want confirmed", got)
	}
	if got := items[1].Proposal.TaskID; got != "human-1" {
		t.Fatalf("proposal task id = %q", got)
	}
	if items[1].Proposal.Title != "SQLi on /login" || items[1].Proposal.Phase != 6 {
		t.Fatalf("proposal payload lost: %+v", items[1].Proposal)
	}

	human := humanTranscriptItems(items)
	if len(human) != 2 {
		t.Fatalf("human projection = %+v, want reply + proposal", human)
	}
	for _, item := range human {
		if item.Kind == "tool_result" {
			t.Fatalf("tool result leaked into human projection: %+v", item)
		}
	}
}

// TestIntentProposalRouteRejectsExpiredDraft covers the routing + honest
// failure: a proposal lives in the live agent's memory, so confirming after
// the run stopped must be a 409 rather than a fake success.
func TestIntentProposalRouteRejectsExpiredDraft(t *testing.T) {
	s, _, _ := newSideQuestionFixture(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/inst-q/intent-proposals/p1/confirm", nil)
	s.handleConversationDetail(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("confirm expired proposal status = %d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/conversations/inst-q/intent-proposals/p1/reject", nil)
	s.handleConversationDetail(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("reject expired proposal status = %d body=%s", rr.Code, rr.Body.String())
	}
}

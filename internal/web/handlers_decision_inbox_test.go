package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/intelaccess"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// seedCapabilityFixture prepares one confirmed credential with an operator
// grant and one paused intent whose target only that grant covers.
func seedCapabilityFixture(t *testing.T, scanID, unitKey string) (*intelaccess.MemoryStore, int64, string) {
	t.Helper()
	graphStore := graph.NewMemoryStore()
	graph.SetBackend(graphStore)
	access := intelaccess.NewMemoryStore()
	intelaccess.SetStore(access)
	t.Cleanup(func() {
		intelaccess.SetStore(nil)
		graph.SetBackend(nil)
	})

	ctx := t.Context()
	item := intelligence.Item{
		Fingerprint: "fp-cap", Kind: intelligence.KindCredential,
		Endpoint: "10.0.177.5:22", Title: "SSH password",
	}
	if _, err := access.DeriveScan(ctx, scanID, []intelligence.Item{item}); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if _, err := access.Confirm(ctx, intelaccess.ConfirmInput{
		ScanID: scanID, Fingerprint: "fp-cap", Kind: intelaccess.KindCredential,
		Targets: []string{"10.0.177.9:22"}, By: "operator",
	}); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	intent, err := graphStore.UpsertNode(ctx, graph.NodeInput{
		ScanID: scanID, Kind: graph.KindIntent, UnitKey: unitKey,
		State: graph.StateRunning, Owner: "work#1", Priority: 100,
		Payload: `{"task_id":"` + unitKey + `","target":"10.0.177.9:22","target_kind":"container"}`,
	})
	if err != nil {
		t.Fatalf("running intent: %v", err)
	}
	moved, err := graphStore.SettleIntent(ctx, scanID, intent.ID, "work#1", graph.StatePaused,
		"capability required for 10.0.177.9:22 (intel fp-cap)")
	if err != nil || !moved {
		t.Fatalf("park intent = %v, %v", moved, err)
	}
	return access, intent.ID, "fp-cap"
}

// The operator approves a parked intent: one capability row is written for the
// exact target and the intent is resumed for the pool to claim.
func TestDecisionIssueCapabilityIssuesAndResumes(t *testing.T) {
	s := newPlatformTestServer(t)
	const scanID = "cap-scan"
	access, intentID, _ := seedCapabilityFixture(t, scanID, "t-cap")

	rr := doJSON(t, s, http.MethodPost, "/api/scans/"+scanID+"/decisions",
		`{"action":"issue_capability","unit_id":"t-cap","uses_left":2,"ttl_s":120}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue_capability code = %d body=%s", rr.Code, rr.Body.String())
	}
	var ack struct {
		Issued  int  `json:"capabilities_issued"`
		Resumed bool `json:"resumed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Issued != 1 || !ack.Resumed {
		t.Fatalf("ack = %+v body=%s", ack, rr.Body.String())
	}

	caps, err := access.ListCapabilities(t.Context(), scanID, intentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 1 || caps[0].Target != "10.0.177.9:22" || caps[0].UsesLeft != 2 || caps[0].IssuedBy != "operator" {
		t.Fatalf("capabilities = %+v", caps)
	}
	node, err := graph.Backend().GetNode(t.Context(), scanID, intentID)
	if err != nil {
		t.Fatal(err)
	}
	if node.State != graph.StateOpen {
		t.Fatalf("resumed intent state = %s, want open", node.State)
	}
}

// A target no usable intel grant covers cannot be approved: the operator gets a
// conflict instead of a stray permission.
func TestDecisionIssueCapabilityRefusesUngrantedTarget(t *testing.T) {
	s := newPlatformTestServer(t)
	const scanID = "cap-scan-deny"
	_, _, _ = seedCapabilityFixture(t, scanID, "t-cap")

	rr := doJSON(t, s, http.MethodPost, "/api/scans/"+scanID+"/decisions",
		`{"action":"issue_capability","unit_id":"t-cap","targets":["10.9.9.9:22"]}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("ungranted target code = %d body=%s", rr.Code, rr.Body.String())
	}

	rr = doJSON(t, s, http.MethodPost, "/api/scans/"+scanID+"/decisions",
		`{"action":"issue_capability","unit_id":"t-cap","targets":["not a target"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid target code = %d body=%s", rr.Code, rr.Body.String())
	}
}

// The inbox surfaces parked intents with the engine's reason so the operator
// knows what to approve.
func TestDecisionInboxListsPausedIntent(t *testing.T) {
	s := newPlatformTestServer(t)
	const scanID = "cap-scan-inbox"
	_, _, _ = seedCapabilityFixture(t, scanID, "t-cap")

	rr := doJSON(t, s, http.MethodGet, "/api/scans/"+scanID+"/decisions", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("inbox code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view struct {
		Paused []struct {
			UnitID string `json:"unit_id"`
			Reason string `json:"reason"`
			Target string `json:"target"`
		} `json:"paused"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Paused) != 1 || view.Paused[0].UnitID != "t-cap" || view.Paused[0].Target != "10.0.177.9:22" {
		t.Fatalf("paused inbox = %+v", view.Paused)
	}
	if !strings.Contains(view.Paused[0].Reason, "capability required") {
		t.Fatalf("paused reason = %q", view.Paused[0].Reason)
	}
}

// The intel-access projection is the read surface the panel/report uses for
// "what doors does this run hold, who approved them, what was used".
func TestScanIntelAccessProjection(t *testing.T) {
	s := newPlatformTestServer(t)
	const scanID = "intel-proj-scan"
	access, intentID, fp := seedCapabilityFixture(t, scanID, "t-proj")
	row, _, err := access.Get(t.Context(), scanID, fp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.IssueCapabilities(t.Context(), intelaccess.IssueInput{
		ScanID: scanID, IntentID: intentID, AccessID: row.ID,
		Targets: []string{"10.0.177.9:22"}, UsesLeft: 2, TTL: time.Minute, IssuedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if err := access.RecordUse(t.Context(), intelaccess.UseRecord{
		ScanID: scanID, Fingerprint: fp, Decision: intelaccess.DecisionDetailFetch, Reason: "detail_fetch",
	}); err != nil {
		t.Fatal(err)
	}

	rr := doJSON(t, s, http.MethodGet, "/api/scans/"+scanID+"/intel-access", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("intel-access code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view struct {
		Access []struct {
			Fingerprint string   `json:"fingerprint"`
			State       string   `json:"state"`
			Grants      []string `json:"grants"`
		} `json:"access"`
		Capabilities []struct {
			Target   string `json:"target"`
			UsesLeft int    `json:"uses_left"`
		} `json:"capabilities"`
		Uses []struct {
			Decision string `json:"decision"`
		} `json:"uses"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Access) != 1 || view.Access[0].Fingerprint != fp || view.Access[0].State != intelaccess.StateConfirmed {
		t.Fatalf("access projection = %+v", view.Access)
	}
	if len(view.Capabilities) != 1 || view.Capabilities[0].Target != "10.0.177.9:22" || view.Capabilities[0].UsesLeft != 2 {
		t.Fatalf("capability projection = %+v", view.Capabilities)
	}
	if len(view.Uses) != 1 || view.Uses[0].Decision != intelaccess.DecisionDetailFetch {
		t.Fatalf("audit projection = %+v", view.Uses)
	}
}

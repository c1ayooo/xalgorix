package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/intelaccess"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

func newSnapshotTestAgent(t *testing.T) (*Agent, *graph.MemoryStore, *scanctx.ScanContext, string) {
	t.Helper()
	dir := t.TempDir()
	sctx := scanctx.New("scan-snap", dir)
	sctx.SetInstanceID("run-snap")
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	state := NewScanState()
	state.ScanContextID = sctx.ID
	a := &Agent{ID: "agent-test", state: state, scanCtx: sctx, events: make(chan Event, 16)}
	return a, store, sctx, dir
}

func seedSnapshotState(t *testing.T, a *Agent, store *graph.MemoryStore, sctx *scanctx.ScanContext) {
	t.Helper()
	ctx := context.Background()

	hintPayload := `{"message":"always test /admin as the admin role","source":"human"}`
	if _, err := store.UpsertNode(ctx, graph.NodeInput{
		ScanID: "run-snap", Kind: graph.KindHint, UnitKey: "hint-chat-abc",
		State: graph.StateActive, Origin: "human", Payload: hintPayload,
	}); err != nil {
		t.Fatalf("hint node: %v", err)
	}
	intentPayload := `{"title":"SQLi sweep","target":"https://app.test","vuln_class":"sqli","endpoint":"/api/users"}`
	if _, err := store.UpsertNode(ctx, graph.NodeInput{
		ScanID: "run-snap", Kind: graph.KindIntent, UnitKey: "t-1",
		State: graph.StateRunning, Origin: "llm", Payload: intentPayload,
	}); err != nil {
		t.Fatalf("intent node: %v", err)
	}

	l := sctx.Ledger
	l.Upsert(scanctx.Hypothesis{
		ID: "H-1", VulnClass: "sqli", Endpoint: "/api/users",
		Status: scanctx.HypothesisQueued, Confidence: 0.6, NextAction: "time-based probe",
	})
	l.AddEvidence("H-1", scanctx.Evidence{
		ID: "EV-1", Kind: "probe", Nature: scanctx.EvidenceObserved,
		Summary: "time-based delay of 5s confirms injection", CreatedAt: time.Now().UTC(),
	})
	l.Upsert(scanctx.Hypothesis{
		ID: "H-2", VulnClass: "xss", Endpoint: "/search",
		Status: scanctx.HypothesisRejected, Attempts: 3,
	})

	reporting.SeedVulnsForContext(sctx.ID, []reporting.Vulnerability{{
		ID: "XALG-1", Title: "Exposed admin console", Severity: "High",
		Endpoint: "/admin", Tags: []string{reporting.TagVerified},
	}})
	a.state.DiscoveredEndpoints = []string{"/api/users", "/search"}
}

func snapshotDroppedWindow() []llm.Message {
	return []llm.Message{
		{Role: "assistant", ToolCalls: []llm.MessageToolCall{
			{Function: llm.MessageToolFunction{Name: "terminal_execute"}},
		}},
		{Role: "tool", Content: "Tool 'terminal_execute' result:\ntime-based delay observed on /api/users\n" + archiveToolResultMarker("to_000042")},
	}
}

func TestContextSnapshotRendersAuthoritativeSections(t *testing.T) {
	a, store, sctx, _ := newSnapshotTestAgent(t)
	seedSnapshotState(t, a, store, sctx)

	snap := a.buildContextSnapshot("lane:agent:agent-test", snapshotDroppedWindow(), 64*1024)
	if !snap.HasState {
		t.Fatal("snapshot reports no state")
	}
	for _, want := range []string{
		"[CONTEXT SNAPSHOT",
		"NOT proof",
		"## Human input",
		"hint-chat-abc",
		"always test /admin",
		"## Plan intents",
		"t-1",
		"SQLi sweep",
		"## Open beliefs",
		"H-1",
		"## Dead ends",
		"H-2",
		"## Confirmed findings",
		"XALG-1",
		"tag=verified",
		"## Recent observations",
		"EV-1",
		"## Recent actions",
		"to_000042",
		"[gate]",
		"[ledger]",
		"[graph]",
		"[engine]",
	} {
		if !strings.Contains(snap.Text, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, snap.Text)
		}
	}
	// A rejected direction must never disappear from the projection.
	deadIdx := strings.Index(snap.Text, "## Dead ends")
	if deadIdx < 0 || !strings.Contains(snap.Text[deadIdx:], "H-2") {
		t.Fatalf("dead-end section lost H-2:\n%s", snap.Text)
	}
	for _, ref := range []string{"H-1", "H-2", "EV-1", "XALG-1", "hint-chat-abc", "t-1", "to_000042"} {
		found := false
		for _, r := range snap.Refs {
			if r == ref {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ref %q missing from %v", ref, snap.Refs)
		}
	}
	// IG6a forbids the digest from claiming accepted findings.
	if strings.Contains(snap.Text, "### Key Findings") {
		t.Fatalf("snapshot uses the old authority-overreaching heading:\n%s", snap.Text)
	}
}

func TestContextSnapshotDeterministic(t *testing.T) {
	a, store, sctx, _ := newSnapshotTestAgent(t)
	seedSnapshotState(t, a, store, sctx)

	first := a.buildContextSnapshot("lane:agent:agent-test", snapshotDroppedWindow(), 64*1024)
	for i := 0; i < 10; i++ {
		got := a.buildContextSnapshot("lane:agent:agent-test", snapshotDroppedWindow(), 64*1024)
		if got.Text != first.Text {
			t.Fatalf("snapshot not deterministic (run %d)", i)
		}
		if strings.Join(got.Refs, ",") != strings.Join(first.Refs, ",") {
			t.Fatalf("refs not deterministic (run %d): %v vs %v", i, got.Refs, first.Refs)
		}
	}
}

func TestPersistContextDigestChainAndInfoItem(t *testing.T) {
	a, store, sctx, dir := newSnapshotTestAgent(t)
	seedSnapshotState(t, a, store, sctx)

	snap := a.buildContextSnapshot(a.digestRegion(), snapshotDroppedWindow(), 64*1024)
	a.persistContextDigest(snap, 100000)

	digests, err := store.ListNodes(context.Background(), "run-snap", graph.KindDigest)
	if err != nil {
		t.Fatalf("list digests: %v", err)
	}
	if len(digests) != 1 || digests[0].State != graph.StateActive {
		t.Fatalf("digest nodes = %+v, want one active", digests)
	}
	firstID := digests[0].ID

	// A second compaction after new evidence must supersede the first and link
	// the lineage edge.
	sctx.Ledger.Upsert(scanctx.Hypothesis{
		ID: "H-3", VulnClass: "ssrf", Endpoint: "/fetch", Status: scanctx.HypothesisQueued,
	})
	a.compactionCount = 1
	snap2 := a.buildContextSnapshot(a.digestRegion(), snapshotDroppedWindow(), 64*1024)
	a.persistContextDigest(snap2, 120000)

	digests, err = store.ListNodes(context.Background(), "run-snap", graph.KindDigest)
	if err != nil {
		t.Fatalf("list digests: %v", err)
	}
	if len(digests) != 2 {
		t.Fatalf("digest nodes = %d, want 2", len(digests))
	}
	active, superseded := graph.Node{}, graph.Node{}
	for _, n := range digests {
		switch n.State {
		case graph.StateActive:
			active = n
		case graph.StateSuperseded:
			superseded = n
		}
	}
	if active.ID == 0 || superseded.ID == 0 {
		t.Fatalf("expected one active + one superseded, got %+v", digests)
	}
	if superseded.ID != firstID {
		t.Fatalf("first digest was not the superseded one: %d vs %d", superseded.ID, firstID)
	}
	edges, err := store.ListEdges(context.Background(), "run-snap")
	if err != nil {
		t.Fatalf("list edges: %v", err)
	}
	lineage := false
	for _, e := range edges {
		if e.SrcID == active.ID && e.DstID == superseded.ID && e.Rel == graph.RelDerivedFrom {
			lineage = true
		}
	}
	if !lineage {
		t.Fatalf("missing derived_from active→superseded edge: %+v", edges)
	}

	// The atomic provenance must be in the journal as an inferred InfoItem with
	// non-empty supports (CORE §3.2 invariant 3).
	records, _, err := domainjournal.ReadRecords(filepath.Join(dir, domainjournal.FileName))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	found := false
	for _, rec := range records {
		if rec.Operation != domainjournal.OpContextDigest || rec.InfoItem == nil {
			continue
		}
		found = true
		if rec.InfoItem.Nature != infoitem.NatureInferred {
			t.Fatalf("digest InfoItem nature = %q, want inferred", rec.InfoItem.Nature)
		}
		if len(rec.InfoItem.Supports) == 0 {
			t.Fatal("digest InfoItem has empty supports")
		}
		if rec.InfoItem.Coordinates.Purpose != "summary" {
			t.Fatalf("digest purpose = %q, want summary", rec.InfoItem.Coordinates.Purpose)
		}
		if rec.InfoItem.SourceSeq != rec.Seq {
			t.Fatalf("source_seq = %d, record seq = %d", rec.InfoItem.SourceSeq, rec.Seq)
		}
	}
	if !found {
		t.Fatal("no context_digest journal record found")
	}
}

func TestContextDigestRequiresSupports(t *testing.T) {
	_, _, sctx, _ := newSnapshotTestAgent(t)
	item := infoitem.Item{Nature: infoitem.NatureInferred}
	if err := sctx.Ledger.AppendContextDigest(&item, "no supports"); err == nil {
		t.Fatal("AppendContextDigest accepted an inferred item without supports")
	}
}

func TestWorkerSeedIsBoundedAndCarriesDeadEnds(t *testing.T) {
	a, store, sctx, _ := newSnapshotTestAgent(t)
	seedSnapshotState(t, a, store, sctx)

	seed := a.workerSeedContext("t-1")
	if seed == "" {
		t.Fatal("worker seed is empty")
	}
	if len(seed) > snapshotWorkerSeedBytes+256 {
		t.Fatalf("worker seed %d bytes exceeds the 8 KiB budget", len(seed))
	}
	for _, want := range []string{"Shared run state", "## Dead ends", "H-2", "hint-chat-abc"} {
		if !strings.Contains(seed, want) {
			t.Fatalf("worker seed missing %q:\n%s", want, seed)
		}
	}
}

func TestMainChatListHints(t *testing.T) {
	a, store, sctx, _ := newSnapshotTestAgent(t)
	seedSnapshotState(t, a, store, sctx)

	out, err := a.mainChatListHints(map[string]string{})
	if err != nil {
		t.Fatalf("list hints: %v", err)
	}
	for _, want := range []string{"hint-chat-abc", "always test /admin"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list hints missing %q: %s", want, out)
		}
	}
	consumed, err := a.mainChatListHints(map[string]string{"state": "consumed"})
	if err != nil {
		t.Fatalf("list consumed hints: %v", err)
	}
	if !strings.Contains(consumed, "No operator hints") {
		t.Fatalf("consumed filter returned %q", consumed)
	}
	if _, err := a.mainChatListHints(map[string]string{"state": "bogus"}); err == nil {
		t.Fatal("invalid state filter accepted")
	}
}

// End-to-end wiring: the production compaction path renders the projection
// (not the old text digest) and persists one active digest node.
func TestCompactMessagesProjectsSnapshotAndDigestNode(t *testing.T) {
	a, store, sctx, _ := newSnapshotTestAgent(t)
	seedSnapshotState(t, a, store, sctx)
	a.cfg = &config.Config{
		ContextCompactTokens: -1, // auto: force the window-relative trigger only
		LLMContextWindow:     1000,
		ContextCompactRatio:  0.75,
	}
	a.lastPromptTokens.Store(800)

	input := make([]*schema.Message, 0, 44)
	input = append(input, schema.SystemMessage("system"))
	for i := 1; i < 44; i++ {
		input = append(input, schema.UserMessage(fmt.Sprintf("message %d", i)))
	}
	out := (&einoObserver{agent: a}).CompactMessages(input)
	if len(out) != 42 {
		t.Fatalf("compacted length = %d, want 42", len(out))
	}
	digest := out[1].Content
	for _, want := range []string{"CONTEXT SNAPSHOT", "## Dead ends", "H-2", "## Confirmed findings", "XALG-1"} {
		if !strings.Contains(digest, want) {
			t.Fatalf("compacted message missing %q:\n%s", want, digest)
		}
	}
	if strings.Contains(digest, "## Compacted History") {
		t.Fatalf("compaction still used the legacy text digest:\n%s", digest)
	}
	digests, err := store.ListNodes(context.Background(), "run-snap", graph.KindDigest)
	if err != nil {
		t.Fatalf("list digests: %v", err)
	}
	if len(digests) != 1 || digests[0].State != graph.StateActive {
		t.Fatalf("digest nodes = %+v, want one active", digests)
	}
}

// The worker seed lists endorsed intel only: proposed items are observations,
// not doors, and the seed is auto-injected so no content may travel with it
// (INTEL_HANDOFF_SPEC v1.1 D4/D5 + M2). Once the operator confirms, the row
// appears with its grants and a detail-fetch hint.
func TestWorkerSeedListsConfirmedIntelOnly(t *testing.T) {
	a, store, sctx, _ := newSnapshotTestAgent(t)
	seedSnapshotState(t, a, store, sctx)
	intelligence.ResetForContext(sctx.ID)
	access := intelaccess.NewMemoryStore()
	intelaccess.SetStore(access)
	t.Cleanup(func() {
		intelaccess.SetStore(nil)
		intelligence.CleanupContext(sctx.ID)
	})

	if _, err := intelligence.RecordForContext(sctx.ID, map[string]string{
		"kind":     "credential",
		"title":    "SSH password on app-01",
		"content":  "root uses Passw0rd! on 10.0.177.5:22",
		"endpoint": "10.0.177.5:22",
	}); err != nil {
		t.Fatalf("record intel: %v", err)
	}
	items := intelligence.ItemsForContext(sctx.ID)
	if len(items) != 1 {
		t.Fatalf("intel items = %d, want 1", len(items))
	}
	fp := items[0].Fingerprint

	// Access rows are keyed by the graph run id, not the intel store's context
	// id — the production shape this section must work in.
	graphScanID := a.planGraphScanID()
	if graphScanID == "" {
		graphScanID = sctx.ID
	}
	if _, err := access.DeriveScan(context.Background(), graphScanID, items); err != nil {
		t.Fatalf("derive: %v", err)
	}
	seed := a.workerSeedContext("t-1")
	if strings.Contains(seed, fp) {
		t.Fatalf("proposed intel must not appear in the seed:\n%s", seed)
	}

	if _, err := access.Confirm(context.Background(), intelaccess.ConfirmInput{
		ScanID: graphScanID, Fingerprint: fp, Kind: intelaccess.KindCredential,
		Subject: "10.0.177.5", Targets: []string{"10.0.177.5:22"}, By: "operator",
	}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	seed = a.workerSeedContext("t-1")
	if strings.Contains(seed, "Passw0rd!") {
		t.Fatalf("worker seed leaked intel content:\n%s", seed)
	}
	for _, want := range []string{
		"Available intel", "fp=" + fp, "state=confirmed", "grants=10.0.177.5:22",
		"read_intelligence(fingerprint=" + fp + ")",
	} {
		if !strings.Contains(seed, want) {
			t.Fatalf("worker seed missing %q:\n%s", want, seed)
		}
	}
}

// The intel section is capped and deterministically ordered, so a run with
// many confirmed items cannot crowd out the rest of the seed.
func TestWorkerSeedIntelIsCapped(t *testing.T) {
	a, store, sctx, _ := newSnapshotTestAgent(t)
	seedSnapshotState(t, a, store, sctx)
	intelligence.ResetForContext(sctx.ID)
	access := intelaccess.NewMemoryStore()
	intelaccess.SetStore(access)
	t.Cleanup(func() {
		intelaccess.SetStore(nil)
		intelligence.CleanupContext(sctx.ID)
	})

	for i := 0; i < snapIntelMax+3; i++ {
		res, err := intelligence.RecordForContext(sctx.ID, map[string]string{
			"kind":    "lead",
			"title":   fmt.Sprintf("lead %d", i),
			"content": fmt.Sprintf("distinct observation %d", i),
		})
		if err != nil || res.Error != "" {
			t.Fatalf("record intel %d: err=%v res=%+v", i, err, res)
		}
	}
	items := intelligence.ItemsForContext(sctx.ID)
	if len(items) != snapIntelMax+3 {
		t.Fatalf("intel items = %d, want %d", len(items), snapIntelMax+3)
	}
	// Access rows are keyed by the graph run id, not the intel store's context
	// id — the production shape this section must work in.
	graphScanID := a.planGraphScanID()
	if graphScanID == "" {
		graphScanID = sctx.ID
	}
	if _, err := access.DeriveScan(context.Background(), graphScanID, items); err != nil {
		t.Fatalf("derive: %v", err)
	}
	for _, item := range items {
		if _, err := access.Confirm(context.Background(), intelaccess.ConfirmInput{
			ScanID: graphScanID, Fingerprint: item.Fingerprint, Kind: intelaccess.KindPath, By: "operator",
		}); err != nil {
			t.Fatalf("confirm: %v", err)
		}
	}
	seed := a.workerSeedContext("t-2")
	if got := strings.Count(seed, "- [intel] "); got != snapIntelMax {
		t.Fatalf("intel lines in seed = %d, want %d:\n%s", got, snapIntelMax, seed)
	}
}

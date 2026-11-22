package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/domainjournal"
	"github.com/xalgord/xalgorix/v4/internal/infoitem"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

func explorationGet(t *testing.T, s *Server, id string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleScanExploration(rr, httptest.NewRequest(http.MethodGet, "/api/scans/"+id+"/exploration", nil))
	return rr
}

// The projection must reduce the durable journal + ledger into the shared
// node/edge model: goal/agent/hypothesis/evidence/finding nodes, claim /
// yields / proves edges, and the ledger's authoritative titles/states.
func TestScanExplorationEndpoint_ReducesJournalAndLedger(t *testing.T) {
	s := newTestServer(t, nil)
	id := "explore-reduce-1"
	dir := ledgerTestScan(t, s, id)

	// Ledger authority: titles/states and the finding_ref structural link.
	store := scanctx.NewLedgerStore()
	store.SetPersistPath(dir)
	now := time.Now().UTC()
	hID := store.Upsert(scanctx.Hypothesis{
		Title:     "SQLi on login",
		VulnClass: "sqli",
		Endpoint:  "https://example.com/login",
		Status:    scanctx.HypothesisProven,
		CreatedAt: now,
		UpdatedAt: now,
	}).ID
	store.AddEvidence(hID, scanctx.Evidence{
		Kind:       "exploit",
		Summary:    "payload reflected without encoding",
		Nature:     scanctx.EvidenceObserved,
		Confidence: 0.9,
		CreatedAt:  now,
	})
	store.AddEvidence(hID, scanctx.Evidence{
		Kind:      scanctx.EvidenceFindingRef,
		FindingID: "XALG-7",
		Summary:   "Reported as finding XALG-7",
		CreatedAt: now,
	})
	stored, ok := store.Get(hID)
	if !ok || len(stored.Evidence) != 2 {
		t.Fatalf("fixture evidence = %v (%d)", ok, len(stored.Evidence))
	}
	evID, refID := stored.Evidence[0].ID, stored.Evidence[1].ID

	// Journal history: objective, delegation, seed, claim, evidence, finding.
	j, err := domainjournal.Open(filepath.Join(dir, domainjournal.FileName), id)
	if err != nil {
		t.Fatal(err)
	}
	objItem := &infoitem.Item{ID: "ii-obj", Nature: infoitem.NatureObserved,
		Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{Text: "验证登录接口是否存在SQL注入"},
			Source:  infoitem.Source{Kind: "human"},
			Subject: infoitem.Subject{Identifier: "task:objective"}},
	}
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorHuman, Source: "task_objective"},
		Operation: domainjournal.OpRecordObjective, Summary: "objective", InfoItem: objItem,
	})
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "coord"},
		Operation: domainjournal.OpAgentDelegat, Summary: `create_agent id=spec-1 role="sqli specialist" task="probe login"`,
	})
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "hook"},
		Operation: domainjournal.OpSeedHypothesis, HypothesisID: hID,
		After: &domainjournal.State{Status: "queued"}, Summary: "seed " + hID,
	})
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "spec-1", Source: "ledger_tool"},
		Operation: domainjournal.OpClaimHypothesis, HypothesisID: hID,
		After: &domainjournal.State{Status: "testing"}, Summary: "claim " + hID,
	})
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "spec-1", Source: "ledger_tool"},
		Operation: domainjournal.OpAddEvidence, HypothesisID: hID, EvidenceID: evID,
		Summary: "evidence " + evID + " [exploit/observed]",
		InfoItem: &infoitem.Item{ID: "ii-" + evID, Nature: infoitem.NatureObserved, Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{Text: "payload reflected without encoding"},
			Source:  infoitem.Source{Kind: "agent"},
			Subject: infoitem.Subject{Identifier: "https://example.com/login"}}},
	})
	findingItem := &infoitem.Item{ID: "ii-find", Nature: infoitem.NatureObserved,
		Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{Text: "SQLi confirmed"},
			Source:  infoitem.Source{Kind: "report_gate"},
			Subject: infoitem.Subject{Kind: "endpoint", Identifier: "https://example.com/login"},
			Context: map[string]string{"finding_id": "XALG-7", "finding": "SQLi on login", "severity": "high", "verified": "true"},
		}}
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "report-gate"},
		Operation: domainjournal.OpFindingRecorded, Summary: "finding XALG-7: SQLi on login", InfoItem: findingItem,
	})
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "trafficproxy"},
		Operation: domainjournal.OpObserveTraffic, Summary: "traffic observed",
		InfoItem: &infoitem.Item{ID: "ii-traffic", Nature: infoitem.NatureObserved, Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{Text: "POST /login 401"},
			Source:  infoitem.Source{Kind: "trafficproxy"},
			Subject: infoitem.Subject{Identifier: "https://example.com/login"}}},
	})

	rr := explorationGet(t, s, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var view explorationView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.JournalPresent || view.JournalDamaged || !view.LedgerPresent || view.Incomplete {
		t.Fatalf("flags = journal:%v damaged:%v ledger:%v incomplete:%v",
			view.JournalPresent, view.JournalDamaged, view.LedgerPresent, view.Incomplete)
	}

	byID := map[string]explorationNode{}
	for _, n := range view.Nodes {
		byID[n.ID] = n
	}
	// Goal node from the objective record.
	if goal, ok := byID["goal"]; !ok || goal.Kind != exploreKindGoal {
		t.Fatalf("goal node missing: %+v", byID["goal"])
	}
	// Specialist node with parsed role; claim edge from specialist (not main).
	agent, ok := byID["agent:spec-1"]
	if !ok || agent.Title != "sqli specialist" {
		t.Fatalf("agent node = %+v, want parsed role", byID["agent:spec-1"])
	}
	// Hypothesis node: ledger title/state win over journal records.
	hyp, ok := byID[hypNodeID(hID)]
	if !ok || hyp.Title != "SQLi on login" || hyp.State != string(scanctx.HypothesisProven) {
		t.Fatalf("hypothesis node = %+v", hyp)
	}
	// Evidence node with nature from the journal record.
	ev, ok := byID[evNodeID(evID)]
	if !ok || ev.Nature != string(scanctx.EvidenceObserved) || ev.Title != "exploit" {
		t.Fatalf("evidence node = %+v", ev)
	}
	// Finding node from the projection InfoItem context.
	finding, ok := byID[findingNodeID("XALG-7")]
	if !ok || finding.Severity != "high" || finding.Title != "SQLi on login" {
		t.Fatalf("finding node = %+v", finding)
	}
	// Observation node floats in the fact column (no structural edge).
	if _, ok := byID["obs:ii-traffic"]; !ok {
		t.Fatalf("observation node missing")
	}

	edgeKey := func(src, dst, rel string) bool {
		for _, e := range view.Edges {
			if e.Src == src && e.Dst == dst && e.Rel == rel {
				return true
			}
		}
		return false
	}
	if !edgeKey("agent:spec-1", hypNodeID(hID), exploreRelClaim) {
		t.Fatalf("claim edge missing (specialist→hypothesis): %+v", view.Edges)
	}
	if edgeKey(exploreMainNodeID, hypNodeID(hID), exploreRelClaim) {
		t.Fatalf("claim edge wrongly attributed to main loop: %+v", view.Edges)
	}
	if !edgeKey(hypNodeID(hID), evNodeID(evID), exploreRelYields) {
		t.Fatalf("yields edge missing: %+v", view.Edges)
	}
	if !edgeKey(evNodeID(refID), findingNodeID("XALG-7"), exploreRelProves) {
		t.Fatalf("proves edge missing (finding_ref from ledger): %+v", view.Edges)
	}
	// Counts: hypothesis has one claim in, one yields out.
	if hyp.Up != 1 || hyp.Down != 1 {
		t.Fatalf("hypothesis counts = %d/%d, want 1/1", hyp.Up, hyp.Down)
	}
}

// The main-loop node only appears when a claim has no matching specialist —
// single-agent runs attribute claims to the Eino iteration node.
func TestScanExplorationEndpoint_MainLoopClaimsAttributeToMainNode(t *testing.T) {
	s := newTestServer(t, nil)
	id := "explore-main-1"
	dir := ledgerTestScan(t, s, id)

	j, err := domainjournal.Open(filepath.Join(dir, domainjournal.FileName), id)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "main-agent", Source: "ledger_tool"},
		Operation: domainjournal.OpClaimHypothesis, HypothesisID: "H-1",
		After: &domainjournal.State{Status: "testing"}, Summary: "claim H-1",
	})

	rr := explorationGet(t, s, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var view explorationView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if _, ok := byNodeID(view, exploreMainNodeID); !ok {
		t.Fatalf("main node missing: %+v", view.Nodes)
	}
	found := false
	for _, e := range view.Edges {
		if e.Src == exploreMainNodeID && e.Dst == hypNodeID("H-1") && e.Rel == exploreRelClaim {
			found = true
		}
	}
	if !found {
		t.Fatalf("main claim edge missing: %+v", view.Edges)
	}
}

// Missing journal with a present ledger: graph = ledger current state, no
// fabricated history, incomplete flag + honest note (never 404).
func TestScanExplorationEndpoint_MissingJournalFallsBackToLedger(t *testing.T) {
	s := newTestServer(t, nil)
	id := "explore-nojournal-1"
	dir := ledgerTestScan(t, s, id)
	store := scanctx.NewLedgerStore()
	store.SetPersistPath(dir)
	now := time.Now().UTC()
	store.Upsert(scanctx.Hypothesis{
		Title: "XSS probe", Status: scanctx.HypothesisQueued, CreatedAt: now, UpdatedAt: now,
	})

	rr := explorationGet(t, s, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var view explorationView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.JournalPresent || !view.LedgerPresent || !view.Incomplete || view.Note == "" {
		t.Fatalf("flags = %+v", view)
	}
	if len(view.Nodes) != 1 || view.Nodes[0].Kind != exploreKindHypothesis {
		t.Fatalf("nodes = %+v, want one ledger hypothesis", view.Nodes)
	}
}

// Neither journal nor ledger exists: honest absence, no fabricated empty chain.
func TestScanExplorationEndpoint_EmptyScanIsIncomplete(t *testing.T) {
	s := newTestServer(t, nil)
	id := "explore-empty-1"
	ledgerTestScan(t, s, id)

	rr := explorationGet(t, s, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var view explorationView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Incomplete || view.Note == "" || len(view.Nodes) != 0 {
		t.Fatalf("view = %+v, want incomplete with note and no nodes", view)
	}
}

// Damaged journal tail: projection stays authoritative below the damage and
// flags the incompleteness (same discipline as the audit endpoint).
func TestScanExplorationEndpoint_DamagedJournalIsFlagged(t *testing.T) {
	s := newTestServer(t, nil)
	id := "explore-damaged-1"
	dir := ledgerTestScan(t, s, id)
	j, err := domainjournal.Open(filepath.Join(dir, domainjournal.FileName), id)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine},
		Operation: domainjournal.OpSeedHypothesis, HypothesisID: "H-1",
		After: &domainjournal.State{Status: "queued"}, Summary: "seed H-1",
	})
	// Corrupt the tail with a truncated line (journal keeps no lock that
	// ReadRecords would honor — append is fsync-per-record, file is stable).
	f, err := os.OpenFile(filepath.Join(dir, domainjournal.FileName), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"scan_id":"` + id + `","seq":2,"at":"2`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	rr := explorationGet(t, s, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var view explorationView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.JournalPresent || !view.JournalDamaged || !view.Incomplete {
		t.Fatalf("flags = %+v", view)
	}
	if _, ok := byNodeID(view, hypNodeID("H-1")); !ok {
		t.Fatalf("pre-damage node lost: %+v", view.Nodes)
	}
}

func byNodeID(view explorationView, id string) (explorationNode, bool) {
	for _, n := range view.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return explorationNode{}, false
}

// Empty edge sets marshal as [] not null: the dashboard iterates view.edges
// unconditionally (探索链路拓扑 + 播报板流水), and an observations-only journal
// (no hypothesis/evidence structure yet) is a normal live-run state.
func TestScanExplorationEndpoint_ZeroEdgesMarshallAsEmptyArray(t *testing.T) {
	s := newTestServer(t, nil)
	id := "explore-obs-only-1"
	dir := ledgerTestScan(t, s, id)
	j, err := domainjournal.Open(filepath.Join(dir, domainjournal.FileName), id)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, j, domainjournal.Record{
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "trafficproxy"},
		Operation: domainjournal.OpObserveTraffic, Summary: "GET https://example.com/ -> 200 OK",
	})

	rr := explorationGet(t, s, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"edges":null`) {
		t.Fatalf("edges marshaled as null: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"edges":[]`) {
		t.Fatalf("edges not marshaled as empty array: %s", rr.Body.String())
	}
}

// D11: an instance is a run identity. Same-target sibling sessions (1:1
// re-dispatch 续跑) merge in the instance-form projection — the resumed run
// keeps its previous chain — with the merge disclosed in the note; exact
// record-id requests still resolve their own session only.
func TestScanExplorationEndpoint_InstanceFormMergesSameTargetSessions(t *testing.T) {
	s := newTestServer(t, nil)
	inst := "inst-redispatch-1"

	newest := "10.0.100.58_new"
	oldest := "10.0.100.58_old"
	mkScan := func(id, startedAt string) string {
		dir := filepath.Join(s.dataDir, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		s.saveScanRecordTo(&ScanRecord{
			ID:         id,
			Target:     "https://example.com",
			InstanceID: inst,
			Status:     "finished",
			StartedAt:  startedAt,
		}, dir)
		return dir
	}
	oldDir := mkScan(oldest, "2026-11-21T10:00:00Z")
	newDir := mkScan(newest, "2026-11-22T10:00:00Z")

	// 每个会话一份 ledger,标题可区分。真实续跑的 ledger 是整体拷贝(同一 id
	// 序列),这里用显式 id/dedup_key 写文件以模拟该形状——独立 store 的自增
	// 序列会彼此碰撞,不能用作跨会话 fixture。
	writeLedgerHyp := func(dir, id, dedup, title string) {
		snap := map[string]any{
			"seq":   1,
			"order": []string{id},
			"hypotheses": []map[string]any{{
				"id":        id,
				"dedup_key": dedup,
				"title":     title,
				"status":    "queued",
			}},
		}
		data, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ledger.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeLedgerHyp(oldDir, "H-1", "k-old", "old session hypothesis")
	writeLedgerHyp(newDir, "H-2", "k-new", "newest session hypothesis")

	// Instance 形态 → 合并同 target 的全部 sibling 会话 + 合并披露。
	rr := explorationGet(t, s, inst)
	if rr.Code != http.StatusOK {
		t.Fatalf("instance form code=%d body=%s", rr.Code, rr.Body.String())
	}
	var view explorationView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	foundNewest, foundOld := false, false
	for _, n := range view.Nodes {
		if n.Title == "newest session hypothesis" {
			foundNewest = true
		}
		if n.Title == "old session hypothesis" {
			foundOld = true
		}
	}
	if !foundNewest || !foundOld {
		t.Fatalf("instance form lost a sibling session's chain (newest=%v old=%v)", foundNewest, foundOld)
	}
	if view.Note == "" || !strings.Contains(view.Note, "sibling sessions") {
		t.Fatalf("merge disclosure missing: %q", view.Note)
	}

	// 记录 id 形态仍精确解析各自的会话。
	for _, tc := range []struct{ id, wantTitle, absentTitle string }{
		{oldest, "old session hypothesis", "newest session hypothesis"},
		{newest, "newest session hypothesis", "old session hypothesis"},
	} {
		rr := explorationGet(t, s, tc.id)
		if rr.Code != http.StatusOK {
			t.Fatalf("record form %s code=%d", tc.id, rr.Code)
		}
		var v explorationView
		if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		foundWant, foundAbsent := false, false
		for _, n := range v.Nodes {
			if n.Title == tc.wantTitle {
				foundWant = true
			}
			if n.Title == tc.absentTitle {
				foundAbsent = true
			}
		}
		if !foundWant || foundAbsent {
			t.Fatalf("record form %s scoped wrong (want %v, absent %v)", tc.id, foundWant, foundAbsent)
		}
	}
	_ = oldDir
	_ = newDir
}

// D11 multi-target fan-out: sibling records claiming one instance with
// DIFFERENT targets are separate chains. The instance-form projection binds
// to the newest target's chain and discloses the fan-out instead of mixing
// targets into one graph (对象映射不得反向: 一条链属于一个 target).
func TestScanExplorationEndpoint_InstanceFormDisclosesMultiTargetFanOut(t *testing.T) {
	s := newTestServer(t, nil)
	inst := "inst-multi-1"

	mkScan := func(id, target, startedAt string) string {
		dir := filepath.Join(s.dataDir, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		s.saveScanRecordTo(&ScanRecord{
			ID:         id,
			Target:     target,
			InstanceID: inst,
			Status:     "finished",
			StartedAt:  startedAt,
		}, dir)
		return dir
	}
	otherDir := mkScan("10.0.0.1_old", "https://other.example", "2026-11-21T10:00:00Z")
	newDir := mkScan("10.0.100.58_new", "https://example.com", "2026-11-22T10:00:00Z")

	writeLedgerHyp := func(dir, id, dedup, title string) {
		snap := map[string]any{
			"seq":   1,
			"order": []string{id},
			"hypotheses": []map[string]any{{
				"id":        id,
				"dedup_key": dedup,
				"title":     title,
				"status":    "queued",
			}},
		}
		data, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ledger.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeLedgerHyp(otherDir, "H-1", "k-other", "other target hypothesis")
	writeLedgerHyp(newDir, "H-1", "k-newest", "newest target hypothesis")

	rr := explorationGet(t, s, inst)
	if rr.Code != http.StatusOK {
		t.Fatalf("instance form code=%d body=%s", rr.Code, rr.Body.String())
	}
	var view explorationView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	foundNewest, foundOther := false, false
	for _, n := range view.Nodes {
		if n.Title == "newest target hypothesis" {
			foundNewest = true
		}
		if n.Title == "other target hypothesis" {
			foundOther = true
		}
	}
	if !foundNewest || foundOther {
		t.Fatalf("multi-target instance form must bind the newest target's chain only (newest=%v other=%v)", foundNewest, foundOther)
	}
	if view.Note == "" || !strings.Contains(view.Note, "further target scans") {
		t.Fatalf("multi-target disclosure missing: %q", view.Note)
	}
}

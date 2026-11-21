package web

// guidance_observer_test.go — pins the guidance consumed flip closed loop
// (REDESIGN §5.2 R2a deferred item, API_CONTRACT §3.1):
//
//	marker parsing (extractGuidanceMarkerIDs),
//	watcher flip on journal records citing gi:<id> (Summary and InfoItem text),
//	no flip without a marker / for foreign ids,
//	store failure → log + keep pending, no panic,
//	end-to-end: POST guidance (running agent) → ledger evidence citing the
//	marker → consumed + consumed_at + guidance_consumed WS event,
//	path B (queued → briefing merge) stays injected with no marker carrier.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/agent"
	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/domainjournal"
	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
)

// ── marker parsing ───────────────────────────────────────────────────────────

func TestExtractGuidanceMarkerIDs(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"no marker", "plain hypothesis summary about /admin", nil},
		{"single", "pivot per operator guidance gi:abc-123 on staging", []string{"abc-123"}},
		{"uuid", "follow gi:6ba7b810-9dad-11d1-80b4-00c04fd430c8", []string{"6ba7b810-9dad-11d1-80b4-00c04fd430c8"}},
		{"multiple in one text", "gi:id-one first, then gi:id-two", []string{"id-one", "id-two"}},
		{"duplicate ids deduped", "gi:dup and again gi:dup", []string{"dup"}},
		{"embedded word boundary", "catalog logi:8080 handling rules", nil},
		{"punctuation suffix", "(see gi:x_1.y above)", []string{"x_1.y"}},
		{"marker at end", "plan follows gi:tail", []string{"tail"}},
		{"empty text", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractGuidanceMarkerIDs(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("extractGuidanceMarkerIDs(%q) = %v, want %v", tc.text, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("extractGuidanceMarkerIDs(%q) = %v, want %v", tc.text, got, tc.want)
				}
			}
		})
	}
	// Operator guidance rendering must carry the marker exactly once, prefixed.
	msg := operatorGuidanceMessage("g-9", "check /admin first")
	if !strings.HasPrefix(msg, "[OPERATOR GUIDANCE gi:g-9]: ") || !strings.HasSuffix(msg, "check /admin first") {
		t.Fatalf("operatorGuidanceMessage = %q, want the gi:<id> carrier prefix", msg)
	}
	if got := extractGuidanceMarkerIDs(msg); len(got) != 1 || got[0] != "g-9" {
		t.Fatalf("marker lost in the rendered message: %v", got)
	}
}

// ── harness ──────────────────────────────────────────────────────────────────

// newObserverTestContext activates a real per-test scan context so the
// watcher's session-liveness prune (scanctx.Get) sees an active session.
func newObserverTestContext(t *testing.T, s *Server, instanceID string) (*scanctx.ScanContext, string) {
	t.Helper()
	sctx := scanctx.New(instanceID, s.dataDir)
	scanctx.Activate(sctx)
	t.Cleanup(func() { scanctx.Deactivate(instanceID) })
	return sctx, filepath.Join(sctx.ScanDir, domainjournal.FileName)
}

// newTestGuidanceWatcher registers a watcher directly (bypassing the POST
// handler) for unit-level scenarios.
func newTestGuidanceWatcher(t *testing.T, s *Server, instanceID, journalPath string, store platform.Repository, pending ...string) *guidanceWatcher {
	t.Helper()
	guidanceObserver.Do(func() { domainjournal.RegisterObserver(dispatchGuidanceJournalAppend) })
	w := &guidanceWatcher{
		journalPath: journalPath,
		scanCtxID:   instanceID,
		instanceID:  instanceID,
		store:       store,
		broadcast:   func(evt WSEvent) { s.broadcastToInstance(instanceID, evt) },
		pending:     make(map[string]struct{}),
		notifyCh:    make(chan uint64, guidanceWatchCapacity),
		stopCh:      make(chan struct{}),
	}
	for _, id := range pending {
		w.pending[id] = struct{}{}
	}
	guidanceWatchersMu.Lock()
	guidanceWatchers[journalPath] = w
	guidanceWatchersMu.Unlock()
	t.Cleanup(func() { w.retire("") })
	go w.drain()
	return w
}

// guidanceWatcherForPath reads the registry without mutating it.
func guidanceWatcherForPath(journalPath string) *guidanceWatcher {
	guidanceWatchersMu.Lock()
	defer guidanceWatchersMu.Unlock()
	return guidanceWatchers[journalPath]
}

// appendJournalRecord writes one audit record through a dedicated Journal
// handle on the same file (the store serializes appends itself).
func appendJournalRecord(t *testing.T, journalPath, scanID string, rec domainjournal.Record) domainjournal.Record {
	t.Helper()
	j, err := domainjournal.Open(journalPath, scanID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := j.Append(rec)
	if err != nil {
		t.Fatalf("append journal record: %v", err)
	}
	return out
}

func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within deadline: %s", what)
}

func waitForGuidanceStatus(t *testing.T, store platform.Repository, taskID, guidanceID, want string) platform.Guidance {
	t.Helper()
	var found platform.Guidance
	waitForCondition(t, "guidance "+guidanceID+" reaches "+want, func() bool {
		items, err := store.ListGuidance(context.Background(), taskID, 10)
		if err != nil {
			return false
		}
		for _, g := range items {
			if g.ID == guidanceID && g.Status == want {
				found = g
				return true
			}
		}
		return false
	})
	return found
}

// instanceEventOfType scans the instance's replay buffer for one event.
func instanceEventOfType(t *testing.T, s *Server, instanceID, eventType string) *WSEvent {
	t.Helper()
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		t.Fatalf("instance %s missing", instanceID)
	}
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	for i := range inst.events {
		if inst.events[i].Type == eventType {
			evt := inst.events[i]
			return &evt
		}
	}
	return nil
}

// flakyGuidanceStore injects UpdateGuidanceStatus failures.
type flakyGuidanceStore struct {
	platform.Repository
	mu   sync.Mutex
	fail bool
}

func (f *flakyGuidanceStore) UpdateGuidanceStatus(ctx context.Context, id, status string) (*platform.Guidance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("platform store unavailable (simulated)")
	}
	return f.Repository.UpdateGuidanceStatus(ctx, id, status)
}

// ── watcher unit behavior ────────────────────────────────────────────────────

// TestGuidanceObserverFlipsConsumedOnMarker pins: a journal record whose text
// cites gi:<pending-id> flips that row to consumed; one record can carry
// markers for several pending rows; unmarked and foreign-id records flip
// nothing.
func TestGuidanceObserverFlipsConsumedOnMarker(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "observer task"})
	if err != nil {
		t.Fatal(err)
	}
	g1, _, err := store.CreateGuidance(ctx, platform.GuidanceInput{TaskID: task.ID, Message: "first", RequestID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	g2, _, err := store.CreateGuidance(ctx, platform.GuidanceInput{TaskID: task.ID, Message: "second", RequestID: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []*platform.Guidance{g1, g2} {
		if _, err := store.UpdateGuidanceStatus(ctx, g.ID, platform.GuidanceInjected); err != nil {
			t.Fatal(err)
		}
	}

	s.instancesMu.Lock()
	s.instances["inst-obs"] = &ScanInstance{ID: "inst-obs", Status: "running"}
	s.instancesMu.Unlock()

	sctx, journalPath := newObserverTestContext(t, s, "inst-obs")
	newTestGuidanceWatcher(t, s, "inst-obs", journalPath, store, g1.ID, g2.ID)

	// 1. Unmarked record: nothing flips.
	appendJournalRecord(t, journalPath, sctx.ID, domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, Source: "test"},
		Operation:    domainjournal.OpRecordHypothesis,
		HypothesisID: "H-1",
		Summary:      "record_hypothesis sqli /admin — no marker here",
	})
	time.Sleep(100 * time.Millisecond)
	for _, g := range []*platform.Guidance{g1, g2} {
		row := waitForGuidanceStatus(t, store, task.ID, g.ID, platform.GuidanceInjected)
		if row.ConsumedAt != nil {
			t.Errorf("guidance %s flipped without its marker", g.ID)
		}
	}

	// 2. Foreign id: extracted but not pending → nothing flips.
	appendJournalRecord(t, journalPath, sctx.ID, domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, Source: "test"},
		Operation:    domainjournal.OpRecordHypothesis,
		HypothesisID: "H-1",
		Summary:      "someone else's marker gi:not-a-pending-guidance ignored",
	})
	time.Sleep(100 * time.Millisecond)
	if row := waitForGuidanceStatus(t, store, task.ID, g1.ID, platform.GuidanceInjected); row.ConsumedAt != nil {
		t.Error("foreign gi:<id> flipped a pending guidance row")
	}

	// 3. One record citing both pending markers flips both; WS events land.
	appendJournalRecord(t, journalPath, sctx.ID, domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, Source: "test"},
		Operation:    domainjournal.OpAddEvidence,
		HypothesisID: "H-1",
		EvidenceID:   "EV-1",
		Summary:      "acting on gi:" + g1.ID + " and gi:" + g2.ID + " together",
	})
	for _, g := range []*platform.Guidance{g1, g2} {
		row := waitForGuidanceStatus(t, store, task.ID, g.ID, platform.GuidanceConsumed)
		if row.ConsumedAt == nil {
			t.Errorf("guidance %s consumed without consumed_at", g.ID)
		}
	}
	waitForCondition(t, "watcher retired after pending drained", func() bool {
		return guidanceWatcherForPath(journalPath) == nil
	})
	if evt := instanceEventOfType(t, s, "inst-obs", "guidance_consumed"); evt == nil {
		t.Error("guidance_consumed WS event missing from the instance buffer")
	} else if evt.Guidance == nil || (evt.Guidance.ID != g1.ID && evt.Guidance.ID != g2.ID) {
		t.Errorf("guidance_consumed payload = %+v, want one of the flipped rows", evt.Guidance)
	}
}

// TestGuidanceObserverStoreErrorKeepsPending pins the resilience contract: a
// store failure logs, keeps the row pending, never panics, never retires the
// watcher — and a later record citing the same marker still flips the row.
func TestGuidanceObserverStoreErrorKeepsPending(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "flaky task"})
	if err != nil {
		t.Fatal(err)
	}
	g, _, err := store.CreateGuidance(ctx, platform.GuidanceInput{TaskID: task.ID, Message: "retry me", RequestID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateGuidanceStatus(ctx, g.ID, platform.GuidanceInjected); err != nil {
		t.Fatal(err)
	}
	flaky := &flakyGuidanceStore{Repository: store, fail: true}

	s.instancesMu.Lock()
	s.instances["inst-flaky"] = &ScanInstance{ID: "inst-flaky", Status: "running"}
	s.instancesMu.Unlock()

	sctx, journalPath := newObserverTestContext(t, s, "inst-flaky")
	w := newTestGuidanceWatcher(t, s, "inst-flaky", journalPath, flaky, g.ID)

	appendJournalRecord(t, journalPath, sctx.ID, domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, Source: "test"},
		Operation:    domainjournal.OpRecordHypothesis,
		HypothesisID: "H-1",
		Summary:      "attempt while store is down, cites gi:" + g.ID,
	})
	time.Sleep(150 * time.Millisecond)
	if row := waitForGuidanceStatus(t, store, task.ID, g.ID, platform.GuidanceInjected); row.ConsumedAt != nil {
		t.Error("row flipped although the store failed")
	}
	if guidanceWatcherForPath(journalPath) != w {
		t.Error("watcher must stay armed while its pending set is non-empty")
	}

	// Store recovers; the next record citing the marker flips the row.
	flaky.mu.Lock()
	flaky.fail = false
	flaky.mu.Unlock()
	appendJournalRecord(t, journalPath, sctx.ID, domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, Source: "test"},
		Operation:    domainjournal.OpRecordHypothesis,
		HypothesisID: "H-1",
		Summary:      "retry after recovery, cites gi:" + g.ID + " again",
	})
	waitForGuidanceStatus(t, store, task.ID, g.ID, platform.GuidanceConsumed)
	waitForCondition(t, "watcher retired after recovery flip", func() bool {
		return guidanceWatcherForPath(journalPath) == nil
	})
}

// ── end to end: POST guidance → real ledger evidence → consumed ──────────────

// TestGuidanceConsumedClosedLoop pins the whole path-A loop through the real
// pipelines: POST /guidance with a live running agent injects the marked
// message, the row flips to injected, and a later hypothesis/evidence
// production whose InfoItem text cites gi:<id> flips it to consumed with
// consumed_at and a guidance_consumed WS event.
func TestGuidanceConsumedClosedLoop(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "closed loop task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-loop", Mode: "single"}); err != nil {
		t.Fatal(err)
	}

	// The instance's session context id must equal the instance id (production
	// wiring: scanctx.New(sess.id, sess.scanDir)); the journal observer keys on
	// both.
	sctx := scanctx.New("inst-loop", s.dataDir)
	scanctx.Activate(sctx)
	t.Cleanup(func() { scanctx.Deactivate("inst-loop") })

	events := make(chan agent.Event, 16)
	agnt := agent.NewAgent(&config.Config{}, "XalgorixAgent", events, scopeguard.Config{}, sctx)
	s.instancesMu.Lock()
	s.instances["inst-loop"] = &ScanInstance{ID: "inst-loop", Status: "running", agent: agnt, sctx: sctx}
	s.instancesMu.Unlock()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/guidance",
		strings.NewReader(`{"message":"pivot to the staging token endpoint","request_id":"req-loop","instance_id":"inst-loop"}`))
	s.handleTaskDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST guidance = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Status     string `json:"status"`
		GuidanceID string `json:"guidance_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "injected" || out.GuidanceID == "" {
		t.Fatalf("POST outcome = %+v, want injected with a guidance id", out)
	}

	// The agent's next-turn message carries the stable gi:<id> marker.
	select {
	case evt := <-events:
		if !strings.Contains(evt.Content, "[OPERATOR GUIDANCE gi:"+out.GuidanceID+"]") {
			t.Errorf("agent message lost the marker: %q", evt.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("no agent event emitted")
	}

	journalPath := filepath.Join(s.dataDir, domainjournal.FileName)
	if guidanceWatcherForPath(journalPath) == nil {
		t.Fatal("injection did not arm the consumed watcher")
	}

	// Engine-side hypothesis production first (no marker → still injected).
	h, err := sctx.Ledger.UpsertAs(scanctx.Hypothesis{
		VulnClass: "authbypass",
		Endpoint:  "/staging/token",
	}, scanctx.AuditActorForAgent("coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if row := waitForGuidanceStatus(t, store, task.ID, out.GuidanceID, platform.GuidanceInjected); row.ConsumedAt != nil {
		t.Error("marker-free hypothesis production must not flip the row")
	}

	// Evidence production whose model-written summary cites the marker → flip.
	err = sctx.Ledger.AddEvidenceAs(h.ID, scanctx.Evidence{
		Kind:       "probe",
		Nature:     scanctx.EvidenceObserved,
		Summary:    "staging token endpoint reachable; pivoting per operator guidance gi:" + out.GuidanceID,
		Confidence: 0.7,
	}, scanctx.AuditActorForAgent("coordinator"))
	if err != nil {
		t.Fatal(err)
	}

	row := waitForGuidanceStatus(t, store, task.ID, out.GuidanceID, platform.GuidanceConsumed)
	if row.ConsumedAt == nil {
		t.Error("consumed row missing consumed_at")
	}
	waitForCondition(t, "guidance_consumed WS event", func() bool {
		return instanceEventOfType(t, s, "inst-loop", "guidance_consumed") != nil
	})
	if evt := instanceEventOfType(t, s, "inst-loop", "guidance_consumed"); evt.Guidance == nil || evt.Guidance.ID != out.GuidanceID {
		t.Errorf("guidance_consumed payload = %+v, want the consumed row", evt.Guidance)
	}
	waitForCondition(t, "watcher retired after consumption", func() bool {
		return guidanceWatcherForPath(journalPath) == nil
	})
}

// ── path B: briefing merge stays injected ────────────────────────────────────

// TestBriefingMergeGuidanceStaysInjected pins the path-B ruling: the queued →
// briefing merge has no per-row marker carrier, so merged rows keep status
// injected and the watcher registry stays untouched — no silent auto-consumed.
func TestBriefingMergeGuidanceStaysInjected(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "briefing no-flip task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-brief2", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	g, _, err := store.CreateGuidance(ctx, platform.GuidanceInput{TaskID: task.ID, Message: "fold this into the plan", RequestID: "b1"})
	if err != nil {
		t.Fatal(err)
	}

	s.instancesMu.Lock()
	s.instances["inst-brief2"] = &ScanInstance{ID: "inst-brief2", Status: "running"}
	s.instancesMu.Unlock()

	sess := &scanSession{instanceID: "inst-brief2"}
	merged := s.mergeQueuedGuidance(sess, "base instruction")

	if !strings.Contains(merged, "[OPERATOR GUIDANCE]:") || !strings.Contains(merged, g.Message) {
		t.Fatalf("merged briefing lost the guidance section: %q", merged)
	}
	if strings.Contains(merged, "gi:"+g.ID) {
		t.Error("briefing merge must not fabricate a per-row gi:<id> marker (that carrier belongs to path A only)")
	}
	time.Sleep(150 * time.Millisecond)
	row := waitForGuidanceStatus(t, store, task.ID, g.ID, platform.GuidanceInjected)
	if row.ConsumedAt != nil {
		t.Error("briefing-merged guidance must stay injected (path B ruling)")
	}
}

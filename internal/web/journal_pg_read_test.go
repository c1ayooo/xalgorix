package web

// journal_pg_read_test.go — R6.1 read-source switch: with the domainjournal
// backend registered (the PG primary store in production), the audit
// projection, the exploration projection, the cold-path sync position and
// the guidance consumed-flip all read the durable run_journal stream — and
// a scan whose journal exists only in PG is present, while a scan with
// neither source stays honestly absent/incomplete (charter §5.2).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// pgJournalBackend is the in-memory journal backend for web read tests.
type pgJournalBackend struct {
	mu      sync.Mutex
	records map[string][]domainjournal.Record
	sources []domainjournal.JournalSource
	loadErr error
}

func (f *pgJournalBackend) AppendJournalRecord(_ string, _ string, _ domainjournal.Record) error {
	return errors.New("web test backend is read-only")
}

func (f *pgJournalBackend) LoadJournalRecordsByScan(_ context.Context, scanID string) ([]domainjournal.Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return nil, false, f.loadErr
	}
	return append([]domainjournal.Record(nil), f.records[scanID]...), false, nil
}

func (f *pgJournalBackend) LoadJournalSources(_ context.Context) ([]domainjournal.JournalSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domainjournal.JournalSource(nil), f.sources...), nil
}

func (f *pgJournalBackend) setRecords(scanID string, records []domainjournal.Record, lastSeq uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[scanID] = records
	f.sources = []domainjournal.JournalSource{{ScanID: scanID, LastSeq: lastSeq}}
}

func registerPGJournalBackend(t *testing.T, b *pgJournalBackend) {
	t.Helper()
	domainjournal.SetBackend(b)
	t.Cleanup(func() { domainjournal.SetBackend(nil) })
}

func pgJournalRecord(scanID string, seq uint64, hypothesisID, summary string) domainjournal.Record {
	return domainjournal.Record{
		ScanID:       scanID,
		Seq:          seq,
		At:           time.Now().UTC(),
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "hook.ledger_seed"},
		Operation:    domainjournal.OpRecordHypothesis,
		HypothesisID: hypothesisID,
		Summary:      summary,
		After:        &domainjournal.State{Status: "queued", Confidence: 0.5},
	}
}

// With the backend registered, the audit projection reads the PG stream even
// though no journal file exists for the scan — the claim history is
// reconstructed and reported complete (write→read→project consistency).
func TestScanAuditEndpoint_ReadsPGJournal(t *testing.T) {
	s := newTestServer(t, nil)
	id := "audit-pg-1"
	auditTestScan(t, s, id) // no domain_journal.jsonl anywhere

	b := &pgJournalBackend{records: map[string][]domainjournal.Record{}}
	b.setRecords(id, []domainjournal.Record{
		pgJournalRecord(id, 1, "H-1", "recorded H-1"),
		{
			ScanID:       id,
			Seq:          2,
			At:           time.Now().UTC(),
			Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent_2", Source: "ledger_tool"},
			Operation:    domainjournal.OpClaimHypothesis,
			HypothesisID: "H-1",
			After:        &domainjournal.State{Status: "testing", AssignedTo: "agent_2"},
			Summary:      "model claims H-1",
		},
	}, 2)
	registerPGJournalBackend(t, b)

	rr := auditGet(t, s, "/api/scans/"+id+"/audit")
	if rr.Code != http.StatusOK {
		t.Fatalf("audit code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view ScanAuditView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.JournalPresent || view.JournalDamaged {
		t.Fatalf("journal flags = present:%v damaged:%v", view.JournalPresent, view.JournalDamaged)
	}
	if view.Incomplete || view.Projection == nil || view.Projection.Status != "complete" {
		t.Fatalf("projection must be complete from the PG stream, got %+v", view.Projection)
	}
	if len(view.Projection.Claims) != 1 || view.Projection.Claims[0].HypothesisID != "H-1" {
		t.Fatalf("claims = %+v", view.Projection.Claims)
	}
	if view.Projection.LastSeq != 2 {
		t.Fatalf("last seq = %d, want 2", view.Projection.LastSeq)
	}
}

// A scan whose journal exists in NEITHER source (no file, empty PG stream —
// e.g. the import has not landed) stays honestly absent/incomplete, never a
// fabricated complete empty history.
func TestScanAuditEndpoint_PGEmptyAndFileAbsentStaysIncomplete(t *testing.T) {
	s := newTestServer(t, nil)
	id := "audit-pg-absent"
	auditTestScan(t, s, id)
	registerPGJournalBackend(t, &pgJournalBackend{records: map[string][]domainjournal.Record{}})

	rr := auditGet(t, s, "/api/scans/"+id+"/audit")
	if rr.Code != http.StatusOK {
		t.Fatalf("audit code = %d", rr.Code)
	}
	var view ScanAuditView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.JournalPresent {
		t.Fatal("a scan with no file and an empty PG stream must not read as present")
	}
	if !view.Incomplete || view.Note == "" {
		t.Fatalf("absence must stay incomplete with a note: %+v", view)
	}
}

// The cold-path sync position's source side (source_seq) follows the same
// seam: with the backend registered it is the PG stream's max seq, so an
// applied watermark catches up with the durable stream, not a stale file.
func TestScanColdPathEndpoint_PGJournalSourceSeq(t *testing.T) {
	s := newTestServer(t, nil)
	id := "coldpath-pg-1"
	auditTestScan(t, s, id)

	b := &pgJournalBackend{records: map[string][]domainjournal.Record{}}
	b.setRecords(id, []domainjournal.Record{
		pgJournalRecord(id, 1, "H-1", "one"),
		pgJournalRecord(id, 2, "H-2", "two"),
	}, 2)
	registerPGJournalBackend(t, b)
	s.SetColdPathSyncer(stubSyncer{position: ColdPathPosition{AppliedSeq: 2, Status: "applied"}})

	rr := auditGet(t, s, "/api/scans/"+id+"/coldpath")
	if rr.Code != http.StatusOK {
		t.Fatalf("coldpath code = %d", rr.Code)
	}
	var state ColdPathSyncState
	if err := json.Unmarshal(rr.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.SourceSeq != 2 {
		t.Fatalf("source seq = %d, want 2 (PG stream position)", state.SourceSeq)
	}
	if state.State != "applied" {
		t.Fatalf("state = %q, want applied", state.State)
	}
}

// The exploration projection reduces the PG journal stream into the node/edge
// model when no journal file exists.
func TestScanExplorationEndpoint_ReadsPGJournal(t *testing.T) {
	s := newTestServer(t, nil)
	id := "explore-pg-1"
	auditTestScan(t, s, id)

	b := &pgJournalBackend{records: map[string][]domainjournal.Record{}}
	b.setRecords(id, []domainjournal.Record{
		pgJournalRecord(id, 1, "H-1", "recorded H-1"),
		{
			ScanID:       id,
			Seq:          2,
			At:           time.Now().UTC(),
			Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent_2", Source: "ledger_tool"},
			Operation:    domainjournal.OpClaimHypothesis,
			HypothesisID: "H-1",
			After:        &domainjournal.State{Status: "testing", AssignedTo: "agent_2"},
		},
	}, 2)
	registerPGJournalBackend(t, b)

	rr := explorationGet(t, s, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("exploration code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view explorationView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.JournalPresent || view.JournalDamaged {
		t.Fatalf("journal flags = present:%v damaged:%v", view.JournalPresent, view.JournalDamaged)
	}
	var hypNode bool
	for _, n := range view.Nodes {
		if n.Kind == "hypothesis" && n.ID == "hyp:H-1" {
			hypNode = true
		}
	}
	if !hypNode {
		t.Fatalf("PG journal stream did not reduce to a hypothesis node: %+v", view.Nodes)
	}
	if len(view.Edges) == 0 {
		t.Fatal("claim edge missing from the PG-sourced projection")
	}
}

// The guidance consumed-flip reads newly durable records through the same
// seam: a PG-only journal (no file) still flips the pending row.
func TestGuidanceWatcherFlipsConsumedFromPGJournal(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "pg observer task"})
	if err != nil {
		t.Fatal(err)
	}
	g, _, err := store.CreateGuidance(ctx, platform.GuidanceInput{TaskID: task.ID, Message: "pg flip", RequestID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateGuidanceStatus(ctx, g.ID, platform.GuidanceInjected); err != nil {
		t.Fatal(err)
	}

	s.instancesMu.Lock()
	s.instances["inst-pg"] = &ScanInstance{ID: "inst-pg", Status: "running"}
	s.instancesMu.Unlock()

	// Journal file path (the legacy would-be path) but records live in PG.
	sctx, journalPath := newObserverTestContext(t, s, "inst-pg")
	b := &pgJournalBackend{records: map[string][]domainjournal.Record{}}
	registerPGJournalBackend(t, b)
	newTestGuidanceWatcher(t, s, "inst-pg", journalPath, store, g.ID)

	b.setRecords(sctx.ID, []domainjournal.Record{
		{
			ScanID:       sctx.ID,
			Seq:          1,
			At:           time.Now().UTC(),
			Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, Source: "test"},
			Operation:    domainjournal.OpAddEvidence,
			HypothesisID: "H-1",
			EvidenceID:   "EV-1",
			Summary:      "acting on gi:" + g.ID,
		},
	}, 1)
	// The append notification the watcher would receive from the PG-backed
	// journal: same (path, scanID, seq) shape, no file written.
	dispatchGuidanceJournalAppend(journalPath, sctx.ID, 1)

	row := waitForGuidanceStatus(t, store, task.ID, g.ID, platform.GuidanceConsumed)
	if row.ConsumedAt == nil {
		t.Fatal("guidance flipped without consumed_at")
	}
	// Retirement happens after the broadcast, so waiting for it removes the
	// flip→broadcast race before asserting on the instance event buffer.
	waitForCondition(t, "watcher retired after pending drained", func() bool {
		return guidanceWatcherForPath(journalPath) == nil
	})
	if evt := instanceEventOfType(t, s, "inst-pg", "guidance_consumed"); evt == nil {
		t.Error("guidance_consumed WS event missing")
	}
}

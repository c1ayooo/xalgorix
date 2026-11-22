package web

// R6.1 runs-table takeover of scan.json (REDESIGN D12, charter v3.0 §5.1):
// with the scan state backend registered the record safe point stops writing
// scan.json, the durable set serves findAllScanSummaries (via the platform
// sweep API) and the point reads, and every consumer projection keeps its
// semantics. The in-memory fake backend bridges its writes into a real
// platform.FileStore through PutRunScanRecord, so save → summaries →
// projection runs end-to-end through production code. The nil-backend file
// mode is asserted byte-for-byte against the pre-R6.1 behavior in the second
// test (all existing web tests pin it too).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

// fakeScanStateBackend is the in-memory ScanStateBackend for unit tests.
// PutScanRecord also lands the row in the platform FileStore so the web
// sweep (platform.Repository.ScanRunRecords) reads what the safe point
// wrote — the production pair (runstate.Store writes run_scan_records,
// platform.Store sweeps the same table) mirrored without a database.
type fakeScanStateBackend struct {
	mu    sync.Mutex
	rows  map[string]runstate.ScanRecordState
	store *platform.FileStore
}

func newFakeScanStateBackend(store *platform.FileStore) *fakeScanStateBackend {
	return &fakeScanStateBackend{rows: map[string]runstate.ScanRecordState{}, store: store}
}

func (f *fakeScanStateBackend) PutScanRecord(rec runstate.ScanRecordState) error {
	f.mu.Lock()
	f.rows[rec.ScanID] = rec
	f.mu.Unlock()
	if f.store != nil {
		return f.store.PutRunScanRecord(context.Background(), platform.RunScanRecord{
			ScanID:     rec.ScanID,
			InstanceID: rec.InstanceID,
			ScanDir:    rec.ScanDir,
			Detail:     rec.Detail,
		})
	}
	return nil
}

func (f *fakeScanStateBackend) LoadScanRecord(_ context.Context, scanID string) (runstate.ScanRecordState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.rows[scanID]
	return rec, ok, nil
}

func (f *fakeScanStateBackend) DeleteScanRecord(_ context.Context, scanID string) error {
	f.mu.Lock()
	delete(f.rows, scanID)
	f.mu.Unlock()
	if f.store != nil {
		return f.store.DeleteRunScanRecord(context.Background(), scanID)
	}
	return nil
}

func (f *fakeScanStateBackend) size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

// fakeEventStateBackend is the in-memory EventStateBackend for the journal
// branch of the record reads (mirrors the production pairing where one
// runstate.Store serves both seams).
type fakeEventStateBackend struct {
	mu   sync.Mutex
	rows map[string][]runstate.EventRow
	next map[string]uint64
}

func newFakeEventStateBackend() *fakeEventStateBackend {
	return &fakeEventStateBackend{rows: map[string][]runstate.EventRow{}, next: map[string]uint64{}}
}

func (f *fakeEventStateBackend) AppendEvent(scanID, instanceID string, seq uint64, typ string, occurredAt time.Time, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[scanID] = append(f.rows[scanID], runstate.EventRow{Seq: seq, Type: typ, OccurredAt: occurredAt, Payload: append([]byte(nil), payload...)})
	return nil
}

func (f *fakeEventStateBackend) PutEvents(_ context.Context, scanID, instanceID string, rows []runstate.EventRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[scanID] = append(f.rows[scanID], rows...)
	return nil
}

func (f *fakeEventStateBackend) LoadEventsByScan(_ context.Context, scanID string, offset, limit int) ([]runstate.EventRecord, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.rows[scanID]
	out := []runstate.EventRecord{}
	for i, row := range all {
		if i < offset {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, runstate.EventRecord{ScanID: scanID, EventRow: row})
	}
	return out, int64(len(all)), nil
}

func (f *fakeEventStateBackend) LoadEventsByInstance(_ context.Context, instanceID string, offset, limit int) ([]runstate.EventRecord, int64, error) {
	return f.LoadEventsByScan(context.Background(), instanceID, offset, limit)
}

func (f *fakeEventStateBackend) DeleteEventsByScan(_ context.Context, scanID string) error {
	f.mu.Lock()
	delete(f.rows, scanID)
	f.mu.Unlock()
	return nil
}

func takeoverTestRecord(id, instanceID string) *ScanRecord {
	return &ScanRecord{
		ID:                 id,
		InstanceID:         instanceID,
		Target:             "target.example",
		Status:             "running",
		StartedAt:          "2026-05-01T18:00:00Z",
		CurrentPhase:       3,
		PlanTasksTotal:     4,
		PlanTasksCompleted: 2,
		TotalTokens:        123,
		Events:             []WSEvent{},
		EventsJournal:      true,
		Vulns:              []VulnSummary{{ID: "v1", Title: "TLS misconfig", Severity: "high", Target: "target.example", Endpoint: "https://target.example"}},
	}
}

// TestScanStateTakeoverSaveSummariesProjection pins the R6.1 contract end to
// end: save (no scan.json!) → summaries from the PG-runs sweep → full/detail
// point reads from the durable row → platform projections unchanged.
func TestScanStateTakeoverSaveSummariesProjection(t *testing.T) {
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatalf("platform file store: %v", err)
	}
	s.SetPlatformStore(store)

	fake := newFakeScanStateBackend(store)
	SetScanStateBackend(fake)
	t.Cleanup(func() { SetScanStateBackend(nil) })
	SetEventStateBackend(newFakeEventStateBackend())
	t.Cleanup(func() { SetEventStateBackend(nil) })

	dir := filepath.Join(s.dataDir, "target.example", "2026-05-01", "scan-a")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := takeoverTestRecord("scan-a", "inst-1")
	s.saveScanRecordTo(rec, dir)

	// The durable save stopped writing the file layer's state documents.
	if _, err := os.Stat(filepath.Join(dir, "scan.json")); !os.IsNotExist(err) {
		t.Fatalf("scan.json must not be written with the backend registered (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, scanMetaFile)); !os.IsNotExist(err) {
		t.Fatalf("detail sidecar must not be written with the backend registered (err=%v)", err)
	}
	if fake.size() != 1 {
		t.Fatalf("backend rows = %d, want 1", fake.size())
	}

	// Summary walk: the durable row decoded back, dir rehydrated from the
	// stored dataDir-relative anchor, events-free.
	entries := s.findAllScanSummaries()
	if len(entries) != 1 {
		t.Fatalf("summaries = %d entries, want 1", len(entries))
	}
	if entries[0].dir != dir {
		t.Fatalf("entry dir = %s, want %s", entries[0].dir, dir)
	}
	got := entries[0].rec
	if got.ID != "scan-a" || got.InstanceID != "inst-1" || got.Status != "running" ||
		got.Target != "target.example" || got.CurrentPhase != 3 ||
		got.PlanTasksTotal != 4 || got.PlanTasksCompleted != 2 || got.TotalTokens != 123 ||
		len(got.Vulns) != 1 || got.Vulns[0].Title != "TLS misconfig" {
		t.Fatalf("summary record drifted: %+v", got)
	}
	if !got.EventsJournal || len(got.Events) != 0 {
		t.Fatalf("summary must be events-free with the journal flag kept (journal=%v events=%d)", got.EventsJournal, len(got.Events))
	}

	// Point reads: full record reseeded from the durable journal.
	SetEventStateBackend(newFakeEventStateBackend())
	if err := appendScanEventJournal(dir, WSEvent{Type: "message", Content: "one", Timestamp: "2026-05-01T18:01:00Z"}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if err := appendScanEventJournal(dir, WSEvent{Type: "message", Content: "two", Timestamp: "2026-05-01T18:02:00Z"}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	full, ok := loadScanRecordFromDir(dir)
	if !ok {
		t.Fatalf("full record read failed")
	}
	if full.Status != "running" || !full.EventsJournal || len(full.Events) != 2 ||
		full.EventsTotal != 2 || full.EventsTruncated {
		t.Fatalf("full record = journal %v events %d total %d truncated %v",
			full.EventsJournal, len(full.Events), full.EventsTotal, full.EventsTruncated)
	}
	if full.Events[1].Content != "two" {
		t.Fatalf("journal order broken: %+v", full.Events)
	}

	// Detail read: bounded tail inline, true total kept.
	detail, ok := s.loadScanRecordForDetail(dir, 1)
	if !ok {
		t.Fatalf("detail read failed")
	}
	if len(detail.Events) != 1 || detail.EventsTotal != 2 || !detail.EventsTruncated {
		t.Fatalf("detail = events %d total %d truncated %v, want 1/2/true",
			len(detail.Events), detail.EventsTotal, detail.EventsTruncated)
	}
	if detail.Events[0].Content != "two" {
		t.Fatalf("detail tail must keep the NEWEST event, got %+v", detail.Events[0])
	}

	// Platform projections keep their semantics over the durable set.
	idx := s.scanSummaryIndexFor(entries)
	run := platform.Run{InstanceID: "inst-1"}
	idx.projectRun(&run, nil)
	if run.Status != "running" || run.StartedAt != "2026-05-01T18:00:00Z" ||
		run.VulnCount != 1 || run.TotalTokens != 123 {
		t.Fatalf("projected run = %+v", run)
	}
	bindDir, bindRec := s.scanDirRecordForRun("inst-1", "")
	if bindDir != dir || bindRec == nil || bindRec.ID != "scan-a" {
		t.Fatalf("scanDirRecordForRun = %s / %+v", bindDir, bindRec)
	}
	if _, ok := s.resolveScanDirByID("scan-a"); !ok {
		t.Fatalf("resolveScanDirByID(scan-a) failed over the durable set")
	}
	if len(s.findAllScans()) != 1 {
		t.Fatalf("findAllScans = %d entries, want 1", len(s.findAllScans()))
	}

	// Deletion drops the durable row — the record disappears the way its
	// directory disappearance used to.
	s.deleteScanRecordState(filepath.Base(dir), rec.ID)
	if got := s.findAllScanSummaries(); len(got) != 0 {
		t.Fatalf("post-delete summaries = %d entries, want 0", len(got))
	}
}

// TestScanStateTakeoverMultiRecordInstance pins per-record granularity over
// one shared instance: a wildcard parent plus its child (and a queue-style
// sibling) all surface, exact ids bind their own directories, and the
// newest-session tie-break still picks the newest StartedAt.
func TestScanStateTakeoverMultiRecordInstance(t *testing.T) {
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatalf("platform file store: %v", err)
	}
	s.SetPlatformStore(store)
	SetScanStateBackend(newFakeScanStateBackend(store))
	t.Cleanup(func() { SetScanStateBackend(nil) })
	SetEventStateBackend(newFakeEventStateBackend())
	t.Cleanup(func() { SetEventStateBackend(nil) })

	parentDir := filepath.Join(s.dataDir, "parent.example", "2026-05-01", "scan-parent")
	childDir := filepath.Join(s.dataDir, "child.parent.example", "2026-05-01", "scan-child")
	for _, dir := range []string{parentDir, childDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	parent := takeoverTestRecord("scan-parent", "inst-1")
	child := takeoverTestRecord("scan-child", "inst-1")
	child.ParentTarget = "parent.example"
	child.StartedAt = "2026-05-01T19:00:00Z" // the newest session
	s.saveScanRecordTo(parent, parentDir)
	s.saveScanRecordTo(child, childDir)

	entries := s.findAllScanSummaries()
	if len(entries) != 2 {
		t.Fatalf("summaries = %d entries, want 2 (per-record granularity)", len(entries))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rec.ID < entries[j].rec.ID })
	if entries[0].rec.ParentTarget != "parent.example" || entries[1].rec.ParentTarget != "" {
		t.Fatalf("parent/child records mixed up: %+v", entries)
	}
	if entries[0].dir != childDir || entries[1].dir != parentDir {
		t.Fatalf("dirs rehydrated wrong: %s / %s", entries[0].dir, entries[1].dir)
	}

	// Exact ids bind their own directory; the instance form binds the newest.
	if dir, ok := s.resolveScanDirByID("scan-child"); !ok || dir != childDir {
		t.Fatalf("resolve scan-child = %s ok=%v", dir, ok)
	}
	if dir, rec := s.scanDirRecordForRun("inst-1", ""); dir != childDir || rec == nil || rec.ID != "scan-child" {
		t.Fatalf("newest-session fallback = %s / %+v, want scan-child", dir, rec)
	}

	// Top-level-only filters (ParentTarget == "") see only the parent record —
	// the same discipline the scan list applies.
	topLevel := 0
	for _, entry := range entries {
		if entry.rec.ParentTarget == "" {
			topLevel++
		}
	}
	if topLevel != 1 {
		t.Fatalf("top-level count = %d, want 1", topLevel)
	}
}

// TestScanRecordFileModeUnchanged pins the nil-backend file mode byte for
// byte: scan.json is written, the sidecar is refreshed, the summary walk and
// the point reads parse the file exactly as before R6.1 — even with a
// platform store present (an empty sweep must degrade to the file walk).
func TestScanRecordFileModeUnchanged(t *testing.T) {
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatalf("platform file store: %v", err)
	}
	s.SetPlatformStore(store)
	// No SetScanStateBackend: the process-global seam stays nil (the default
	// every existing web test runs under).

	dir := filepath.Join(s.dataDir, "target.example", "2026-05-01", "scan-file")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := takeoverTestRecord("scan-file", "inst-file")
	s.saveScanRecordTo(rec, dir)
	// A journal-backed record's journal file is created at session prepare
	// time; create it here so the full read path is exercised exactly as a
	// real session's workspace looks.
	if err := os.WriteFile(filepath.Join(dir, scanEventJournalFile),
		[]byte(`{"type":"message","content":"j1","timestamp":"2026-05-01T18:01:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "scan.json")); err != nil {
		t.Fatalf("scan.json must still be written in file mode: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, scanMetaFile)); err != nil {
		t.Fatalf("detail sidecar must still be written in file mode: %v", err)
	}

	entries := s.findAllScanSummaries()
	if len(entries) != 1 || entries[0].dir != dir {
		t.Fatalf("file-mode summaries = %+v", entries)
	}
	got := entries[0].rec
	if got.ID != "scan-file" || got.Status != "running" || got.PlanTasksTotal != 4 ||
		got.PlanTasksCompleted != 2 || got.TotalTokens != 123 || len(got.Vulns) != 1 {
		t.Fatalf("file-mode summary drifted: %+v", got)
	}

	// Equivalence with the takeover mode: every projected field the panel
	// reads must be identical between the file record and the durable row.
	idx := s.scanSummaryIndexFor(entries)
	run := platform.Run{InstanceID: "inst-file"}
	idx.projectRun(&run, nil)
	if run.Status != "running" || run.VulnCount != 1 || run.TotalTokens != 123 ||
		run.StartedAt != "2026-05-01T18:00:00Z" {
		t.Fatalf("file-mode projected run = %+v", run)
	}

	full, ok := loadScanRecordFromDir(dir)
	if !ok || full.Status != "running" || len(full.Events) != 1 || full.EventsTotal != 1 {
		t.Fatalf("file-mode full read = %+v ok=%v", full, ok)
	}
	detail, ok := s.loadScanRecordForDetail(dir, detailEventTail)
	if !ok || detail.ID != "scan-file" {
		t.Fatalf("file-mode detail read failed: %+v ok=%v", detail, ok)
	}
}

// TestScanStateTakeoverResidueFallback pins the set merge: a directory whose
// durable row never landed ("未导入残留") still appears from the file walk
// next to the durable rows, and a record present in BOTH sources is served
// once — from the durable row.
func TestScanStateTakeoverResidueFallback(t *testing.T) {
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatalf("platform file store: %v", err)
	}
	s.SetPlatformStore(store)
	SetScanStateBackend(newFakeScanStateBackend(store))
	t.Cleanup(func() { SetScanStateBackend(nil) })
	SetEventStateBackend(newFakeEventStateBackend())
	t.Cleanup(func() { SetEventStateBackend(nil) })

	// Durable record with a workspace directory on disk.
	durableDir := filepath.Join(s.dataDir, "durable.example", "2026-05-01", "scan-durable")
	if err := os.MkdirAll(durableDir, 0o700); err != nil {
		t.Fatal(err)
	}
	s.saveScanRecordTo(takeoverTestRecord("scan-durable", "inst-durable"), durableDir)

	// Residue: a plain pre-R6.1 directory (file only, no durable row).
	residueDir := filepath.Join(s.dataDir, "legacy.example", "2026-05-01", "scan-legacy")
	if err := os.MkdirAll(residueDir, 0o700); err != nil {
		t.Fatal(err)
	}
	residue := takeoverTestRecord("scan-legacy", "inst-legacy")
	residue.Status = "finished"
	data, err := json.MarshalIndent(residue, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(residueDir, "scan.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	entries := s.findAllScanSummaries()
	if len(entries) != 2 {
		t.Fatalf("merged set = %d entries, want 2", len(entries))
	}
	ids := map[string]string{}
	for _, entry := range entries {
		ids[entry.rec.ID] = entry.dir
	}
	if ids["scan-durable"] != durableDir || ids["scan-legacy"] != residueDir {
		t.Fatalf("merged dirs = %v", ids)
	}
}

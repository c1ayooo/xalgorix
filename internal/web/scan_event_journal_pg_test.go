package web

// R6.1 events slice unit tests: the durable-mode journal read/write paths
// against an in-memory fake backend. The nil-backend file behavior stays
// pinned by TestEventJournalMigrationPaginationAndInterruptedAppend and the
// redispatch projections tests — these tests cover the switch itself: write
// routing, seq allocation, window semantics over the durable union, damaged
// row skipping, and the legacy-file fallback order.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

// fakeEventState is an in-memory EventStateBackend: the write shape of
// run_events without PostgreSQL. LoadEventsByInstance mirrors the store's
// union ordering (sessions by their earliest event time, scan id, then seq)
// so the sibling tests exercise the same semantics as production.
type fakeEventState struct {
	mu         sync.Mutex
	rows       map[string][]runstate.EventRow // scan_id → seq-ordered rows
	instanceOf map[string]string              // scan_id → instance_id
	deleted    []string
	appendErr  error // when set, every append fails with it
}

func newFakeEventState() *fakeEventState {
	return &fakeEventState{
		rows:       map[string][]runstate.EventRow{},
		instanceOf: map[string]string{},
	}
}

func registerFakeEventState(t *testing.T) *fakeEventState {
	t.Helper()
	fake := newFakeEventState()
	SetEventStateBackend(fake)
	t.Cleanup(func() { SetEventStateBackend(nil) })
	return fake
}

// seed lands rows for one session directly (the importer's shape).
func (f *fakeEventState) seed(scanID, instanceID string, rows ...runstate.EventRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[scanID] = append(f.rows[scanID], rows...)
	sort.Slice(f.rows[scanID], func(i, j int) bool { return f.rows[scanID][i].Seq < f.rows[scanID][j].Seq })
	f.instanceOf[scanID] = instanceID
}

func (f *fakeEventState) scanRows(scanID string) []runstate.EventRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runstate.EventRow(nil), f.rows[scanID]...)
}

func (f *fakeEventState) upsert(scanID, instanceID string, row runstate.EventRow) {
	replaced := false
	for i := range f.rows[scanID] {
		if f.rows[scanID][i].Seq == row.Seq {
			f.rows[scanID][i] = row
			replaced = true
			break
		}
	}
	if !replaced {
		f.rows[scanID] = append(f.rows[scanID], row)
		sort.Slice(f.rows[scanID], func(i, j int) bool { return f.rows[scanID][i].Seq < f.rows[scanID][j].Seq })
	}
	f.instanceOf[scanID] = instanceID
}

func (f *fakeEventState) AppendEvent(scanID, instanceID string, seq uint64, typ string, occurredAt time.Time, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	f.upsert(scanID, instanceID, runstate.EventRow{Seq: seq, Type: typ, OccurredAt: occurredAt, Payload: payload})
	return nil
}

func (f *fakeEventState) PutEvents(_ context.Context, scanID, instanceID string, rows []runstate.EventRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	for _, row := range rows {
		f.upsert(scanID, instanceID, row)
	}
	return nil
}

func (f *fakeEventState) DeleteEventsByScan(_ context.Context, scanID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, scanID)
	f.deleted = append(f.deleted, scanID)
	return nil
}

func (f *fakeEventState) LoadEventsByScan(_ context.Context, scanID string, offset, limit int) ([]runstate.EventRecord, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := append([]runstate.EventRow(nil), f.rows[scanID]...)
	total := int64(len(rows))
	return windowEventRows(rows, offset, limit), total, nil
}

func (f *fakeEventState) LoadEventsByInstance(_ context.Context, instanceID string, offset, limit int) ([]runstate.EventRecord, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Collect the instance's sessions and order them like the store does:
	// sessions by their earliest event time (ties by scan id), seq within.
	type session struct {
		scanID string
		start  time.Time
	}
	var sessions []session
	byScan := map[string][]runstate.EventRow{}
	for scanID, rows := range f.rows {
		if f.instanceOf[scanID] != instanceID || len(rows) == 0 {
			continue
		}
		start := rows[0].OccurredAt
		for _, r := range rows[1:] {
			if r.OccurredAt.Before(start) {
				start = r.OccurredAt
			}
		}
		sessions = append(sessions, session{scanID: scanID, start: start})
		byScan[scanID] = rows
	}
	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].start.Equal(sessions[j].start) {
			return sessions[i].start.Before(sessions[j].start)
		}
		return sessions[i].scanID < sessions[j].scanID
	})
	var union []runstate.EventRow
	for _, s := range sessions {
		union = append(union, byScan[s.scanID]...)
	}
	total := int64(len(union))
	return windowEventRows(union, offset, limit), total, nil
}

// windowEventRows applies the file-era window semantics: a negative offset
// clamps to 0; limit <= 0 is unbounded.
func windowEventRows(rows []runstate.EventRow, offset, limit int) []runstate.EventRecord {
	if offset < 0 {
		offset = 0
	}
	out := []runstate.EventRecord(nil)
	for i, row := range rows {
		if i < offset {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, runstate.EventRecord{ScanID: "", EventRow: row})
	}
	return out
}

func fakeRow(ev WSEvent, at time.Time) runstate.EventRow {
	payload, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}
	return runstate.EventRow{Seq: 0, Type: ev.Type, OccurredAt: at, Payload: payload}
}

func fakeEvent(content string) WSEvent {
	return WSEvent{Type: "message", Content: content}
}

func withBackend(t *testing.T) *fakeEventState {
	t.Helper()
	return registerFakeEventState(t)
}

func TestAppendScanEventJournalWritesRunEvents(t *testing.T) {
	fake := withBackend(t)
	dir := t.TempDir()
	scanID := filepath.Base(dir)

	at := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	if err := appendScanEventJournal(dir, WSEvent{EventID: scanID + ":5", Type: "message", Content: "first", Timestamp: at.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	// The session's numbered stream continues without an EventID label.
	if err := appendScanEventJournal(dir, WSEvent{Type: "tool_result", Output: "ok", Timestamp: at.Add(time.Second).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}

	rows := fake.scanRows(scanID)
	if len(rows) != 2 || rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("durable seq allocation wrong: %+v", rows)
	}
	if rows[0].Type != "message" || rows[1].Type != "tool_result" {
		t.Fatalf("row types wrong: %+v", rows)
	}
	var decoded WSEvent
	if err := json.Unmarshal(rows[0].Payload, &decoded); err != nil || decoded.Content != "first" {
		t.Fatalf("payload round-trip broken: %v %s", err, rows[0].Payload)
	}
	if _, statErr := os.Stat(filepath.Join(dir, scanEventJournalFile)); !os.IsNotExist(statErr) {
		t.Fatal("backend mode must not write the legacy events.jsonl file")
	}
}

func TestPrepareSeedsTailAndTotalFromDurableRows(t *testing.T) {
	fake := withBackend(t)
	dir := t.TempDir()
	scanID := filepath.Base(dir)

	start := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	rows := make([]runstate.EventRow, 0, 5)
	for i := 0; i < 5; i++ {
		row := fakeRow(fakeEvent(fmt.Sprintf("event-%d", i)), start.Add(time.Duration(i)*time.Second))
		row.Seq = uint64(i + 1)
		rows = append(rows, row)
	}
	fake.seed(scanID, "run-9", rows...)

	rec := &ScanRecord{ID: scanID, InstanceID: "run-9", EventsJournal: true}
	if err := prepareScanEventJournal(dir, rec, false); err != nil {
		t.Fatal(err)
	}
	if rec.EventsTotal != 5 || rec.EventsTruncated != false || len(rec.Events) != 5 {
		t.Fatalf("prepare did not seed the record from run_events: total=%d len=%d", rec.EventsTotal, len(rec.Events))
	}
	if rec.Events[0].Content != "event-0" || rec.Events[4].Content != "event-4" {
		t.Fatalf("tail order wrong: %q..%q", rec.Events[0].Content, rec.Events[4].Content)
	}

	// The primed allocator continues the durable stream, stamping the
	// instance the row aggregation is keyed on.
	if err := appendScanEventJournal(dir, WSEvent{Type: "message", Content: "next"}); err != nil {
		t.Fatal(err)
	}
	appended := fake.scanRows(scanID)
	if len(appended) != 6 || appended[5].Seq != 6 || fake.instanceOf[scanID] != "run-9" {
		t.Fatalf("append after prepare wrong: seq=%v instance=%q", appended[len(appended)-1].Seq, fake.instanceOf[scanID])
	}
}

func TestPrepareResetDeletesDurableRows(t *testing.T) {
	fake := withBackend(t)
	dir := t.TempDir()
	scanID := filepath.Base(dir)

	start := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	r1, r2 := fakeRow(fakeEvent("old-1"), start), fakeRow(fakeEvent("old-2"), start.Add(time.Second))
	r1.Seq, r2.Seq = 1, 2
	fake.seed(scanID, "run-1", r1, r2)

	rec := &ScanRecord{ID: scanID, InstanceID: "run-1", EventsJournal: true}
	if err := prepareScanEventJournal(dir, rec, true); err != nil {
		t.Fatal(err)
	}
	if len(fake.scanRows(scanID)) != 0 {
		t.Fatal("reset must delete the session's durable rows")
	}
	if rec.EventsTotal != 0 || len(rec.Events) != 0 || !rec.EventsJournal {
		t.Fatalf("reset record state wrong: total=%d len=%d journal=%v", rec.EventsTotal, len(rec.Events), rec.EventsJournal)
	}
	// The re-run starts the journal at seq 1 again.
	if err := appendScanEventJournal(dir, WSEvent{Type: "message", Content: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if rows := fake.scanRows(scanID); len(rows) != 1 || rows[0].Seq != 1 {
		t.Fatalf("post-reset append seq = %v, want 1", rows[0].Seq)
	}
}

func TestPrepareImportsLegacyFileJournalOnce(t *testing.T) {
	fake := withBackend(t)
	dir := t.TempDir()
	scanID := filepath.Base(dir)

	lines := []string{
		`{"type":"message","content":"legacy-1","timestamp":"2026-11-23T08:00:00Z"}`,
		`{"type":"message","content":"legacy-2","timestamp":"2026-11-23T08:00:01Z"}`,
		`{"type":"unfinished`, // torn final append: skipped, never imported
	}
	if err := os.WriteFile(filepath.Join(dir, scanEventJournalFile), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := &ScanRecord{ID: scanID, InstanceID: "run-2", EventsJournal: false}
	if err := prepareScanEventJournal(dir, rec, false); err != nil {
		t.Fatal(err)
	}
	rows := fake.scanRows(scanID)
	if len(rows) != 2 || rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("legacy import wrong: %+v", rows)
	}
	if rec.EventsTotal != 2 || !rec.EventsJournal || rec.EventsTruncated {
		t.Fatalf("record after import wrong: total=%d journal=%v", rec.EventsTotal, rec.EventsJournal)
	}
	// The directory is never modified in backend mode (the torn line stays).
	data, err := os.ReadFile(filepath.Join(dir, scanEventJournalFile))
	if err != nil || strings.Count(string(data), "\n") != 2 {
		t.Fatalf("legacy file was modified by the import: %v %q", err, data)
	}
	// Writes continue the imported stream instead of colliding with it.
	if err := appendScanEventJournal(dir, WSEvent{Type: "message", Content: "continued"}); err != nil {
		t.Fatal(err)
	}
	if rows := fake.scanRows(scanID); len(rows) != 3 || rows[2].Seq != 3 {
		t.Fatalf("post-import append seq = %+v", rows)
	}
}

func TestWalkEventJournalSkipsDamagedDurableRows(t *testing.T) {
	fake := withBackend(t)
	dir := t.TempDir()
	scanID := filepath.Base(dir)

	start := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	r1, r3 := fakeRow(fakeEvent("one"), start), fakeRow(fakeEvent("three"), start.Add(2*time.Second))
	r1.Seq, r3.Seq = 1, 3
	damaged := runstate.EventRow{Seq: 2, Type: "message", OccurredAt: start.Add(time.Second), Payload: []byte(`{"type":"mess`)}
	fake.seed(scanID, "run-1", r1, damaged, r3)

	var contents []string
	total, _, err := walkEventJournal(dir, func(_ int, ev WSEvent) { contents = append(contents, ev.Content) })
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if total != 2 || strings.Join(contents, ",") != "one,three" {
		t.Fatalf("damaged row not skipped: total=%d events=%v", total, contents)
	}
}

func TestLoadScanEventsWindowDurableSemantics(t *testing.T) {
	fake := withBackend(t)
	dir := t.TempDir()
	scanID := filepath.Base(dir)

	start := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	rows := make([]runstate.EventRow, 0, 25)
	for i := 0; i < 25; i++ {
		row := fakeRow(fakeEvent(fmt.Sprintf("event-%d", i)), start.Add(time.Duration(i)*time.Second))
		row.Seq = uint64(i + 1)
		rows = append(rows, row)
	}
	fake.seed(scanID, "run-1", rows...)

	window, total, ok := loadScanEventsWindow(dir, 10, 10)
	if !ok || total != 25 || len(window) != 10 || window[0].Content != "event-10" || window[9].Content != "event-19" {
		t.Fatalf("durable window semantics wrong: ok=%t total=%d len=%d", ok, total, len(window))
	}
	if tail, total, ok := loadScanEventsWindow(dir, 100, 10); !ok || total != 25 || len(tail) != 0 {
		t.Fatalf("window past the end wrong: ok=%t total=%d len=%d", ok, total, len(tail))
	}

	// A damaged row inside the page is skipped; the total counts every row.
	damaged := runstate.EventRow{Seq: 11, Type: "message", OccurredAt: start.Add(10 * time.Second), Payload: []byte("{bad")}
	fake.seed(scanID, "run-1", damaged)
	page, total, ok := loadScanEventsWindow(dir, 10, 3)
	if !ok || total != 26 || len(page) != 2 {
		t.Fatalf("damaged row inside the window wrong: ok=%t total=%d len=%d", ok, total, len(page))
	}

	// A session with no durable rows falls back to its legacy file (the
	// pre-R6 shape: journal-backed record + events.jsonl on disk).
	legacy := writeSiblingDirForJournal(t, t.TempDir(), "legacy", "scan-legacy", "run-legacy")
	lines := []string{
		`{"type":"message","content":"file-1","timestamp":"2026-11-23T08:00:00Z"}`,
		`{"type":"message","content":"file-2","timestamp":"2026-11-23T08:00:01Z"}`,
		"", // the trailing newline every completed append writes
	}
	if err := os.WriteFile(filepath.Join(legacy, scanEventJournalFile), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := `{"id":"scan-legacy","instance_id":"run-legacy","events_journal":true}`
	if err := os.WriteFile(filepath.Join(legacy, "scan.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	if evs, total, ok := loadScanEventsWindow(legacy, 0, 10); !ok || total != 2 || len(evs) != 2 || evs[1].Content != "file-2" {
		t.Fatalf("legacy fallback wrong: ok=%t total=%d", ok, total)
	}

	// And a clean durable miss with no files reads as an empty journal.
	empty := t.TempDir()
	if evs, total, ok := loadScanEventsWindow(empty, 0, 10); !ok || total != 0 || evs == nil {
		t.Fatalf("empty durable journal wrong: ok=%t total=%d events=%v", ok, total, evs)
	}
}

func writeSiblingDirForJournal(t *testing.T, parent, name, scanID, instanceID string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := fmt.Sprintf(`{"id":%q,"instance_id":%q,"status":"finished"}`, scanID, instanceID)
	if err := os.WriteFile(filepath.Join(dir, "scan.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadRunEventsWindowAggregatesSiblingSessions(t *testing.T) {
	fake := withBackend(t)
	parent := t.TempDir()
	instance := "run-siblings"
	oldStart := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	newStart := oldStart.Add(time.Hour)

	dirOld := writeSiblingDirForJournal(t, parent, "scan-old", "scan-old", instance)
	dirNew := writeSiblingDirForJournal(t, parent, "scan-new", "scan-new", instance)

	oldRows := []runstate.EventRow{
		withSeq(fakeRow(fakeEvent("old-1"), oldStart), 1),
		withSeq(fakeRow(fakeEvent("old-2"), oldStart.Add(time.Second)), 2),
	}
	newRows := []runstate.EventRow{
		withSeq(fakeRow(fakeEvent("new-1"), newStart), 1),
		withSeq(fakeRow(fakeEvent("new-2"), newStart.Add(time.Second)), 2),
		withSeq(fakeRow(fakeEvent("new-3"), newStart.Add(2*time.Second)), 3),
	}
	fake.seed("scan-old", instance, oldRows...)
	fake.seed("scan-new", instance, newRows...)

	// Oldest session first, seq order within a session; offset/limit index
	// into the union.
	window, total, ok := loadRunEventsWindow([]string{dirOld, dirNew}, 0, 10)
	if !ok || total != 5 || len(window) != 5 {
		t.Fatalf("union wrong: ok=%t total=%d len=%d", ok, total, len(window))
	}
	got := make([]string, 0, len(window))
	for _, ev := range window {
		got = append(got, ev.Content)
	}
	if strings.Join(got, ",") != "old-1,old-2,new-1,new-2,new-3" {
		t.Fatalf("union order wrong: %v", got)
	}

	if page, total, ok := loadRunEventsWindow([]string{dirOld, dirNew}, 1, 2); !ok || total != 5 || len(page) != 2 || page[0].Content != "old-2" || page[1].Content != "new-1" {
		t.Fatalf("union paging wrong: ok=%t total=%d %+v", ok, total, page)
	}
	if page, total, ok := loadRunEventsWindow([]string{dirOld, dirNew}, 4, 5); !ok || total != 5 || len(page) != 1 || page[0].Content != "new-3" {
		t.Fatalf("union tail wrong: ok=%t total=%d %+v", ok, total, page)
	}

	// A damaged row is skipped inside the union without shifting the totals.
	damaged := runstate.EventRow{Seq: 4, Type: "message", OccurredAt: newStart.Add(3 * time.Second), Payload: []byte("{bad")}
	fake.seed("scan-new", instance, damaged)
	window, total, ok = loadRunEventsWindow([]string{dirOld, dirNew}, 0, 10)
	if !ok || total != 6 || len(window) != 5 {
		t.Fatalf("damaged row in the union wrong: ok=%t total=%d len=%d", ok, total, len(window))
	}

	// With the durable rows present, the union must not double-count the
	// legacy files in the same directories.
	if err := os.WriteFile(filepath.Join(dirOld, scanEventJournalFile), []byte(`{"type":"message","content":"stale-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, total, ok := loadRunEventsWindow([]string{dirOld, dirNew}, 0, 10); !ok || total != 6 {
		t.Fatalf("durable rows and files both counted: total=%d", total)
	}
}

func TestLoadRunEventsWindowFallsBackToLegacyFiles(t *testing.T) {
	withBackend(t)
	parent := t.TempDir()
	dirA := writeSiblingDirForJournal(t, parent, "a", "scan-a", "run-legacy")
	dirB := writeSiblingDirForJournal(t, parent, "b", "scan-b", "run-legacy")
	for dir, first := range map[string]string{dirA: "file-a", dirB: "file-b"} {
		// A complete journal line always ends with the newline the append
		// path writes; a line without one reads as a torn append.
		lines := fmt.Sprintf(`{"type":"message","content":%q,"timestamp":"2026-11-23T08:00:00Z"}`+"\n", first)
		if err := os.WriteFile(filepath.Join(dir, scanEventJournalFile), []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	window, total, ok := loadRunEventsWindow([]string{dirA, dirB}, 0, 10)
	if !ok || total != 2 || len(window) != 2 || window[0].Content != "file-a" || window[1].Content != "file-b" {
		t.Fatalf("legacy sibling fallback wrong: ok=%t total=%d %+v", ok, total, window)
	}
}

func TestAppendFailureReturnsErrorAndKeepsPosition(t *testing.T) {
	fake := withBackend(t)
	dir := t.TempDir()
	scanID := filepath.Base(dir)
	fake.appendErr = fmt.Errorf("postgres down")

	err := appendScanEventJournal(dir, WSEvent{Type: "message", Content: "doomed"})
	if err == nil {
		t.Fatal("persist failure must return an error to the caller (charter §5.2)")
	}
	// The stream position was spent on the failed seq: the next successful
	// append leaves an honest gap instead of rewriting history.
	fake.appendErr = nil
	if err := appendScanEventJournal(dir, WSEvent{Type: "message", Content: "after failure"}); err != nil {
		t.Fatal(err)
	}
	rows := fake.scanRows(scanID)
	if len(rows) != 1 || rows[0].Seq != 2 {
		t.Fatalf("post-failure append seq = %+v, want seq 2 (gap, no rewrite)", rows)
	}
}

func withSeq(row runstate.EventRow, seq uint64) runstate.EventRow {
	row.Seq = seq
	return row
}

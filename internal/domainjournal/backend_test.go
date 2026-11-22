package domainjournal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeBackend is the in-memory backend for the seam's unit tests.
type fakeBackend struct {
	mu          sync.Mutex
	records     map[string][]Record
	stamps      map[string]string // scan id → instance id last appended with
	appendErr   error
	loadErr     error
	sourcesErr  error
	sources     []JournalSource
	appendCalls int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		records: make(map[string][]Record),
		stamps:  make(map[string]string),
	}
}

func (f *fakeBackend) AppendJournalRecord(scanID, instanceID string, rec Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendCalls++
	if f.appendErr != nil {
		return f.appendErr
	}
	f.records[scanID] = append(f.records[scanID], rec)
	f.stamps[scanID] = instanceID
	return nil
}

func (f *fakeBackend) LoadJournalRecordsByScan(context.Context, string) ([]Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil, false, f.loadErr
}

func (f *fakeBackend) LoadJournalSources(context.Context) ([]JournalSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sourcesErr != nil {
		return nil, f.sourcesErr
	}
	return append([]JournalSource(nil), f.sources...), nil
}

// setRecords installs the read side (LoadJournalRecordsByScan above returns
// nothing so failure postures stay injectable; readers that must see rows
// use this backed map variant).
func (f *fakeBackend) setRecords(scanID string, records []Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[scanID] = records
}

// readingBackend returns the records a scan appended (or installed).
func (f *fakeBackend) scanRecords(scanID string) []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Record(nil), f.records[scanID]...)
}

// registerTestBackend registers b process-wide and restores nil on cleanup,
// so the package's file-mode tests are never affected.
func registerTestBackend(t *testing.T, b Backend) {
	t.Helper()
	SetBackend(b)
	t.Cleanup(func() { SetBackend(nil) })
}

// readLoadBackend is a fakeBackend whose LoadJournalRecordsByScan serves the
// records map (the plain fake's read path stays nil so loadErr can be
// injected independently).
type readLoadBackend struct {
	fakeBackend
}

func (f *readLoadBackend) LoadJournalRecordsByScan(_ context.Context, scanID string) ([]Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return nil, false, f.loadErr
	}
	return append([]Record(nil), f.records[scanID]...), false, nil
}

func TestAppendWithBackendSkipsFileAndNotifiesObserver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	b := newFakeBackend()
	registerTestBackend(t, b)

	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	type event struct {
		path, scanID string
		seq          uint64
	}
	ch := make(chan event, 1)
	cancel := RegisterObserver(func(p, scanID string, seq uint64) {
		ch <- event{path: p, scanID: scanID, seq: seq}
	})
	defer cancel()

	rec, err := j.Append(testRecord("scan-1"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Seq != 1 {
		t.Fatalf("seq = %d, want 1", rec.Seq)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backend mode must not write the journal file, stat err = %v", err)
	}
	stored := b.scanRecords("scan-1")
	if len(stored) != 1 || stored[0].Seq != 1 || stored[0].Operation != OpRecordHypothesis {
		t.Fatalf("backend records = %+v", stored)
	}
	if j.LastSeq() != 1 {
		t.Fatalf("last seq = %d, want 1", j.LastSeq())
	}
	select {
	case got := <-ch:
		if got.path != path || got.scanID != "scan-1" || got.seq != 1 {
			t.Fatalf("observer event = %+v", got)
		}
	default:
		t.Fatal("observer was not notified in backend mode")
	}
}

func TestAppendStampsInstanceOnBackendRows(t *testing.T) {
	dir := t.TempDir()
	b := newFakeBackend()
	registerTestBackend(t, b)

	j, err := Open(filepath.Join(dir, FileName), "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	j.SetInstanceID("inst-42")
	if _, err := j.Append(testRecord("scan-1")); err != nil {
		t.Fatal(err)
	}
	if got := b.stamps["scan-1"]; got != "inst-42" {
		t.Fatalf("instance stamp = %q, want inst-42", got)
	}
}

func TestAppendBackendFailureLeavesSeqReusableAndWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	b := newFakeBackend()
	b.appendErr = errors.New("pg unavailable")
	registerTestBackend(t, b)

	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(testRecord("scan-1")); err == nil {
		t.Fatal("expected append failure to be returned (persist-failure is not success)")
	}
	if j.LastSeq() != 0 {
		t.Fatalf("last seq after failure = %d, want 0 (sequence stays reusable)", j.LastSeq())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed append must not fall back to the file, stat err = %v", err)
	}

	// Recovery: the next append reuses the sequence (idempotent key).
	b.appendErr = nil
	rec, err := j.Append(testRecord("scan-1"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Seq != 1 {
		t.Fatalf("seq after recovery = %d, want 1", rec.Seq)
	}
}

func TestOpenSeedsDurableSeqFromBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	b := newFakeBackend()
	b.sources = []JournalSource{{ScanID: "scan-1", LastSeq: 7}}
	registerTestBackend(t, b)

	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if j.LastSeq() != 7 {
		t.Fatalf("last seq = %d, want 7 (durable seed)", j.LastSeq())
	}
	rec, err := j.Append(testRecord("scan-1"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Seq != 8 {
		t.Fatalf("seq = %d, want 8 (continues after durable history)", rec.Seq)
	}
}

func TestOpenBackendSeedErrorDisablesJournal(t *testing.T) {
	dir := t.TempDir()
	b := newFakeBackend()
	b.sourcesErr = errors.New("pg unavailable")
	registerTestBackend(t, b)

	if _, err := Open(filepath.Join(dir, FileName), "scan-1"); err == nil {
		t.Fatal("open must fail when the durable seq cannot be seeded (a wrong first seq would overwrite durable history)")
	}
}

func TestOpenBackendModeIgnoresDamagedLegacyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newFakeBackend()
	b.sources = []JournalSource{{ScanID: "scan-1", LastSeq: 3}}
	registerTestBackend(t, b)

	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if j.LastSeq() != 3 {
		t.Fatalf("last seq = %d, want 3 (durable seed, legacy damage ignored)", j.LastSeq())
	}
}

func TestReadRecordsResolvedPrefersBackendOverStaleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	// A stale legacy file with DIFFERENT content must not be presented when
	// the backend serves the scan's durable stream (D12: no bypassing PG).
	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	SetBackend(nil) // write the stale file in file mode
	if _, err := j.Append(testRecord("scan-1")); err != nil {
		t.Fatal(err)
	}

	b := &readLoadBackend{fakeBackend: *newFakeBackend()}
	pgRecords := []Record{testRecord("scan-1"), testRecord("scan-1")}
	pgRecords[0].Seq, pgRecords[1].Seq = 1, 2
	b.setRecords("scan-1", pgRecords)
	registerTestBackend(t, b)

	records, damaged, err := ReadRecordsResolved(context.Background(), path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if damaged {
		t.Fatal("damaged must be false")
	}
	if len(records) != 2 || records[0].Seq != 1 || records[1].Seq != 2 {
		t.Fatalf("records = %+v, want the backend's durable stream", records)
	}
}

func TestReadRecordsResolvedFallsBackToLegacyFileWhenStreamEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	SetBackend(nil)
	appended, err := j.Append(testRecord("scan-1"))
	if err != nil {
		t.Fatal(err)
	}
	// Re-arm a backend that holds no rows for this scan (pre-R6 directory
	// whose import has not produced rows).
	b := &readLoadBackend{fakeBackend: *newFakeBackend()}
	registerTestBackend(t, b)

	records, _, err := ReadRecordsResolved(context.Background(), path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Seq != appended.Seq {
		t.Fatalf("records = %+v, want the file fallback", records)
	}
}

func TestReadRecordsResolvedBackendErrorNeverReadsStaleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	SetBackend(nil)
	if _, err := j.Append(testRecord("scan-1")); err != nil {
		t.Fatal(err)
	}
	b := &readLoadBackend{fakeBackend: *newFakeBackend()}
	b.loadErr = errors.New("pg unavailable")
	registerTestBackend(t, b)

	if _, _, err := ReadRecordsResolved(context.Background(), path, "scan-1"); err == nil {
		t.Fatal("backend error must surface, not silently read the stale file")
	}
}

func TestReadRecordsResolvedNilBackendIsFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	appended, err := j.Append(testRecord("scan-1"))
	if err != nil {
		t.Fatal(err)
	}
	records, _, err := ReadRecordsResolved(context.Background(), path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Seq != appended.Seq {
		t.Fatalf("records = %+v", records)
	}
}

func TestSetInstanceIDNilReceiverAndTrim(t *testing.T) {
	var j *Journal
	j.SetInstanceID("x") // must not panic
	real, err := Open(filepath.Join(t.TempDir(), FileName), "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	real.SetInstanceID("  inst-7  ")
	if _, err := real.Append(testRecord("scan-1")); err != nil {
		t.Fatal(err)
	}
}

// TestAppendRecordTimesMonotonicPerScan pins the seq contract both modes
// share: one writer, strictly increasing per-scan seq.
func TestAppendRecordTimesMonotonicPerScan(t *testing.T) {
	dir := t.TempDir()
	b := newFakeBackend()
	registerTestBackend(t, b)
	j, err := Open(filepath.Join(dir, FileName), "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := j.Append(testRecord("scan-1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.Append(testRecord("scan-1"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != first.Seq+1 {
		t.Fatalf("seqs = %d,%d", first.Seq, second.Seq)
	}
}

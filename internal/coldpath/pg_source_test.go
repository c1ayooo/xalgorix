package coldpath

// pg_source_test.go — R6.1: the journal's durable source is the PG primary
// store once the domainjournal backend is registered (charter §6.3). The
// processor reads records through the read seam (backend first → file
// fallback) and the reconciler enumerates run_journal streams through the
// backend — production journals have no file under the data root, so the
// crash catch-up must see them (charter §6.4).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// fakeJournalBackend is the in-memory domainjournal.Backend.
type fakeJournalBackend struct {
	mu         sync.Mutex
	records    map[string][]domainjournal.Record
	sources    []domainjournal.JournalSource
	loadErr    error
	sourcesErr error
}

func (f *fakeJournalBackend) AppendJournalRecord(_ string, _ string, _ domainjournal.Record) error {
	return errors.New("not used in this test")
}

func (f *fakeJournalBackend) LoadJournalRecordsByScan(_ context.Context, scanID string) ([]domainjournal.Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return nil, false, f.loadErr
	}
	return append([]domainjournal.Record(nil), f.records[scanID]...), false, nil
}

func (f *fakeJournalBackend) LoadJournalSources(_ context.Context) ([]domainjournal.JournalSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sourcesErr != nil {
		return nil, f.sourcesErr
	}
	return append([]domainjournal.JournalSource(nil), f.sources...), nil
}

func (f *fakeJournalBackend) setRecords(scanID string, records []domainjournal.Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[scanID] = records
}

func registerTestJournalBackend(t *testing.T, b domainjournal.Backend) {
	t.Helper()
	domainjournal.SetBackend(b)
	t.Cleanup(func() { domainjournal.SetBackend(nil) })
}

func pgRecord(scanID string, seq uint64) domainjournal.Record {
	return domainjournal.Record{
		ScanID:    scanID,
		Seq:       seq,
		At:        time.Now().UTC(),
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine},
		Operation: domainjournal.OpSetStatus,
		Summary:   "pg",
	}
}

func TestProcessorAppliesRangeFromPGJournal(t *testing.T) {
	root := t.TempDir() // no file under the root: the journal lives in PG only
	b := &fakeJournalBackend{records: map[string][]domainjournal.Record{}}
	b.setRecords("pg-scan", []domainjournal.Record{
		pgRecord("pg-scan", 1), pgRecord("pg-scan", 2), pgRecord("pg-scan", 3),
	})
	registerTestJournalBackend(t, b)

	applier := &fakeApplier{}
	p, err := NewProcessor(root, applier)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessRange(context.Background(), "pg-scan", "pg-scan", 2, 3); err != nil {
		t.Fatal(err)
	}
	if len(applier.seqs) != 2 || applier.seqs[0] != 2 || applier.seqs[1] != 3 {
		t.Fatalf("applied seqs = %v", applier.seqs)
	}
}

func TestProcessorPGReadErrorSurfaces(t *testing.T) {
	root := t.TempDir()
	b := &fakeJournalBackend{records: map[string][]domainjournal.Record{}, loadErr: errors.New("pg unavailable")}
	registerTestJournalBackend(t, b)

	p, err := NewProcessor(root, &fakeApplier{})
	if err != nil {
		t.Fatal(err)
	}
	// No file fallback may mask the PG error (D12 red line).
	if err := p.ProcessRange(context.Background(), "pg-scan", "pg-scan", 1, 1); err == nil {
		t.Fatal("expected the backend read error to surface")
	}
}

func TestReconcilerEnqueuesPGJournalSources(t *testing.T) {
	root := t.TempDir()
	// A PG-only stream (no file anywhere) plus a legacy file journal: both
	// families must be reconciled against their watermarks. The legacy file
	// is written BEFORE the backend is armed — with a backend registered the
	// journal appends land in PG and skip the file (R6.1 red line).
	writeJournal(t, root, "file-scan", 3)
	b := &fakeJournalBackend{
		records: map[string][]domainjournal.Record{},
		sources: []domainjournal.JournalSource{{ScanID: "pg-scan", LastSeq: 3}},
	}
	registerTestJournalBackend(t, b)

	store := &fakeStore{applied: 1}
	enq := &fakeEnqueuer{}
	r, err := NewReconciler(root, store, enq)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := map[string]enqueueCall{}
	for _, c := range enq.calls {
		calls[c.scanID] = c
	}
	if len(enq.calls) != 2 {
		t.Fatalf("enqueue calls = %+v", enq.calls)
	}
	if c := calls["pg-scan"]; c.scanID != "pg-scan" || c.dir != "pg-scan" || c.from != 2 || c.to != 3 {
		t.Fatalf("pg-scan call = %+v", c)
	}
	if c := calls["file-scan"]; c.scanID != "file-scan" || c.from != 2 || c.to != 3 {
		t.Fatalf("file-scan call = %+v", c)
	}

	// Applied watermark: neither family enqueues again.
	store.applied = 3
	enq.calls = nil
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(enq.calls) != 0 {
		t.Fatalf("unexpected enqueue at applied watermark: %+v", enq.calls)
	}
}

func TestReconcilerPGSourceErrorIsReportedNotSilent(t *testing.T) {
	root := t.TempDir()
	b := &fakeJournalBackend{records: map[string][]domainjournal.Record{}, sourcesErr: errors.New("pg unavailable")}
	registerTestJournalBackend(t, b)

	r, err := NewReconciler(root, &fakeStore{}, &fakeEnqueuer{})
	if err != nil {
		t.Fatal(err)
	}
	// The backstop being unable to see the durable streams is itself a
	// visible failure (charter §6.4).
	if err := r.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("expected the PG source enumeration error to be reported")
	}
}

func TestReconcilerWithoutBackendIsFileOnly(t *testing.T) {
	root := t.TempDir()
	writeJournal(t, root, "scan1", 2)
	r, err := NewReconciler(root, &fakeStore{}, &fakeEnqueuer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

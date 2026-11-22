package coldpath

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem/postgres"
)

type fakeApplier struct {
	seqs []uint64
	err  error
}

func (f *fakeApplier) Apply(_ context.Context, rec domainjournal.Record) error {
	if f.err != nil {
		return f.err
	}
	f.seqs = append(f.seqs, rec.Seq)
	return nil
}

func writeJournal(t *testing.T, root, scanID string, n int) string {
	t.Helper()
	dir := filepath.Join(root, scanID)
	j, err := domainjournal.Open(filepath.Join(dir, domainjournal.FileName), scanID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := j.Append(domainjournal.Record{
			Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine},
			Operation: domainjournal.OpSetStatus,
			Summary:   "test",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestProcessorAppliesExactRange(t *testing.T) {
	root := t.TempDir()
	writeJournal(t, root, "scan1", 3)
	applier := &fakeApplier{}
	p, err := NewProcessor(root, applier)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessRange(context.Background(), "scan1", "scan1", 2, 3); err != nil {
		t.Fatal(err)
	}
	if len(applier.seqs) != 2 || applier.seqs[0] != 2 || applier.seqs[1] != 3 {
		t.Fatalf("applied seqs = %v", applier.seqs)
	}
}

func TestProcessorRejectsMissingSequenceAndTraversal(t *testing.T) {
	root := t.TempDir()
	dir := writeJournal(t, root, "scan1", 1)
	path := filepath.Join(dir, domainjournal.FileName)
	j, err := domainjournal.Open(path, "scan1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(domainjournal.Record{
		Actor: domainjournal.Actor{Kind: domainjournal.ActorEngine}, Operation: domainjournal.OpSetStatus,
	}); err != nil {
		t.Fatal(err)
	}
	// Remove the middle sequence by rewriting the file with seq 1 and 3.
	p, err := NewProcessor(root, &fakeApplier{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessRange(context.Background(), "scan1", "scan1", 1, 3); err == nil {
		t.Fatal("expected missing sequence error")
	}
	if err := p.ProcessRange(context.Background(), "scan1", "../escape", 1, 1); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

type fakeStore struct {
	applied uint64
	err     error
}

func (f *fakeStore) Watermark(context.Context, string) (postgres.Watermark, error) {
	return postgres.Watermark{AppliedSeq: f.applied, Status: "applied"}, f.err
}

type enqueueCall struct {
	scanID, dir string
	from, to    uint64
}

type fakeEnqueuer struct {
	calls []enqueueCall
	err   error
}

func (f *fakeEnqueuer) EnqueueRange(_ context.Context, scanID, scanDir string, from, to uint64) error {
	f.calls = append(f.calls, enqueueCall{scanID: scanID, dir: scanDir, from: from, to: to})
	return f.err
}

func TestReconcilerEnqueuesOnlyMissingTail(t *testing.T) {
	root := t.TempDir()
	writeJournal(t, root, "scan1", 3)
	store := &fakeStore{applied: 1}
	enq := &fakeEnqueuer{}
	r, err := NewReconciler(root, store, enq)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(enq.calls) != 1 {
		t.Fatalf("enqueue calls = %+v", enq.calls)
	}
	call := enq.calls[0]
	if call.scanID != "scan1" || call.dir != "scan1" || call.from != 2 || call.to != 3 {
		t.Fatalf("unexpected call: %+v", call)
	}

	store.applied = 3
	enq.calls = nil
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(enq.calls) != 0 {
		t.Fatalf("unexpected enqueue at applied watermark: %+v", enq.calls)
	}
}

func TestServiceDisabledWithoutDSN(t *testing.T) {
	s, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled() {
		t.Fatal("empty DSN must disable cold path")
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRiverReconcilesJournal(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	root := t.TempDir()
	scanID := fmt.Sprintf("river-%d", time.Now().UnixNano())

	svc, err := New(context.Background(), Config{
		DSN: dsn, DataDir: root, ReconcileInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !svc.Enabled() {
		t.Fatal("expected enabled service")
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Stop(stopCtx)
		_, _ = svc.pool.Exec(context.Background(), `DELETE FROM info_audit_records WHERE scan_id=$1`, scanID)
		_, _ = svc.pool.Exec(context.Background(), `DELETE FROM info_sync_watermarks WHERE scan_id=$1`, scanID)
	})

	// Let the immediate startup reconciliation finish. With a one-hour
	// interval, only the fast-path journal observer can deliver these records.
	time.Sleep(150 * time.Millisecond)
	writeJournal(t, root, scanID, 2)

	deadline := time.Now().Add(10 * time.Second)
	for {
		wm, err := svc.Store().Watermark(context.Background(), scanID)
		if err != nil {
			t.Fatal(err)
		}
		if wm.AppliedSeq == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("River did not apply journal; watermark=%+v", wm)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

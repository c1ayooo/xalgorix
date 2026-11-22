package scanctx

// R6.1 ledger persistence backend tests: with a backend registered, snapshot
// persistence goes to the backend (never to ledger.json), and a
// re-dispatched session (new scan id, same instance id) inherits the
// previous sibling session's chain via the instance continuity read. Without
// a backend the v2.1 file behavior is byte-identical.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeLedgerBackend struct {
	mu        sync.Mutex
	snapshots map[string]struct {
		data       []byte
		seq        int64
		instanceID string
		updatedAt  time.Time
	}
	saveErr error
}

func newFakeLedgerBackend() *fakeLedgerBackend {
	return &fakeLedgerBackend{snapshots: make(map[string]struct {
		data       []byte
		seq        int64
		instanceID string
		updatedAt  time.Time
	})}
}

func (f *fakeLedgerBackend) SaveLedgerSnapshot(scanID, instanceID string, seq int64, data []byte) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots[scanID] = struct {
		data       []byte
		seq        int64
		instanceID string
		updatedAt  time.Time
	}{data, seq, instanceID, time.Now().UTC()}
	return nil
}

func (f *fakeLedgerBackend) LoadLedgerSnapshot(scanID string) ([]byte, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap, ok := f.snapshots[scanID]
	if !ok {
		return nil, 0, false, nil
	}
	return snap.data, snap.seq, true, nil
}

func (f *fakeLedgerBackend) LoadLatestLedgerSnapshotForInstance(instanceID, excludeScanID string) ([]byte, int64, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best string
	var bestAt time.Time
	for scanID, snap := range f.snapshots {
		if snap.instanceID != instanceID || scanID == excludeScanID {
			continue
		}
		if best == "" || snap.updatedAt.After(bestAt) {
			best = scanID
			bestAt = snap.updatedAt
		}
	}
	if best == "" {
		return nil, 0, "", false, nil
	}
	snap := f.snapshots[best]
	return snap.data, snap.seq, best, true, nil
}

func backendTestLedger(t *testing.T, scanID, instanceID string) *LedgerStore {
	t.Helper()
	ls := NewLedgerStore()
	ls.SetScanID(scanID)
	ls.SetInstanceID(instanceID)
	return ls
}

func TestLedgerBackendSavesRoundTrip(t *testing.T) {
	backend := newFakeLedgerBackend()
	SetLedgerBackend(backend)
	t.Cleanup(func() { SetLedgerBackend(nil) })

	ls := backendTestLedger(t, "scan-a", "")
	dir := t.TempDir()
	ls.SetPersistPath(dir)
	now := time.Now().UTC()
	ls.Upsert(Hypothesis{Title: "backend snapshot", VulnClass: "sqli", Endpoint: "https://example.com", Status: HypothesisQueued, CreatedAt: now, UpdatedAt: now})

	// No file may be written while the backend is registered (charter v3.0:
	// the durable home is PG; ledger.json is not a write target).
	if _, err := os.Stat(filepath.Join(dir, "ledger.json")); !os.IsNotExist(err) {
		t.Fatalf("ledger.json written despite registered backend")
	}

	restored := NewLedgerStore()
	restored.SetScanID("scan-a")
	restored.SetPersistPath(t.TempDir()) // decoupled from the writer's dir
	if got := restored.LoadFromDisk(); got != 1 {
		t.Fatalf("loaded %d hypotheses, want 1", got)
	}
	if h, ok := restored.Get("H-1"); !ok || h.Title != "backend snapshot" {
		t.Fatalf("restored hypothesis = %+v ok=%v", h, ok)
	}
}

func TestLedgerBackendInstanceContinuityAcrossRedispatch(t *testing.T) {
	backend := newFakeLedgerBackend()
	SetLedgerBackend(backend)
	t.Cleanup(func() { SetLedgerBackend(nil) })

	// First session: scan-a of instance inst, accumulates a hypothesis.
	first := backendTestLedger(t, "scan-a", "inst")
	now := time.Now().UTC()
	first.Upsert(Hypothesis{Title: "carried across redispatch", VulnClass: "xss", Endpoint: "https://example.com/x", Status: HypothesisTesting, CreatedAt: now, UpdatedAt: now})

	// Re-dispatched second session: NEW scan id, SAME instance, no snapshot
	// of its own — the first LoadFromDisk inherits the previous chain.
	second := backendTestLedger(t, "scan-b", "inst")
	if got := second.LoadFromDisk(); got != 1 {
		t.Fatalf("continuity load = %d hypotheses, want 1", got)
	}
	if h, ok := second.Get("H-1"); !ok || h.Title != "carried across redispatch" {
		t.Fatalf("continuity hypothesis = %+v ok=%v", h, ok)
	}
	// The inherited sequence advanced: the next hypothesis gets H-2, never
	// colliding with the carried chain.
	next := second.Upsert(Hypothesis{Title: "fresh in resumed session", VulnClass: "sqli", Endpoint: "https://example.com/y", CreatedAt: now, UpdatedAt: now})
	if next.ID != "H-2" {
		t.Fatalf("next hypothesis id = %s, want H-2 (no id collision with carried chain)", next.ID)
	}
}

func TestLedgerBackendSaveFailureReturnsToMutationCaller(t *testing.T) {
	backend := newFakeLedgerBackend()
	backend.saveErr = os.ErrPermission
	SetLedgerBackend(backend)
	t.Cleanup(func() { SetLedgerBackend(nil) })

	ls := backendTestLedger(t, "scan-a", "")
	// persist-failure is not success (charter §5.2): the mutation surfaces
	// the error instead of silently losing the snapshot.
	_, err := ls.UpsertAs(Hypothesis{Title: "x", VulnClass: "sqli", Endpoint: "/e"}, engineActor("test"))
	if err == nil {
		t.Fatalf("mutation succeeded despite backend save failure")
	}
	// In-memory state stays authoritative.
	if _, ok := ls.Get("H-1"); !ok {
		t.Fatalf("in-memory hypothesis lost after save failure")
	}
}

func TestLedgerFileModeUnchangedWithoutBackend(t *testing.T) {
	// No backend registered: v2.1 file behavior (fixture for the legacy path
	// every existing scanctx test pins).
	dir := t.TempDir()
	ls := NewLedgerStore()
	ls.SetScanID("scan-file")
	ls.SetPersistPath(dir)
	now := time.Now().UTC()
	ls.Upsert(Hypothesis{Title: "file mode", VulnClass: "sqli", Endpoint: "https://example.com", CreatedAt: now, UpdatedAt: now})
	if _, err := os.Stat(filepath.Join(dir, "ledger.json")); err != nil {
		t.Fatalf("ledger.json not written in file mode: %v", err)
	}
	restored := NewLedgerStore()
	restored.SetScanID("scan-file")
	restored.SetPersistPath(dir)
	if got := restored.LoadFromDisk(); got != 1 {
		t.Fatalf("file mode load = %d, want 1", got)
	}
}

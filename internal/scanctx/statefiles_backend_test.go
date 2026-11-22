package scanctx

// R6.1 state-files persistence backend tests (charter v3.0 §6.3): with a
// backend registered, the NoteStore map and the CoverageStore matrix persist
// to the backend (never to notes.json / coverage.json), and a re-dispatched
// session (new scan id, same instance id) inherits the previous sibling
// session's snapshot via the instance continuity read. Without a backend the
// legacy file behavior is unchanged.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type stateFileKey struct {
	scanID string
	kind   string
}

type fakeStateFile struct {
	data       []byte
	seq        int64
	instanceID string
	updatedAt  time.Time
}

type fakeStateFilesBackend struct {
	mu      sync.Mutex
	files   map[stateFileKey]fakeStateFile
	saveErr error
}

func newFakeStateFilesBackend() *fakeStateFilesBackend {
	return &fakeStateFilesBackend{files: make(map[stateFileKey]fakeStateFile)}
}

func (f *fakeStateFilesBackend) SaveStateFileSnapshot(scanID, kind, instanceID string, seq int64, data []byte) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[stateFileKey{scanID, kind}] = fakeStateFile{data: data, seq: seq, instanceID: instanceID, updatedAt: time.Now().UTC()}
	return nil
}

func (f *fakeStateFilesBackend) LoadStateFileSnapshot(scanID, kind string) ([]byte, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[stateFileKey{scanID, kind}]
	if !ok {
		return nil, 0, false, nil
	}
	return file.data, file.seq, true, nil
}

func (f *fakeStateFilesBackend) LoadLatestStateFileSnapshotForInstance(instanceID, excludeScanID, kind string) ([]byte, int64, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best stateFileKey
	var bestAt time.Time
	found := false
	for key, file := range f.files {
		if key.kind != kind || file.instanceID != instanceID || key.scanID == excludeScanID {
			continue
		}
		if !found || file.updatedAt.After(bestAt) {
			best = key
			bestAt = file.updatedAt
			found = true
		}
	}
	if !found {
		return nil, 0, "", false, nil
	}
	file := f.files[best]
	return file.data, file.seq, best.scanID, true, nil
}

func withStateFilesBackend(t *testing.T, b *fakeStateFilesBackend) {
	t.Helper()
	SetStateFilesBackend(b)
	t.Cleanup(func() { SetStateFilesBackend(nil) })
}

// ── NoteStore ────────────────────────────────────────────────────────────

func TestNoteStoreBackendSavesRoundTrip(t *testing.T) {
	backend := newFakeStateFilesBackend()
	withStateFilesBackend(t, backend)

	ns := NewNoteStore()
	ns.SetScanID("scan-a")
	dir := t.TempDir()
	ns.SetPersistPath(dir) // set, but must NOT be written while the backend is registered
	if err := ns.Set("csrf", "token-1"); err != nil {
		t.Fatalf("Set with backend: %v", err)
	}

	// No file may be written while the backend is registered (charter v3.0:
	// the durable home is PG; notes.json is not a write target).
	if _, err := os.Stat(filepath.Join(dir, "notes.json")); !os.IsNotExist(err) {
		t.Fatalf("notes.json written despite registered backend")
	}
	if file, ok := backend.files[stateFileKey{"scan-a", StateFileKindNotes}]; !ok {
		t.Fatalf("no backend snapshot saved for scan-a/notes")
	} else if file.instanceID != "" {
		t.Fatalf("instance id should be empty until stamped, got %q", file.instanceID)
	}

	restored := NewNoteStore()
	restored.SetScanID("scan-a")
	restored.SetPersistPath(t.TempDir()) // decoupled from the writer's dir
	if got := restored.LoadFromDisk(); got != 1 {
		t.Fatalf("loaded %d notes, want 1", got)
	}
	if v, ok := restored.Get("csrf"); !ok || v != "token-1" {
		t.Fatalf("restored note = (%q, %v)", v, ok)
	}
}

func TestNoteStoreBackendInstanceContinuityAcrossRedispatch(t *testing.T) {
	withStateFilesBackend(t, newFakeStateFilesBackend())

	// First session: scan-a of instance inst, accumulates a note.
	first := NewNoteStore()
	first.SetScanID("scan-a")
	first.SetInstanceID("inst")
	if err := first.Set("chain", "carried across redispatch"); err != nil {
		t.Fatalf("first session Set: %v", err)
	}

	// Re-dispatched second session: NEW scan id, SAME instance, no snapshot
	// of its own — the first LoadFromDisk inherits the previous notes.
	second := NewNoteStore()
	second.SetScanID("scan-b")
	second.SetInstanceID("inst")
	second.SetPersistPath(t.TempDir()) // empty dir: continuity must come from PG, not disk
	if got := second.LoadFromDisk(); got != 1 {
		t.Fatalf("continuity load = %d notes, want 1", got)
	}
	if v, ok := second.Get("chain"); !ok || v != "carried across redispatch" {
		t.Fatalf("continuity note = (%q, %v)", v, ok)
	}

	// The resumed session does not clobber its own live keys on the next load.
	if err := second.Set("chain", "updated in resumed session"); err != nil {
		t.Fatalf("resumed Set: %v", err)
	}
	if got := second.LoadFromDisk(); got != 0 {
		t.Fatalf("re-load merged %d stale notes, want 0 (live state wins)", got)
	}
	v, _ := second.Get("chain")
	if v != "updated in resumed session" {
		t.Fatalf("live note clobbered by stale snapshot: %q", v)
	}
}

func TestNoteStoreBackendSaveFailureReturnsToCaller(t *testing.T) {
	backend := newFakeStateFilesBackend()
	backend.saveErr = os.ErrPermission
	withStateFilesBackend(t, backend)

	ns := NewNoteStore()
	ns.SetScanID("scan-a")
	// persist-failure is not success (charter §5.2): Set surfaces the error.
	if err := ns.Set("k", "v"); err == nil {
		t.Fatalf("Set succeeded despite backend save failure")
	}
	// In-memory state stays authoritative.
	if v, ok := ns.Get("k"); !ok || v != "v" {
		t.Fatalf("in-memory note lost after save failure: (%q, %v)", v, ok)
	}
}

func TestNoteStoreFileModeUnchangedWithoutBackend(t *testing.T) {
	// No backend registered: pre-R6.1 file behavior (the fixture every
	// existing NoteStore test pins).
	dir := t.TempDir()
	ns := NewNoteStore()
	ns.SetScanID("scan-file")
	ns.SetPersistPath(dir)
	if err := ns.Set("token", "xyz"); err != nil {
		t.Fatalf("file-mode Set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.json")); err != nil {
		t.Fatalf("notes.json not written in file mode: %v", err)
	}
	restored := NewNoteStore()
	restored.SetScanID("scan-file")
	restored.SetPersistPath(dir)
	if got := restored.LoadFromDisk(); got != 1 {
		t.Fatalf("file mode load = %d, want 1", got)
	}
}

// ── CoverageStore ────────────────────────────────────────────────────────

func TestCoverageBackendSavesRoundTrip(t *testing.T) {
	backend := newFakeStateFilesBackend()
	withStateFilesBackend(t, backend)

	cs := NewCoverageStore()
	cs.SetScanID("scan-a")
	dir := t.TempDir()
	cs.Mark("example.test/api/users", "sqli")
	cs.MarkVerified("example.test/api/login", "authz")
	if err := cs.SaveCheckpoint(dir); err != nil {
		t.Fatalf("SaveCheckpoint with backend: %v", err)
	}

	// No file may be written while the backend is registered.
	if _, err := os.Stat(filepath.Join(dir, "coverage.json")); !os.IsNotExist(err) {
		t.Fatalf("coverage.json written despite registered backend")
	}

	restored := NewCoverageStore()
	restored.SetScanID("scan-a")
	if err := restored.LoadCheckpoint(t.TempDir()); err != nil { // decoupled dir
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if !restored.Has("example.test/api/users", "sqli") {
		t.Fatal("executed pair not restored from backend snapshot")
	}
	if !restored.HasVerified("example.test/api/login", "authz") {
		t.Fatal("verifier-attributed tier not restored from backend snapshot")
	}
}

func TestCoverageBackendInstanceContinuityAcrossRedispatch(t *testing.T) {
	withStateFilesBackend(t, newFakeStateFilesBackend())

	first := NewCoverageStore()
	first.SetScanID("scan-a")
	first.SetInstanceID("inst")
	first.Mark("example.test/api/users", "sqli")
	if err := first.SaveCheckpoint(t.TempDir()); err != nil {
		t.Fatalf("first session SaveCheckpoint: %v", err)
	}

	// Re-dispatched session: NEW scan id, SAME instance — inherits coverage.
	second := NewCoverageStore()
	second.SetScanID("scan-b")
	second.SetInstanceID("inst")
	if err := second.LoadCheckpoint(t.TempDir()); err != nil {
		t.Fatalf("continuity LoadCheckpoint: %v", err)
	}
	if !second.Has("example.test/api/users", "sqli") {
		t.Fatal("continuity coverage missing after re-dispatch")
	}
}

func TestCoverageBackendSaveFailureReturnsToCaller(t *testing.T) {
	backend := newFakeStateFilesBackend()
	backend.saveErr = os.ErrPermission
	withStateFilesBackend(t, backend)

	cs := NewCoverageStore()
	cs.SetScanID("scan-a")
	cs.Mark("example.test/api/users", "sqli")
	if err := cs.SaveCheckpoint(t.TempDir()); err == nil {
		t.Fatalf("SaveCheckpoint succeeded despite backend save failure")
	}
	// In-memory matrix stays authoritative.
	if !cs.Has("example.test/api/users", "sqli") {
		t.Fatal("in-memory coverage lost after save failure")
	}
}

func TestCoverageFileModeUnchangedWithoutBackend(t *testing.T) {
	// No backend registered: pre-R6.1 file behavior.
	dir := t.TempDir()
	cs := NewCoverageStore()
	cs.SetScanID("scan-file")
	cs.Mark("example.test/api/users", "sqli")
	cs.MarkVerified("example.test/api/login", "authz")
	if err := cs.SaveCheckpoint(dir); err != nil {
		t.Fatalf("file-mode SaveCheckpoint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "coverage.json")); err != nil {
		t.Fatalf("coverage.json not written in file mode: %v", err)
	}
	restored := NewCoverageStore()
	restored.SetScanID("scan-file")
	if err := restored.LoadCheckpoint(dir); err != nil {
		t.Fatalf("file-mode LoadCheckpoint: %v", err)
	}
	if !restored.Has("example.test/api/users", "sqli") || !restored.HasVerified("example.test/api/login", "authz") {
		t.Fatal("file-mode round trip lost coverage evidence")
	}
}

// Compile-time pin: the state-files backend seam is process-global
// infrastructure; registration is the R6.1 wiring slice's job (main.go).
var _ StateFilesBackend = (*fakeStateFilesBackend)(nil)

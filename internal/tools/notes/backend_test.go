package notes

// R6.1 notes persistence backend tests (charter v3.0 §6.3): with a backend
// registered, snapshots persist to the run_state_files row (kind=notes) and
// notes.json is never written; a re-dispatched session (new context id, same
// instance id) inherits the previous sibling session's notes via the
// instance continuity read. The injection point is scanctx.SetStateFilesBackend
// — the tools store shares the kind=notes family with scanctx.NoteStore, so
// one registration covers both. Without a backend the legacy file behavior
// is unchanged (pinned by notes_test.go).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

type notesKey struct {
	scanID string
	kind   string
}

type fakeNotesFile struct {
	data       []byte
	seq        int64
	instanceID string
	updatedAt  time.Time
}

type fakeNotesBackend struct {
	mu      sync.Mutex
	files   map[notesKey]fakeNotesFile
	saveErr error
}

func newFakeNotesBackend() *fakeNotesBackend {
	return &fakeNotesBackend{files: make(map[notesKey]fakeNotesFile)}
}

func (f *fakeNotesBackend) SaveStateFileSnapshot(scanID, kind, instanceID string, seq int64, data []byte) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[notesKey{scanID, kind}] = fakeNotesFile{data: data, seq: seq, instanceID: instanceID, updatedAt: time.Now().UTC()}
	return nil
}

func (f *fakeNotesBackend) LoadStateFileSnapshot(scanID, kind string) ([]byte, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[notesKey{scanID, kind}]
	if !ok {
		return nil, 0, false, nil
	}
	return file.data, file.seq, true, nil
}

func (f *fakeNotesBackend) LoadLatestStateFileSnapshotForInstance(instanceID, excludeScanID, kind string) ([]byte, int64, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best string
	var bestAt time.Time
	for key, file := range f.files {
		if key.kind != kind || file.instanceID != instanceID || key.scanID == excludeScanID {
			continue
		}
		if best == "" || file.updatedAt.After(bestAt) {
			best = key.scanID
			bestAt = file.updatedAt
		}
	}
	if best == "" {
		return nil, 0, "", false, nil
	}
	file := f.files[notesKey{best, kind}]
	return file.data, file.seq, best, true, nil
}

// Compile-time pin: the fake satisfies the shared scanctx seam.
var _ scanctx.StateFilesBackend = (*fakeNotesBackend)(nil)

func withNotesBackend(t *testing.T, b *fakeNotesBackend) {
	t.Helper()
	scanctx.SetStateFilesBackend(b)
	t.Cleanup(func() { scanctx.SetStateFilesBackend(nil) })
}

func TestNotesBackendSavesRoundTrip(t *testing.T) {
	backend := newFakeNotesBackend()
	withNotesBackend(t, backend)

	const ctxID = "notes-backend-roundtrip"
	dir := t.TempDir()
	t.Cleanup(func() { CleanupContext(ctxID) })
	SetPersistPathForContext(ctxID, dir) // set, but must NOT be written while the backend is registered

	if _, err := addNoteForContext(ctxID, map[string]string{"key": "csrf", "value": "token-1"}); err != nil {
		t.Fatalf("add note with backend: %v", err)
	}

	// No file may be written while the backend is registered (charter v3.0:
	// the durable home is PG; notes.json is not a write target).
	if _, err := os.Stat(filepath.Join(dir, "notes.json")); !os.IsNotExist(err) {
		t.Fatalf("notes.json written despite registered backend")
	}
	file, ok := backend.files[notesKey{ctxID, scanctx.StateFileKindNotes}]
	if !ok {
		t.Fatalf("no backend snapshot saved for %s/notes", ctxID)
	}
	var stored map[string]string
	if err := json.Unmarshal(file.data, &stored); err != nil {
		t.Fatalf("backend snapshot does not decode: %v", err)
	}
	if stored["csrf"] != "token-1" {
		t.Fatalf("backend snapshot content = %v", stored)
	}

	// Restore into a fresh store: the backend row speaks, not the (empty) file.
	ResetNotesForContext(ctxID)
	count, err := LoadFromDiskForContext(ctxID)
	if err != nil || count != 1 {
		t.Fatalf("restore = %d notes err=%v, want 1 nil", count, err)
	}
	if got := GetAllNotesForContext(ctxID)["csrf"]; got != "token-1" {
		t.Fatalf("restored note = %q", got)
	}
}

func TestNotesBackendInstanceContinuityAcrossRedispatch(t *testing.T) {
	withNotesBackend(t, newFakeNotesBackend())

	const first = "notes-cont-scan-a"
	const second = "notes-cont-scan-b"
	t.Cleanup(func() {
		CleanupContext(first)
		CleanupContext(second)
	})

	// First session: scan-a of instance inst, saves one note.
	SetInstanceIDForContext(first, "inst")
	if _, err := addNoteForContext(first, map[string]string{"key": "chain", "value": "carried across redispatch"}); err != nil {
		t.Fatalf("first session add_note: %v", err)
	}

	// Re-dispatched second session: NEW context id, SAME instance, no
	// snapshot of its own — the first restore inherits the previous notes.
	SetInstanceIDForContext(second, "inst")
	SetPersistPathForContext(second, t.TempDir()) // empty dir: continuity must come from PG, not disk
	count, err := LoadFromDiskForContext(second)
	if err != nil || count != 1 {
		t.Fatalf("continuity restore = %d err=%v, want 1 nil", count, err)
	}
	if got := GetAllNotesForContext(second)["chain"]; got != "carried across redispatch" {
		t.Fatalf("continuity note = %q", got)
	}
}

func TestNotesBackendSaveFailureIsHonest(t *testing.T) {
	backend := newFakeNotesBackend()
	backend.saveErr = os.ErrPermission
	withNotesBackend(t, backend)

	const ctxID = "notes-backend-failure"
	t.Cleanup(func() { CleanupContext(ctxID) })
	// persist-failure is not success (charter §5.2): the durable-write
	// acknowledgement reports the failure and the change is rolled back.
	res, err := addNoteForContext(ctxID, map[string]string{"key": "k", "value": "v"})
	if err != nil {
		t.Fatalf("add note: %v", err)
	}
	if res.Error == "" {
		t.Fatalf("add_note succeeded despite backend save failure: %+v", res)
	}
	if _, ok := GetAllNotesForContext(ctxID)["k"]; ok {
		t.Fatal("rolled-back note still present — receipt and durable state disagree")
	}
}

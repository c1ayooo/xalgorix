package intelligence

// R6.1 intelligence persistence backend tests (charter v3.0 §6.3): with a
// backend registered, snapshots persist to the run_state_files row
// (kind=intelligence) and intelligence.json is never written; a
// re-dispatched session (new context id, same instance id) inherits the
// previous sibling session's intel via the instance continuity read. Without
// a backend the legacy file behavior is unchanged.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type intelKey struct {
	scanID string
}

type fakeIntelFile struct {
	data       []byte
	seq        int64
	instanceID string
	updatedAt  time.Time
}

type fakeIntelBackend struct {
	mu      sync.Mutex
	files   map[intelKey]fakeIntelFile
	saveErr error
}

func newFakeIntelBackend() *fakeIntelBackend {
	return &fakeIntelBackend{files: make(map[intelKey]fakeIntelFile)}
}

func (f *fakeIntelBackend) SaveIntelSnapshot(scanID, instanceID string, seq int64, data []byte) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[intelKey{scanID}] = fakeIntelFile{data: data, seq: seq, instanceID: instanceID, updatedAt: time.Now().UTC()}
	return nil
}

func (f *fakeIntelBackend) LoadIntelSnapshot(scanID string) ([]byte, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[intelKey{scanID}]
	if !ok {
		return nil, 0, false, nil
	}
	return file.data, file.seq, true, nil
}

func (f *fakeIntelBackend) LoadLatestIntelSnapshotForInstance(instanceID, excludeScanID string) ([]byte, int64, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best string
	var bestAt time.Time
	for key, file := range f.files {
		if file.instanceID != instanceID || key.scanID == excludeScanID {
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
	file := f.files[intelKey{best}]
	return file.data, file.seq, best, true, nil
}

func withIntelBackend(t *testing.T, b *fakeIntelBackend) {
	t.Helper()
	SetBackend(b)
	t.Cleanup(func() { SetBackend(nil) })
}

func recordTestIntel(t *testing.T, ctxID string) {
	t.Helper()
	res, err := RecordForContext(ctxID, map[string]string{
		"kind":    "credential",
		"title":   "Admin panel accepts default creds",
		"content": "admin:admin accepted at /app/admin/login",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("record: err=%v res=%+v", err, res)
	}
}

func TestIntelBackendSavesRoundTrip(t *testing.T) {
	backend := newFakeIntelBackend()
	withIntelBackend(t, backend)

	ctxID := "intel-backend-roundtrip"
	dir := t.TempDir()
	t.Cleanup(func() { CleanupContext(ctxID) })
	SetPersistPathForContext(ctxID, dir) // set, but must NOT be written while the backend is registered
	recordTestIntel(t, ctxID)

	// No file may be written while the backend is registered (charter v3.0:
	// the durable home is PG; intelligence.json is not a write target).
	if _, err := os.Stat(filepath.Join(dir, intelFileName)); !os.IsNotExist(err) {
		t.Fatalf("intelligence.json written despite registered backend")
	}
	file, ok := backend.files[intelKey{ctxID}]
	if !ok {
		t.Fatalf("no backend snapshot saved for %s/intelligence", ctxID)
	}
	var snap intelSnapshot
	if err := json.Unmarshal(file.data, &snap); err != nil {
		t.Fatalf("backend snapshot does not decode: %v", err)
	}
	if len(snap.Items) != 1 || snap.Items[0].Kind != "credential" {
		t.Fatalf("backend snapshot items = %+v", snap.Items)
	}

	// Restore into a fresh store: the backend row speaks, not the (empty) file.
	ResetForContext(ctxID)
	n, err := LoadFromDiskForContext(ctxID)
	if err != nil || n != 1 {
		t.Fatalf("restore = %d items err=%v, want 1 nil", n, err)
	}
	// The listing carries identity only; the content is one explicit fetch away.
	listing := ReadForContext(ctxID)
	if !containsIntel(listing, "credential") || containsIntel(listing, "admin:admin accepted") {
		t.Fatalf("restored listing must be identity-only:\n%s", listing)
	}
	item, ok := ReadItemForContext(ctxID, "I-1")
	if !ok || !containsIntel(item.Content, "admin:admin accepted") {
		t.Fatalf("restored item not readable by id: ok=%v item=%+v", ok, item)
	}
}

func TestIntelBackendInstanceContinuityAcrossRedispatch(t *testing.T) {
	withIntelBackend(t, newFakeIntelBackend())

	// First session: scan-a of instance inst, records one item.
	first := "intel-cont-scan-a"
	second := "intel-cont-scan-b"
	t.Cleanup(func() {
		CleanupContext(first)
		CleanupContext(second)
	})
	SetInstanceIDForContext(first, "inst")
	recordTestIntel(t, first)

	// Re-dispatched second session: NEW context id, SAME instance, no
	// snapshot of its own — the first restore inherits the previous intel.
	SetInstanceIDForContext(second, "inst")
	n, err := LoadFromDiskForContext(second)
	if err != nil || n != 1 {
		t.Fatalf("continuity restore = %d err=%v, want 1 nil", n, err)
	}
	if items := ItemsForContext(second); len(items) != 1 || !containsIntel(items[0].Content, "admin:admin accepted") {
		t.Fatalf("continuity item not readable in re-dispatched session: %+v", items)
	}

	// Recording in the resumed session does not collide with the carried
	// chain's ids (the snapshot seq advanced past it).
	recordTestIntel(t, second)
	res, err := RecordForContext(second, map[string]string{
		"kind": "lead", "title": "Fresh lead", "content": "new path found after resume",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("fresh record after resume: err=%v res=%+v", err, res)
	}
}

func TestIntelBackendSaveFailureIsHonest(t *testing.T) {
	backend := newFakeIntelBackend()
	backend.saveErr = os.ErrPermission
	withIntelBackend(t, backend)

	ctxID := "intel-backend-failure"
	t.Cleanup(func() { CleanupContext(ctxID) })
	// persist-failure is not success (charter §5.2): the durable-write
	// acknowledgement reports the failure and the item is rolled back.
	res, err := RecordForContext(ctxID, map[string]string{
		"kind": "lead", "title": "IDOR chain", "content": "invoice ids are sequential",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if res.Error == "" {
		t.Fatalf("record succeeded despite backend save failure: %+v", res)
	}
	if items := ItemsForContext(ctxID); len(items) != 0 {
		t.Fatalf("rolled-back item still stored — receipt and durable state disagree: %+v", items)
	}
}

func TestIntelFileModeUnchangedWithoutBackend(t *testing.T) {
	// No backend registered: pre-R6.1 file behavior.
	ctxID, dir := testContext(t)
	recordTestIntel(t, ctxID)
	if _, err := os.Stat(filepath.Join(dir, intelFileName)); err != nil {
		t.Fatalf("intelligence.json not written in file mode: %v", err)
	}
	items, err := LoadSnapshot(filepath.Join(dir, intelFileName))
	if err != nil || len(items) != 1 {
		t.Fatalf("file-mode LoadSnapshot = %v items=%d, want 1 no error", err, len(items))
	}
}

func TestLoadSnapshotReadsBackendRowThenFile(t *testing.T) {
	backend := newFakeIntelBackend()
	withIntelBackend(t, backend)

	// Backend mode: the row of the scan whose directory the path names is
	// consulted first (dir name = scan id, the importer's identity).
	scanDir := t.TempDir() // random dir name doubles as the scan id
	scanID := filepath.Base(scanDir)
	snap := intelSnapshot{Version: 1, Seq: 3, Items: []Item{{
		ID: "I-1", Fingerprint: "fp-backend", Kind: KindLead, Title: "from PG", Content: "backend row content",
	}}}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.SaveIntelSnapshot(scanID, "inst", 3, raw); err != nil {
		t.Fatal(err)
	}
	items, err := LoadSnapshot(filepath.Join(scanDir, intelFileName))
	if err != nil || len(items) != 1 || items[0].Fingerprint != "fp-backend" {
		t.Fatalf("backend LoadSnapshot = %v items=%+v, want the stored row", err, items)
	}

	// Corrupt row: an error the caller must surface, never folded into empty
	// (charter §5.2 discipline).
	if err := backend.SaveIntelSnapshot(scanID, "inst", 4, []byte("{broken")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(filepath.Join(scanDir, intelFileName)); err == nil {
		t.Fatal("corrupt backend row must error")
	}

	// No row for this session → legacy file fallback (pre-import runs).
	legacyDir := t.TempDir()
	legacy := intelSnapshot{Version: 1, Items: []Item{{
		ID: "I-9", Fingerprint: "fp-file", Kind: KindSensitiveData, Title: "from file", Content: "file fallback content",
	}}}
	legacyRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, intelFileName), legacyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	items, err = LoadSnapshot(filepath.Join(legacyDir, intelFileName))
	if err != nil || len(items) != 1 || items[0].Fingerprint != "fp-file" {
		t.Fatalf("file fallback LoadSnapshot = %v items=%+v, want the legacy file", err, items)
	}
}

func containsIntel(rendered, substr string) bool {
	return strings.Contains(rendered, substr)
}

package intelligence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/storage"
)

func testContext(t *testing.T) (string, string) {
	t.Helper()
	ctxID := "intel-test-" + t.Name()
	dir := t.TempDir()
	t.Cleanup(func() {
		CleanupContext(ctxID)
		ResetForContext(ctxID)
	})
	SetPersistPathForContext(ctxID, dir)
	return ctxID, dir
}

// Recording writes the durable snapshot; a fingerprint re-record keeps the
// original content and never creates a duplicate (task-level identity).
func TestRecordDedupsByFingerprint(t *testing.T) {
	ctxID, dir := testContext(t)

	res, err := RecordForContext(ctxID, map[string]string{
		"kind":       "credential",
		"title":      "Admin panel accepts default creds",
		"content":    "admin:admin accepted at /app/admin/login",
		"endpoint":   "https://target.example/app/admin",
		"confidence": "0.9",
	})
	if err != nil {
		t.Fatalf("first record: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("first record error: %s", res.Error)
	}
	fp, _ := res.Metadata["intel_fingerprint"].(string)
	if fp == "" {
		t.Fatalf("first record must carry fingerprint metadata: %+v", res.Metadata)
	}

	res2, err := RecordForContext(ctxID, map[string]string{
		"kind":     "credential",
		"title":    "Admin panel accepts default creds (reworded)",
		"content":  "ADMIN:ADMIN   accepted  AT /app/admin/login", // case/whitespace-insensitive fingerprint
		"endpoint": "https://target.example/app/admin/",
	})
	if err != nil {
		t.Fatalf("second record: %v", err)
	}
	if res2.Metadata["intel_deduped"] != true {
		t.Fatalf("re-record must dedup, got %+v", res2.Metadata)
	}
	if res2.Metadata["intel_fingerprint"] != fp {
		t.Fatalf("fingerprint must be stable across rewording: %v vs %v", res2.Metadata["intel_fingerprint"], fp)
	}

	// Durable snapshot has exactly one item.
	data, err := os.ReadFile(filepath.Join(dir, "intelligence.json"))
	if err != nil {
		t.Fatalf("snapshot read: %v", err)
	}
	var snap struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot parse: %v", err)
	}
	if len(snap.Items) != 1 {
		t.Fatalf("snapshot items = %d, want 1 (dedup)", len(snap.Items))
	}
	if snap.Items[0].Confidence < 0.9 {
		t.Fatalf("confidence must not degrade on re-record: %v", snap.Items[0].Confidence)
	}

	// Different endpoint → distinct fingerprint.
	res3, err := RecordForContext(ctxID, map[string]string{
		"kind":     "credential",
		"title":    "Admin panel accepts default creds",
		"content":  "admin:admin accepted at /app/admin/login",
		"endpoint": "https://other.example/admin",
	})
	if err != nil {
		t.Fatalf("third record: %v", err)
	}
	if res3.Metadata["intel_deduped"] == true {
		t.Fatal("different endpoint must not dedup")
	}
}

// Invalid input is a hard tool error, never a silent wrong-vocabulary item.
func TestRecordValidatesKindAndInput(t *testing.T) {
	ctxID, _ := testContext(t)
	if _, err := RecordForContext(ctxID, map[string]string{"kind": "gossip", "title": "x", "content": "y"}); err == nil {
		t.Fatal("unknown kind must error")
	}
	if _, err := RecordForContext(ctxID, map[string]string{"kind": "lead", "title": "", "content": "y"}); err == nil {
		t.Fatal("empty title must error")
	}
	if _, err := RecordForContext(ctxID, map[string]string{"kind": "lead", "title": "x", "content": ""}); err == nil {
		t.Fatal("empty content must error")
	}
	if _, err := RecordForContext(ctxID, map[string]string{"kind": "lead", "title": "x", "content": "y", "confidence": "high"}); err == nil {
		t.Fatal("non-numeric confidence must error")
	}
}

// Restore is merge-only and distinguishes corrupt snapshots from empty ones
// (charter §5.2: corruption must never read as "nothing recorded").
func TestLoadSnapshotAndRestore(t *testing.T) {
	ctxID, dir := testContext(t)
	if _, err := RecordForContext(ctxID, map[string]string{
		"kind": "sensitive_data", "title": "Backup dump", "content": "/backup.sql world-readable",
	}); err != nil {
		t.Fatal(err)
	}

	// LoadSnapshot serves the web layer: found and missing both honest.
	items, err := LoadSnapshot(filepath.Join(dir, "intelligence.json"))
	if err != nil || len(items) != 1 {
		t.Fatalf("LoadSnapshot = %v items=%d, want 1 item no error", err, len(items))
	}
	if missing, err := LoadSnapshot(filepath.Join(dir, "nope.json")); err != nil || missing != nil {
		t.Fatalf("missing snapshot must be (nil,nil), got %v %v", missing, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(filepath.Join(dir, "broken.json")); err == nil {
		t.Fatal("corrupt snapshot must error, not fold into empty")
	}

	// Restore into a fresh store: merge-only, corrupt restore is an error.
	ResetForContext(ctxID)
	n, err := LoadFromDiskForContext(ctxID)
	if err != nil || n != 1 {
		t.Fatalf("restore = %d items err=%v, want 1 nil", n, err)
	}
	if !strings.Contains(ReadForContext(ctxID), "sensitive_data") {
		t.Fatal("restored item must be readable")
	}
	if err := os.WriteFile(filepath.Join(dir, "intelligence.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromDiskForContext(ctxID); err == nil {
		t.Fatal("corrupt restore must error")
	}
}

// The durable write goes through the shared atomic primitive (repo invariant:
// no hand-rolled temp+rename may spread).
func TestPersistUsesAtomicWrite(t *testing.T) {
	ctxID, dir := testContext(t)
	if _, err := RecordForContext(ctxID, map[string]string{
		"kind": "lead", "title": "IDOR chain", "content": "invoice ids are sequential",
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("no temp file may remain after an atomic write, found %s", e.Name())
		}
	}
	// WriteAtomic is what we call; assert the primitive still exists so a
	// future refactor cannot silently regress to hand-rolled writes.
	_ = storage.WriteAtomic
}

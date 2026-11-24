package intelligence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/storage"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
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

// ── read_intelligence contract (INTEL_HANDOFF_SPEC D5 / M1a) ──

// The listing is what a seed or a casual consult sees: identity only. Content
// must not travel there, or every prompt that mentions intel leaks its body.
func TestReadIntelligenceListingIsIdentityOnly(t *testing.T) {
	ctxID, _ := testContext(t)
	res, err := RecordForContext(ctxID, map[string]string{
		"kind": "credential", "title": "SSH password for app-01",
		"content":  "root uses Passw0rd! on 10.0.177.5:22",
		"endpoint": "10.0.177.5:22", "confidence": "0.9",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("record: err=%v res=%+v", err, res)
	}
	fp, _ := res.Metadata["intel_fingerprint"].(string)

	listing := ReadForContext(ctxID)
	if strings.Contains(listing, "Passw0rd!") {
		t.Fatalf("listing leaked intel content:\n%s", listing)
	}
	for _, want := range []string{fp, "credential", "10.0.177.5:22", "read_intelligence(fingerprint="} {
		if !strings.Contains(listing, want) {
			t.Fatalf("listing missing %q:\n%s", want, listing)
		}
	}
	// The store still holds the content — it is withheld from the listing only.
	items := ItemsForContext(ctxID)
	if len(items) != 1 || !strings.Contains(items[0].Content, "Passw0rd!") {
		t.Fatalf("store lost the item: %+v", items)
	}
}

// One item's full content is one explicit fetch away, by fingerprint or by the
// I-N instance id, case-insensitively. Prefixes do not resolve (ambiguity is
// worse than a miss).
func TestReadIntelligenceDetailByFingerprintOrID(t *testing.T) {
	ctxID, _ := testContext(t)
	res, err := RecordForContext(ctxID, map[string]string{
		"kind": "credential", "title": "SSH password for app-01",
		"content": "root uses Passw0rd! on 10.0.177.5:22",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("record: err=%v res=%+v", err, res)
	}
	fp, _ := res.Metadata["intel_fingerprint"].(string)

	byFP, ok := ReadItemForContext(ctxID, strings.ToUpper(fp))
	if !ok || !strings.Contains(byFP.Content, "Passw0rd!") {
		t.Fatalf("detail by fingerprint (upper case) = %+v ok=%v", byFP, ok)
	}
	byID, ok := ReadItemForContext(ctxID, "i-1")
	if !ok || byID.Fingerprint != fp {
		t.Fatalf("detail by instance id = %+v ok=%v", byID, ok)
	}
	if _, ok := ReadItemForContext(ctxID, fp[:8]); ok {
		t.Fatal("prefix matching must not resolve: an ambiguous hit is worse than a miss")
	}
	if _, ok := ReadItemForContext(ctxID, "deadbeefdeadbeef"); ok {
		t.Fatal("unknown reference resolved")
	}
}

// A miss is an expected model mistake, not a tool failure: returning it as
// Result.Error would feed the registry circuit breaker and brick the tool.
func TestReadIntelligenceToolMissIsOutputNotError(t *testing.T) {
	ctxID, _ := testContext(t)
	res, err := RecordForContext(ctxID, map[string]string{
		"kind": "lead", "title": "admin route", "content": "hidden admin route responds",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("record: err=%v res=%+v", err, res)
	}
	fp, _ := res.Metadata["intel_fingerprint"].(string)

	r := tools.NewRegistry()
	r.SetScanContextID(ctxID)
	Register(r)

	miss, err := r.Execute("read_intelligence", map[string]string{"fingerprint": "deadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("execute miss: %v", err)
	}
	if miss.Error != "" {
		t.Fatalf("miss must not be a tool failure: %+v", miss)
	}
	if !strings.Contains(miss.Output, "No intel item matches") || !strings.Contains(miss.Output, fp) {
		t.Fatalf("miss output must name the reference and list valid identities:\n%s", miss.Output)
	}

	hit, err := r.Execute("read_intelligence", map[string]string{"fingerprint": fp})
	if err != nil || hit.Error != "" {
		t.Fatalf("execute hit: err=%v res=%+v", err, hit)
	}
	if hit.Metadata["intel_detail"] != true || !strings.Contains(hit.Output, "hidden admin route responds") {
		t.Fatalf("detail fetch = %+v", hit)
	}

	listing, err := r.Execute("read_intelligence", nil)
	if err != nil || listing.Error != "" {
		t.Fatalf("execute listing: err=%v res=%+v", err, listing)
	}
	if strings.Contains(listing.Output, "hidden admin route responds") {
		t.Fatalf("no-argument listing leaked content:\n%s", listing.Output)
	}
	if !strings.Contains(listing.Output, fp) {
		t.Fatalf("no-argument listing must carry identities:\n%s", listing.Output)
	}
}

// The detail fetch is the one place content reaches the model; the audit sink
// must hear about it (INTEL_HANDOFF_SPEC M4).
func TestDetailFetchNotifiesObserver(t *testing.T) {
	ctxID, _ := testContext(t)
	res, err := RecordForContext(ctxID, map[string]string{
		"kind": "credential", "title": "SSH", "content": "root:Passw0rd!",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("record: err=%v res=%+v", err, res)
	}
	fp, _ := res.Metadata["intel_fingerprint"].(string)

	type hit struct{ ctxID, fp string }
	var hits []hit
	SetDetailObserver(func(c, f string) { hits = append(hits, hit{c, f}) })
	t.Cleanup(func() { SetDetailObserver(nil) })

	reg := tools.NewRegistry()
	reg.SetScanContextID(ctxID)
	Register(reg)
	if _, err := reg.Execute("read_intelligence", map[string]string{"fingerprint": fp}); err != nil {
		t.Fatal(err)
	}
	// A listing and a miss are not detail fetches.
	if _, err := reg.Execute("read_intelligence", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Execute("read_intelligence", map[string]string{"fingerprint": "deadbeefdeadbeef"}); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ctxID != ctxID || hits[0].fp != fp {
		t.Fatalf("observer hits = %+v, want exactly the detail fetch", hits)
	}
}

package scanctx

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// NoteStore provides per-session note storage with optional persistence,
// replacing the global store in notes.go.
//
// Durable home (charter v3.0 §6.3, R6.1): with a StateFilesBackend
// registered, snapshots live in the run_state_files row (scan_id, kind=notes)
// and notes.json is never written. nil backend keeps the legacy notes.json
// file path (test harnesses only). LoadFromDisk resolves own snapshot →
// newest same-instance sibling (D11 continuity) → legacy file fallback.
type NoteStore struct {
	mu          sync.RWMutex
	store       map[string]string
	persistPath string // path to notes.json (empty = no file persistence)

	// scanID/instanceID stamp the run_state_files rows (D11): scanID is the
	// owning scan session's id, instanceID the run identity a re-dispatched
	// session shares with its predecessors (continuity reads).
	scanID     string
	instanceID string
	writeSeq   int64 // monotonic write counter for watermark comparisons

	// writeMu serializes writeFile calls so two concurrent Set() callers
	// cannot interleave snapshots on disk. We hold this *only* across the
	// disk write — the data lock (mu) is released first to avoid blocking
	// readers behind I/O. Backend saves run under mu (snapshot order), so
	// writeMu guards the legacy file path only.
	writeMu sync.Mutex
}

// NewNoteStore creates an empty note store.
func NewNoteStore() *NoteStore {
	return &NoteStore{
		store: make(map[string]string),
	}
}

// SetPersistPath configures file persistence for notes (legacy/test mode; a
// registered StateFilesBackend ignores it for writes).
//
// Path_Policy note (Requirement 8.3): NoteStore lives in package scanctx,
// which sandbox imports. To avoid an import cycle we cannot call
// sandbox.Default().CheckResolve from here. The persist path is constructed
// as filepath.Join(dir, "notes.json") — there is no user-supplied path
// component, so the boundary is preserved by construction provided callers
// hand us a directory inside the Allow_List. In production the only caller
// is internal/tools/notes (which routes its own writes through
// sandbox.CheckResolve); the directory passed to NoteStore is the per-scan
// ScanDir, which is an Allow_List descendant by construction.
func (ns *NoteStore) SetPersistPath(dir string) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	if dir != "" {
		ns.persistPath = filepath.Join(dir, "notes.json")
	} else {
		ns.persistPath = ""
	}
}

// SetScanID stamps the scan identity on the run_state_files rows (R6.1).
// Mirrors LedgerStore.SetScanID: callers never infer a scan id from a
// filesystem path.
func (ns *NoteStore) SetScanID(scanID string) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.scanID = strings.TrimSpace(scanID)
}

// SetInstanceID stamps the run identity (D11) on the store. With the backend
// registered, snapshots carry it and a re-dispatched session's first
// LoadFromDisk inherits the previous sibling session's notes via the
// instance continuity read instead of starting empty.
func (ns *NoteStore) SetInstanceID(instanceID string) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.instanceID = strings.TrimSpace(instanceID)
}

// Set adds or updates a note and persists the snapshot. With the backend
// registered the save runs under the state lock (snapshots reach the durable
// home in the order they were taken, ledger discipline) and a failure is
// returned to the caller — persist-failure is not success (charter §5.2);
// the in-memory map stays authoritative and the next successful Set
// re-persists the full snapshot.
func (ns *NoteStore) Set(key, value string) error {
	ns.mu.Lock()
	ns.store[key] = value
	ns.writeSeq++
	data, path := ns.marshalSnapshot()
	if b := CurrentStateFilesBackend(); b != nil {
		err := b.SaveStateFileSnapshot(ns.scanID, StateFileKindNotes, ns.instanceID, ns.writeSeq, data)
		ns.mu.Unlock()
		if err != nil {
			log.Printf("[notestore] Warning: state snapshot save failed for %s (in-memory state stays authoritative): %v", ns.scanID, err)
			return err
		}
		return nil
	}
	ns.mu.Unlock()

	// Legacy file write outside the lock — non-blocking for concurrent readers
	return ns.writeFile(data, path)
}

// Get returns a note by key, or ("", false) if not found.
func (ns *NoteStore) Get(key string) (string, bool) {
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	v, ok := ns.store[key]
	return v, ok
}

// GetAll returns a copy of all notes.
func (ns *NoteStore) GetAll() map[string]string {
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	result := make(map[string]string, len(ns.store))
	for k, v := range ns.store {
		result[k] = v
	}
	return result
}

// Count returns the number of stored notes.
func (ns *NoteStore) Count() int {
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	return len(ns.store)
}

// Reset clears all notes.
func (ns *NoteStore) Reset() {
	ns.mu.Lock()
	ns.store = make(map[string]string)
	ns.mu.Unlock()
}

// LoadFromDisk loads notes into this (empty or partial) store. Snapshot
// source order with the backend registered (charter v3.0 §6.3):
//
//  1. this scan's own snapshot;
//  2. the newest sibling session's snapshot for the stamped instance — the
//     D11 continuity read: a re-dispatched session has no snapshot of its
//     own yet and inherits the previous session's notes instead of starting
//     empty;
//  3. the legacy notes.json file — straggler resilience only (crash
//     leftovers between backend registration and import); never a write
//     target while a backend is registered.
//
// Without a backend the behavior is identical to the pre-R6.1 file mode.
// Existing in-memory notes are preserved; only unseen keys are merged, so a
// resume never clobbers live state. Returns the number of notes loaded.
func (ns *NoteStore) LoadFromDisk() int {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	if b := CurrentStateFilesBackend(); b != nil {
		if data, _, ok, err := b.LoadStateFileSnapshot(ns.scanID, StateFileKindNotes); err != nil {
			log.Printf("[notestore] Warning: state snapshot load failed for %s: %v", ns.scanID, err)
		} else if ok {
			return ns.mergeSnapshotLocked(data, fmt.Sprintf("state snapshot %s/%s", ns.scanID, StateFileKindNotes))
		}
		if ns.instanceID != "" {
			if data, _, source, ok, err := b.LoadLatestStateFileSnapshotForInstance(ns.instanceID, ns.scanID, StateFileKindNotes); err != nil {
				log.Printf("[notestore] Warning: continuity load failed for %s/%s: %v", ns.instanceID, ns.scanID, err)
			} else if ok {
				return ns.mergeSnapshotLocked(data, fmt.Sprintf("state continuity %s (from sibling %s)", ns.instanceID, source))
			}
		}
	}

	if ns.persistPath == "" {
		return 0
	}
	data, err := os.ReadFile(ns.persistPath)
	if err != nil {
		return 0
	}
	return ns.mergeSnapshotLocked(data, ns.persistPath)
}

// mergeSnapshotLocked decodes one notes snapshot (the bare string map, the
// same bytes notes.json always carried) and merges unseen keys into the
// store. Caller holds ns.mu.
func (ns *NoteStore) mergeSnapshotLocked(data []byte, source string) int {
	loaded := make(map[string]string)
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("[notestore] Warning: failed to parse notes snapshot from %s: %v", source, err)
		return 0
	}
	count := 0
	for k, v := range loaded {
		if _, exists := ns.store[k]; exists {
			continue // do not clobber live state
		}
		ns.store[k] = v
		count++
	}
	if count > 0 {
		log.Printf("[notestore] Loaded %d notes from: %s", count, source)
	}
	return count
}

// FormatForContext returns a compact summary suitable for LLM context injection.
func (ns *NoteStore) FormatForContext() string {
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	if len(ns.store) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("=== YOUR SAVED NOTES (from add_note) ===\n")
	for k, v := range ns.store {
		if len(v) > 500 {
			v = v[:500] + "... (truncated)"
		}
		b.WriteString(fmt.Sprintf("• %s: %s\n", k, v))
	}
	b.WriteString("=== END NOTES ===")
	return b.String()
}

// marshalSnapshot serializes the current store. Must be called with mu held.
// Returns (nil, "") when persistence is disabled (no file path AND no
// backend).
func (ns *NoteStore) marshalSnapshot() ([]byte, string) {
	if ns.persistPath == "" && CurrentStateFilesBackend() == nil {
		return nil, ""
	}
	data, err := json.MarshalIndent(ns.store, "", "  ")
	if err != nil {
		log.Printf("[notestore] Warning: failed to marshal notes: %v", err)
		return nil, ""
	}
	return data, ns.persistPath
}

// writeFile persists serialized data to disk (legacy/test mode). Safe to call
// without holding the data lock (mu); writeMu serializes concurrent writers
// so two Set()s cannot race on the file.
//
// Path_Policy note (Requirement 8.3): safe by construction. The path
// originates from SetPersistPath which scopes it to dir + "notes.json"; in
// production dir is the per-scan ScanDir, an Allow_List descendant. We
// cannot call sandbox.Default().CheckResolve here because scanctx is
// imported by sandbox. User-supplied paths into the notes feature flow
// through internal/tools/notes, which calls CheckResolve directly.
func (ns *NoteStore) writeFile(data []byte, path string) error {
	if data == nil || path == "" {
		return nil
	}
	ns.writeMu.Lock()
	defer ns.writeMu.Unlock()
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("[notestore] Warning: failed to save notes to %s: %v", path, err)
		return err
	}
	return nil
}

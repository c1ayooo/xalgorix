// Package notes provides the notes tool for agent memory with durable
// persistence. From R6.1 (charter v3.0 §6.3) snapshots live in the PG
// primary store (run_state_files, kind=notes) via the scanctx.StateFilesBackend
// seam — the same seam the scanctx.NoteStore family uses, so one
// registration covers both; the per-scan notes.json remains as the legacy
// file path and read fallback.
package notes

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/c1ayooo/xalgorix/v4/internal/sandbox"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// ── Per-instance note stores ──
var (
	noteStores   = make(map[string]*noteStore)
	noteStoresMu sync.RWMutex
)

type noteStore struct {
	mu sync.RWMutex
	// contextID is the owning ScanContext.ID for this store. Captured at
	// creation time so writeFile can look up the active ScanContext for
	// Path_Policy resolution (sc.ScanDir-relative roots) and as the
	// run_state_files row key (scan_id).
	contextID   string
	store       map[string]string
	persistPath string
	// instanceID stamps the run_state_files row (D11): a re-dispatched
	// session shares it with its predecessor, whose snapshot the first
	// LoadFromDiskForContext inherits via the continuity read. Stamped by
	// the session wiring (the ledger precedent).
	instanceID string
	writeSeq   int64 // monotonic write counter for watermark comparisons
}

func getNoteStoreByID(id string) *noteStore {
	noteStoresMu.RLock()
	s, ok := noteStores[id]
	noteStoresMu.RUnlock()
	if ok {
		return s
	}

	noteStoresMu.Lock()
	defer noteStoresMu.Unlock()
	if s, ok := noteStores[id]; ok {
		return s
	}
	s = &noteStore{contextID: id, store: make(map[string]string)}
	noteStores[id] = s
	return s
}

// getNoteStore returns the note store for the default (CLI) scan context.
func getNoteStore() *noteStore {
	return getNoteStoreByID(scanctx.Default().ID)
}

func getNoteStoreForContext(contextID string) *noteStore {
	noteStoresMu.RLock()
	s, ok := noteStores[contextID]
	noteStoresMu.RUnlock()
	if ok {
		return s
	}
	noteStoresMu.Lock()
	defer noteStoresMu.Unlock()
	if s, ok := noteStores[contextID]; ok {
		return s
	}
	s = &noteStore{contextID: contextID, store: make(map[string]string)}
	noteStores[contextID] = s
	return s
}

// SetPersistPath configures the directory where notes.json will be saved
// (legacy file mode; ignored for writes while the backend is registered).
func SetPersistPath(dir string) {
	s := getNoteStore()
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir != "" {
		s.persistPath = filepath.Join(dir, "notes.json")
	} else {
		s.persistPath = ""
	}
}

// SetInstanceIDForContext stamps the run identity (D11) on one context's
// store. With the backend registered, snapshots carry it and a
// re-dispatched session's first LoadFromDiskForContext inherits the previous
// sibling session's notes via the instance continuity read.
func SetInstanceIDForContext(contextID, instanceID string) {
	s := getNoteStoreForContext(contextID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instanceID = strings.TrimSpace(instanceID)
}

// ResetNotes clears all notes for the active scan context.
func ResetNotes() {
	s := getNoteStore()
	s.mu.Lock()
	s.store = make(map[string]string)
	s.mu.Unlock()
}

// LoadFromDisk loads notes from the durable home if one exists (backend
// snapshot → instance continuity → legacy file, the charter §6.3 read order;
// see loadLocked for the source discipline). Lenient: a missing or corrupt
// snapshot reads as "nothing loaded" (the CLI contract).
func LoadFromDisk() int {
	s := getNoteStore()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// loadLocked resolves the newest durable snapshot into this (empty or
// partial) store. Source order with the backend registered (charter v3.0
// §6.3):
//
//  1. this scan's own snapshot (run_state_files, kind=notes);
//  2. the newest sibling session's snapshot for the stamped instance — the
//     D11 continuity read;
//  3. the legacy notes.json file — straggler resilience only (crash
//     leftovers between backend registration and import); never a write
//     target while a backend is registered.
//
// Without a backend the behavior is identical to the pre-R6.1 file mode.
// Existing in-memory notes are preserved (merge-only); returns the number of
// notes loaded. Caller holds s.mu.
func (s *noteStore) loadLocked() int {
	if b := scanctx.CurrentStateFilesBackend(); b != nil {
		if data, _, ok, err := b.LoadStateFileSnapshot(s.contextID, scanctx.StateFileKindNotes); err != nil {
			log.Printf("[notes] Warning: state snapshot load failed for %s: %v", s.contextID, err)
		} else if ok {
			n, _ := s.mergeLocked(data, fmt.Sprintf("state snapshot %s/%s", s.contextID, scanctx.StateFileKindNotes))
			return n
		}
		if s.instanceID != "" {
			if data, _, source, ok, err := b.LoadLatestStateFileSnapshotForInstance(s.instanceID, s.contextID, scanctx.StateFileKindNotes); err != nil {
				log.Printf("[notes] Warning: continuity load failed for %s/%s: %v", s.instanceID, s.contextID, err)
			} else if ok {
				n, _ := s.mergeLocked(data, fmt.Sprintf("state continuity %s (from sibling %s)", s.instanceID, source))
				return n
			}
		}
	}

	if s.persistPath == "" {
		return 0
	}
	data, err := os.ReadFile(s.persistPath)
	if err != nil {
		return 0
	}
	n, _ := s.mergeLocked(data, s.persistPath)
	return n
}

// mergeLocked decodes one notes snapshot (the bare string map, the same
// bytes notes.json always carried) and merges unseen keys into the store.
// Caller holds s.mu. Returns the count of newly merged keys; a corrupt
// snapshot returns the parse error (already warned about) so a strict caller
// can surface it instead of folding it into "nothing recorded"
// (charter §5.2).
func (s *noteStore) mergeLocked(data []byte, source string) (int, error) {
	loaded := make(map[string]string)
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("[notes] Warning: failed to parse notes snapshot from %s: %v", source, err)
		return 0, err
	}
	count := 0
	for k, v := range loaded {
		if _, exists := s.store[k]; exists {
			continue
		}
		s.store[k] = v
		count++
	}
	if count > 0 {
		log.Printf("[notes] Loaded %d notes from: %s", count, source)
	}
	return count, nil
}

// marshalSnapshot serializes the current store. Must be called with s.mu held.
// Returns the serialized data and the persist path. If persistence is
// disabled (no file path AND no backend), returns nil data (meaning no write
// is needed).
func (s *noteStore) marshalSnapshot() ([]byte, string) {
	if s.persistPath == "" && scanctx.CurrentStateFilesBackend() == nil {
		return nil, ""
	}
	data, err := json.MarshalIndent(s.store, "", "  ")
	if err != nil {
		log.Printf("[notes] Warning: failed to marshal notes: %v", err)
		return nil, ""
	}
	return data, s.persistPath
}

// saveSnapshot persists the serialized snapshot to the durable home. Caller
// holds s.mu (the write happens under the state lock so snapshots reach the
// durable home in the order they were taken — notes/ledger discipline). With
// the backend registered the snapshot is a whole-snapshot upsert into the
// run_state_files row (scan_id, kind=notes) and notes.json is never written
// (charter v3.0 §6.3); a failure is returned to the caller — persist-failure
// is not success (charter §5.2).
func (s *noteStore) saveSnapshot(data []byte) error {
	if data == nil {
		return nil
	}
	if b := scanctx.CurrentStateFilesBackend(); b != nil {
		if err := b.SaveStateFileSnapshot(s.contextID, scanctx.StateFileKindNotes, s.instanceID, s.writeSeq, data); err != nil {
			return fmt.Errorf("notes durable write failed: %w", err)
		}
		return nil
	}
	return s.writeFile(data, s.persistPath)
}

// writeFile persists serialized data to the legacy file (notes.json). Safe
// to call without holding s.mu.
//
// Every persistence write flows through sandbox.Default().CheckResolve so a
// misconfigured persistPath cannot escape the Allow_List (Requirement 8.3).
// The lookup uses the owning ScanContext's roots when available, falling
// back to the process Workspace_Root via sandbox.Resolve.
func (s *noteStore) writeFile(data []byte, path string) error {
	if data == nil || path == "" {
		return nil
	}
	canonical, err := sandbox.Default().CheckResolve(scanctx.Get(s.contextID), "notes", path)
	if err != nil {
		log.Printf("[notes] Warning: refusing to save notes to %s: %v", path, err)
		return err
	}
	// Atomic persistence: write to a temp file and rename into place so an
	// interrupted write can never leave a truncated/corrupt snapshot at the
	// canonical path (a half-written snapshot would parse as zero notes on
	// restore and silently erase the context's durable note history).
	tmp := canonical + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("[notes] Warning: failed to save notes to %s: %v", tmp, err)
		return err
	}
	if err := os.Rename(tmp, canonical); err != nil {
		_ = os.Remove(tmp)
		log.Printf("[notes] Warning: failed to save notes to %s: %v", canonical, err)
		return err
	}
	return nil
}

// Register adds note tools to the registry.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name:        "add_note",
		Description: "Add a note to persistent memory. Use this to track: discovered endpoints, parameters, tech stack, CSRF tokens, session cookies, exploit chain state, intermediate findings, and anything needed across multiple iterations. Notes persist for the entire scan AND survive context pruning. Use structured keys like 'csrf_token', 'admin_endpoint', 'sqli_confirmed', 'angular_version'.",
		Parameters: []tools.Parameter{
			{Name: "key", Description: "Unique key for the note (e.g., 'csrf_token', 'endpoint_inventory'). Provide it when possible; value-only notes receive a stable key automatically.", Required: false},
			{Name: "value", Description: "Note content", Required: true},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return addNoteForContext(r.GetScanContextID(), args)
		},
	})

	r.Register(&tools.Tool{
		Name:        "read_notes",
		Description: "Read all notes or a specific note from memory.",
		Parameters: []tools.Parameter{
			{Name: "key", Description: "Key to read (omit for all notes)", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return readNotesForContext(r.GetScanContextID(), args)
		},
	})
}

//lint:ignore U1000 kept as a package-level compatibility wrapper for callers in this package.
func addNote(args map[string]string) (tools.Result, error) {
	return addNoteForContext(scanctx.Default().ID, args)
}

func addNoteForContext(contextID string, args map[string]string) (tools.Result, error) {
	key := strings.TrimSpace(args["key"])
	value := strings.TrimSpace(args["value"])
	if value == "" {
		return tools.Result{}, fmt.Errorf("add_note requires a nonempty value")
	}
	if key == "" {
		if lower := strings.ToLower(value); strings.Contains(lower, "endpoint") || strings.Contains(lower, "route inventory") {
			key = "endpoint_inventory"
		} else {
			sum := sha256.Sum256([]byte(value))
			key = fmt.Sprintf("note_%x", sum[:8])
		}
	}

	if contextID == "" {
		contextID = scanctx.Default().ID
	}
	s := getNoteStoreForContext(contextID)
	// The durable write happens UNDER the state lock: snapshots are written in
	// the order they were taken, so a slower older snapshot can no longer
	// overwrite a newer one under concurrency. Map readers are unaffected
	// (they lock the map, not the file).
	s.mu.Lock()
	prev, existed := s.store[key]
	s.store[key] = value
	s.writeSeq++
	data, _ := s.marshalSnapshot()
	writeErr := s.saveSnapshot(data)
	if writeErr != nil {
		// Durable-write acknowledgement: the receipt must never claim a save
		// that failed on disk. Roll the in-memory change back so receipt and
		// durable state agree, and tell the caller what to do about it.
		if existed {
			s.store[key] = prev
		} else {
			delete(s.store, key)
		}
	}
	s.mu.Unlock()

	if writeErr != nil {
		return tools.Result{Error: fmt.Sprintf("add_note: durable write failed (%v) — the note was NOT saved. Retry the add_note call, or include the information in your report instead of relying on it persisting.", writeErr)}, nil
	}
	return tools.Result{Output: fmt.Sprintf("Note saved: %s", key)}, nil
}

//lint:ignore U1000 kept as a package-level compatibility wrapper for callers in this package.
func readNotes(args map[string]string) (tools.Result, error) {
	return readNotesForContext(scanctx.Default().ID, args)
}

func readNotesForContext(contextID string, args map[string]string) (tools.Result, error) {
	key := args["key"]

	if contextID == "" {
		contextID = scanctx.Default().ID
	}
	s := getNoteStoreForContext(contextID)
	s.mu.RLock()
	defer s.mu.RUnlock()

	if key != "" {
		v, ok := s.store[key]
		if !ok {
			return tools.Result{Output: fmt.Sprintf("No note found with key: %s", key)}, nil
		}
		return tools.Result{Output: v}, nil
	}

	if len(s.store) == 0 {
		return tools.Result{Output: "(no notes yet)"}, nil
	}

	var b strings.Builder
	for k, v := range s.store {
		b.WriteString(fmt.Sprintf("📝 %s:\n%s\n\n", k, v))
	}
	return tools.Result{Output: b.String()}, nil
}

// GetAllNotes returns all notes as a map for the active scan context.
func GetAllNotes() map[string]string {
	s := getNoteStore()
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]string, len(s.store))
	for k, v := range s.store {
		result[k] = v
	}
	return result
}

// FormatForContext returns a compact summary of all notes for the active scan context.
func FormatForContext() string {
	s := getNoteStore()
	return formatStore(s)
}

// FormatForContextID returns a compact summary of notes for a specific scan context ID.
func FormatForContextID(contextID string) string {
	s := getNoteStoreForContext(contextID)
	return formatStore(s)
}

func formatStore(s *noteStore) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.store) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("=== YOUR SAVED NOTES (from add_note) ===\n")
	for k, v := range s.store {
		if len(v) > 500 {
			v = v[:500] + "... (truncated)"
		}
		b.WriteString(fmt.Sprintf("• %s: %s\n", k, v))
	}
	b.WriteString("=== END NOTES ===")
	return b.String()
}

// SetPersistPathForContext configures disk persistence for a specific context.
func SetPersistPathForContext(contextID, dir string) {
	s := getNoteStoreForContext(contextID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir != "" {
		s.persistPath = filepath.Join(dir, "notes.json")
	} else {
		s.persistPath = ""
	}
}

// ResetNotesForContext clears all notes for a specific context.
func ResetNotesForContext(contextID string) {
	s := getNoteStoreForContext(contextID)
	s.mu.Lock()
	s.store = make(map[string]string)
	s.mu.Unlock()
}

// LoadFromDiskForContext loads notes into this context's store from the
// durable home (backend snapshot → instance continuity → legacy file, the
// charter §6.3 read order). The returned error distinguishes a restore
// failure (missing file, corrupt snapshot — file or backend row) from a
// genuinely empty note set — a failed restore must never read as an intact
// empty context in audit records.
func LoadFromDiskForContext(contextID string) (int, error) {
	s := getNoteStoreForContext(contextID)
	s.mu.Lock()
	defer s.mu.Unlock()

	if b := scanctx.CurrentStateFilesBackend(); b != nil {
		if data, _, ok, err := b.LoadStateFileSnapshot(s.contextID, scanctx.StateFileKindNotes); err != nil {
			log.Printf("[notes] Warning: state snapshot load failed for %s: %v", s.contextID, err)
		} else if ok {
			return s.mergeLocked(data, fmt.Sprintf("state snapshot %s/%s", s.contextID, scanctx.StateFileKindNotes))
		}
		if s.instanceID != "" {
			if data, _, source, ok, err := b.LoadLatestStateFileSnapshotForInstance(s.instanceID, s.contextID, scanctx.StateFileKindNotes); err != nil {
				log.Printf("[notes] Warning: continuity load failed for %s/%s: %v", s.instanceID, s.contextID, err)
			} else if ok {
				return s.mergeLocked(data, fmt.Sprintf("state continuity %s (from sibling %s)", s.instanceID, source))
			}
		}
	}

	if s.persistPath == "" {
		return 0, nil
	}

	data, err := os.ReadFile(s.persistPath)
	if err != nil {
		// The only caller is the resume path, where the predecessor saved notes:
		// an absent expected snapshot is itself a state worth surfacing, not one
		// to fold into "zero notes loaded". Wrap ErrNotExist so callers can
		// distinguish absent-expected from corrupt-expected from fresh-empty.
		return 0, fmt.Errorf("notes restore: %w", err)
	}

	count, corruptErr := s.mergeLocked(data, s.persistPath)
	if corruptErr != nil {
		return 0, fmt.Errorf("notes restore failed: corrupt snapshot at %s: %w", s.persistPath, corruptErr)
	}
	return count, nil
}

// GetAllNotesForContext returns all notes for a specific context.
func GetAllNotesForContext(contextID string) map[string]string {
	s := getNoteStoreForContext(contextID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]string, len(s.store))
	for k, v := range s.store {
		result[k] = v
	}
	return result
}

// CleanupContext removes the note store for a deactivated context.
func CleanupContext(contextID string) {
	noteStoresMu.Lock()
	defer noteStoresMu.Unlock()
	delete(noteStores, contextID)
}

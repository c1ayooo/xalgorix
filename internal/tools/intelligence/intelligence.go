// Package intelligence provides the record_intelligence tool: structured,
// operator-facing intel (credentials / leads / sensitive data) the agent
// captures during a run. Items persist in the PG primary store
// (run_state_files, kind=intelligence) via Backend from R6.1 (charter v3.0
// §6.3); the per-scan-dir intelligence.json remains as the legacy/test-mode
// path and read fallback. Items are aggregated at task level by the web
// layer (GET /api/tasks/{id}/intelligence), where operator triage lives as
// a separate human annotation (platform store), never overwriting the
// observed record.
//
// Scope boundary: this tool records TARGET-side intel only — findings-class
// data the operator's engagement produced. Operator-side secrets (provider
// keys, machine tokens) have no path into this store, keeping the
// "API never returns secrets" red line intact for what it was written for.
package intelligence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/c1ayooo/xalgorix/v4/internal/sandbox"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/storage"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// Kind vocabulary. Deliberately small: each kind maps to one triage column in
// the UI and one operator question. New kinds are a product decision, not a
// free-form tag dump.
const (
	KindCredential    = "credential"     // a discovered credential or auth material
	KindLead          = "lead"           // an attack-path hint worth human follow-up
	KindSensitiveData = "sensitive_data" // exposed data that is not a credential
)

var validKinds = map[string]bool{
	KindCredential:    true,
	KindLead:          true,
	KindSensitiveData: true,
}

// Bounds mirror the ledger's discipline: bounded strings keep the hot-path
// snapshot small and the UI dense.
const (
	maxIntelTitleLen   = 512
	maxIntelContentLen = 4096
	maxIntelFieldLen   = 512
)

// intelFileName is the legacy per-scan snapshot file. From R6.1 (charter
// v3.0 §6.3) the durable home is the run_state_files row
// (scan_id, kind=intelligence) via Backend; the file remains only as the
// test-mode path and the straggler/import read fallback (atomic writes via
// storage.WriteAtomic — the shared primitive, not a hand-rolled
// temp+rename).
const intelFileName = "intelligence.json"

// Item is one recorded intel observation. Content is immutable after first
// record (an observation is what it is); only Confidence may be upgraded on a
// fingerprint match, mirroring how 4zreco's store never degrades a seen item.
type Item struct {
	ID string `json:"id"`
	// Fingerprint = sha256(kind + NUL + normalized content + NUL + endpoint),
	// first 16 hex chars. Task-level identity: the same finding re-recorded by
	// a later run aggregates under the same fingerprint.
	Fingerprint string    `json:"fingerprint"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Content     string    `json:"content"`
	Endpoint    string    `json:"endpoint,omitempty"`
	Confidence  float64   `json:"confidence,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// intelSnapshot is the on-disk JSON shape.
type intelSnapshot struct {
	Version int    `json:"version"`
	Seq     int    `json:"seq"`
	Items   []Item `json:"items"`
}

// ── Per-context stores (notes discipline) ──

var (
	intelStores   = make(map[string]*intelStore)
	intelStoresMu sync.RWMutex

	detailMu       sync.RWMutex
	detailObserver func(contextID, fingerprint string)
)

// SetDetailObserver registers the audit sink for explicit detail fetches
// (INTEL_HANDOFF_SPEC M4: the only moment intel content reaches the model).
// The callback must be fast and non-blocking; it never gates the read.
func SetDetailObserver(fn func(contextID, fingerprint string)) {
	detailMu.Lock()
	defer detailMu.Unlock()
	detailObserver = fn
}

func notifyDetailFetch(contextID, fingerprint string) {
	detailMu.RLock()
	fn := detailObserver
	detailMu.RUnlock()
	if fn != nil {
		fn(contextID, fingerprint)
	}
}

type intelStore struct {
	mu            sync.RWMutex
	contextID     string
	items         []Item
	byFingerprint map[string]int // fingerprint -> index into items
	seq           int
	persistPath   string
	// instanceID stamps the run_state_files row (D11): a re-dispatched
	// session shares it with its predecessor, whose snapshot the first
	// LoadFromDiskForContext inherits via the continuity read. Empty until
	// the session wiring stamps it (the ledger precedent).
	instanceID string
}

func getIntelStoreForContext(contextID string) *intelStore {
	intelStoresMu.RLock()
	s, ok := intelStores[contextID]
	intelStoresMu.RUnlock()
	if ok {
		return s
	}
	intelStoresMu.Lock()
	defer intelStoresMu.Unlock()
	if s, ok = intelStores[contextID]; ok {
		return s
	}
	s = &intelStore{contextID: contextID, byFingerprint: make(map[string]int)}
	intelStores[contextID] = s
	return s
}

// SetPersistPathForContext configures legacy file persistence for one
// context: <scanDir>/intelligence.json (ignored for writes while a Backend
// is registered).
func SetPersistPathForContext(contextID, dir string) {
	s := getIntelStoreForContext(contextID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir != "" {
		s.persistPath = filepath.Join(dir, intelFileName)
	} else {
		s.persistPath = ""
	}
}

// SetInstanceIDForContext stamps the run identity (D11) on one context's
// store. With the backend registered, snapshots carry it and a
// re-dispatched session's first LoadFromDiskForContext inherits the previous
// sibling session's intel via the instance continuity read.
func SetInstanceIDForContext(contextID, instanceID string) {
	s := getIntelStoreForContext(contextID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instanceID = strings.TrimSpace(instanceID)
}

// ResetForContext clears the in-memory items of one context (fresh dispatch
// reuse of a context id must not inherit stale intel).
func ResetForContext(contextID string) {
	s := getIntelStoreForContext(contextID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = nil
	s.byFingerprint = make(map[string]int)
	s.seq = 0
}

// CleanupContext drops the store of a deactivated context.
func CleanupContext(contextID string) {
	intelStoresMu.Lock()
	defer intelStoresMu.Unlock()
	delete(intelStores, contextID)
}

// LoadFromDiskForContext restores items into this (empty or partial) store.
// Snapshot source order with the backend registered (charter v3.0 §6.3):
//
//  1. this scan's own snapshot (run_state_files, kind=intelligence);
//  2. the newest sibling session's snapshot for the stamped instance — the
//     D11 continuity read: a re-dispatched session has no snapshot of its
//     own yet and inherits the previous session's intel instead of starting
//     empty;
//  3. the legacy intelligence.json file — straggler resilience only (crash
//     leftovers between backend registration and import); never a write
//     target while a backend is registered.
//
// Without a backend the behavior is identical to the pre-R6.1 file mode.
// Merge-only: items already in memory are never clobbered by an older
// snapshot. The returned error distinguishes a failed restore from a
// genuinely empty set — a corrupt snapshot must never read as "no intel
// recorded" (charter §5.2 honesty discipline).
func LoadFromDiskForContext(contextID string) (int, error) {
	s := getIntelStoreForContext(contextID)
	s.mu.Lock()
	defer s.mu.Unlock()

	if b := currentBackend(); b != nil {
		if data, _, ok, err := b.LoadIntelSnapshot(contextID); err != nil {
			log.Printf("[intelligence] Warning: state snapshot load failed for %s: %v", contextID, err)
		} else if ok {
			return s.mergeSnapshotLocked(data, fmt.Sprintf("state snapshot %s", contextID), contextID)
		}
		if s.instanceID != "" {
			if data, _, source, ok, err := b.LoadLatestIntelSnapshotForInstance(s.instanceID, contextID); err != nil {
				log.Printf("[intelligence] Warning: continuity load failed for %s/%s: %v", s.instanceID, contextID, err)
			} else if ok {
				return s.mergeSnapshotLocked(data, fmt.Sprintf("state continuity %s (from sibling %s)", s.instanceID, source), contextID)
			}
		}
	}

	if s.persistPath == "" {
		return 0, nil
	}
	data, err := os.ReadFile(s.persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("intelligence restore: %w", err)
	}
	return s.mergeSnapshotLocked(data, s.persistPath, contextID)
}

// mergeSnapshotLocked decodes one snapshot and merges unseen items into the
// store. Caller holds s.mu. A corrupt snapshot returns an error — it must
// never read as "nothing recorded" (charter §5.2).
func (s *intelStore) mergeSnapshotLocked(data []byte, source, contextID string) (int, error) {
	var snap intelSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		log.Printf("[intelligence] Warning: failed to parse %s: %v", source, err)
		return 0, fmt.Errorf("intelligence restore failed: corrupt snapshot at %s: %w", source, err)
	}
	count := 0
	for _, item := range snap.Items {
		if item.Fingerprint == "" {
			continue
		}
		if _, exists := s.byFingerprint[item.Fingerprint]; exists {
			continue
		}
		s.seq++
		if item.ID == "" {
			item.ID = fmt.Sprintf("I-%d", s.seq)
		}
		s.items = append(s.items, item)
		s.byFingerprint[item.Fingerprint] = len(s.items) - 1
		count++
	}
	if s.seq < snap.Seq {
		s.seq = snap.Seq
	}
	if count > 0 {
		log.Printf("[intelligence] Loaded %d intel items from: %s (context=%s)", count, source, contextID)
	}
	return count, nil
}

// marshalSnapshot serializes the store. Must be called with s.mu held.
// Returns (nil, "") when persistence is disabled (no file path AND no
// backend).
func (s *intelStore) marshalSnapshot() ([]byte, string) {
	if s.persistPath == "" && currentBackend() == nil {
		return nil, ""
	}
	snap := intelSnapshot{Version: 1, Seq: s.seq, Items: s.items}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		log.Printf("[intelligence] Warning: failed to marshal snapshot: %v", err)
		return nil, ""
	}
	return data, s.persistPath
}

// persist writes the snapshot. The caller holds s.mu: the durable write
// happens under the state lock so snapshots reach the durable home in the
// order they were taken (notes/ledger discipline). With the backend
// registered the snapshot is a whole-snapshot upsert into the
// run_state_files row (scan_id, kind=intelligence) — intelligence.json is
// never written (charter v3.0 §6.3).
func (s *intelStore) persist() error {
	if b := currentBackend(); b != nil {
		data, _ := s.marshalSnapshot()
		if data == nil {
			return nil
		}
		if err := b.SaveIntelSnapshot(s.contextID, s.instanceID, int64(s.seq), data); err != nil {
			return fmt.Errorf("intelligence durable write failed: %w", err)
		}
		return nil
	}
	data, path := s.marshalSnapshot()
	if data == nil || path == "" {
		return nil
	}
	canonical, err := sandbox.Default().CheckResolve(scanctx.Get(s.contextID), "intelligence", path)
	if err != nil {
		return fmt.Errorf("intelligence path policy refused %s: %w", path, err)
	}
	if err := storage.WriteAtomic(canonical, data); err != nil {
		return fmt.Errorf("intelligence durable write failed: %w", err)
	}
	return nil
}

// LoadSnapshot reads one intel snapshot for the web layer's task-level
// aggregation. With the backend registered, the durable row of the scan
// whose directory the path names is consulted first (the dir name is the
// scan id — the same identity the importer keys rows by); the legacy
// intelligence.json file remains the fallback for sessions without a row
// (pre-import runs). A missing snapshot returns (nil, nil) — an honest "no
// intel recorded in this run". A corrupt snapshot (file or row) returns an
// error the caller must surface, never folded into empty (charter §5.2
// discipline). Sibling continuity is NOT resolved here: the aggregation
// merges sibling sessions itself, and the R6.2 read switch replaces this
// seam wholesale.
func LoadSnapshot(path string) ([]Item, error) {
	if b := currentBackend(); b != nil {
		scanID := filepath.Base(filepath.Dir(path))
		data, _, ok, err := b.LoadIntelSnapshot(scanID)
		if err != nil {
			return nil, fmt.Errorf("intelligence snapshot %s: %w", scanID, err)
		}
		if ok {
			var snap intelSnapshot
			if err := json.Unmarshal(data, &snap); err != nil {
				return nil, fmt.Errorf("intelligence snapshot %s does not parse: %w", scanID, err)
			}
			return snap.Items, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var snap intelSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("intelligence snapshot %s does not parse: %w", path, err)
	}
	return snap.Items, nil
}

// fingerprintFor derives the task-level identity of an intel record.
func fingerprintFor(kind, content, endpoint string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + normalizeIntelText(content) + "\x00" + strings.ToLower(strings.TrimSpace(endpoint))))
	return hex.EncodeToString(h[:8]) // 16 hex chars
}

// normalizeIntelText makes the fingerprint robust to trivial reformatting:
// trimmed, internal whitespace collapsed, case-folded.
func normalizeIntelText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// normalizeEndpoint strips a scheme prefix and trailing slash so
// "https://host/path" and "host/path" dedup together.
func normalizeEndpoint(e string) string {
	e = strings.TrimSpace(e)
	e = strings.TrimPrefix(e, "https://")
	e = strings.TrimPrefix(e, "http://")
	return strings.TrimSuffix(e, "/")
}

// RecordForContext records one intel item. Dedup by fingerprint: a re-record
// keeps the original content (immutable observation) and may only raise the
// confidence. The bool reports whether this call created a new item.
func RecordForContext(contextID string, args map[string]string) (tools.Result, error) {
	kind := strings.ToLower(strings.TrimSpace(args["kind"]))
	if !validKinds[kind] {
		return tools.Result{}, fmt.Errorf("record_intelligence requires kind to be one of credential|lead|sensitive_data, got %q", kind)
	}
	title := strings.TrimSpace(args["title"])
	if title == "" {
		return tools.Result{}, fmt.Errorf("record_intelligence requires a nonempty title")
	}
	content := strings.TrimSpace(args["content"])
	if content == "" {
		return tools.Result{}, fmt.Errorf("record_intelligence requires nonempty content (the observed evidence)")
	}
	if len(title) > maxIntelTitleLen {
		title = title[:maxIntelTitleLen]
	}
	if len(content) > maxIntelContentLen {
		content = content[:maxIntelContentLen] + "… (truncated)"
	}
	endpoint := normalizeEndpoint(args["endpoint"])
	if len(endpoint) > maxIntelFieldLen {
		endpoint = endpoint[:maxIntelFieldLen]
	}
	confidence := 0.5
	if raw := strings.TrimSpace(args["confidence"]); raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil || parsed < 0 || parsed > 1 {
			return tools.Result{}, fmt.Errorf("record_intelligence confidence must be a number in [0,1], got %q", raw)
		}
		confidence = parsed
	}

	fp := fingerprintFor(kind, content, endpoint)
	if contextID == "" {
		contextID = scanctx.Default().ID
	}
	s := getIntelStoreForContext(contextID)

	s.mu.Lock()
	var (
		deduped  bool
		id       string
		writeErr error
	)
	if idx, ok := s.byFingerprint[fp]; ok {
		// Immutable observation: keep the first content; confidence may only
		// rise; UpdatedAt records that a second run re-confirmed the item.
		deduped = true
		id = s.items[idx].ID
		if confidence > s.items[idx].Confidence {
			s.items[idx].Confidence = confidence
		}
		s.items[idx].UpdatedAt = time.Now().UTC()
	} else {
		s.seq++
		id = fmt.Sprintf("I-%d", s.seq)
		now := time.Now().UTC()
		s.items = append(s.items, Item{
			ID:          id,
			Fingerprint: fp,
			Kind:        kind,
			Title:       title,
			Content:     content,
			Endpoint:    endpoint,
			Confidence:  confidence,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
		s.byFingerprint[fp] = len(s.items) - 1
	}
	writeErr = s.persist()
	if writeErr != nil {
		// Durable-write acknowledgement: roll back so receipt and durable
		// state agree (notes discipline).
		if deduped {
			// Confidence/updatedAt upgrade is cosmetic; leave as-is.
		} else {
			delete(s.byFingerprint, fp)
			s.items = s.items[:len(s.items)-1]
			s.seq--
		}
	}
	s.mu.Unlock()

	if writeErr != nil {
		return tools.Result{Error: fmt.Sprintf("record_intelligence: durable write failed (%v) — the item was NOT saved. Retry the call, or include the information in your report instead of relying on it persisting.", writeErr)}, nil
	}

	if deduped {
		return tools.Result{
			Output:   fmt.Sprintf("Intel already recorded (%s %s): confidence kept/upgraded, no duplicate created. [%s]", kind, id, fp),
			Metadata: map[string]any{"intel_fingerprint": fp, "intel_kind": kind, "intel_deduped": true},
		}, nil
	}
	return tools.Result{
		Output: fmt.Sprintf("Intel recorded: %s %s [%s]", kind, id, fp),
		Metadata: map[string]any{
			"intel_fingerprint": fp,
			"intel_kind":        kind,
			"intel_title":       title,
			"intel_endpoint":    endpoint,
			"intel_deduped":     false,
		},
	}, nil
}

// ReadForContext returns the model-facing identity listing for
// read_intelligence without a reference (INTEL_HANDOFF_SPEC D5/M1a): one line
// per item carrying identity fields only — fingerprint, instance id, kind,
// endpoint, confidence, title. Content is deliberately withheld so the listing
// can be consulted (and injected) without moving credential plaintext into the
// transcript; one item's full content is fetched on demand through
// ReadItemForContext.
func ReadForContext(contextID string) string {
	if contextID == "" {
		contextID = scanctx.Default().ID
	}
	s := getIntelStoreForContext(contextID)
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.items) == 0 {
		return "(no intelligence recorded yet)"
	}
	ordered := make([]Item, len(s.items))
	copy(ordered, s.items)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].CreatedAt.Before(ordered[j].CreatedAt) })

	var b strings.Builder
	b.WriteString("=== RECORDED INTELLIGENCE (identity only; content withheld) ===\n")
	for _, item := range ordered {
		endpoint := item.Endpoint
		if endpoint == "" {
			endpoint = "-"
		}
		fmt.Fprintf(&b, "• [%s] %s fp=%s endpoint=%s conf=%.2f title=%s\n",
			item.Kind, item.ID, item.Fingerprint, endpoint, item.Confidence, clipIntelLine(item.Title, 120))
	}
	fmt.Fprintf(&b, "=== END INTELLIGENCE (%d item(s)) — read_intelligence(fingerprint=<fp or id>) for one item's full content ===", len(ordered))
	return b.String()
}

// ReadItemForContext resolves one item by fingerprint or instance id (I-N).
// Matching is exact and case-insensitive — no prefix matching, because an
// ambiguous hit is worse than an honest miss. found distinguishes "no such
// item" from an empty store.
func ReadItemForContext(contextID, ref string) (Item, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Item{}, false
	}
	if contextID == "" {
		contextID = scanctx.Default().ID
	}
	s := getIntelStoreForContext(contextID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if idx, ok := s.byFingerprint[strings.ToLower(ref)]; ok {
		return s.items[idx], true
	}
	for _, item := range s.items {
		if strings.EqualFold(item.ID, ref) {
			return item, true
		}
	}
	return Item{}, false
}

// ItemsForContext returns a snapshot copy of one context's items for read-only
// projections (the worker seed). Callers must not mutate the result and must
// not treat it as proof: content stays out of auto-injected prompts.
func ItemsForContext(contextID string) []Item {
	if contextID == "" {
		contextID = scanctx.Default().ID
	}
	s := getIntelStoreForContext(contextID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.items) == 0 {
		return nil
	}
	out := make([]Item, len(s.items))
	copy(out, s.items)
	return out
}

// formatIntelDetail renders one item's full record for the explicit
// detail fetch (the only place intel content is handed to the model).
func formatIntelDetail(item Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Intel %s [%s] fp=%s\n", item.ID, item.Kind, item.Fingerprint)
	fmt.Fprintf(&b, "Title: %s\n", item.Title)
	if item.Endpoint != "" {
		fmt.Fprintf(&b, "Endpoint: %s\n", item.Endpoint)
	}
	fmt.Fprintf(&b, "Confidence: %.2f\n", item.Confidence)
	fmt.Fprintf(&b, "Recorded: %s\n", item.CreatedAt.UTC().Format(time.RFC3339))
	if item.UpdatedAt.After(item.CreatedAt) {
		fmt.Fprintf(&b, "Last re-confirmed: %s\n", item.UpdatedAt.UTC().Format(time.RFC3339))
	}
	b.WriteString("Content (observed data; verify before use):\n")
	b.WriteString(item.Content)
	return b.String()
}

// clipIntelLine collapses whitespace and bounds one listing field without
// splitting a UTF-8 rune.
func clipIntelLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Register adds the intelligence tools to a registry.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name:        "record_intelligence",
		Description: "Record operator-facing intelligence discovered during testing: stolen/discovered credentials, attack-path leads worth human follow-up, or sensitive data exposure. Use this for findings-class observations that are NOT vulnerabilities (do not duplicate report_vulnerability) but that the operator must triage. Kind is one of: credential (discovered login material), lead (a promising attack path), sensitive_data (exposed non-credential data). Content should carry the concrete observation (what was found and where); endpoint optionally names the surface. Duplicate records of the same observation are deduplicated automatically.",
		Parameters: []tools.Parameter{
			{Name: "kind", Description: "credential | lead | sensitive_data", Required: true},
			{Name: "title", Description: "Short human-readable headline (e.g. 'Admin panel accepts default creds')", Required: true},
			{Name: "content", Description: "The concrete observation: what was found, where, and why it matters (bounded to ~4k chars)", Required: true},
			{Name: "endpoint", Description: "Optional endpoint/host the intel applies to (e.g. https://target/app/admin)", Required: false},
			{Name: "confidence", Description: "Optional quality of this observation in [0,1] (default 0.5)", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return RecordForContext(r.GetScanContextID(), args)
		},
	})

	r.Register(&tools.Tool{
		Name:        "read_intelligence",
		Description: "Read recorded intelligence (credentials, leads, sensitive data). Without arguments: an identity-only listing (fingerprint, id, kind, endpoint, confidence, title) — content is withheld so consulting what the run holds does not move plaintext into the conversation. Pass fingerprint (the fp= value or the I-N id) to read one item's full content.",
		Parameters: []tools.Parameter{
			{Name: "fingerprint", Description: "Fingerprint (fp= value) or I-N item id to read in full; omit for the identity listing", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			contextID := r.GetScanContextID()
			ref := strings.TrimSpace(args["fingerprint"])
			if ref == "" {
				return tools.Result{
					Output:   ReadForContext(contextID),
					Metadata: map[string]any{"intel_listing": true},
				}, nil
			}
			item, ok := ReadItemForContext(contextID, ref)
			if !ok {
				// A miss is an expected model mistake, not an engine failure:
				// returning it as Output keeps the registry circuit breaker out of
				// the picture (Result.Error counts as tool failure — read_notes
				// precedent). The listing gives the model the valid identities.
				return tools.Result{
					Output: fmt.Sprintf("No intel item matches %q. Recorded items (identity only):\n%s", ref, ReadForContext(contextID)),
				}, nil
			}
			notifyDetailFetch(contextID, item.Fingerprint)
			return tools.Result{
				Output: formatIntelDetail(item),
				Metadata: map[string]any{
					"intel_fingerprint": item.Fingerprint,
					"intel_kind":        item.Kind,
					"intel_detail":      true,
				},
			}, nil
		},
	})
}

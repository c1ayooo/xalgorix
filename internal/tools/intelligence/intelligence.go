// Package intelligence provides the record_intelligence tool: structured,
// operator-facing intel (credentials / leads / sensitive data) the agent
// captures during a run. Items persist per scan dir as intelligence.json and
// are aggregated at task level by the web layer (GET /api/tasks/{id}/intelligence),
// where operator triage lives as a separate human annotation (platform store),
// never overwriting the observed record.
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

	"github.com/xalgord/xalgorix/v4/internal/sandbox"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/storage"
	"github.com/xalgord/xalgorix/v4/internal/tools"
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

// intelFileName is the per-scan snapshot the store persists to (atomic writes
// via storage.WriteAtomic — the shared primitive, not a hand-rolled
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
)

type intelStore struct {
	mu            sync.RWMutex
	contextID     string
	items         []Item
	byFingerprint map[string]int // fingerprint -> index into items
	seq           int
	persistPath   string
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

// SetPersistPathForContext configures disk persistence for one context:
// <scanDir>/intelligence.json.
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

// LoadFromDiskForContext restores items from the persist path. Merge-only:
// items already in memory are never clobbered by an older snapshot. The
// returned error distinguishes a failed restore from a genuinely empty set —
// a corrupt snapshot must never read as "no intel recorded" (charter §5.2
// honesty discipline).
func LoadFromDiskForContext(contextID string) (int, error) {
	s := getIntelStoreForContext(contextID)
	s.mu.Lock()
	defer s.mu.Unlock()

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
	var snap intelSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		log.Printf("[intelligence] Warning: failed to parse %s: %v", s.persistPath, err)
		return 0, fmt.Errorf("intelligence restore failed: corrupt snapshot at %s: %w", s.persistPath, err)
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
		log.Printf("[intelligence] Loaded %d intel items from: %s (context=%s)", count, s.persistPath, contextID)
	}
	return count, nil
}

// marshalSnapshot serializes the store. Must be called with s.mu held.
// Returns (nil, "") when persistence is disabled.
func (s *intelStore) marshalSnapshot() ([]byte, string) {
	if s.persistPath == "" {
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
// happens under the state lock so snapshots reach disk in the order they were
// taken (notes/ledger discipline).
func (s *intelStore) persist() error {
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

// LoadSnapshot reads one on-disk snapshot for the web layer's task-level
// aggregation. A missing file returns (nil, nil) — an honest "no intel
// recorded in this run". A corrupt snapshot returns an error the caller must
// surface, never folded into empty (charter §5.2 discipline).
func LoadSnapshot(path string) ([]Item, error) {
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

// ReadForContext returns a compact listing for the model (read_intelligence).
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
	b.WriteString("=== RECORDED INTELLIGENCE (record_intelligence) ===\n")
	for _, item := range ordered {
		content := item.Content
		if len(content) > 300 {
			content = content[:300] + "... (truncated)"
		}
		b.WriteString(fmt.Sprintf("• %s %s [%s] conf=%.2f %s\n  %s\n", item.Kind, item.ID, item.Fingerprint, item.Confidence, item.Endpoint, content))
	}
	b.WriteString("=== END INTELLIGENCE ===")
	return b.String()
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
		Description: "Read the intelligence items recorded so far in this run (credentials, leads, sensitive data). Useful before recording to avoid repeating yourself and to build on earlier findings.",
		Parameters:  []tools.Parameter{},
		Execute: func(_ map[string]string) (tools.Result, error) {
			return tools.Result{Output: ReadForContext(r.GetScanContextID())}, nil
		},
	})
}

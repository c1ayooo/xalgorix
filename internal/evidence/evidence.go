// Package evidence pins the wire evidence behind a reported finding.
//
// The traffic archive is disposable: it lives under the scan directory and is
// removed wholesale by retention. A finding that only carries session:seq
// therefore loses its proof when the scan dir is pruned. This package copies
// every referenced request/response (or tool output) into a content-addressed
// blob store under the durable data directory, records sha256 hashes and
// lengths, and binds the copies to the finding through PostgreSQL.
//
// Storage split (charter §7 / D12): PostgreSQL is the authority for bindings,
// versions and hashes; the raw bytes are file-layer artifacts written with
// storage.WriteAtomic. Nothing here executes or re-interprets evidence: it
// copies, hashes and verifies.
package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/storage"
)

// Snapshot sources.
const (
	SourceTraffic    = "traffic"
	SourceToolOutput = "tool_output"
)

// Binding roles.
const (
	RoleBaseline = "baseline"
	RoleProof    = "proof"
	RoleOutput   = "output"
)

// Snapshot is one immutable copy of evidence bytes.
type Snapshot struct {
	ID            string    `json:"id"`
	ScanID        string    `json:"scan_id"`
	Session       string    `json:"session,omitempty"`
	Seq           int64     `json:"seq,omitempty"`
	Source        string    `json:"source"`
	SourceRef     string    `json:"source_ref"`
	CapturedAt    time.Time `json:"captured_at,omitempty"`
	Method        string    `json:"method,omitempty"`
	URL           string    `json:"url,omitempty"`
	Status        int       `json:"status,omitempty"`
	ReqSHA256     string    `json:"req_sha256,omitempty"`
	RespSHA256    string    `json:"resp_sha256,omitempty"`
	ReqLen        int64     `json:"req_len,omitempty"`
	RespLen       int64     `json:"resp_len,omitempty"`
	ReqHead       string    `json:"req_head,omitempty"`
	RespHead      string    `json:"resp_head,omitempty"`
	ReqTruncated  bool      `json:"req_truncated,omitempty"`
	RespTruncated bool      `json:"resp_truncated,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Binding links a snapshot to a finding.
type Binding struct {
	SnapshotID string `json:"snapshot_id"`
	Role       string `json:"role"`
	Position   int    `json:"position"`
}

// DB is the PostgreSQL persistence seam. platform.Store implements it.
type DB interface {
	UpsertEvidenceSnapshots(ctx context.Context, snaps []Snapshot) error
	BindFindingEvidence(ctx context.Context, scanID, findingID string, bindings []Binding) (version int, err error)
	ListFindingEvidence(ctx context.Context, scanID, findingID string) (snaps []Snapshot, version int, err error)
	GetEvidenceSnapshot(ctx context.Context, snapshotID string) (Snapshot, error)
	CollectEvidence(ctx context.Context, now time.Time) error
	EvidenceHashLive(ctx context.Context, hash string) (bool, error)
}

// Store is the evidence service.
type Store struct {
	db  DB
	dir string
}

// New builds a store rooted at dir (the durable evidence directory).
func New(db DB, dir string) *Store { return &Store{db: db, dir: dir} }

var (
	defaultMu sync.RWMutex
	defaultSt *Store
)

// SetDefault installs the process-wide store. Called once at startup.
func SetDefault(s *Store) {
	defaultMu.Lock()
	defaultSt = s
	defaultMu.Unlock()
}

// Default returns the installed store, or nil when PostgreSQL is not wired
// (unit tests, CLI-only runs). Callers must tolerate nil.
func Default() *Store {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultSt
}

// ScanIDFor returns the evidence scan key for a session directory. The session
// directory basename is stable across restarts and is the same identity the
// traffic archive uses (TrafficRef.Session).
func ScanIDFor(scanDir string) string {
	dir := strings.TrimSpace(scanDir)
	if dir == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(dir))
}

// ResolveSessionDir maps a TrafficRef session name to its directory. An empty
// session (or the scan's own basename) means the current scan dir; any other
// value must name a sibling session directory (no separators, no traversal).
func ResolveSessionDir(scanDir, session string) (string, error) {
	scanDir = filepath.Clean(scanDir)
	session = strings.TrimSpace(session)
	if session == "" || session == filepath.Base(scanDir) {
		return scanDir, nil
	}
	if strings.ContainsAny(session, `/\`) || session == "." || session == ".." {
		return "", fmt.Errorf("invalid session name %q", session)
	}
	dir := filepath.Join(filepath.Dir(scanDir), session)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("session %q not found beside the current scan", session)
	}
	return dir, nil
}

// ParseTrafficRefs parses the model-supplied traffic_refs argument. Accepted
// forms: "seq", "session:seq", comma/semicolon/space separated. It returns the
// resolvable refs and one human-readable problem per rejected token.
func ParseTrafficRefs(scanDir, raw string) ([]scanctx.TrafficRef, []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	var refs []scanctx.TrafficRef
	var problems []string
	seen := map[string]bool{}
	for _, tok := range tokens {
		session, seq, err := ParseTrafficRefToken(tok)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		dir, err := ResolveSessionDir(scanDir, session)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		ex, err := scanctx.ReadTrafficExchangeAt(dir, seq)
		if err != nil {
			problems = append(problems, fmt.Sprintf("traffic %s: %v", tok, err))
			continue
		}
		if ex == nil {
			problems = append(problems, fmt.Sprintf("traffic %s does not exist in this run", tok))
			continue
		}
		key := fmt.Sprintf("%s:%d", session, seq)
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, scanctx.TrafficRef{Session: session, Seq: seq})
	}
	return refs, problems
}

// ParseTrafficRefToken parses one "seq" or "session:seq" token.
func ParseTrafficRefToken(tok string) (session string, seq int64, err error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", 0, errors.New("empty traffic reference")
	}
	if i := strings.LastIndex(tok, ":"); i >= 0 {
		session = strings.TrimSpace(tok[:i])
		tok = strings.TrimSpace(tok[i+1:])
	}
	seq, err = parseInt64(tok)
	if err != nil || seq <= 0 {
		return "", 0, fmt.Errorf("traffic reference %q must end in a positive sequence number", tok)
	}
	return session, seq, nil
}

func parseInt64(s string) (int64, error) {
	var n int64
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int64(r-'0')
		if n > 1<<40 {
			return 0, errors.New("out of range")
		}
	}
	return n, nil
}

// Record copies the referenced evidence and binds it to the finding. It is
// idempotent: re-reporting the same evidence raises the finding's version
// without duplicating blobs (content addressing) or bindings.
func (s *Store) Record(ctx context.Context, scanDir, findingID string, refs []scanctx.TrafficRef, outputs []string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("evidence store is not configured")
	}
	if s.dir == "" {
		return 0, errors.New("evidence directory is not configured")
	}
	scanID := ScanIDFor(scanDir)
	if scanID == "" || findingID == "" {
		return 0, errors.New("scan and finding identity are required")
	}
	var snaps []Snapshot
	var bindings []Binding
	for _, ref := range refs {
		dir, err := ResolveSessionDir(scanDir, ref.Session)
		if err != nil {
			return 0, err
		}
		ex, err := scanctx.ReadTrafficExchangeAt(dir, ref.Seq)
		if err != nil {
			return 0, fmt.Errorf("traffic %s:%d: %w", ref.Session, ref.Seq, err)
		}
		if ex == nil {
			return 0, fmt.Errorf("traffic %s:%d no longer exists", ref.Session, ref.Seq)
		}
		snap, err := s.snapshotTraffic(scanID, ref, *ex)
		if err != nil {
			return 0, err
		}
		snaps = append(snaps, snap)
		bindings = append(bindings, Binding{SnapshotID: snap.ID, Role: RoleProof, Position: len(bindings)})
	}
	for _, id := range outputs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		snap, err := s.snapshotOutput(scanID, id)
		if err != nil {
			return 0, err
		}
		snaps = append(snaps, snap)
		bindings = append(bindings, Binding{SnapshotID: snap.ID, Role: RoleOutput, Position: len(bindings)})
	}
	if len(snaps) == 0 {
		return 0, errors.New("no evidence to record")
	}
	if err := s.db.UpsertEvidenceSnapshots(ctx, snaps); err != nil {
		return 0, err
	}
	return s.db.BindFindingEvidence(ctx, scanID, findingID, bindings)
}

func (s *Store) snapshotTraffic(scanID string, ref scanctx.TrafficRef, ex scanctx.TrafficExchange) (Snapshot, error) {
	reqHash, err := s.writeBlob(scanID, []byte(ex.RequestRaw))
	if err != nil {
		return Snapshot{}, err
	}
	respHash, err := s.writeBlob(scanID, []byte(ex.ResponseRaw))
	if err != nil {
		return Snapshot{}, err
	}
	session := ref.Session
	if session == "" {
		session = scanID
	}
	snap := Snapshot{
		ScanID:        scanID,
		Session:       session,
		Seq:           ex.Seq,
		Source:        SourceTraffic,
		SourceRef:     fmt.Sprintf("%s:%d", session, ex.Seq),
		CapturedAt:    ex.At,
		Method:        ex.Method,
		URL:           ex.URL,
		Status:        ex.Status,
		ReqSHA256:     reqHash,
		RespSHA256:    respHash,
		ReqLen:        int64(len(ex.RequestRaw)),
		RespLen:       int64(len(ex.ResponseRaw)),
		ReqHead:       headBlock(ex.RequestRaw),
		RespHead:      headBlock(ex.ResponseRaw),
		ReqTruncated:  ex.ReqTruncated,
		RespTruncated: ex.RespTruncated,
		CreatedAt:     time.Now().UTC(),
	}
	snap.ID = snapshotID(snap)
	return snap, nil
}

func (s *Store) snapshotOutput(scanID, id string) (Snapshot, error) {
	sc := scanctx.Get(scanID)
	if sc == nil || sc.ToolOutputs == nil {
		return Snapshot{}, fmt.Errorf("tool output %q: scan context is not available", id)
	}
	content, ok := sc.ToolOutputs.Get(id)
	if !ok {
		return Snapshot{}, fmt.Errorf("tool output %q does not exist", id)
	}
	hash, err := s.writeBlob(scanID, []byte(content))
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		ScanID:     scanID,
		Source:     SourceToolOutput,
		SourceRef:  id,
		RespSHA256: hash,
		RespLen:    int64(len(content)),
		RespHead:   headBlock(content),
		CreatedAt:  time.Now().UTC(),
	}
	snap.ID = snapshotID(snap)
	return snap, nil
}

func snapshotID(s Snapshot) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		s.ScanID, s.Source, s.SourceRef, s.ReqSHA256, s.RespSHA256,
	}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// headBlock returns the header section of a raw wire message (everything
// before the first blank line), bounded so the metadata row stays small.
func headBlock(raw string) string {
	head := raw
	if i := strings.Index(head, "\r\n\r\n"); i >= 0 {
		head = head[:i]
	} else if i := strings.Index(head, "\n\n"); i >= 0 {
		head = head[:i]
	}
	const maxHead = 2048
	if len(head) > maxHead {
		head = head[:maxHead]
	}
	return head
}

// writeBlob stores bytes under the content hash and returns it. An existing
// blob with the same hash is verified; a mismatch is corruption, not a
// recoverable condition.
func (s *Store) writeBlob(scanID string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if len(data) == 0 {
		return hash, nil
	}
	dir := filepath.Join(s.dir, scanID, "blobs", hash[:2])
	if err := storage.EnsureSecureDir(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, hash+".bin")
	if existing, err := os.ReadFile(path); err == nil {
		if sha256.Sum256(existing) != sum {
			return "", fmt.Errorf("evidence blob %s is corrupted", hash)
		}
		return hash, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := storage.WriteAtomic(path, data); err != nil {
		return "", err
	}
	return hash, nil
}

// OpenBody returns the immutable bytes for one side of a snapshot after
// verifying their sha256. Missing or mismatched blobs are hard errors: served
// evidence must be byte-identical to what was recorded.
func (s *Store) OpenBody(snap Snapshot, side string) ([]byte, error) {
	var hash string
	switch side {
	case "request":
		hash = snap.ReqSHA256
	case "response":
		hash = snap.RespSHA256
	default:
		return nil, errors.New("side must be request or response")
	}
	if hash == "" {
		return nil, nil
	}
	path := filepath.Join(s.dir, snap.ScanID, "blobs", hash[:2], hash+".bin")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("evidence blob %s failed verification", hash)
	}
	return data, nil
}

// List returns the finding's bound snapshots and current version.
func (s *Store) List(ctx context.Context, scanDir, findingID string) ([]Snapshot, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, errors.New("evidence store is not configured")
	}
	return s.db.ListFindingEvidence(ctx, ScanIDFor(scanDir), findingID)
}

// Collect reconciles the blob store with the metadata: metadata rows with no
// binding older than the grace period are removed; unreferenced blobs older
// than the grace period are deleted. Referenced blobs are never touched.
func (s *Store) Collect(ctx context.Context, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("evidence store is not configured")
	}
	if err := s.db.CollectEvidence(ctx, now); err != nil {
		return err
	}
	return s.collectBlobs(now)
}

func (s *Store) collectBlobs(now time.Time) error {
	root := filepath.Join(s.dir)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	// The DB sweep already removed unreferenced metadata rows; blobs still
	// referenced by a surviving snapshot are skipped by mtime alone only after
	// the grace period, which is long enough for a racing write to finish.
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".bin") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) < 24*time.Hour {
			return nil
		}
		// Blobs are shared by content hash across findings; a metadata check is
		// required before deletion. The DB sweep leaves a referenced row, so we
		// ask the DB whether this hash is still live.
		hash := strings.TrimSuffix(entry.Name(), ".bin")
		live, err := s.db.EvidenceHashLive(context.Background(), hash)
		if err != nil {
			return err
		}
		if live {
			return nil
		}
		return os.Remove(path)
	})
}

// RunGC runs Collect hourly until the context is cancelled.
func (s *Store) RunGC(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.Collect(ctx, now); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[evidence] cleanup failed: %v", err)
			}
		}
	}
}

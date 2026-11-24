package web

// GET /api/scans/{id}/traffic — read-only raw HTTP exchange projection
// (2026-11-22 traffic raw amendment). Serves the per-scan traffic archive the
// MITM proxy wrote (request/response wire packets, unredacted) so the
// findings UI can display the exact packets behind a finding.
//
// Query:
//
//	endpoint=<substring>  case-insensitive substring match against the
//	                      exchange URL (a finding's endpoint/path); empty →
//	                      newest exchanges regardless of URL
//	method=<METHOD>       exact method filter (e.g. POST); empty → any
//	host=<hostname>       case-insensitive exact hostname match (port ignored) —
//	                      binds a traffic record to a discovered asset
//	refs=<session:seq,…>  exact records a finding persisted as its evidence
//	                      (preferred over the endpoint/host heuristic)
//	seq=<n>               one exact record from the scan's own session
//	limit=<n>             newest-window cap, default 20, hard cap 100
//
// Run-scoped reads merge every sibling session directory the run owns: a run
// can span several sessions (multi-target dispatch, checkpoint restore), and
// each session restarts its own archive sequence. Every returned exchange
// carries its derived `host` and its owning `session` (directory basename), so
// the UI can key rows uniquely and bind packets to a discovered asset.
//
// The exchange files live under the scan directory (scanctx.TrafficArchive),
// so this works after the scan context is torn down and without a DSN — the
// file layer is the authority, the handler is a pure projection.
import (
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

const (
	trafficDefaultLimit = 20
	trafficMaxLimit     = 100
)

// trafficExchangeView is an archived exchange plus its derived asset host and
// owning session, so the UI can bind a raw packet to a discovered asset and
// key rows uniquely across sibling sessions without re-parsing URLs.
type trafficExchangeView struct {
	scanctx.TrafficExchange
	Host    string `json:"host,omitempty"`
	Session string `json:"session,omitempty"`
}

type scanTrafficView struct {
	ScanID       string                `json:"scan_id"`
	Exchanges    []trafficExchangeView `json:"exchanges"`
	TotalMatched int                   `json:"total_matched"`
	Truncated    bool                  `json:"truncated"`
	// Present distinguishes "no traffic proxy archive was ever written for
	// this scan" (pre-R2b scan, proxy off, or unattributed-only traffic) from
	// an empty match — the UI renders a different honest empty state for each.
	Present bool `json:"present"`
}

// handleScanTraffic serves GET /api/scans/{id}/traffic.
func (s *Server) handleScanTraffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/traffic")
	if scanID == "" {
		http.Error(w, `{"error":"scan id is required"}`, http.StatusBadRequest)
		return
	}
	entries := s.findAllScanSummaries()
	res, ok := s.resolveScanRecordByIDFrom(scanID, func() []scanEntry { return entries })
	if !ok {
		http.Error(w, `{"error":"scan not found"}`, http.StatusNotFound)
		return
	}

	limit := trafficDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > trafficMaxLimit {
		limit = trafficMaxLimit
	}

	view := scanTrafficView{ScanID: scanID}

	// Exact evidence refs first: a finding persisted (session, seq) at
	// confirmation time, so the UI renders the packet the finding was actually
	// proven with rather than whatever the endpoint heuristic matches later.
	if refs, present := s.trafficExchangesByRefs(res, r.URL.Query().Get("refs")); len(refs) > 0 {
		view.Present = present || len(refs) > 0
		view.Exchanges = newestWindow(refs, limit)
		view.TotalMatched = len(refs)
		view.Truncated = len(refs) > len(view.Exchanges)
		writeJSONStatus(w, http.StatusOK, view)
		return
	}
	if seq, ok := parseTrafficSeq(r.URL.Query().Get("seq")); ok {
		if ex, err := scanctx.ReadTrafficExchangeAt(res.dir, seq); err == nil && ex != nil {
			view.Present = true
			view.Exchanges = []trafficExchangeView{{TrafficExchange: *ex, Host: scanctx.HostOf(*ex), Session: filepath.Base(res.dir)}}
			view.TotalMatched = 1
			writeJSONStatus(w, http.StatusOK, view)
			return
		}
	}

	dirs := s.trafficSessionDirs(res, entries)
	q := r.URL.Query()
	query := scanctx.TrafficQuery{
		Endpoint: q.Get("endpoint"),
		Method:   q.Get("method"),
		Host:     q.Get("host"),
	}
	filtered := trafficQueryHasFilters(query)

	collected := make([]trafficExchangeView, 0, limit*len(dirs))
	totalFiles := 0
	for _, dir := range dirs {
		session := filepath.Base(dir)
		if filtered {
			all, err := scanctx.ReadTrafficExchanges(dir)
			if err != nil {
				continue
			}
			totalFiles += len(all)
			matched, _ := scanctx.FilterTrafficExchangesWith(all, query)
			for _, ex := range matched {
				collected = append(collected, trafficExchangeView{TrafficExchange: ex, Host: scanctx.HostOf(ex), Session: session})
			}
			continue
		}
		// Live path: only the newest window of each session is parsed; the
		// endpoint must stay O(window), not O(whole run), on a 5s poll.
		newest, total, err := scanctx.ReadTrafficExchangesNewest(dir, limit)
		if err != nil {
			continue
		}
		totalFiles += total
		for _, ex := range newest {
			collected = append(collected, trafficExchangeView{TrafficExchange: ex, Host: scanctx.HostOf(ex), Session: session})
		}
	}

	view.Present = totalFiles > 0
	view.TotalMatched = totalFiles
	if filtered {
		view.TotalMatched = len(collected)
	}
	view.Exchanges = newestWindow(collected, limit)
	view.Truncated = view.TotalMatched > len(view.Exchanges)
	writeJSONStatus(w, http.StatusOK, view)
}

// trafficSessionDirs expands a run resolution into every session directory the
// run owns. Record-id requests stay scoped to their own session; instance-form
// requests cover siblings, because the archive is per session and sequence
// numbers restart in each one.
func (s *Server) trafficSessionDirs(res scanResolution, entries []scanEntry) []string {
	if !res.instanceForm || res.rec == nil || strings.TrimSpace(res.rec.InstanceID) == "" {
		return []string{res.dir}
	}
	dirs := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, entry := range entries {
		if entry.rec.InstanceID != res.rec.InstanceID || entry.dir == "" {
			continue
		}
		if _, dup := seen[entry.dir]; dup {
			continue
		}
		seen[entry.dir] = struct{}{}
		dirs = append(dirs, entry.dir)
	}
	if _, ok := seen[res.dir]; !ok && res.dir != "" {
		dirs = append(dirs, res.dir)
	}
	if len(dirs) == 0 {
		return []string{res.dir}
	}
	return dirs
}

// trafficExchangesByRefs loads an explicit evidence reference list
// ("session:seq,session:seq"). Unknown or unreadable refs are skipped; the
// caller falls back to the heuristic filters only when none resolve.
func (s *Server) trafficExchangesByRefs(res scanResolution, raw string) ([]trafficExchangeView, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	out := make([]trafficExchangeView, 0, 4)
	present := false
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		session, seqRaw := "", part
		if i := strings.LastIndex(part, ":"); i >= 0 {
			session, seqRaw = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
		}
		seq, ok := parseTrafficSeq(seqRaw)
		if !ok {
			continue
		}
		dir, ok := trafficSessionDir(res, session)
		if !ok {
			continue
		}
		ex, err := scanctx.ReadTrafficExchangeAt(dir, seq)
		if err != nil || ex == nil {
			continue
		}
		present = true
		out = append(out, trafficExchangeView{TrafficExchange: *ex, Host: scanctx.HostOf(*ex), Session: filepath.Base(dir)})
	}
	return out, present
}

// trafficSessionDir maps a stored session basename to a directory. Empty or
// the resolved session itself binds to res.dir; any other name must be a bare
// sibling directory name under the resolved session's parent (no traversal).
func trafficSessionDir(res scanResolution, session string) (string, bool) {
	if session == "" || session == filepath.Base(res.dir) {
		return res.dir, true
	}
	if filepath.Base(session) != session || strings.ContainsAny(session, `/\`) {
		return "", false
	}
	dir := filepath.Join(filepath.Dir(res.dir), session)
	if filepath.Base(dir) != session {
		return "", false
	}
	return dir, true
}

// newestWindow sorts exchanges chronologically and keeps the newest limit.
// Sibling sessions restart their sequence numbers, so the tie-break is
// (session, seq) to keep the order deterministic.
func newestWindow(exchanges []trafficExchangeView, limit int) []trafficExchangeView {
	// Non-nil even when empty: the JSON contract is `exchanges: []`, never
	// `null` (an absent archive is present=false with an empty array).
	ordered := make([]trafficExchangeView, len(exchanges))
	copy(ordered, exchanges)
	sort.SliceStable(ordered, func(i, j int) bool {
		ti, tj := ordered[i].At, ordered[j].At
		if ti.IsZero() {
			ti = time.Time{}
		}
		if tj.IsZero() {
			tj = time.Time{}
		}
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if ordered[i].Session != ordered[j].Session {
			return ordered[i].Session < ordered[j].Session
		}
		return ordered[i].Seq < ordered[j].Seq
	})
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[len(ordered)-limit:]
	}
	return ordered
}

func parseTrafficSeq(raw string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func trafficQueryHasFilters(q scanctx.TrafficQuery) bool {
	return strings.TrimSpace(q.Endpoint) != "" ||
		strings.TrimSpace(q.Method) != "" ||
		strings.TrimSpace(q.Host) != ""
}

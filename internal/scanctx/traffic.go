package scanctx

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/storage"
)

// TrafficArchive — per-scan raw HTTP exchange record (2026-11-22 operator
// decision on the R2b red line): every attributed request/response that the
// traffic proxy observed is persisted UNREDACTED (header values, bodies) so
// findings can display the exact wire packets. The operator explicitly chose
// raw fidelity over credential masking for this archive; it lives under the
// scan's own directory, inside the scan isolation boundary.
//
// This is deliberately separate from the InfoItem traffic observation: the
// observation is the compact 4 KiB audit line (summary + seven coordinates,
// masked query values), while the archive holds the bounded raw packets and
// the observation references it by sequence number.
//
// Bounding: the proxy captures at most rawCaptureLimit bytes per side
// (trafficproxy package); the archive itself adds no second truncation — it
// stores what it is handed. One JSON file per exchange under
// <ScanDir>/traffic/ via storage.WriteAtomic (invariant: O_EXCL + fsync +
// rename + dir fsync), sequence numbers resume past existing files so a
// restart never overwrites a recorded exchange.

// trafficArchiveDir is the per-scan directory holding raw HTTP exchanges.
const trafficArchiveDir = "traffic"

// trafficIDPattern validates archive ids (tr_<seq>) and extracts the sequence
// number as submatch 1 (used by NewTrafficArchive to resume the counter and
// by the reader to order records).
var trafficIDPattern = regexp.MustCompile(`^tr_([0-9]{1,12})\.json$`)

// TrafficExchange is one raw proxied request/response pair. RequestRaw and
// ResponseRaw are wire-fidelity renderings (request line, headers, body) with
// the only bound being the proxy's capture limit; nothing is masked.
type TrafficExchange struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Method string    `json:"method"`
	URL    string    `json:"url"`
	Status int       `json:"status,omitempty"`
	// Channel records how the run token reached the proxy (bearer / subprocess).
	Channel string `json:"channel,omitempty"`
	// HighRisk carries the AssessHTTP reason when the request pended the
	// proxy-side gate; Denied marks a request the gate refused (403, the
	// upstream was never contacted).
	HighRisk string `json:"high_risk,omitempty"`
	Denied   bool   `json:"denied,omitempty"`
	Upstream string `json:"upstream,omitempty"`

	RequestRaw    string `json:"request_raw,omitempty"`
	ResponseRaw   string `json:"response_raw,omitempty"`
	ReqTruncated  bool   `json:"request_truncated,omitempty"`
	RespTruncated bool   `json:"response_truncated,omitempty"`
}

// TrafficArchive persists raw HTTP exchanges for one scan.
type TrafficArchive struct {
	mu   sync.Mutex
	dir  string
	next int64
}

// NewTrafficArchive creates an archive rooted at <scanDir>/traffic.
// A nil-safe disabled archive is returned when scanDir is empty. The sequence
// counter starts past the highest existing id so reopen (resume) appends
// without overwriting.
func NewTrafficArchive(scanDir string) *TrafficArchive {
	if scanDir == "" {
		return &TrafficArchive{}
	}
	a := &TrafficArchive{dir: filepath.Join(scanDir, trafficArchiveDir)}
	if entries, err := os.ReadDir(a.dir); err == nil {
		for _, e := range entries {
			if m := trafficIDPattern.FindStringSubmatch(e.Name()); m != nil {
				if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > a.next {
					a.next = n
				}
			}
		}
	}
	return a
}

// enabled reports whether the archive has a backing directory.
func (a *TrafficArchive) enabled() bool {
	return a != nil && a.dir != ""
}

// Count reports how many exchanges this archive holds. The sequence counter is
// seeded past the highest existing record on reopen, so it is also the append
// count. Zero means the capture plane produced nothing for this run — the
// report gate uses it to distinguish "the model named no evidence" from
// "evidence capture is unavailable in this environment".
func (a *TrafficArchive) Count() int64 {
	if !a.enabled() {
		return 0
	}
	// Count on disk rather than trusting the in-memory counter: another
	// archive instance (test seed, resume reopen) may have appended since this
	// object was constructed, and the report gate must see those records.
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.next
	}
	var n int64
	for _, entry := range entries {
		if trafficIDPattern.MatchString(entry.Name()) {
			n++
		}
	}
	return n
}

// Append stores one exchange and returns its sequence number. Failure is
// reported but never fatal to the caller: the observation journal line (which
// carries the summary) is written independently, and a lost raw packet must
// not take the sink goroutine down — callers log and continue.
func (a *TrafficArchive) Append(ex *TrafficExchange) (int64, error) {
	if !a.enabled() {
		return 0, fmt.Errorf("scanctx: traffic archive disabled")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next++
	ex.Seq = a.next
	// Belt-and-braces immutability: never overwrite an existing record even if
	// the reopen scan raced a concurrent writer (mirrors ToolArchive.Archive).
	target := ""
	for {
		target = filepath.Join(a.dir, fmt.Sprintf("tr_%06d.json", a.next))
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		a.next++
		ex.Seq = a.next
	}
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return 0, err
	}
	data, err := json.Marshal(ex)
	if err != nil {
		return 0, err
	}
	if err := storage.WriteAtomic(target, data); err != nil {
		return 0, err
	}
	return ex.Seq, nil
}

// TrafficExchangeFile renders the archive-relative file name for a seq so the
// InfoItem context can point at the raw record.
func TrafficExchangeFile(seq int64) string {
	return trafficArchiveDir + "/" + fmt.Sprintf("tr_%06d.json", seq)
}

// TrafficRef pins one archived exchange inside a run's traffic evidence.
// Session is the session directory basename that owns the record — a run can
// span sibling session directories (re-dispatch/checkpoint restores), and
// sequence numbers restart in each one, so (Session, Seq) is the stable
// identity. An empty Session means "the scan's own directory".
type TrafficRef struct {
	Session string `json:"session,omitempty"`
	Seq     int64  `json:"seq"`
}

// trafficFiles lists the archive records of one scan directory ordered by
// sequence. total is the number of records; a missing archive is empty, not an
// error.
func trafficFiles(scanDir string) (files []int64, err error) {
	dir := filepath.Join(scanDir, trafficArchiveDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if m := trafficIDPattern.FindStringSubmatch(e.Name()); m != nil {
			if n, perr := strconv.ParseInt(m[1], 10, 64); perr == nil {
				files = append(files, n)
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i] < files[j] })
	return files, nil
}

// ReadTrafficExchangesNewest loads only the newest `limit` archived exchanges
// of a scan directory (ascending sequence) plus the total record count. It is
// the live-panel read path: listing and parsing every record on every poll
// would make the endpoint proportional to the whole run, while the UI only
// renders the newest window. limit <= 0 reads everything.
func ReadTrafficExchangesNewest(scanDir string, limit int) ([]TrafficExchange, int, error) {
	files, err := trafficFiles(scanDir)
	if err != nil || len(files) == 0 {
		return nil, 0, err
	}
	total := len(files)
	if limit > 0 && len(files) > limit {
		files = files[len(files)-limit:]
	}
	exchanges := make([]TrafficExchange, 0, len(files))
	for _, seq := range files {
		ex, rerr := ReadTrafficExchangeAt(scanDir, seq)
		if rerr != nil || ex == nil {
			continue // a torn record must not blank the whole window
		}
		exchanges = append(exchanges, *ex)
	}
	return exchanges, total, nil
}

// ReadTrafficExchangeAt loads one archived exchange by sequence. A missing
// record returns (nil, nil) so callers can answer "not found" without
// treating it as an infrastructure error.
func ReadTrafficExchangeAt(scanDir string, seq int64) (*TrafficExchange, error) {
	if seq <= 0 {
		return nil, nil
	}
	path := filepath.Join(scanDir, trafficArchiveDir, fmt.Sprintf("tr_%06d.json", seq))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ex TrafficExchange
	if err := json.Unmarshal(data, &ex); err != nil {
		return nil, err
	}
	return &ex, nil
}

// MatchTrafficRefs filters one scan directory's archive for a finding and
// returns the newest matches as durable refs (Session = the directory
// basename). host, when non-empty, pins the match to that asset so a
// same-path request against another asset never becomes evidence.
func MatchTrafficRefs(scanDir, endpoint, method, host string, limit int) ([]TrafficRef, int) {
	all, err := ReadTrafficExchanges(scanDir)
	if err != nil || len(all) == 0 {
		return nil, 0
	}
	matched, total := FilterTrafficExchangesWith(all, TrafficQuery{
		Endpoint: endpoint,
		Method:   method,
		Host:     host,
		Limit:    limit,
	})
	if len(matched) == 0 {
		return nil, total
	}
	session := filepath.Base(strings.TrimSpace(scanDir))
	refs := make([]TrafficRef, 0, len(matched))
	for _, ex := range matched {
		refs = append(refs, TrafficRef{Session: session, Seq: ex.Seq})
	}
	return refs, total
}

// ReadTrafficExchanges loads the archived exchanges for a scan directory,
// ordered by sequence. Works without a live ScanContext (post-scan reads);
// a missing archive yields an empty slice, not an error.
func ReadTrafficExchanges(scanDir string) ([]TrafficExchange, error) {
	files, err := trafficFiles(scanDir)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	exchanges := make([]TrafficExchange, 0, len(files))
	for _, seq := range files {
		ex, rerr := ReadTrafficExchangeAt(scanDir, seq)
		if rerr != nil || ex == nil {
			continue // a torn record must not blank the whole archive
		}
		exchanges = append(exchanges, *ex)
	}
	return exchanges, nil
}

// TrafficQuery narrows an archive listing. Empty fields mean "any"; Limit <= 0
// means "all matches". Host is matched case-insensitively against the exchange
// URL's hostname (port ignored), which is the join key between captured traffic
// and a discovered asset.
type TrafficQuery struct {
	Endpoint string
	Method   string
	Host     string
	Limit    int
}

// MatchTrafficExchanges filters archived exchanges for a finding's endpoint.
// The endpoint is matched as a case-insensitive substring of the exchange URL
// (findings carry paths or URLs; the archive stores absolute URLs); method,
// when non-empty, must match exactly. At most limit newest matches are
// returned in ascending sequence order. An empty endpoint returns the newest
// exchanges regardless of URL.
func MatchTrafficExchanges(scanDir, endpoint, method string, limit int) ([]TrafficExchange, int, error) {
	all, err := ReadTrafficExchanges(scanDir)
	if err != nil || len(all) == 0 {
		return nil, 0, err
	}
	matched, total := FilterTrafficExchanges(all, endpoint, method, limit)
	return matched, total, nil
}

// FilterTrafficExchanges applies the endpoint-substring / method filters with
// a newest-window limit to an already-loaded exchange list (shared with the
// web-layer projection, which also distinguishes an empty archive from an
// empty match).
func FilterTrafficExchanges(all []TrafficExchange, endpoint, method string, limit int) ([]TrafficExchange, int) {
	return FilterTrafficExchangesWith(all, TrafficQuery{Endpoint: endpoint, Method: method, Limit: limit})
}

// FilterTrafficExchangesWith applies the full query (endpoint substring,
// method, asset host, newest-window limit) to an already-loaded exchange list.
// Host matching uses the exchange URL's hostname so a traffic record can be
// pinned to the discovered asset it was observed against.
func FilterTrafficExchangesWith(all []TrafficExchange, q TrafficQuery) ([]TrafficExchange, int) {
	ep := strings.ToLower(strings.TrimSpace(q.Endpoint))
	m := strings.ToUpper(strings.TrimSpace(q.Method))
	host := strings.ToLower(strings.TrimSpace(q.Host))
	matched := make([]TrafficExchange, 0)
	for _, ex := range all {
		if m != "" && strings.ToUpper(ex.Method) != m {
			continue
		}
		if ep != "" && !strings.Contains(strings.ToLower(ex.URL), ep) {
			continue
		}
		if host != "" && HostOf(ex) != host {
			continue
		}
		matched = append(matched, ex)
	}
	total := len(matched)
	if q.Limit > 0 && len(matched) > q.Limit {
		matched = matched[len(matched)-q.Limit:] // newest window, ascending order
	}
	return matched, total
}

// HostOf returns the lowercased hostname of an exchange URL (port stripped).
// Unparseable URLs yield "" — callers treat that as "no asset binding".
func HostOf(ex TrafficExchange) string {
	u, err := url.Parse(strings.TrimSpace(ex.URL))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

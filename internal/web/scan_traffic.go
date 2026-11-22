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
//	limit=<n>             newest-window cap, default 20, hard cap 100
//
// Every returned exchange carries its derived `host` so the UI can group raw
// packets by asset without re-parsing URLs client-side.
//
// The exchange files live under the scan directory (scanctx.TrafficArchive),
// so this works after the scan context is torn down and without a DSN — the
// file layer is the authority, the handler is a pure projection.
import (
	"net/http"
	"strconv"
	"strings"

	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

const (
	trafficDefaultLimit = 20
	trafficMaxLimit     = 100
)

// trafficExchangeView is an archived exchange plus its derived asset host, so
// the UI can bind a raw packet to a discovered asset without re-parsing URLs.
type trafficExchangeView struct {
	scanctx.TrafficExchange
	Host string `json:"host,omitempty"`
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
	dir, ok := s.resolveScanDirByID(scanID)
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
	all, err := scanctx.ReadTrafficExchanges(dir)
	if err != nil {
		http.Error(w, `{"error":"failed to read traffic archive"}`, http.StatusInternalServerError)
		return
	}
	view.Present = len(all) > 0

	q := r.URL.Query()
	matched, total := scanctx.FilterTrafficExchangesWith(all, scanctx.TrafficQuery{
		Endpoint: q.Get("endpoint"),
		Method:   q.Get("method"),
		Host:     q.Get("host"),
		Limit:    limit,
	})
	view.Exchanges = make([]trafficExchangeView, 0, len(matched))
	for _, ex := range matched {
		view.Exchanges = append(view.Exchanges, trafficExchangeView{
			TrafficExchange: ex,
			Host:            scanctx.HostOf(ex),
		})
	}
	view.TotalMatched = total
	view.Truncated = total > len(matched)
	writeJSONStatus(w, http.StatusOK, view)
}

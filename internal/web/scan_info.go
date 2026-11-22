package web

// Project-scoped, read-only InfoItem activation endpoints (R5 / CORE §4).
//
// GET /api/info/region       — property filter across the project's scans
// GET /api/info/association  — bounded relation traversal: within one
//                              whitelisted scan (scan_id form) or aggregated
//                              across the project's scans (project-wide form)
// GET /api/info/context      — fixed-dimension context slice within one scan
//
// Every endpoint accepts an optional task_id narrowing: the task must belong
// to the project (404 otherwise) and the whitelist shrinks to that task's run
// scan ids — it can only narrow, never widen. info_items has no task column;
// the task dimension is the run scan set (REDESIGN D6/D11).
//
// Project isolation is enforced before any infoitem query: the requested
// scan_id (when present) must belong to the project, and Region always receives
// the project's scan_id set. The infoitem package stays platform-agnostic; no
// project_id column is added to info_items.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/infoitem"
	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// SetInfoReader wires the optional info cold-path reader. nil falls back to
// infoitem.DefaultReader() at request time, so startup registration order does
// not matter. Tests can inject a bounded fake.
func (s *Server) SetInfoReader(reader infoitem.Reader) {
	s.infoReaderMu.Lock()
	defer s.infoReaderMu.Unlock()
	s.infoReader = reader
}

func (s *Server) currentInfoReader() infoitem.Reader {
	s.infoReaderMu.RLock()
	reader := s.infoReader
	s.infoReaderMu.RUnlock()
	if reader != nil {
		return reader
	}
	return infoitem.DefaultReader()
}

type infoRegionResponse struct {
	Available bool `json:"available"`
	infoitem.RegionResult
}

type infoContextResponse struct {
	Available bool `json:"available"`
	infoitem.ContextResult
}

type infoAssociationResponse struct {
	Available bool `json:"available"`
	infoitem.AssociationResult
	// ScansConsulted is set only for the project-wide form and discloses how
	// many whitelisted scans the aggregation actually queried.
	ScansConsulted int `json:"scans_consulted,omitempty"`
}

// maxProjectAssociationScans bounds the per-scan fan-out of the project-wide
// association form. Aggregation cost is one Reader.Associate call per scan, so
// a project with hundreds of runs would turn one UI request into hundreds of
// cold-path round-trips. Runs beyond the cap are not queried and the omission
// is disclosed honestly via truncated=true plus the scans_consulted count.
const maxProjectAssociationScans = 20

func writeInfoDisabled(w http.ResponseWriter) {
	writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
		"available": false,
		"reason":    "cold_path_disabled",
		"error":     "info space requires the PostgreSQL cold path (no DSN configured)",
	})
}

func writeInfoUnavailable(w http.ResponseWriter, err error) {
	writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
		"available": false,
		"reason":    "cold_path_unavailable",
		"error":     err.Error(),
	})
}

// handleInfoRoutes dispatches /api/info/{region|association|context}.
func (s *Server) handleInfoRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/api/info/") {
	case "region":
		s.handleInfoRegion(w, r)
	case "association":
		s.handleInfoAssociation(w, r)
	case "context":
		s.handleInfoContext(w, r)
	default:
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "unknown info endpoint"})
	}
}

func (s *Server) infoProject(w http.ResponseWriter, r *http.Request) (platform.Repository, string, []string, bool) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return nil, "", nil, false
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "project_id is required"})
		return nil, "", nil, false
	}
	if _, err := store.GetProject(r.Context(), projectID); err != nil {
		writePlatformError(w, err)
		return nil, "", nil, false
	}
	scanIDs, err := store.ProjectScanIDs(r.Context(), projectID)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "project scan lookup failed"})
		return nil, "", nil, false
	}
	return store, projectID, scanIDs, true
}

func infoQueryLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, nil // infoitem.NormalizeLimit applies the default
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > infoitem.MaxQueryLimit {
		return 0, errors.New("limit must be within [1,200]")
	}
	return n, nil
}

func infoQueryTime(r *http.Request, key string) (*time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, errors.New(key + " must be RFC3339")
	}
	return &t, nil
}

func infoQueryIDs(r *http.Request, key string) []string {
	raw := strings.Split(r.URL.Query().Get(key), ",")
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, entry := range raw {
		id := strings.TrimSpace(entry)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func infoQueryEdgeTypes(r *http.Request) ([]infoitem.RelationType, error) {
	raw := infoQueryIDs(r, "edge_types")
	out := make([]infoitem.RelationType, 0, len(raw))
	for _, value := range raw {
		typ := infoitem.RelationType(value)
		if !typ.Valid() {
			return nil, errors.New("invalid edge_type " + value)
		}
		out = append(out, typ)
	}
	return out, nil
}

// narrowInfoScans applies the optional task_id narrowing to the project scan
// whitelist: the task must belong to the project (ErrNotFound → 404) and the
// task's run scan ids are intersected with the whitelist, so isolation can
// only shrink, never widen. Without the parameter the whitelist passes
// through unchanged.
func (s *Server) narrowInfoScans(w http.ResponseWriter, r *http.Request, store platform.Repository, projectID string, scanIDs []string) ([]string, bool) {
	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	if taskID == "" {
		return scanIDs, true
	}
	taskScans, err := store.TaskScanIDs(r.Context(), projectID, taskID)
	if err != nil {
		writePlatformError(w, err)
		return nil, false
	}
	out := make([]string, 0, len(taskScans))
	for _, id := range taskScans {
		if containsString(scanIDs, id) {
			out = append(out, id)
		}
	}
	return out, true
}

func (s *Server) handleInfoRegion(w http.ResponseWriter, r *http.Request) {
	store, projectID, scanIDs, ok := s.infoProject(w, r)
	if !ok {
		return
	}
	scanIDs, ok = s.narrowInfoScans(w, r, store, projectID, scanIDs)
	if !ok {
		return
	}
	reader := s.currentInfoReader()
	if reader == nil {
		writeInfoDisabled(w)
		return
	}
	limit, err := infoQueryLimit(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	after, err := infoQueryTime(r, "after")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	before, err := infoQueryTime(r, "before")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(scanIDs) == 0 {
		writeJSONStatus(w, http.StatusOK, infoRegionResponse{Available: true})
		return
	}
	q := infoitem.RegionQuery{
		ScanIDs:        scanIDs,
		SubjectKind:    strings.TrimSpace(r.URL.Query().Get("subject_kind")),
		Subject:        strings.TrimSpace(r.URL.Query().Get("subject")),
		Purpose:        strings.TrimSpace(r.URL.Query().Get("purpose")),
		Nature:         infoitem.Nature(strings.TrimSpace(r.URL.Query().Get("nature"))),
		ObservedAfter:  after,
		ObservedBefore: before,
		Limit:          limit,
	}
	res, err := reader.Region(r.Context(), q)
	if err != nil {
		if s.infoReaderUnavailable(err) {
			writeInfoUnavailable(w, err)
			return
		}
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSONStatus(w, http.StatusOK, infoRegionResponse{Available: true, RegionResult: res})
}

func (s *Server) handleInfoContext(w http.ResponseWriter, r *http.Request) {
	_, _, scanIDs, ok := s.infoProject(w, r)
	if !ok {
		return
	}
	scanID := strings.TrimSpace(r.URL.Query().Get("scan_id"))
	if scanID == "" || !containsString(scanIDs, scanID) {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "scan_id is not in this project"})
		return
	}
	reader := s.currentInfoReader()
	if reader == nil {
		writeInfoDisabled(w)
		return
	}
	limit, err := infoQueryLimit(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	since, err := infoQueryTime(r, "since")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	until, err := infoQueryTime(r, "until")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	q := infoitem.ContextQuery{
		ScanID:      scanID,
		Subject:     strings.TrimSpace(r.URL.Query().Get("subject")),
		SubjectKind: strings.TrimSpace(r.URL.Query().Get("subject_kind")),
		Since:       since,
		Until:       until,
		Limit:       limit,
	}
	res, err := reader.ContextSlice(r.Context(), q)
	if err != nil {
		if s.infoReaderUnavailable(err) {
			writeInfoUnavailable(w, err)
			return
		}
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSONStatus(w, http.StatusOK, infoContextResponse{Available: true, ContextResult: res})
}

func (s *Server) handleInfoAssociation(w http.ResponseWriter, r *http.Request) {
	store, projectID, scanIDs, ok := s.infoProject(w, r)
	if !ok {
		return
	}
	scanIDs, ok = s.narrowInfoScans(w, r, store, projectID, scanIDs)
	if !ok {
		return
	}
	// Two request shapes (CORE v2.1 §4.2):
	//   scan_id present    — bounded traversal within that one whitelisted
	//                        scan (contract §3.3 single-scan form, unchanged);
	//   scan_id absent     — project-wide form: the same bounded traversal
	//                        aggregated over every whitelisted scan of the
	//                        project (optionally narrowed by task_id),
	//                        merged/deduped on the item id dimension.
	// Cross-scan aggregation never widens the whitelist: the scan set comes
	// from platform.ProjectScanIDs for the requested project only.
	scanID := strings.TrimSpace(r.URL.Query().Get("scan_id"))
	if scanID != "" {
		if !containsString(scanIDs, scanID) {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "scan_id is not in this project"})
			return
		}
		s.associationQuery(w, r, []string{scanID}, false)
		return
	}
	s.associationQuery(w, r, scanIDs, true)
}

// associationQuery runs the shared bounded-traversal parameter parsing and,
// for the project-wide form, the per-scan aggregation loop. The infoitem
// Reader interface stays untouched: aggregation is plain sequential
// Reader.Associate calls plus an id-dimension merge (no recursive CTE, no
// cross-store transaction).
func (s *Server) associationQuery(w http.ResponseWriter, r *http.Request, scanIDs []string, projectWide bool) {
	reader := s.currentInfoReader()
	if reader == nil {
		writeInfoDisabled(w)
		return
	}
	limit, err := infoQueryLimit(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	edgeTypes, err := infoQueryEdgeTypes(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	depth := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("max_depth")); raw != "" {
		depth, err = strconv.Atoi(raw)
		if err != nil || depth < 1 || depth > infoitem.MaxAssociationDepth {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "max_depth must be within [1,3]"})
			return
		}
	}
	startIDs := infoQueryIDs(r, "ids")
	if projectWide && len(startIDs) == 0 {
		// Without explicit start ids a fan-out would query every scan with an
		// invalid traversal; the Reader would reject it anyway, so fail fast.
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "ids is required"})
		return
	}
	// The per-scan queries receive the raw limit (0 lets the Reader normalize
	// it); the merged result set is capped by the effective budget here.
	budget := infoitem.NormalizeLimit(limit)
	consulted := scanIDs
	capped := false
	if len(consulted) > maxProjectAssociationScans {
		consulted = consulted[:maxProjectAssociationScans]
		capped = true
	}
	merged := infoitem.AssociationResult{}
	seen := map[string]struct{}{}
	for _, scan := range consulted {
		q := infoitem.AssociationQuery{
			ScanID:    scan,
			StartIDs:  startIDs,
			EdgeTypes: edgeTypes,
			MaxDepth:  depth,
			Limit:     limit,
		}
		res, err := reader.Associate(r.Context(), q)
		if err != nil {
			if s.infoReaderUnavailable(err) {
				writeInfoUnavailable(w, err)
				return
			}
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		for _, hit := range res.Hits {
			if _, dup := seen[hit.Item.ID]; dup {
				continue
			}
			if len(merged.Hits) >= budget {
				merged.Truncated = true
				break
			}
			seen[hit.Item.ID] = struct{}{}
			merged.Hits = append(merged.Hits, hit)
		}
		if res.Truncated {
			merged.Truncated = true
		}
		if res.Incomplete {
			merged.Incomplete = true
		}
		if merged.GapReason == "" {
			merged.GapReason = res.GapReason
		}
		if len(merged.Hits) >= budget {
			break
		}
	}
	if capped {
		merged.Truncated = true
	}
	resp := infoAssociationResponse{Available: true, AssociationResult: merged}
	if projectWide {
		resp.ScansConsulted = len(consulted)
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

// infoReaderUnavailable distinguishes a missing cold-path backend (503) from
// a bad filter (400). The message is produced by the PG store when disabled.
func (s *Server) infoReaderUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "store unavailable")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

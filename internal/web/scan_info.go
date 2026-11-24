package web

// Project-scoped, read-only InfoItem activation endpoints (R5 / CORE §4).
//
// GET /api/info/region       — property filter across the project's scans
// GET /api/info/heatmap      — bounded subject × info-type density aggregation
// GET /api/info/relations    — subject-level relation aggregates (project graph)
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
// Every endpoint also accepts an optional layer filter (IG1b):
//
//	layer=aggregate|atom|all  (default all)
//
// When the graph authority implements infospace.Reader (the PG backend does),
// the query is served from the unified information-space views and the layer
// filter is applied there; the project scan whitelist is mapped onto the
// canonical run identity with the same run-id resolution the other graph-backed
// web handlers use. When the graph reader is absent (no PG pool, memory/legacy
// store, tests) the handlers keep the InfoItem cold-path behavior: the cold
// path only knows the atom layer, so layer=aggregate without a graph reader is
// an explicit 503 rather than a silently empty aggregate answer.
//
// Project isolation is enforced before any infoitem query: the requested
// scan_id (when present) must belong to the project, and Region always receives
// the project's scan_id set. The infoitem package stays platform-agnostic; no
// project_id column is added to info_items.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// infoGraphReader resolves the unified information-space reader. The graph
// authority's PGStore implements infospace.Reader; a nil or non-implementing
// backend means the graph layer is unavailable and the handlers fall back to
// the InfoItem cold path. This is a package-level seam so tests can inject a
// bounded fake without installing a process-wide graph backend.
var infoGraphReader = func() infospace.Reader {
	if repo := graph.Backend(); repo != nil {
		if reader, ok := repo.(infospace.Reader); ok {
			return reader
		}
	}
	return nil
}

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
	// Layer echoes the effective layer filter ("aggregate"|"atom"|"all").
	Layer string `json:"layer,omitempty"`
	// Source names the backend that served the response ("graph"|"cold_path").
	Source string `json:"source,omitempty"`
	infoitem.RegionResult
}

type infoContextResponse struct {
	Available bool   `json:"available"`
	Layer     string `json:"layer,omitempty"`
	Source    string `json:"source,omitempty"`
	infoitem.ContextResult
}

type infoAssociationResponse struct {
	Available bool   `json:"available"`
	Layer     string `json:"layer,omitempty"`
	Source    string `json:"source,omitempty"`
	infoitem.AssociationResult
	// ScansConsulted is set only for the project-wide form and discloses how
	// many whitelisted scans the aggregation actually queried.
	ScansConsulted int `json:"scans_consulted,omitempty"`
}

// infoHeatmapResponse is the bounded subject × info-type density map. Counts
// are aggregation results, not bodies; drill-down goes back through Region
// with the same subject and type.
type infoHeatmapResponse struct {
	Available    bool             `json:"available"`
	Source       string           `json:"source,omitempty"`
	Types        []string         `json:"types"`
	Rows         []infoHeatmapRow `json:"rows"`
	Truncated    bool             `json:"truncated"`
	SubjectTotal int              `json:"subject_total"`
	Unlabeled    int              `json:"unlabeled"`
	Incomplete   bool             `json:"incomplete"`
	GapReason    string           `json:"gap_reason,omitempty"`
}

type infoHeatmapRow struct {
	Subject      string         `json:"subject"`
	SubjectKind  string         `json:"subject_kind,omitempty"`
	Total        int            `json:"total"`
	Counts       map[string]int `json:"counts"`
	LastObserved *time.Time     `json:"last_observed,omitempty"`
}

// infoRelationsResponse is the subject-level project graph. Each edge carries
// the real relation, a count and one representative title per endpoint; it is
// a read model, never a new write authority.
type infoRelationsResponse struct {
	Available  bool               `json:"available"`
	Source     string             `json:"source,omitempty"`
	Edges      []infoRelationEdge `json:"edges"`
	Truncated  bool               `json:"truncated"`
	Incomplete bool               `json:"incomplete"`
	GapReason  string             `json:"gap_reason,omitempty"`
}

type infoRelationEdge struct {
	SrcSubject string     `json:"src_subject"`
	SrcKind    string     `json:"src_kind,omitempty"`
	DstSubject string     `json:"dst_subject"`
	DstKind    string     `json:"dst_kind,omitempty"`
	Rel        string     `json:"rel"`
	Category   string     `json:"category"`
	Count      int        `json:"count"`
	SrcTitle   string     `json:"src_title,omitempty"`
	DstTitle   string     `json:"dst_title,omitempty"`
	LastAt     *time.Time `json:"last_at,omitempty"`
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

// nonNilItems normalizes a reader result so empty result sets marshal as []
// instead of JSON null: the dashboard iterates these arrays unconditionally,
// and a nil slice from any Reader implementation would crash the page.
func nonNilItems(items []infoitem.Item) []infoitem.Item {
	if items == nil {
		return []infoitem.Item{}
	}
	return items
}

func writeInfoUnavailable(w http.ResponseWriter, err error) {
	writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
		"available": false,
		"reason":    "cold_path_unavailable",
		"error":     err.Error(),
	})
}

// writeInfoLayerUnavailable is the explicit answer when a graph-only layer is
// requested while the unified space is unavailable. Returning an empty
// aggregate page would claim "nothing was found" about a layer the cold path
// cannot see at all.
func writeInfoLayerUnavailable(w http.ResponseWriter, layer infospace.Layer) {
	writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
		"available": false,
		"reason":    "layer_unavailable",
		"layer":     string(layer),
		"error":     "layer " + string(layer) + " requires the graph information space (backend unavailable)",
	})
}

// writeInfoGraphFailed reports a graph read that failed for a reason other
// than absence. It is a server-side failure, not a bad filter.
func writeInfoGraphFailed(w http.ResponseWriter, err error) {
	writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
		"available": false,
		"reason":    "graph_query_failed",
		"error":     err.Error(),
	})
}

// infoQueryLayer parses the optional layer parameter. The empty value and
// "all" both mean the full unified space; the graph reader encodes that as an
// empty infospace.Layer.
func infoQueryLayer(r *http.Request) (infospace.Layer, error) {
	switch raw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("layer"))); raw {
	case "", "all":
		return "", nil
	case string(infospace.LayerAggregate):
		return infospace.LayerAggregate, nil
	case string(infospace.LayerAtom):
		return infospace.LayerAtom, nil
	default:
		return "", errors.New("layer must be one of aggregate, atom, all")
	}
}

func infoLayerLabel(layer infospace.Layer) string {
	if layer == "" {
		return "all"
	}
	return string(layer)
}

// infoGraphUnavailable reports whether a graph read failed because the backend
// is absent rather than because the query was rejected. Absence falls back to
// the cold path; any other error is surfaced instead of silently degrading.
func infoGraphUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unavailable")
}

// infoRunIDs maps the project scan-id whitelist onto the canonical run
// identity the unified views key on (`run_id` = the D11 instance id, with the
// raw ScanContext id as the atom fallback for un-journaled rows). The mapping
// uses the same resolver as every other graph-backed web handler
// (graphRunID), so no new identity is introduced. Both the raw id and the
// resolved run id are kept, deduped and in whitelist order: dropping the raw
// id would hide atoms whose run_journal row is missing, and the expansion can
// only reach an already-whitelisted run instance.
func (s *Server) infoRunIDs(scanIDs []string) []string {
	out := make([]string, 0, len(scanIDs))
	seen := make(map[string]struct{}, len(scanIDs))
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range scanIDs {
		add(id)
		add(s.graphRunID(id))
	}
	return out
}

// infoItemFromSpace adapts one unified-space row onto the dashboard's existing
// InfoItem shape. Only summary coordinates are returned: the body stays behind
// the InfoItem/ledger detail endpoints (spec §4.1), and layer-native extras
// (state, unit_key, run_id) are deliberately not invented here.
func infoItemFromSpace(item infospace.Item) infoitem.Item {
	scanID := item.ScanID
	if scanID == "" {
		scanID = item.RunID
	}
	nature := infoitem.Nature(item.Nature)
	if !nature.Valid() {
		nature = ""
	}
	out := infoitem.Item{
		ID:     item.InfoID,
		ScanID: scanID,
		Nature: nature,
		Coordinates: infoitem.Coordinates{
			Source:     infoitem.Source{Kind: item.Origin},
			ObservedAt: item.ObservedAt,
			Subject:    infoitem.Subject{Kind: item.SubjectKind, Identifier: item.Subject},
			Purpose:    item.Purpose,
			Context:    decodeInfoSpaceContext(item.Context),
		},
		CreatedAt: item.CreatedAt,
	}
	applyInfoSpacePayload(&out, item.Payload)
	return out
}

// applyInfoSpacePayload lifts the summary fields both layers already carry in
// their native payload (content, title/summary, supports, confidence). Bodies
// stay behind the item detail endpoint; these are the same one-line summaries
// the region table always showed. Decode failures are ignored: a malformed
// legacy payload must not break the whole page.
func applyInfoSpacePayload(item *infoitem.Item, raw []byte) {
	if len(raw) == 0 || item == nil {
		return
	}
	var payload struct {
		Content *struct {
			Text      string `json:"text"`
			MediaType string `json:"media_type"`
		} `json:"content"`
		Title      string          `json:"title"`
		Summary    string          `json:"summary"`
		Supports   json.RawMessage `json:"supports"`
		Confidence *float64        `json:"confidence"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	if payload.Confidence != nil {
		item.Confidence = *payload.Confidence
	}
	if item.Coordinates.Content.Text == "" {
		switch {
		case payload.Content != nil && payload.Content.Text != "":
			item.Coordinates.Content = infoitem.Content{
				Text:      payload.Content.Text,
				MediaType: payload.Content.MediaType,
			}
		case payload.Summary != "":
			item.Coordinates.Content = infoitem.Content{Text: payload.Summary, MediaType: "text/plain"}
		case payload.Title != "":
			item.Coordinates.Content = infoitem.Content{Text: payload.Title, MediaType: "text/plain"}
		}
	}
	if len(payload.Supports) == 0 {
		return
	}
	var supports []string
	if err := json.Unmarshal(payload.Supports, &supports); err != nil {
		return
	}
	item.Supports = supports
}

// decodeInfoSpaceContext adapts the view's jsonb context onto the string map
// the dashboard consumes. Non-string values are kept as their JSON text
// instead of dropping the key.
func decodeInfoSpaceContext(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	out := make(map[string]string, len(decoded))
	for key, value := range decoded {
		var text string
		if err := json.Unmarshal(value, &text); err == nil {
			out[key] = text
			continue
		}
		out[key] = string(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func infoRegionFromSpace(res infospace.RegionResult) infoitem.RegionResult {
	items := make([]infoitem.Item, 0, len(res.Items))
	for _, item := range res.Items {
		items = append(items, infoItemFromSpace(item))
	}
	return infoitem.RegionResult{
		Items:      items,
		Truncated:  res.Truncated,
		HasMore:    res.HasMore,
		NextCursor: res.NextCursor,
		Incomplete: res.Incomplete,
		GapReason:  res.GapReason,
	}
}

func infoContextFromSpace(res infospace.ContextResult) infoitem.ContextResult {
	items := make([]infoitem.Item, 0, len(res.Items))
	for _, item := range res.Items {
		items = append(items, infoItemFromSpace(item))
	}
	return infoitem.ContextResult{
		Items:      items,
		Truncated:  res.Truncated,
		Incomplete: res.Incomplete,
		GapReason:  res.GapReason,
	}
}

// filterSpaceHitsByLayer restricts traversal hits to the requested layer. The
// traversal itself spans both layers (edges connect them); the returned hits
// respect the filter while the reader's truncation/gap disclosures are kept
// as-is (conservative: a dropped hit never hides that the walk was cut).
func filterSpaceHitsByLayer(hits []infospace.AssociationHit, layer infospace.Layer) []infospace.AssociationHit {
	if layer == "" {
		return hits
	}
	out := make([]infospace.AssociationHit, 0, len(hits))
	for _, hit := range hits {
		if hit.Item.Layer == layer {
			out = append(out, hit)
		}
	}
	return out
}

func infoAssociationFromSpace(res infospace.AssociationResult) infoitem.AssociationResult {
	hits := make([]infoitem.AssociationHit, 0, len(res.Hits))
	for _, hit := range res.Hits {
		var rels []infoitem.RelationType
		if len(hit.Rels) > 0 {
			rels = make([]infoitem.RelationType, 0, len(hit.Rels))
			for _, rel := range hit.Rels {
				rels = append(rels, infoitem.RelationType(rel))
			}
		}
		hits = append(hits, infoitem.AssociationHit{
			Item:  infoItemFromSpace(hit.Item),
			Path:  hit.Path,
			Rels:  rels,
			Depth: hit.Depth,
		})
	}
	return infoitem.AssociationResult{
		Hits:       hits,
		Truncated:  res.Truncated,
		Incomplete: res.Incomplete,
		GapReason:  res.GapReason,
	}
}

// mergeAssociationResult folds one per-run traversal into the project-wide
// aggregate: dedupe on the item id dimension, cap at the shared budget, and
// carry the truncation/gap disclosures. It reports whether the budget is
// exhausted. Deterministic: callers traverse the whitelist in order and the
// merged order is the first-seen order.
func mergeAssociationResult(merged *infoitem.AssociationResult, seen map[string]struct{}, res infoitem.AssociationResult, budget int) bool {
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
	return len(merged.Hits) >= budget
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
	case "heatmap":
		s.handleInfoHeatmap(w, r)
	case "relations":
		s.handleInfoRelations(w, r)
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
	return store, projectID, s.expandSiblingScanIDs(scanIDs), true
}

// expandSiblingScanIDs unions the platform whitelist with the on-disk sibling
// record ids behind each whitelisted run instance (REDESIGN D11). A re-started
// run journals and cold-path-syncs under its own session scan id, which the
// fill-once legacy linkage never learns — without this expansion /api/info/*
// silently drops every item a resumed session produces (the same blind spot
// 09d06a3e fixed for finding projections). Expansion can only add records
// claiming already-whitelisted instances, so project isolation never widens.
func (s *Server) expandSiblingScanIDs(scanIDs []string) []string {
	if len(scanIDs) == 0 {
		return scanIDs
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	out := append([]string(nil), scanIDs...)
	for _, id := range scanIDs {
		for _, rec := range idx.byInstance[id] {
			if rec.ID == "" || containsString(out, rec.ID) {
				continue
			}
			out = append(out, rec.ID)
		}
	}
	return out
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

// infoQueryRelationLimit is the graph-edge budget. It is larger than the
// item-region limit because one edge row is a summary, not a body.
func infoQueryRelationLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, nil // infospace.RelationsQuery.Normalize applies the default
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > infospace.RelationsMaxLimit {
		return 0, errors.New("relation limit must be within [1,800]")
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

// infoQueryKinds parses the optional aggregate-kind filter (comma list). The
// unified view owns the authoritative kind set, so this only rejects empty
// entries; unknown kinds simply match nothing.
func infoQueryKinds(r *http.Request) []string {
	return infoQueryIDs(r, "kind")
}

// infoQueryInfoTypes parses the heatmap columns. Empty means the default
// observation/inference/belief/fact/finding order.
func infoQueryInfoTypes(r *http.Request) ([]infospace.InfoType, error) {
	raw := infoQueryIDs(r, "type")
	if len(raw) == 0 {
		return infospace.DefaultInfoTypes(), nil
	}
	out := make([]infospace.InfoType, 0, len(raw))
	for _, value := range raw {
		typ := infospace.InfoType(value)
		if !typ.Valid() {
			return nil, errors.New("unsupported info type " + value)
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
	// The task whitelist needs the same sibling expansion as the project one:
	// intersecting an un-expanded task set with the expanded project set
	// would silently narrow the resumed session's id back out.
	taskScans = s.expandSiblingScanIDs(taskScans)
	out := make([]string, 0, len(taskScans))
	for _, id := range taskScans {
		if containsString(scanIDs, id) {
			out = append(out, id)
		}
	}
	return out, true
}

// narrowInfoScanID applies the optional single-run narrowing used by Region
// and the heatmap. The scan must belong to the already-narrowed project/task
// whitelist; a foreign id is a 400, never a silently wider query.
func narrowInfoScanID(w http.ResponseWriter, r *http.Request, scanIDs []string) ([]string, bool) {
	runScan := strings.TrimSpace(r.URL.Query().Get("scan_id"))
	if runScan == "" {
		return scanIDs, true
	}
	if !containsString(scanIDs, runScan) {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "scan_id is not in this project"})
		return nil, false
	}
	return []string{runScan}, true
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
	scanIDs, ok = narrowInfoScanID(w, r, scanIDs)
	if !ok {
		return
	}
	layer, err := infoQueryLayer(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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
	nature := infoitem.Nature(strings.TrimSpace(r.URL.Query().Get("nature")))
	if nature != "" && !nature.Valid() {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "nature must be observed or inferred"})
		return
	}
	subjectKind := strings.TrimSpace(r.URL.Query().Get("subject_kind"))
	subject := strings.TrimSpace(r.URL.Query().Get("subject"))
	purpose := strings.TrimSpace(r.URL.Query().Get("purpose"))
	kinds := infoQueryKinds(r)
	if len(scanIDs) == 0 {
		source := "cold_path"
		if infoGraphReader() != nil {
			source = "graph"
		}
		writeJSONStatus(w, http.StatusOK, infoRegionResponse{
			Available: true, Layer: infoLayerLabel(layer), Source: source,
		})
		return
	}
	cursor, err := infoitem.ParseRegionCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if graphReader := infoGraphReader(); graphReader != nil {
		graphRes, err := graphReader.Region(r.Context(), infospace.RegionQuery{
			RunIDs:         s.infoRunIDs(scanIDs),
			Layer:          layer,
			Kinds:          kinds,
			SubjectKind:    subjectKind,
			Subject:        subject,
			Purpose:        purpose,
			Nature:         nature,
			ObservedAfter:  after,
			ObservedBefore: before,
			Cursor:         cursor,
			Limit:          limit,
		})
		if err == nil {
			res := infoRegionFromSpace(graphRes)
			// Same keyset disclosure as the cold path: a truncated page implies
			// a next page even for adapters that only fill Truncated.
			if res.Truncated && !res.HasMore {
				res.HasMore = true
			}
			writeJSONStatus(w, http.StatusOK, infoRegionResponse{
				Available: true, Layer: infoLayerLabel(layer), Source: "graph", RegionResult: res,
			})
			return
		}
		if !infoGraphUnavailable(err) {
			writeInfoGraphFailed(w, err)
			return
		}
		// The graph authority has no pool: the cold path is the only source.
	}
	if layer == infospace.LayerAggregate {
		writeInfoLayerUnavailable(w, layer)
		return
	}
	if len(kinds) > 0 {
		// The cold path has no aggregate kind dimension; answering an
		// unfiltered atom list would silently ignore the caller's filter.
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "kind filter requires the graph information space",
		})
		return
	}
	reader := s.currentInfoReader()
	if reader == nil {
		writeInfoDisabled(w)
		return
	}
	q := infoitem.RegionQuery{
		ScanIDs:        scanIDs,
		SubjectKind:    subjectKind,
		Subject:        subject,
		Purpose:        purpose,
		Nature:         nature,
		ObservedAfter:  after,
		ObservedBefore: before,
		Cursor:         cursor,
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
	res.Items = nonNilItems(res.Items)
	// HasMore is the keyset-paging form of Truncated: a Reader that only fills
	// Truncated (in-memory/bounded test adapters) must still tell the caller a
	// next page exists. The PG store additionally fills NextCursor.
	if res.Truncated && !res.HasMore {
		res.HasMore = true
	}
	writeJSONStatus(w, http.StatusOK, infoRegionResponse{
		Available: true, Layer: infoLayerLabel(layer), Source: "cold_path", RegionResult: res,
	})
}

// handleInfoHeatmap serves the bounded subject × info-type density map. It is
// an aggregation form of Region: no bodies, bounded subject rows, deterministic
// ordering. Authorization is identical to Region (project whitelist, optional
// task narrowing, optional single-run narrowing).
func (s *Server) handleInfoHeatmap(w http.ResponseWriter, r *http.Request) {
	store, projectID, scanIDs, ok := s.infoProject(w, r)
	if !ok {
		return
	}
	scanIDs, ok = s.narrowInfoScans(w, r, store, projectID, scanIDs)
	if !ok {
		return
	}
	scanIDs, ok = narrowInfoScanID(w, r, scanIDs)
	if !ok {
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
	types, err := infoQueryInfoTypes(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	subject := strings.TrimSpace(r.URL.Query().Get("subject"))
	subjectKind := strings.TrimSpace(r.URL.Query().Get("subject_kind"))
	purpose := strings.TrimSpace(r.URL.Query().Get("purpose"))
	search := strings.TrimSpace(r.URL.Query().Get("q"))

	typeLabels := make([]string, len(types))
	for i, t := range types {
		typeLabels[i] = string(t)
	}
	if len(scanIDs) == 0 {
		writeJSONStatus(w, http.StatusOK, infoHeatmapResponse{
			Available: true, Source: "graph", Types: typeLabels, Rows: []infoHeatmapRow{},
		})
		return
	}

	reader := infoGraphReader()
	heat, ok := reader.(infospace.HeatmapReader)
	if reader == nil || !ok {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"available": false,
			"reason":    "heatmap_unavailable",
			"error":     "subject × info-type aggregation requires the graph information space",
		})
		return
	}
	res, err := heat.Heatmap(r.Context(), infospace.HeatmapQuery{
		RunIDs:         s.infoRunIDs(scanIDs),
		Subject:        subject,
		SubjectKind:    subjectKind,
		Purpose:        purpose,
		Query:          search,
		Types:          types,
		ObservedAfter:  after,
		ObservedBefore: before,
		RowLimit:       limit,
	})
	if err != nil {
		if infoGraphUnavailable(err) {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
				"available": false,
				"reason":    "heatmap_unavailable",
				"error":     err.Error(),
			})
			return
		}
		writeInfoGraphFailed(w, err)
		return
	}

	resp := infoHeatmapResponse{
		Available:    true,
		Source:       "graph",
		Types:        typeLabels,
		Rows:         make([]infoHeatmapRow, 0, len(res.Rows)),
		Truncated:    res.Truncated,
		SubjectTotal: res.SubjectTotal,
		Unlabeled:    res.Unlabeled,
		Incomplete:   res.Incomplete,
		GapReason:    res.GapReason,
	}
	for _, row := range res.Rows {
		counts := make(map[string]int, len(typeLabels))
		for _, label := range typeLabels {
			counts[label] = row.Counts[infospace.InfoType(label)]
		}
		resp.Rows = append(resp.Rows, infoHeatmapRow{
			Subject:      row.Subject,
			SubjectKind:  row.SubjectKind,
			Total:        row.Total,
			Counts:       counts,
			LastObserved: row.LastObserved,
		})
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

// handleInfoRelations serves the subject-level project graph. Authorization is
// identical to the heatmap; the response is bounded and carries real relation
// types plus representative titles, never invented "pivot" edges.
func (s *Server) handleInfoRelations(w http.ResponseWriter, r *http.Request) {
	store, projectID, scanIDs, ok := s.infoProject(w, r)
	if !ok {
		return
	}
	scanIDs, ok = s.narrowInfoScans(w, r, store, projectID, scanIDs)
	if !ok {
		return
	}
	scanIDs, ok = narrowInfoScanID(w, r, scanIDs)
	if !ok {
		return
	}
	limit, err := infoQueryRelationLimit(r)
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
		writeJSONStatus(w, http.StatusOK, infoRelationsResponse{
			Available: true, Source: "graph", Edges: []infoRelationEdge{},
		})
		return
	}

	reader := infoGraphReader()
	relations, ok := reader.(infospace.RelationsReader)
	if reader == nil || !ok {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"available": false,
			"reason":    "relations_unavailable",
			"error":     "subject relations require the graph information space",
		})
		return
	}
	res, err := relations.Relations(r.Context(), infospace.RelationsQuery{
		RunIDs:         s.infoRunIDs(scanIDs),
		ObservedAfter:  after,
		ObservedBefore: before,
		Limit:          limit,
	})
	if err != nil {
		if infoGraphUnavailable(err) {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
				"available": false,
				"reason":    "relations_unavailable",
				"error":     err.Error(),
			})
			return
		}
		writeInfoGraphFailed(w, err)
		return
	}

	resp := infoRelationsResponse{
		Available:  true,
		Source:     "graph",
		Edges:      make([]infoRelationEdge, 0, len(res.Edges)),
		Truncated:  res.Truncated,
		Incomplete: res.Incomplete,
		GapReason:  res.GapReason,
	}
	for _, edge := range res.Edges {
		resp.Edges = append(resp.Edges, infoRelationEdge{
			SrcSubject: edge.SrcSubject,
			SrcKind:    edge.SrcKind,
			DstSubject: edge.DstSubject,
			DstKind:    edge.DstKind,
			Rel:        edge.Rel,
			Category:   string(edge.Category),
			Count:      edge.Count,
			SrcTitle:   edge.SrcTitle,
			DstTitle:   edge.DstTitle,
			LastAt:     edge.LastAt,
		})
	}
	writeJSONStatus(w, http.StatusOK, resp)
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
	layer, err := infoQueryLayer(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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
	subject := strings.TrimSpace(r.URL.Query().Get("subject"))
	subjectKind := strings.TrimSpace(r.URL.Query().Get("subject_kind"))
	if graphReader := infoGraphReader(); graphReader != nil {
		runID := s.graphRunID(scanID)
		if runID == "" {
			runID = scanID
		}
		graphRes, err := graphReader.ContextSlice(r.Context(), infospace.ContextQuery{
			RunID:       runID,
			Subject:     subject,
			SubjectKind: subjectKind,
			Layer:       layer,
			Since:       since,
			Until:       until,
			Limit:       limit,
		})
		if err == nil {
			writeJSONStatus(w, http.StatusOK, infoContextResponse{
				Available: true, Layer: infoLayerLabel(layer), Source: "graph",
				ContextResult: infoContextFromSpace(graphRes),
			})
			return
		}
		if !infoGraphUnavailable(err) {
			writeInfoGraphFailed(w, err)
			return
		}
	}
	if layer == infospace.LayerAggregate {
		writeInfoLayerUnavailable(w, layer)
		return
	}
	reader := s.currentInfoReader()
	if reader == nil {
		writeInfoDisabled(w)
		return
	}
	q := infoitem.ContextQuery{
		ScanID:      scanID,
		Subject:     subject,
		SubjectKind: subjectKind,
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
	res.Items = nonNilItems(res.Items)
	writeJSONStatus(w, http.StatusOK, infoContextResponse{
		Available: true, Layer: infoLayerLabel(layer), Source: "cold_path", ContextResult: res,
	})
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
// for the project-wide form, the per-run aggregation loop. With a graph reader
// the traversal is one infospace.Associate call per run id (the views already
// join both layers); otherwise the infoitem Reader stays untouched and
// aggregation is plain sequential Reader.Associate calls plus an id-dimension
// merge (no recursive CTE, no cross-store transaction) — both paths share the
// same bounds, dedupe and disclosure.
func (s *Server) associationQuery(w http.ResponseWriter, r *http.Request, scanIDs []string, projectWide bool) {
	layer, err := infoQueryLayer(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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
	capped := len(scanIDs) > maxProjectAssociationScans
	consulted := scanIDs
	if capped {
		consulted = consulted[:maxProjectAssociationScans]
	}
	if graphReader := infoGraphReader(); graphReader != nil {
		runIDs := s.infoRunIDs(consulted)
		if len(runIDs) > maxProjectAssociationScans {
			runIDs = runIDs[:maxProjectAssociationScans]
			capped = true
		}
		edgeNames := make([]string, 0, len(edgeTypes))
		for _, typ := range edgeTypes {
			edgeNames = append(edgeNames, string(typ))
		}
		merged := infoitem.AssociationResult{Hits: []infoitem.AssociationHit{}}
		seen := map[string]struct{}{}
		unavailable := false
		for _, runID := range runIDs {
			graphRes, err := graphReader.Associate(r.Context(), infospace.AssociationQuery{
				RunID:     runID,
				StartIDs:  startIDs,
				EdgeTypes: edgeNames,
				MaxDepth:  depth,
				Limit:     limit,
			})
			if err != nil {
				if infoGraphUnavailable(err) {
					// No pool: discard the partial aggregate and use the atoms
					// the cold path can see instead of mixing sources.
					unavailable = true
					break
				}
				writeInfoGraphFailed(w, err)
				return
			}
			graphRes.Hits = filterSpaceHitsByLayer(graphRes.Hits, layer)
			if mergeAssociationResult(&merged, seen, infoAssociationFromSpace(graphRes), budget) {
				break
			}
		}
		if !unavailable {
			if capped {
				merged.Truncated = true
			}
			resp := infoAssociationResponse{
				Available: true, Layer: infoLayerLabel(layer), Source: "graph",
				AssociationResult: merged,
			}
			if projectWide {
				resp.ScansConsulted = len(runIDs)
			}
			writeJSONStatus(w, http.StatusOK, resp)
			return
		}
	}
	if layer == infospace.LayerAggregate {
		writeInfoLayerUnavailable(w, layer)
		return
	}
	reader := s.currentInfoReader()
	if reader == nil {
		writeInfoDisabled(w)
		return
	}
	// Hits starts non-nil so a zero-hit traversal marshals as [] not null
	// (the dashboard iterates hits unconditionally).
	merged := infoitem.AssociationResult{Hits: []infoitem.AssociationHit{}}
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
		if mergeAssociationResult(&merged, seen, res, budget) {
			break
		}
	}
	if capped {
		merged.Truncated = true
	}
	resp := infoAssociationResponse{
		Available: true, Layer: infoLayerLabel(layer), Source: "cold_path",
		AssociationResult: merged,
	}
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

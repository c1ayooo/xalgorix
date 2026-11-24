package web

// handlers_assets.go — project asset operations (READ + record/merge/archive).
//
// Authority split: the asset graph owns identity (project, kind, key); the
// InfoItem owns provenance. This surface never creates findings and never
// changes scan state. Paths live under /api/projects/{id}/assets so the
// existing RBAC rules apply (viewer reads; operator/admin writes).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func (s *Server) handleProjectAssets(w http.ResponseWriter, r *http.Request, store platform.Repository, projectID string, rest []string) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "project id is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if _, err := store.GetProject(ctx, projectID); err != nil {
		writePlatformError(w, err)
		return
	}
	repo := assets.Backend()
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			s.listProjectAssets(ctx, w, r, repo, projectID)
		case http.MethodPost:
			s.recordProjectAsset(ctx, w, r, repo, projectID)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
		return
	}
	switch rest[0] {
	case "edges":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		s.listProjectAssetEdges(ctx, w, repo, projectID)
	case "archive":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		s.setProjectAssetsArchived(ctx, w, r, repo, projectID)
	case "merge":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		s.mergeProjectAssets(ctx, w, r, repo, projectID)
	default:
		id, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil || id <= 0 {
			writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		s.getProjectAsset(ctx, w, repo, projectID, id)
	}
}

func (s *Server) listProjectAssets(ctx context.Context, w http.ResponseWriter, r *http.Request, repo assets.Repository, projectID string) {
	if repo == nil {
		writeJSONStatus(w, http.StatusOK, map[string]any{
			"project_id": projectID, "source": "none", "items": []assets.Asset{}, "total": 0,
		})
		return
	}
	q := r.URL.Query()
	filter := assets.AssetFilter{
		ProjectID:       projectID,
		TaskID:          strings.TrimSpace(q.Get("task_id")),
		RunID:           strings.TrimSpace(q.Get("run_id")),
		Query:           strings.TrimSpace(q.Get("q")),
		IncludeArchived: parseBoolQuery(q.Get("include_archived")),
		Limit:           parseLimitQuery(q.Get("limit"), 200, 1000),
	}
	for _, raw := range q["kind"] {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if assets.ValidKind(assets.Kind(part)) {
				filter.Kinds = append(filter.Kinds, assets.Kind(part))
			}
		}
	}
	items, err := repo.ListProjectAssets(ctx, filter)
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": "asset read failed", "detail": err.Error(), "project_id": projectID,
		})
		return
	}
	if items == nil {
		items = []assets.Asset{}
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"project_id": projectID, "source": "assets", "items": items, "total": len(items),
	})
}

// listProjectAssetEdges returns the project's deterministic structural edges
// (host → path, domain → ip, ...). The asset page needs the full edge set to
// group targets and their paths; fetching incident edges one asset at a time
// would turn the page into an N+1 request fan-out.
func (s *Server) listProjectAssetEdges(ctx context.Context, w http.ResponseWriter, repo assets.Repository, projectID string) {
	if repo == nil {
		writeJSONStatus(w, http.StatusOK, map[string]any{
			"project_id": projectID, "edges": []assets.Edge{},
		})
		return
	}
	edges, err := repo.ListEdges(ctx, projectID)
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": "asset edge read failed", "detail": err.Error(), "project_id": projectID,
		})
		return
	}
	if edges == nil {
		edges = []assets.Edge{}
	}
	// The memory store returns map order; sort here so every backend answers
	// the same immutable snapshot deterministically.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].SrcID != edges[j].SrcID {
			return edges[i].SrcID < edges[j].SrcID
		}
		if edges[i].Rel != edges[j].Rel {
			return edges[i].Rel < edges[j].Rel
		}
		return edges[i].DstID < edges[j].DstID
	})
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"project_id": projectID, "edges": edges,
	})
}

func (s *Server) recordProjectAsset(ctx context.Context, w http.ResponseWriter, r *http.Request, repo assets.Repository, projectID string) {
	if repo == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "asset graph unavailable"})
		return
	}
	limitJSONBody(w, r)
	var req struct {
		Kind        string `json:"kind"`
		Key         string `json:"key"`
		DisplayName string `json:"display_name"`
		InfoItemID  string `json:"info_item_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	in := assets.AssetInput{
		ProjectID:   projectID,
		Kind:        assets.Kind(strings.ToLower(strings.TrimSpace(req.Kind))),
		Key:         req.Key,
		DisplayName: req.DisplayName,
		InfoItemID:  strings.TrimSpace(req.InfoItemID),
	}
	if err := in.Normalize(); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	asset, err := repo.UpsertAsset(ctx, in)
	if err != nil {
		writeAssetError(w, err, "asset write failed")
		return
	}
	writeJSONStatus(w, http.StatusCreated, asset)
}

func (s *Server) getProjectAsset(ctx context.Context, w http.ResponseWriter, repo assets.Repository, projectID string, id int64) {
	if repo == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "asset graph unavailable"})
		return
	}
	detail, err := repo.GetAssetDetail(ctx, projectID, id)
	if err != nil {
		writeAssetError(w, err, "asset read failed")
		return
	}
	writeJSONStatus(w, http.StatusOK, detail)
}

func (s *Server) setProjectAssetsArchived(ctx context.Context, w http.ResponseWriter, r *http.Request, repo assets.Repository, projectID string) {
	if repo == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "asset graph unavailable"})
		return
	}
	limitJSONBody(w, r)
	var req struct {
		IDs      []int64 `json:"ids"`
		Archived bool    `json:"archived"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.IDs) == 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "ids is required"})
		return
	}
	actor := ""
	if claims, ok := sessionClaimsFrom(r); ok {
		actor = claims.Username
	}
	updated, err := repo.SetAssetsArchived(ctx, projectID, req.IDs, req.Archived, actor)
	if err != nil {
		writeAssetError(w, err, "asset archive failed")
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"project_id": projectID, "updated": updated, "archived": req.Archived,
	})
}

func (s *Server) mergeProjectAssets(ctx context.Context, w http.ResponseWriter, r *http.Request, repo assets.Repository, projectID string) {
	if repo == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "asset graph unavailable"})
		return
	}
	limitJSONBody(w, r)
	var req struct {
		SourceID int64 `json:"source_id"`
		TargetID int64 `json:"target_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.SourceID <= 0 || req.TargetID <= 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "source_id and target_id are required"})
		return
	}
	actor := ""
	if claims, ok := sessionClaimsFrom(r); ok {
		actor = claims.Username
	}
	if err := repo.MergeAssets(ctx, projectID, req.SourceID, req.TargetID, actor); err != nil {
		writeAssetError(w, err, "asset merge failed")
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"status": "merged", "project_id": projectID, "source_id": req.SourceID, "target_id": req.TargetID,
	})
}

func writeAssetError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, assets.ErrNotFound):
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "asset not found"})
	case errors.Is(err, assets.ErrConflict):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{"error": fallback, "detail": err.Error()})
	}
}

func parseBoolQuery(raw string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(raw))
	return v
}

func parseLimitQuery(raw string, fallback, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return fallback
	}
	if n > max {
		return max
	}
	return n
}

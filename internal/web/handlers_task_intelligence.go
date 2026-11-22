package web

// Task-level intelligence aggregation + operator triage (任务详情 情报 tab):
//
//	GET   /api/tasks/{id}/intelligence                — items aggregated across
//	                                                    the task's runs, deduped
//	                                                    by fingerprint, with
//	                                                    triage overlay + counts.
//	PATCH /api/tasks/{id}/intelligence/{fingerprint}  — operator triage label.
//
// Layering (charter §5/§9): the observed ITEMS are the engine's hot-path
// records in each run's intelligence.json — never mutated here. Triage is a
// human annotation stored separately (platform.IntelTriageStore, dual PG/file
// implementation) and overlaid at read time, so a conflict between what the
// engine recorded and what the operator concluded stays visible instead of one
// silently overwriting the other. Aggregation walks run scan dirs, so the read
// path works with no DSN (file store backs triage too).

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/tools/intelligence"
)

// taskIntelItem is one aggregated intel observation plus its projection
// fields. Item fields are embedded verbatim (fingerprint/kind/title/content/
// endpoint/confidence/timestamps); ScanIDs lists every run that recorded the
// same observation (newest first — ListRuns order).
type taskIntelItem struct {
	intelligence.Item
	ScanIDs []string              `json:"scan_ids"`
	Triage  *platform.IntelTriage `json:"triage,omitempty"`
}

// handleTaskIntelligence serves GET /api/tasks/{id}/intelligence.
func (s *Server) handleTaskIntelligence(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID string) {
	ctx := r.Context()

	entries := s.findAllScanSummaries()
	// run identity → sibling scan dirs: id→dir pairs from the same walk (scan
	// files' dir source of truth), plus a record index for canonical ids.
	idx := s.scanSummaryIndexFor(entries)

	// Walk the task's runs newest first (ListRuns order): the newest run's
	// record of a fingerprint wins, older runs only extend ScanIDs — a
	// re-observation never overwrites the original record. Within a run,
	// every sibling session's snapshot merges too (D11 re-dispatch): a
	// re-dispatched run keeps its previous session's intel instead of
	// dropping it when the first-wins directory pick lands on the fresh
	// session (the lookupAll discipline the finding projections use).
	const pageSize = 500
	var ordered []*taskIntelItem
	deduped := make(map[string]*taskIntelItem)
	damagedRuns := 0
	for page := 1; ; page++ {
		runs, total, err := store.ListRuns(ctx, taskID, page, pageSize)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			recs := idx.lookupAll(run.InstanceID, run.LegacyScanID)
			sort.SliceStable(recs, func(i, j int) bool { return recs[i].StartedAt > recs[j].StartedAt })
			for _, rec := range recs {
				dir := idx.dirByScanID[rec.ID]
				if dir == "" {
					continue // missing session (purged history): nothing to read
				}
				items, err := intelligence.LoadSnapshot(filepath.Join(dir, "intelligence.json"))
				if err != nil {
					// One damaged run snapshot must not fabricate a clean task
					// view: count it and say so in the response.
					damagedRuns++
					log.Printf("[intel] task %s run %s session %s: %v", taskID, run.ID, rec.ID, err)
					continue
				}
				scanKey := run.InstanceID
				if rec.ID != "" {
					scanKey = rec.ID
				}
				for _, item := range items {
					if item.Fingerprint == "" {
						continue
					}
					if existing, ok := deduped[item.Fingerprint]; ok {
						if !scanIDKnown(existing.ScanIDs, scanKey) {
							existing.ScanIDs = append(existing.ScanIDs, scanKey)
						}
						continue
					}
					ti := &taskIntelItem{Item: item, ScanIDs: []string{scanKey}}
					deduped[item.Fingerprint] = ti
					ordered = append(ordered, ti)
				}
			}
		}
		if page*pageSize >= total {
			break
		}
	}

	// Triage overlay: separate human annotation, applied read-time.
	triageAvailable := false
	if ts, ok := store.(platform.IntelTriageStore); ok {
		triage, err := ts.ListIntelTriage(ctx, taskID)
		if err != nil {
			log.Printf("[intel] task %s: triage read failed: %v", taskID, err)
		} else {
			triageAvailable = true
			for _, ti := range ordered {
				if t, ok := triage[ti.Fingerprint]; ok {
					tCopy := t
					ti.Triage = &tCopy
				}
			}
		}
	}

	counts := map[string]int{
		intelligence.KindCredential:    0,
		intelligence.KindLead:          0,
		intelligence.KindSensitiveData: 0,
	}
	items := make([]taskIntelItem, 0, len(ordered))
	for _, ti := range ordered {
		counts[ti.Kind]++
		items = append(items, *ti)
	}
	// Newest observation first (run records are append-time ordered).
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})

	resp := map[string]any{
		"items":            items,
		"counts":           counts,
		"triage_available": triageAvailable,
	}
	if damagedRuns > 0 {
		resp["damaged_runs"] = damagedRuns
		resp["note"] = "some run intelligence snapshots failed to parse; their items are absent below the damage"
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

func scanIDKnown(ids []string, id string) bool {
	for _, known := range ids {
		if known == id {
			return true
		}
	}
	return false
}

// handleIntelTriage serves PATCH /api/tasks/{id}/intelligence/{fingerprint}.
func (s *Server) handleIntelTriage(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID, fingerprint string) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPut {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ts, ok := store.(platform.IntelTriageStore)
	if !ok {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "intel triage requires a platform store"})
		return
	}
	limitJSONBody(w, r)
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "fingerprint is required"})
		return
	}
	if err := ts.SaveIntelTriage(r.Context(), taskID, fingerprint, body.Status, "operator"); err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]string{
		"fingerprint": fingerprint,
		"status":      body.Status,
	})
}

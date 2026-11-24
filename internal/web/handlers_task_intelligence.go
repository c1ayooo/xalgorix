package web

// Task-level intelligence aggregation + operator triage (任务详情 情报 tab):
//
//	GET   /api/tasks/{id}/intelligence                — items aggregated across
//	                                                    the task's runs, deduped
//	                                                    by fingerprint, with
//	                                                    triage + access overlay
//	                                                    and counts.
//	PATCH /api/tasks/{id}/intelligence/{fingerprint}  — operator triage label,
//	                                                    optionally endorsing the
//	                                                    derived access row and
//	                                                    adding reachability grants.
//
// Layering (charter §5/§9): the observed ITEMS are the engine's hot-path
// records in each run's intelligence snapshot — never mutated here. Triage is a
// human annotation stored separately (platform.IntelTriageStore, dual PG/file
// implementation) and overlaid at read time, so a conflict between what the
// engine recorded and what the operator concluded stays visible instead of one
// silently overwriting the other. Aggregation walks run scan dirs, so the read
// path works with no DSN (file store backs triage too).
//
// The access overlay is the reachability projection (INTEL_HANDOFF_SPEC M2):
// confirming an item endorses its derived grants for THIS run's scans; the seed
// reads the same rows through intelaccess.ListUsable.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/intelaccess"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// Compile-time pin (R6.2): the runstate store is the durable intelligence
// backend, so the task-intelligence aggregation reads the run_state_files
// row through intelligence.LoadSnapshot once cmd/xalgorix registers it (the
// R6.1 write-wiring residual; this read path is backend-complete here).
var _ intelligence.Backend = (*runstate.Store)(nil)

// taskIntelItem is one aggregated intel observation plus its projection
// fields. Item fields are embedded verbatim (fingerprint/kind/title/content/
// endpoint/confidence/timestamps); ScanIDs lists every run that recorded the
// same observation (newest first — ListRuns order).
type taskIntelItem struct {
	intelligence.Item
	ScanIDs []string              `json:"scan_ids"`
	Triage  *platform.IntelTriage `json:"triage,omitempty"`
	// accessScanIDs are the graph run ids the access projection is keyed by.
	// ScanIDs stay the session scan ids the UI links to; the two differ in
	// production (legacy scan id vs run id), so they are tracked separately.
	accessScanIDs []string
	// Access is the M2 reachability projection for this fingerprint, resolved
	// across the item's scans (a usable row wins over a stale one).
	Access *intelaccess.Access `json:"access,omitempty"`
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
						accessKey := strings.TrimSpace(run.InstanceID)
						if accessKey == "" {
							accessKey = scanKey
						}
						if !scanIDKnown(existing.accessScanIDs, accessKey) {
							existing.accessScanIDs = append(existing.accessScanIDs, accessKey)
						}
						continue
					}
					accessKey := strings.TrimSpace(run.InstanceID)
					if accessKey == "" {
						accessKey = scanKey
					}
					ti := &taskIntelItem{Item: item, ScanIDs: []string{scanKey}, accessScanIDs: []string{accessKey}}
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

	// Reachability overlay (M2): the derived access row per fingerprint,
	// resolved across the scans that recorded it. No store (file mode) or no
	// row simply means "no projection yet".
	if accessStore := intelaccess.StoreOrNil(); accessStore != nil {
		for _, ti := range ordered {
			access, ok, err := accessStore.Lookup(ctx, ti.accessScanIDs, ti.Fingerprint)
			if err != nil {
				log.Printf("[intel] task %s: access lookup %s failed: %v", taskID, ti.Fingerprint, err)
				continue
			}
			if ok {
				accessCopy := access
				ti.Access = &accessCopy
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

// taskIntelSession is one run session's identities plus its on-disk dir.
// ScanID is the session scan id; RunID is the graph run id the access
// projection is keyed by (they differ in production).
type taskIntelSession struct {
	ScanID string
	RunID  string
	Dir    string
}

// taskIntelSessions lists the task's run sessions newest first, including the
// sibling sessions of a re-dispatched run (D11), using the same walk as the
// aggregation.
func (s *Server) taskIntelSessions(ctx context.Context, store platform.Repository, taskID string) []taskIntelSession {
	entries := s.findAllScanSummaries()
	idx := s.scanSummaryIndexFor(entries)
	const pageSize = 500
	var out []taskIntelSession
	seen := make(map[string]bool)
	for page := 1; ; page++ {
		runs, total, err := store.ListRuns(ctx, taskID, page, pageSize)
		if err != nil {
			log.Printf("[intel] task %s: list runs for triage failed: %v", taskID, err)
			return out
		}
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			recs := idx.lookupAll(run.InstanceID, run.LegacyScanID)
			sort.SliceStable(recs, func(i, j int) bool { return recs[i].StartedAt > recs[j].StartedAt })
			for _, rec := range recs {
				dir := idx.dirByScanID[rec.ID]
				if dir == "" || seen[rec.ID] {
					continue
				}
				seen[rec.ID] = true
				runID := strings.TrimSpace(run.InstanceID)
				if runID == "" {
					runID = rec.ID
				}
				out = append(out, taskIntelSession{ScanID: rec.ID, RunID: runID, Dir: dir})
			}
		}
		if page*pageSize >= total {
			break
		}
	}
	return out
}

// findIntelItem loads one session's snapshot and resolves the fingerprint.
// A damaged snapshot is logged and treated as "no item" (the GET path already
// reports damage separately).
func findIntelItem(dir, fingerprint string) (intelligence.Item, bool) {
	items, err := intelligence.LoadSnapshot(filepath.Join(dir, "intelligence.json"))
	if err != nil {
		log.Printf("[intel] triage: snapshot %s unreadable: %v", dir, err)
		return intelligence.Item{}, false
	}
	for _, item := range items {
		if strings.EqualFold(item.Fingerprint, fingerprint) {
			return item, true
		}
	}
	return intelligence.Item{}, false
}

// handleIntelTriage serves PATCH /api/tasks/{id}/intelligence/{fingerprint}.
//
// The label is the human decision; the same request endorses the derived access
// rows of every run that recorded the item and may add operator grants. One
// action, not two state machines (INTEL_HANDOFF_SPEC D2/D4).
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
		Status    string   `json:"status"`
		Grants    []string `json:"grants"`
		ExpiresAt string   `json:"expires_at"`
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
	if !platform.ValidIntelStatus[body.Status] {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "status must be open|confirmed|dismissed"})
		return
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(body.ExpiresAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "expires_at must be RFC3339"})
			return
		}
		utc := parsed.UTC()
		expiresAt = &utc
	}
	// Normalize grants before any write so a typo is a 400, not a partial
	// endorsement that silently lost its doors.
	grants := make([]string, 0, len(body.Grants))
	for _, raw := range body.Grants {
		normalized, ok := intelaccess.NormalizeTarget(raw)
		if !ok {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{
				"error": "invalid grant " + strings.TrimSpace(raw) + " (expected host:port or CIDR)",
			})
			return
		}
		grants = append(grants, normalized)
	}

	if err := ts.SaveIntelTriage(r.Context(), taskID, fingerprint, body.Status, "operator"); err != nil {
		writePlatformError(w, err)
		return
	}

	// Projection side: endorse/revoke the same fingerprint in each run that
	// recorded it. A failure is logged and reported in the count; the triage
	// label itself is already authoritative for the human decision.
	accessUpdated := 0
	accessStore := intelaccess.StoreOrNil()
	if accessStore != nil {
		ctx := r.Context()
		for _, session := range s.taskIntelSessions(ctx, store, taskID) {
			item, found := findIntelItem(session.Dir, fingerprint)
			if !found {
				continue
			}
			derived, ok := intelaccess.DeriveItem(item)
			if !ok {
				continue // sensitive_data carries no door; nothing to endorse
			}
			switch body.Status {
			case platform.IntelStatusConfirmed:
				if _, err := accessStore.Confirm(ctx, intelaccess.ConfirmInput{
					ScanID:      session.RunID,
					Fingerprint: fingerprint,
					Kind:        derived.Kind,
					Subject:     derived.Subject,
					Targets:     derived.Targets,
					By:          "operator",
					ExpiresAt:   expiresAt,
				}); err != nil {
					log.Printf("[intel] triage %s/%s: confirm failed: %v", session.ScanID, fingerprint, err)
					continue
				}
			case platform.IntelStatusDismissed:
				if err := accessStore.Revoke(ctx, session.RunID, fingerprint, "operator dismissed"); err != nil && !errors.Is(err, intelaccess.ErrNotFound) {
					log.Printf("[intel] triage %s/%s: revoke failed: %v", session.RunID, fingerprint, err)
					continue
				}
			}
			if len(grants) > 0 {
				if body.Status != platform.IntelStatusConfirmed {
					// Grants need a row; derive it so an operator-supplied door
					// is never silently dropped because triage ran first.
					if _, err := accessStore.DeriveScan(ctx, session.RunID, []intelligence.Item{item}); err != nil {
						log.Printf("[intel] triage %s/%s: derive for grants failed: %v", session.RunID, fingerprint, err)
						continue
					}
				}
				if _, err := accessStore.AddGrants(ctx, session.RunID, fingerprint, grants); err != nil {
					log.Printf("[intel] triage %s/%s: grants failed: %v", session.RunID, fingerprint, err)
					continue
				}
			}
			accessUpdated++
		}
	}

	writeJSONStatus(w, http.StatusOK, map[string]any{
		"fingerprint":      fingerprint,
		"status":           body.Status,
		"access_updated":   accessUpdated,
		"access_available": accessStore != nil,
	})
}

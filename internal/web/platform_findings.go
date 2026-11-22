package web

// Findings cold-path projection for task-level views (REDESIGN §3.1).
//
// scan.json remains the authority. With a DSN, already-accepted findings are
// copied into run_findings and task views query the replica; without one, the
// same projection is computed on read over every run page. This file never
// accepts, verifies, relabels or reorders findings.

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// projectedRunFindings converts the saved ScanRecords behind one run into the
// platform projection shape. Records are the run's sibling sessions (REDESIGN
// D11 re-dispatch shares one instance id across several scan.json records), so
// the corpus merges newest-wins over the same (target, endpoint, title,
// severity) key the findings list uses — one record alone is a blind spot, not
// the truth. Callers build this before handing work to a goroutine so cleanup
// never races with further record mutation.
func projectedRunFindings(recs []*ScanRecord) []platform.RunFinding {
	type projected struct {
		finding platform.RunFinding
		started string
	}
	byKey := make(map[string]projected)
	order := make([]string, 0)
	for _, rec := range recs {
		if rec == nil {
			continue
		}
		for _, v := range rec.Vulns {
			key := dedupFindingKey(rec.Target, v)
			if key == "" {
				continue
			}
			if existing, ok := byKey[key]; ok && existing.started >= rec.StartedAt {
				continue
			}
			payload, err := json.Marshal(flatFinding{
				VulnSummary:   v,
				ScanID:        rec.ID,
				ScanTarget:    rec.Target,
				ScanStartedAt: rec.StartedAt,
			})
			if err != nil {
				continue
			}
			if _, exists := byKey[key]; !exists {
				order = append(order, key)
			}
			byKey[key] = projected{
				finding: platform.RunFinding{
					DedupKey:      key,
					ScanStartedAt: parseScanStartedAt(rec.StartedAt),
					Finding:       payload,
				},
				started: rec.StartedAt,
			}
		}
	}
	out := make([]platform.RunFinding, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key].finding)
	}
	return out
}

func parseScanStartedAt(raw string) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// deletePlatformRunsForScan removes the mount(s) tied to a purged scan. The
// run_findings rows cascade. Best-effort: a failed cold-path cleanup logs and
// is retried naturally when the operator deletes the project.
func (s *Server) deletePlatformRunsForScan(instanceID, scanID string) {
	store := s.currentPlatformStore()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	removed, err := store.DeleteRunsByScanIdentity(ctx, instanceID, scanID)
	if err != nil {
		log.Printf("[platform-findings] delete runs for scan %s: %v", scanID, err)
		return
	}
	if removed > 0 {
		log.Printf("[platform] removed %d run mount(s) for purged scan %s", removed, scanID)
	}
}

// attachRunLinkage stamps RESPONSE-ONLY run/task linkage onto a scan-detail
// projection (see ScanRecord.RunID): the unified HITL channel routes guidance
// through POST /api/tasks/{id}/guidance, and the legacy run page resolves its
// task from the run mount instead of the browser guessing. Best-effort — no
// store, lookup error, or unmounted scan leaves both fields empty; empty is
// honest "no task" (the composer hides itself), never an inferred identity.
func (s *Server) attachRunLinkage(rec *ScanRecord) {
	if rec == nil || rec.RunID != "" || rec.InstanceID == "" {
		return
	}
	store := s.currentPlatformStore()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runs, err := store.RunsByInstanceID(ctx, rec.InstanceID)
	if err != nil || len(runs) == 0 {
		return
	}
	run := runs[0]
	// One instance can theoretically carry several mounts (the importer
	// dedups, but stay deterministic): prefer the mount whose legacy scan id
	// matches this record.
	for _, r := range runs {
		if rec.ID != "" && r.LegacyScanID == rec.ID {
			run = r
			break
		}
	}
	rec.RunID = run.ID
	rec.TaskID = run.TaskID
}

// queueRunFindingProjection writes the projection for one completed root
// session. The write is detached from cleanup and bounded: cold-path failure
// only logs and is repaired by the startup backfill.
func (s *Server) queueRunFindingProjection(instanceID string, rec *ScanRecord) {
	store := s.currentPlatformStore()
	fs, ok := store.(platform.FindingsStore)
	if !ok || strings.TrimSpace(instanceID) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Merge across every sibling session of the instance, not just the
		// record that completed: the replace-by-run would otherwise wipe the
		// findings earlier siblings contributed.
		recs := s.scanSummaryIndexFor(s.findAllScanSummaries()).lookupAll(rec.InstanceID, rec.ID)
		findings := projectedRunFindings(recs)
		var started time.Time
		if newest := newestSibling(recs); newest != nil {
			started = parseScanStartedAt(newest.StartedAt)
		}
		runs, err := store.RunsByInstanceID(ctx, instanceID)
		if err != nil {
			log.Printf("[platform-findings] resolve run for instance %s: %v", instanceID, err)
			return
		}
		for _, run := range runs {
			if err := fs.ReplaceRunFindings(ctx, run.ID, started, findings); err != nil {
				log.Printf("[platform-findings] project run %s: %v", run.ID, err)
			}
		}
	}()
}

// backfillRunFindingProjections repairs the "scan.json written, projection
// write missed" window and seeds projections for scans imported at startup.
// It is idempotent (replace-by-run) and never blocks startup.
func (s *Server) backfillRunFindingProjections(ctx context.Context) {
	store := s.currentPlatformStore()
	fs, ok := store.(platform.FindingsStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runs, err := store.AllRuns(ctx)
	if err != nil {
		log.Printf("[platform-findings] backfill enumerate runs: %v", err)
		return
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	projected := 0
	for _, run := range runs {
		recs := idx.lookupAll(run.InstanceID, run.LegacyScanID)
		if len(recs) == 0 {
			continue
		}
		var newest *ScanRecord
		if newest = newestSibling(recs); newest == nil {
			continue
		}
		started := parseScanStartedAt(newest.StartedAt)
		if started.IsZero() {
			started = run.CreatedAt
		}
		if err := fs.ReplaceRunFindings(ctx, run.ID, started, projectedRunFindings(recs)); err != nil {
			log.Printf("[platform-findings] backfill run %s: %v", run.ID, err)
			continue
		}
		projected++
	}
	if projected > 0 {
		log.Printf("[platform-findings] projected findings for %d run(s)", projected)
	}
}

// taskFindingsFromProjection reads the PG replica (one newest row per key).
func (s *Server) taskFindingsFromProjection(ctx context.Context, fs platform.FindingsStore, taskID string) ([]flatFinding, error) {
	rows, err := fs.ListTaskFindings(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]flatFinding, 0, len(rows))
	for _, row := range rows {
		var finding flatFinding
		if err := json.Unmarshal(row.Finding, &finding); err != nil {
			log.Printf("[platform-findings] decode projected finding %q: %v", row.DedupKey, err)
			continue
		}
		out = append(out, finding)
	}
	sortTaskFindings(out)
	return out, nil
}

// taskFindingsFromScans is the no-DSN degradation: page through every run and
// apply the same deterministic newest-wins dedup, without a fixed run cap.
func (s *Server) taskFindingsFromScans(ctx context.Context, store platform.Repository, taskID string) ([]flatFinding, error) {
	const pageSize = 500
	deduped := make(map[string]flatFinding)
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	for page := 1; ; page++ {
		runs, total, err := store.ListRuns(ctx, taskID, page, pageSize)
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			for _, rec := range idx.lookupAll(run.InstanceID, run.LegacyScanID) {
				for _, v := range rec.Vulns {
					key := dedupFindingKey(rec.Target, v)
					candidate := flatFinding{
						VulnSummary:   v,
						ScanID:        rec.ID,
						ScanTarget:    rec.Target,
						ScanStartedAt: rec.StartedAt,
					}
					if existing, ok := deduped[key]; ok && existing.ScanStartedAt >= candidate.ScanStartedAt {
						continue
					}
					deduped[key] = candidate
				}
			}
		}
		if page*pageSize >= total {
			break
		}
	}
	out := make([]flatFinding, 0, len(deduped))
	for _, finding := range deduped {
		out = append(out, finding)
	}
	sortTaskFindings(out)
	return out, nil
}

func sortTaskFindings(findings []flatFinding) {
	sort.Slice(findings, func(i, j int) bool {
		ri, rj := severityRankValue(findings[i].Severity), severityRankValue(findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return findings[i].ScanStartedAt > findings[j].ScanStartedAt
	})
}
